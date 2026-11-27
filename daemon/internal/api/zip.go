// ZIP streaming and Range-based file streaming endpoints.
package api

import (
	"archive/zip"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/resolver"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// caps for on-the-fly ZIP generation (production-safe defaults; a runaway
// 100 GB zip would hammer the array for hours)
const (
	zipMaxEntries = 50_000
	zipMaxBytes   = int64(32) << 30 // 32 GiB
)

// handleZip streams a directory (or single file) as a ZIP archive.
// Method uses Store for incompressible types and Deflate otherwise; the
// walk uses the same containment-checked resolver as every other read.
func (d Deps) handleZip(w http.ResponseWriter, r *http.Request) {
	ref, err := parseRootQ(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		writeErr(w, troveerr.New(troveerr.BadRequest, "zip requires a subdirectory path"))
		return
	}
	root, err := d.Resolver.RootOf(ref)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := d.Resolver.Registry().Verify(root); err != nil {
		writeErr(w, err)
		return
	}

	base := path.Base("/"+p)
	res, err := d.Resolver.ResolveExists(ref, p, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer res.Close()

	// size pre-check for directories (fail fast instead of dying mid-stream)
	if res.IsDir() {
		var total int64
		var count int64
		_, err = d.Resolver.Walk(root, p, func(rel string, st *resolver.StatInfo) error {
			if st.Kind == model.EntryFile {
				total += st.Size
				count++
			} else {
				count++
			}
			if total > zipMaxBytes || count > zipMaxEntries {
				return troveerr.New(troveerr.InsufficientSpace,
					"folder too large for ZIP download (caps: 32 GiB / 50k entries)")
			}
			return nil
		}, resolver.WalkCaps{MaxEntries: zipMaxEntries + 1, MaxDepth: 64, TimeBudgetN: 30e9})
		if err != nil {
			if troveerr.Of(err) == troveerr.InsufficientSpace {
				writeErr(w, err)
				return
			}
			// walk errors (permissions etc.) → proceed with what's readable
			_ = err
		}
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition",
		"attachment; filename*=UTF-8''"+escapeHeaderFilename(base+".zip"))
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)

	zw := zip.NewWriter(w)
	defer zw.Close()
	flusher, _ := w.(http.Flusher)

	var entries int64
	var aborted bool
	_, werr := d.Resolver.Walk(root, p, func(rel string, st *resolver.StatInfo) error {
		if aborted {
			return resolver.WalkSkipDir
		}
		if entries >= zipMaxEntries {
			aborted = true
			return resolver.WalkSkipDir
		}
		if st.Kind != model.EntryFile {
			return nil
		}
		entries++
		sub := strings.TrimPrefix(strings.TrimPrefix(rel, p), "/")
		if sub == "" {
			sub = path.Base("/"+rel)
		}
		method := uint16(zip.Deflate)
		if isIncompressible(sub) {
			method = zip.Store
		}
		hdr := &zip.FileHeader{Name: sub, Modified: time.Unix(st.Mtime, int64(st.MtimeN))}
		hdr.Method = method
		fh, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_ = fh

		src, err := d.Resolver.ResolveExists(ref, rel, false)
		if err != nil {
			return err
		}
		defer src.Close()
		fd, err := resolver.OpenFileNoFollow(src.ParentFd, src.Name, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), src.Name)
		_, cpErr := io.Copy(fh, f)
		f.Close()
		return cpErr
	}, resolver.WalkCaps{MaxEntries: zipMaxEntries + 1, MaxDepth: 64, TimeBudgetN: 0})
	_ = werr

	if flusher != nil {
		flusher.Flush()
	}
}

// isIncompressible reports known-incompressible types (Store beats Deflate
// for media: faster and zero CPU).
func isIncompressible(name string) bool {
	lo := strings.ToLower(name)
	for _, ext := range []string{".mp4", ".mkv", ".avi", ".mov", ".webm", ".wmv",
		".flac", ".mp3", ".m4a", ".ape", ".ogg", ".wav",
		".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".raw",
		".iso", ".vob", ".m2ts"} {
		if strings.HasSuffix(lo, ext) {
			return true
		}
	}
	return false
}

// handleStream serves a file with full Range support for browser-native
// media playback (video/audio) — http.ServeContent implements RFC 7233.
func (d Deps) handleStream(w http.ResponseWriter, r *http.Request) {
	ref, err := parseRootQ(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	p := r.URL.Query().Get("path")
	res, err := d.Resolver.ResolveExists(ref, p, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer res.Close()
	if res.Stat.Kind != model.EntryFile {
		writeErr(w, troveerr.New(troveerr.UnsupportedType, "only files can be streamed"))
		return
	}
	fd, err := resolver.OpenFileNoFollow(res.ParentFd, res.Name, os.O_RDONLY, 0)
	if err != nil {
		writeErr(w, troveerr.Wrap(troveerr.IOFailure, err, "open file"))
		return
	}
	f := os.NewFile(uintptr(fd), res.Name)

	ctype := "application/octet-stream"
	if t := mimeByExtension(res.Name); t != "" {
		ctype = t
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, res.Name, time.Unix(res.Stat.Mtime, int64(res.Stat.MtimeN)), f)
}

// escapeHeaderFilename escapes percent and quote for RFC 5987 filename*.
func escapeHeaderFilename(name string) string {
	return strings.NewReplacer("%", "%25", "\"", "%22").Replace(name)
}

// mimeByExtension maps common NAS file types for the browser.
func mimeByExtension(name string) string {
	lo := strings.ToLower(name)
	for _, e := range mimeTable {
		if strings.HasSuffix(lo, e.ext) {
			return e.typ
		}
	}
	return "application/octet-stream"
}

var mimeTable = []struct{ ext, typ string }{
	{".pdf", "application/pdf"},
	{".txt", "text/plain; charset=utf-8"},
	{".md", "text/plain; charset=utf-8"},
	{".html", "text/html"}, {".htm", "text/html"},
	{".css", "text/css"}, {".js", "text/javascript"},
	{".jpg", "image/jpeg"}, {".jpeg", "image/jpeg"},
	{".png", "image/png"}, {".gif", "image/gif"}, {".webp", "image/webp"},
	{".svg", "image/svg+xml"},
	{".mp4", "video/mp4"}, {".webm", "video/webm"}, {".mkv", "video/x-matroska"},
	{".mov", "video/quicktime"}, {".avi", "video/x-msvideo"},
	{".mp3", "audio/mpeg"}, {".flac", "audio/flac"}, {".m4a", "audio/mp4"},
	{".wav", "audio/wav"}, {".ogg", "audio/ogg"},
	{".zip", "application/zip"}, {".json", "application/json"},
	{".xml", "application/xml"},
}
