//go:build linux

// Linux syscall layer of the resolver. All file I/O of the daemon flows
// through these helpers: path traversal happens only via openat2 with
// RESOLVE_BENEATH, and every final-component operation uses *at() syscalls
// relative to verified directory descriptors, so a swapped directory between
// two syscalls (TOCTOU) cannot redirect an operation outside the root.
package resolver

import (
	"errors"
	"io"
	"os"
	"time"
	"unsafe"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/roots"
	"github.com/turlamai/trove/daemon/internal/troveerr"
	"golang.org/x/sys/unix"
)

func probeOpenat2() bool {
	// RESOLVE_BENEATH rejects absolute paths, so probe with a relative one.
	fd, err := unix.Openat2(unix.AT_FDCWD, ".", &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | unix.O_CLOEXEC),
		Resolve: uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS),
	})
	if err != nil {
		return false
	}
	unix.Close(fd)
	return true
}

func isELOOP(err error) bool { return errors.Is(err, unix.ELOOP) }
func isEXDEV(err error) bool { return errors.Is(err, unix.EXDEV) }
func isEINVAL(err error) bool { return errors.Is(err, unix.EINVAL) }
func isENOTEMPTY(err error) bool { return errors.Is(err, unix.ENOTEMPTY) }

// ENOTEmpty exposes ENOTEMPTY checks to the executor (leftover directories).
func ENOTEmpty(err error) bool { return isENOTEMPTY(err) }

// isEscapeErr classifies kernel containment failures. RESOLVE_BENEATH
// violations surface as ELOOP, crossing attempts as EXDEV, absolute escape
// as EINVAL — every one is treated as an escape attempt (fail closed).
func isEscapeErr(err error) bool {
	var e *troveerr.E
	if errors.As(err, &e) {
		err = e.Wrapped
	}
	return isELOOP(err) || isEXDEV(err) || isEINVAL(err)
}

// OpenRoot opens an O_PATH descriptor of a trusted registry root path.
func OpenRoot(path string) (int, error) {
	return unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
}

// Openat2Beneath resolves rel beneath dirfd with kernel containment.
func Openat2Beneath(dirfd int, rel string) (int, error) {
	if rel == "." || rel == "" {
		return unix.Dup(dirfd)
	}
	return unix.Openat2(dirfd, rel, &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC),
		Resolve: uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS),
	})
}

// OpenResolvedDir opens the directory body of a resolved entry (the root
// itself when the final component is empty).
func OpenResolvedDir(res *Resolved) (int, error) {
	name := res.Name
	if name == "" {
		name = "."
	}
	return OpenDirNoFollow(res.ParentFd, name)
}

// CloseFD closes a raw descriptor.
func CloseFD(fd int) {
	if fd >= 0 {
		_ = unix.Close(fd)
	}
}

// FstatAt lstats name relative to dirfd without following symlinks.
func FstatAt(dirfd int, name string) (*StatInfo, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	return statFromUnix(&st), nil
}

func statFromUnix(st *unix.Stat_t) *StatInfo {
	kind := model.EntryOther
	switch st.Mode & 0170000 {
	case 0040000:
		kind = model.EntryDir
	case 0100000:
		kind = model.EntryFile
	case 0120000:
		kind = model.EntrySymlink
	}
	return &StatInfo{
		Dev:    uint64(st.Dev),
		Ino:    st.Ino,
		Nlink:  uint64(st.Nlink),
		Mode:   st.Mode,
		Size:   st.Size,
		Atime:  st.Atim.Sec,
		AtimeN: st.Atim.Nsec,
		Mtime:  st.Mtim.Sec,
		MtimeN: st.Mtim.Nsec,
		Uid:    st.Uid,
		Gid:    st.Gid,
		Kind:   kind,
	}
}

// ReadLinkAt reads a symlink target (display only).
func ReadLinkAt(dirfd int, name string) (string, error) {
	buf := make([]byte, 4096)
	n, err := unix.Readlinkat(dirfd, name, buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

// ListDir reads all entries of an open directory with per-entry lstat.
// Names are returned unsorted; sorting happens at the API layer.
func ListDir(dirfd int) ([]DirEntry, error) {
	// Real read fd (O_PATH fds cannot readdir).
	rfd, err := unix.Openat(dirfd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rfd)

	var out []DirEntry
	buf := make([]byte, 64*1024)
	for {
		n, err := unix.ReadDirent(rfd, buf)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
		for _, name := range parseDirentNames(buf[:n]) {
			if name == "." || name == ".." {
				continue
			}
			st, err := FstatAt(dirfd, name)
			if err != nil {
				if os.IsNotExist(err) {
					continue // raced with unlink — skip silently, never guess
				}
				return nil, err
			}
			e := DirEntry{Name: name, Stat: *st}
			if st.Kind == model.EntrySymlink {
				if t, err := ReadLinkAt(dirfd, name); err == nil {
					e.Stat.SymLink = t
				}
			}
			out = append(out, e)
		}
	}
	return out, nil
}

func parseDirentNames(buf []byte) []string {
	var names []string
	for len(buf) > 0 {
		if len(buf) < 19 {
			break
		}
		reclen := int(buf[16]) | int(buf[17])<<8
		if reclen <= 0 || reclen > len(buf) {
			break
		}
		// dirent layout: ino(8) off(8) reclen(2) type(1) name...
		nameEnd := reclen
		for nameEnd > 19 && buf[nameEnd-1] == 0 {
			nameEnd--
		}
		names = append(names, string(buf[19:nameEnd]))
		buf = buf[reclen:]
	}
	return names
}

// OpenDirNoFollow opens a real read fd of a directory (no symlink follow on
// the final component).
func OpenDirNoFollow(dirfd int, name string) (int, error) {
	return unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// OpenFileNoFollow opens a file relative to dirfd without following the
// final symlink; extra flags are ORed in by the caller (O_RDONLY etc.).
func OpenFileNoFollow(dirfd int, name string, extraFlags int, mode uint32) (int, error) {
	return unix.Openat(dirfd, name,
		unix.O_NOFOLLOW|unix.O_CLOEXEC|extraFlags, mode&07777)
}

// CreateFileExclusive creates a file relative to dirfd (used for staging).
func CreateFileExclusive(dirfd int, name string, mode uint32) (int, error) {
	return unix.Openat(dirfd, name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode&07777)
}

// MkdirAt creates a directory relative to dirfd (final component).
func MkdirAt(dirfd int, name string, mode uint32) error {
	return unix.Mkdirat(dirfd, name, mode&07777)
}

// UnlinkAt removes a file/symlink relative to dirfd (no follow).
func UnlinkAt(dirfd int, name string) error {
	return unix.Unlinkat(dirfd, name, 0)
}

// RmdirAt removes an empty directory relative to dirfd.
func RmdirAt(dirfd int, name string) error {
	return unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR)
}

// RenameNoReplace atomically refuses an existing target (renameat2 flag).
const RenameNoReplace = 1

// RenameAt renames within/between verified dirfds. When the kernel lacks
// renameat2 it fails closed — Unraid 7 kernels (6.x) have it.
func RenameAt(srcFd int, srcName string, dstFd int, dstName string, flags int) error {
	return unix.Renameat2(srcFd, srcName, dstFd, dstName, uint(flags))
}

// StatFSAt reports (total, avail) bytes for the filesystem holding path.
func StatFSAt(path string) (int64, int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := st.Bsize
	if bsize <= 0 {
		bsize = 4096
	}
	return int64(st.Blocks) * int64(bsize), int64(st.Bavail) * int64(bsize), nil
}

// FsyncFD syncs a file or directory fd.
func FsyncFD(fd int) error { return unix.Fsync(fd) }

// FchmodFD applies a mode to an open fd.
func FchmodFD(fd int, mode uint32) error { return unix.Fchmod(fd, mode&07777) }

// FchownFD applies uid/gid to an open fd.
func FchownFD(fd int, uid, gid uint32) error { return unix.Fchown(fd, int(uid), int(gid)) }

// SetTimesFD preserves atime/mtime of an open fd from a StatInfo via
// utimensat(AT_EMPTY_PATH) — no path re-resolution, no TOCTOU window.
func SetTimesFD(fd int, st *StatInfo) error {
	ts := [2]unix.Timespec{
		{Sec: st.Atime, Nsec: st.AtimeN},
		{Sec: st.Mtime, Nsec: st.MtimeN},
	}
	_, _, errno := unix.Syscall6(unix.SYS_UTIMENSAT, uintptr(fd), 0,
		uintptr(unsafe.Pointer(&ts[0])), uintptr(unix.AT_EMPTY_PATH), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// CopyRange attempts an in-kernel copy between fds (copy_file_range);
// io.ErrNoProgress signals the caller to fall back to read/write.
func CopyRange(dst, src int, limit int64) (int64, error) {
	var done int64
	for done < limit {
		chunk := limit - done
		if chunk > 128<<20 {
			chunk = 128 << 20
		}
		n, err := unix.CopyFileRange(src, nil, dst, nil, int(chunk), 0)
		if n > 0 {
			done += int64(n)
			continue
		}
		if err == unix.EINTR {
			continue
		}
		if err == unix.EXDEV || err == unix.EINVAL || err == unix.ENOSYS ||
			err == unix.EOPNOTSUPP || err == unix.EPERM || err == unix.EAGAIN ||
			err == unix.EACCES {
			return done, io.ErrNoProgress
		}
		if err != nil {
			return done, err
		}
		return done, io.EOF // n == 0: EOF
	}
	return done, nil
}

// ---------------------------------------------------------------------------
// FSView implementation (read-only surface used by the planner)
// ---------------------------------------------------------------------------

var _ FSView = (*Resolver)(nil)

// StatNode implements FSView. Fail-closed: unexpected errors surface coded,
// never swallowed.
func (r *Resolver) StatNode(root *roots.Root, rel string) (*StatInfo, error) {
	res, err := r.Resolve(root.RefModel(), rel, false)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	if !res.Exists {
		return nil, troveerr.New(troveerr.NotFound, "no such entry: "+res.FullPath())
	}
	st := res.Stat
	if st.Kind == model.EntrySymlink {
		res2, err := r.Resolve(root.RefModel(), rel, false)
		if err == nil {
			if t, err := ReadLinkAt(res2.ParentFd, res2.Name); err == nil {
				st.SymLink = t
			}
		}
	}
	return &st, nil
}

// Walk implements FSView: depth-first, parent before children, bounded by
// caps. Symlinked directories are reported but never descended (policy V1).
func (r *Resolver) Walk(root *roots.Root, rel string, cb WalkFunc, caps WalkCaps) (bool, error) {
	res, err := r.Resolve(root.RefModel(), rel, false)
	if err != nil {
		return false, err
	}
	defer res.Close()
	if !res.Exists {
		return false, troveerr.New(troveerr.NotFound, "no such entry: "+res.FullPath())
	}
	if !res.IsDir() {
		st := res.Stat
		return false, cb(rel, &st)
	}
	st := res.Stat
	if err := cb(rel, &st); err != nil && err != WalkSkipDir {
		return false, err
	}
	truncated, err := walkDirFD(res.ParentFd, res.Name, rel, cb, caps, 0)
	return truncated, err
}

func walkDirFD(dirfd int, name, rel string, cb WalkFunc, caps WalkCaps, depth int) (bool, error) {
	if caps.MaxEntries <= 0 {
		caps = DefaultWalkCaps
	}
	var deadline time.Time
	if caps.TimeBudgetN > 0 {
		deadline = time.Now().Add(time.Duration(caps.TimeBudgetN))
	}

	if name == "" {
		name = "." // dirfd itself (resolving the root directory)
	}
	fd, err := OpenDirNoFollow(dirfd, name)
	if err != nil {
		// Unreadable subtree (permissions, vanished): report a partial scan
		// instead of aborting the whole walk.
		return true, nil
	}
	defer CloseFD(fd)

	entries, err := ListDir(fd)
	if err != nil {
		return false, troveerr.Wrap(troveerr.IOFailure, err, "scan dir "+rel)
	}

	var truncated bool
	for i := range entries {
		e := entries[i]
		child := e.Name
		if rel != "" {
			child = rel + "/" + e.Name
		}
		st := e.Stat
		err := cb(child, &st)
		if err == WalkSkipDir {
			continue
		}
		if err != nil {
			return truncated, err
		}
		if e.Stat.Kind == model.EntryDir {
			if depth+1 >= caps.MaxDepth {
				truncated = true
				continue
			}
			t, err := walkDirFD(fd, e.Name, child, cb, caps, depth+1)
			if err != nil {
				return truncated, err
			}
			if t {
				truncated = true
			}
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return true, nil
		}
	}
	return truncated, nil
}

// StatFS implements FSView.
func (r *Resolver) StatFS(root *roots.Root) (int64, int64, error) {
	return StatFSAt(root.Path)
}

// SeekFD seeks a raw descriptor (used after partial kernel copies).
func SeekFD(fd int, off int64) (int64, error) {
	return unix.Seek(fd, off, unix.SEEK_SET)
}

// WriteAllFD writes the full buffer to a raw fd.
func WriteAllFD(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := unix.Write(fd, b)
		if n > 0 {
			b = b[n:]
			continue
		}
		if err == unix.EINTR {
			continue
		}
		return err
	}
	return nil
}

// ReadFD reads up to len(b) bytes from a raw fd; io.EOF reports end of file.
func ReadFD(fd int, b []byte) (int, error) {
	for {
		n, err := unix.Read(fd, b)
		if err == unix.EINTR && n == 0 {
			continue
		}
		if n == 0 && err == nil {
			return 0, io.EOF
		}
		return n, err
	}
}
