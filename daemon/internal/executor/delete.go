// Delete and Hash actions.
package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/planner"
	"github.com/turlamai/trove/daemon/internal/resolver"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// Delete removes the planned entries (children first, as the planner
// ordered them). Identity checks run per item: anything that changed since
// planning is skipped with a warning and kept — deletion never destroys
// content it did not verify.
func (e *Executor) Delete(man *planner.Manifest, h *Hooks) (*Outcome, error) {
	h = e.hooks(h)
	out := &Outcome{}
	if man.Trash {
		return e.DeleteWithTrash(man, h, nil)
	}
	root, err := e.res.RootOf(man.SourceRoot)
	if err != nil {
		return out, err
	}
	if err := e.res.Registry().Verify(root); err != nil {
		return out, err
	}

	for i := range man.Items {
		if err := h.Check(); err != nil {
			return out, err
		}
		it := man.Items[i]
		res, err := e.res.Resolve(man.SourceRoot, it.Rel, true)
		if err != nil {
			if troveerr.Of(err) == troveerr.NotFound {
				h.Warning("already gone: " + it.Rel)
				continue
			}
			return out, err
		}
		st, err := resolver.FstatAt(res.ParentFd, res.Name)
		if err != nil {
			res.Close()
			if os.IsNotExist(err) {
				continue
			}
			return out, troveerr.Wrap(troveerr.IOFailure, err, "stat "+it.Rel)
		}
		if st.Dev != it.Dev || st.Ino != it.Ino {
			res.Close()
			h.Warning("changed since planning — not deleted: " + it.Rel)
			out.Warnings++
			continue
		}
		switch st.Kind {
		case model.EntryDir:
			err = resolver.RmdirAt(res.ParentFd, res.Name)
			if err != nil && resolver.ENOTEmpty(err) {
				res.Close()
				h.Warning("directory gained new content — left intact: " + it.Rel)
				out.Warnings++
				continue
			}
		case model.EntryFile, model.EntrySymlink:
			err = resolver.UnlinkAt(res.ParentFd, res.Name)
		default:
			res.Close()
			h.Warning("special file not deleted: " + it.Rel)
			out.Warnings++
			continue
		}
		res.Close()
		if err != nil && !os.IsNotExist(err) {
			return out, troveerr.Wrap(troveerr.IOFailure, err, "delete "+it.Rel)
		}
		out.Items++
		if st.Kind == model.EntryFile {
			out.Bytes += st.Size
		}
		h.Progress(1, st.Size, it.Rel)
	}
	return out, nil
}

// HashResult is one computed digest.
type HashResult struct {
	Rel   string `json:"rel"`
	Size  int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Hash computes SHA-256 over the planned files, streaming, cancellable.
func (e *Executor) Hash(man *planner.Manifest, h *Hooks,
	sink func(HashResult)) (*Outcome, error) {

	h = e.hooks(h)
	out := &Outcome{}
	root, err := e.res.RootOf(man.SourceRoot)
	if err != nil {
		return out, err
	}
	if err := e.res.Registry().Verify(root); err != nil {
		return out, err
	}

	for i := range man.Items {
		if err := h.Check(); err != nil {
			return out, err
		}
		it := man.Items[i]
		if it.Kind != model.EntryFile {
			continue
		}
		res, err := e.res.ResolveExists(man.SourceRoot, it.Rel, false)
		if err != nil {
			if troveerr.Of(err) == troveerr.NotFound {
				h.Warning("vanished: " + it.Rel)
				continue
			}
			return out, err
		}
		// Identity check: hash what was planned, not what replaced it.
		if res.Stat.Dev != it.Dev || res.Stat.Ino != it.Ino {
			res.Close()
			h.Warning("changed since planning — not hashed: " + it.Rel)
			out.Warnings++
			continue
		}
		fd, err := resolver.OpenFileNoFollow(res.ParentFd, res.Name, os.O_RDONLY, 0)
		res.Close()
		if err != nil {
			return out, troveerr.Wrap(troveerr.IOFailure, err, "open "+it.Rel)
		}
		digest, n, err := e.hashFD(fd, it.Size, h, it.Rel)
		resolver.CloseFD(fd)
		if err != nil {
			return out, err
		}
		if n != it.Size {
			h.Warning("size changed while hashing: " + it.Rel)
			out.Warnings++
			continue
		}
		sink(HashResult{Rel: it.Rel, Size: n, SHA256: digest})
		out.Items++
		out.Bytes += n
	}
	return out, nil
}

func (e *Executor) hashFD(fd int, size int64, h *Hooks, label string) (string, int64, error) {
	d := sha256.New()
	buf := make([]byte, copyChunk)
	var done int64
	for done < size {
		if err := h.Check(); err != nil {
			return "", done, err
		}
		want := size - done
		if want > copyChunk {
			want = copyChunk
		}
		nr, er := readFull(fd, buf[:want])
		if nr > 0 {
			d.Write(buf[:nr])
			done += int64(nr)
			h.Progress(0, int64(nr), label)
		}
		if er == io.EOF {
			break
		}
		if er != nil {
			return "", done, troveerr.Wrap(troveerr.IOFailure, er, "read "+label)
		}
	}
	return hex.EncodeToString(d.Sum(nil)), done, nil
}
