// The public share-link listener: a dedicated TCP HTTP server that serves
// exactly one thing — GET /s/<token> downloads of shared files. It is
// disabled by default; the admin enables it (with a port and optional
// LAN-only bind) in settings.
//
// Hard guarantees:
//   - only /s/<token> responds; everything else is 404
//   - tokens are 256-bit; expiry and optional password are enforced
//   - the file is resolved through the same typed-root + containment
//     checks as the authenticated API, opened with O_NOFOLLOW, and served
//     read-only via http.ServeContent (Range requests supported)
//   - responses are always Content-Disposition: attachment
//   - a concurrency semaphore bounds simultaneous downloads
//   - non-LAN connections are dropped when the bind is "lan"
package shares

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/resolver"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// PublicServer serves share links over TCP.
type PublicServer struct {
	mu       sync.Mutex
	listener net.Listener
	res      *resolver.Resolver
	store    *Store
	sem      chan struct{}
	audit    func(format string, args ...any)
}

// NewPublicServer creates the server (listener starts via Reconfigure).
func NewPublicServer(res *resolver.Resolver, store *Store, audit func(format string, args ...any)) *PublicServer {
	if audit == nil {
		audit = func(string, ...any) {}
	}
	return &PublicServer{res: res, store: store, sem: make(chan struct{}, 4), audit: audit}
}

// PublicConfig for the listener.
type PublicConfig struct {
	Enable bool
	Port   int
	// Bind: "" = all interfaces, "lan" = private IPv4 only, or an IP.
	Bind string
}

// Reconfigure starts or stops the listener per cfg. Safe to call repeatedly
// with the same config (idempotent).
func (s *PublicServer) Reconfigure(cfg PublicConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !cfg.Enable || cfg.Port <= 0 {
		if s.listener != nil {
			addr := s.listener.Addr().String()
			s.listener.Close()
			s.listener = nil
			s.audit("share listener stopped (%s)", addr)
		}
		return
	}
	if s.listener != nil {
		return
	}
	ln, err := listenFor(cfg)
	if err != nil {
		s.audit("share listener failed: %v", err)
		return
	}
	s.listener = ln
	s.audit("share listener serving on %s", ln.Addr().String())
	go s.serve(ln)
}

// listenFor binds per config; "lan" restricts accepted peers to private
// ranges by wrapping the listener.
func listenFor(cfg PublicConfig) (net.Listener, error) {
	addr := net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port))
	if cfg.Bind == "lan" {
		inner, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(cfg.Port)))
		if err != nil {
			return nil, err
		}
		return &lanListener{inner: inner}, nil
	}
	return net.Listen("tcp", addr)
}

type lanListener struct {
	inner net.Listener
}

func (l *lanListener) Accept() (net.Conn, error) {
	for {
		c, err := l.inner.Accept()
		if err != nil {
			return nil, err
		}
		host := c.RemoteAddr().String()
		if i := strings.LastIndexByte(host, ':'); i >= 0 {
			host = strings.Trim(host[:i], "[]")
		}
		if isPrivateAddr(host) {
			return c, nil
		}
		c.Close() // drop non-LAN connections silently
	}
}
func (l *lanListener) Close() error   { return l.inner.Close() }
func (l *lanListener) Addr() net.Addr { return l.inner.Addr() }

func isPrivateAddr(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback())
}

func (s *PublicServer) serve(ln net.Listener) {
	mux := http.NewServeMux()
	mux.HandleFunc("/s/", s.handleShare)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.Serve(ln); err != nil {
		s.mu.Lock()
		alive := s.listener != nil
		s.mu.Unlock()
		if alive {
			s.audit("share listener stopped: %v", err)
		}
	}
}

// handleShare serves one shared file (download only, Range-supported).
func (s *PublicServer) handleShare(w http.ResponseWriter, r *http.Request) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/s/")
	if token == "" || len(token) > 64 || strings.ContainsAny(token, "/\\") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	sh, err := s.store.Resolve(token, r.URL.Query().Get("pw"))
	if err != nil {
		if troveerr.Of(err) == troveerr.CSRFInvalid {
			http.Error(w, "wrong password", http.StatusUnauthorized)
			return
		}
		http.Error(w, "share link not found or expired", http.StatusNotFound)
		return
	}

	root, err := s.res.RootOf(sh.Root)
	if err != nil {
		http.Error(w, "share target unavailable", http.StatusGone)
		return
	}
	if err := s.res.Registry().Verify(root); err != nil {
		http.Error(w, "share target unavailable", http.StatusGone)
		return
	}
	res, err := s.res.Resolve(sh.Root, sh.RelPath, false)
	if err != nil {
		http.Error(w, "share target unavailable", http.StatusGone)
		return
	}
	defer res.Close()
	if !res.Exists || res.Stat.Kind != model.EntryFile {
		http.Error(w, "share target unavailable", http.StatusGone)
		return
	}

	fd, err := resolver.OpenFileNoFollow(res.ParentFd, res.Name, os.O_RDONLY, 0)
	if err != nil {
		http.Error(w, "share target unavailable", http.StatusGone)
		return
	}
	f := os.NewFile(uintptr(fd), res.Name)
	defer f.Close()

	ctype := mimeByExtension(res.Name)
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition",
		"attachment; filename*=UTF-8''"+escapeHeaderFilename(res.Name))
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, res.Name, time.Unix(res.Stat.Mtime, int64(res.Stat.MtimeN)), f)

	s.store.RecordDownload(sh.Token)
	s.audit("share download: %s (%s…) from %s", sh.Name, sh.Token[:8], r.RemoteAddr)
}

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
	{".jpg", "image/jpeg"}, {".jpeg", "image/jpeg"},
	{".png", "image/png"}, {".gif", "image/gif"}, {".webp", "image/webp"},
	{".mp4", "video/mp4"}, {".webm", "video/webm"}, {".mkv", "video/x-matroska"},
	{".mov", "video/quicktime"},
	{".mp3", "audio/mpeg"}, {".flac", "audio/flac"}, {".m4a", "audio/mp4"},
	{".zip", "application/zip"}, {".json", "application/json"},
}

