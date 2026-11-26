// Package resolver turns typed (root, relative path) requests into verified
// kernel-level file handles. This file defines the portable types and the
// FSView interface the planner walks with; the Linux implementation confines
// every resolution with openat2(RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS), so
// symlinks and ".." can never escape the registered root. When openat2 is
// unavailable the resolver fails closed — there is no unsafe fallback.
package resolver

import (
	"fmt"
	"os"
	"path"
	"strings"
	"sync/atomic"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/roots"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// KindFromMode classifies a stat mode into an entry kind.
func KindFromMode(mode uint32, platformFileType uint8) model.EntryKind {
	if platformFileType == fileTypeLink {
		return model.EntrySymlink
	}
	switch mode & 0170000 {
	case 0040000: // S_IFDIR
		return model.EntryDir
	case 0100000: // S_IFREG
		return model.EntryFile
	case 0120000: // S_IFLNK
		return model.EntrySymlink
	default:
		return model.EntryOther
	}
}

const fileTypeLink uint8 = 1 // sentinel used by non-syscall stat providers

// StatInfo is the portable stat snapshot used across planner/executor.
type StatInfo struct {
	Dev     uint64
	Ino     uint64
	Nlink   uint64
	Mode    uint32
	Size    int64
	Atime   int64
	AtimeN  int64
	Mtime   int64
	MtimeN  int64
	Uid     uint32
	Gid     uint32
	Kind    model.EntryKind
	SymLink string // non-empty for symlinks
}

// DirEntry is one name+stat pair from a directory scan.
type DirEntry struct {
	Name string
	Stat StatInfo
}

// Resolved is a verified resolution result holding open O_PATH descriptors.
// The caller must Close it.
type Resolved struct {
	Root     *roots.Root
	Rel      string // normalized relative path ("" = root itself)
	ParentFd int    // O_PATH fd of the parent directory of the final component
	Name     string // final component ("" when Rel == "")
	Exists   bool
	Stat     StatInfo // valid only when Exists
	Err      error    // lstat error for the final component (e.g. ENOENT)
}

// Close releases the descriptors.
func (r *Resolved) Close() {
	if r != nil && r.ParentFd >= 0 {
		CloseFD(r.ParentFd)
		r.ParentFd = -1
	}
}

// FullPath is the absolute path (display/logging only — never used for I/O).
func (r *Resolved) FullPath() string {
	if r.Rel == "" {
		return r.Root.Path
	}
	return r.Root.Path + "/" + r.Rel
}

// TargetPath is the absolute path of the final component.
func (r *Resolved) TargetPath() string {
	if r.Name == "" {
		return r.Root.Path
	}
	return path.Join(r.Root.Path, path.Dir(r.Rel), r.Name)
}

// IsDir reports whether the resolved entry is a directory.
func (r *Resolved) IsDir() bool { return r.Exists && r.Stat.Kind == model.EntryDir }

// Resolver resolves requests against the root registry.
type Resolver struct {
	reg      *roots.Registry
	openat2  atomic.Bool // false until probed OK; writes refuse when false
	probeOld atomic.Bool
}

// New builds a resolver. openat2 support is probed lazily.
func New(reg *roots.Registry) *Resolver {
	return &Resolver{reg: reg}
}

// OpenAT2Supported reports whether the kernel accepted an openat2 probe.
func (r *Resolver) OpenAT2Supported() bool {
	if r.openat2.Load() {
		return true
	}
	if r.probeOld.Swap(true) {
		return r.openat2.Load()
	}
	r.openat2.Store(probeOpenat2())
	return r.openat2.Load()
}

// FSView is the read-only filesystem surface the planner uses. The Linux
// resolver implements it; tests provide a fake.
type FSView interface {
	// StatNode lstats a node inside a root. NotFound must surface as a
	// coded troveerr.NotFound error.
	StatNode(root *roots.Root, rel string) (*StatInfo, error)
	// Walk visits every entry below (and including) rel, depth-first,
	// parents before children. Returns truncated=true when caps were hit.
	Walk(root *roots.Root, rel string, cb WalkFunc, caps WalkCaps) (truncated bool, err error)
	// StatFS reports filesystem capacity for the root's mount.
	StatFS(root *roots.Root) (total, avail int64, err error)
}

// WalkFunc receives each visited node with its root-relative path.
// Returning WalkSkipDir skips descending into the visited directory.
type WalkFunc func(rel string, st *StatInfo) error

// WalkSkipDir sentinel returned by a WalkFunc to skip a directory's children.
var WalkSkipDir = fmt.Errorf("skip dir")

// WalkCaps bound a filesystem walk.
type WalkCaps struct {
	MaxEntries  int
	MaxDepth    int
	TimeBudgetN int64 // nanoseconds; 0 = unlimited
}

// DefaultWalkCaps is the planner's scan budget.
var DefaultWalkCaps = WalkCaps{MaxEntries: 200_000, MaxDepth: 64, TimeBudgetN: 10e9}

// Resolve normalizes, authorizes and kernel-confines a (ref, rel) request.
// When write is true the kernel must support openat2, else fail closed.
func (r *Resolver) Resolve(ref model.RootRef, rel string, write bool) (*Resolved, error) {
	norm, err := NormalizeRelPath(rel)
	if err != nil {
		return nil, err
	}
	root, err := r.reg.Get(ref)
	if err != nil {
		return nil, err
	}
	if write {
		if !r.OpenAT2Supported() {
			return nil, troveerr.New(troveerr.Unsupported,
				"kernel lacks openat2; refusing writes (fail closed)")
		}
		if err := r.reg.Verify(root); err != nil {
			return nil, err
		}
	} else if err := r.reg.Verify(root); err != nil {
		return nil, err
	}

	rootFd, err := OpenRoot(root.Path)
	if err != nil {
		return nil, troveerr.Wrap(troveerr.IOFailure, err, "open root "+root.Key())
	}
	res := &Resolved{Root: root, Rel: norm, ParentFd: rootFd, Name: ""}
	if norm == "" {
		st, serr := FstatAt(rootFd, ".")
		if serr != nil {
			CloseFD(rootFd)
			return nil, troveerr.Wrap(troveerr.IOFailure, serr, "stat root")
		}
		res.Exists = true
		res.Stat = *st
		return res, nil
	}

	dir, name := splitDirFile(norm)
	// openat2 with RESOLVE_BENEATH: every component (including symlink
	// targets) must stay beneath the root directory fd; NO_MAGICLINKS blocks
	// /proc-style magic links. This is the TOCTOU anchor of the whole daemon.
	pfd, err := Openat2Beneath(rootFd, dir)
	if err != nil {
		CloseFD(rootFd)
		if isEscapeErr(err) {
			return nil, troveerr.Wrap(troveerr.PathEscape, err, "kernel containment refused "+rel)
		}
		return nil, troveerr.Wrap(troveerr.IOFailure, err, "resolve dir of "+rel)
	}
	res.ParentFd = pfd
	res.Name = name

	st, serr := FstatAt(pfd, name)
	if serr != nil {
		res.Err = serr
		if isNotExist(serr) {
			return res, nil // caller decides whether not-exists is acceptable
		}
		res.Close()
		return nil, troveerr.Wrap(troveerr.IOFailure, serr, "stat "+rel)
	}
	res.Exists = true
	res.Stat = *st
	return res, nil
}

// ResolveExists is Resolve with a mandatory existence check.
func (r *Resolver) ResolveExists(ref model.RootRef, rel string, write bool) (*Resolved, error) {
	res, err := r.Resolve(ref, rel, write)
	if err != nil {
		return nil, err
	}
	if !res.Exists {
		full := res.FullPath()
		res.Close()
		return nil, troveerr.New(troveerr.NotFound, "no such entry: "+full)
	}
	return res, nil
}

// RootOf returns the registry root for a ref.
func (r *Resolver) RootOf(ref model.RootRef) (*roots.Root, error) { return r.reg.Get(ref) }

// Registry exposes the underlying registry (capabilities reporting).
func (r *Resolver) Registry() *roots.Registry { return r.reg }

func splitDirFile(norm string) (string, string) {
	if i := strings.LastIndexByte(norm, '/'); i >= 0 {
		return norm[:i], norm[i+1:]
	}
	return ".", norm
}

func isNotExist(err error) bool { return os.IsNotExist(err) }

var _ = model.EntryDir
