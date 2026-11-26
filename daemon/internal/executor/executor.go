// Package executor performs approved operations against verified file
// descriptors. Safety rules (specification §4.3):
//
//   - every file is written to a staging file in the target directory,
//     fsynced, verified, then atomically committed with renameat2 — a crash
//     or cancellation never leaves a partial target
//   - a move deletes the source only after the copy verified and the source
//     identity re-checked; a changed source is never deleted
//   - directories are removed bottom-up with removeat, which fails on any
//     leftover content — unknown new files are never destroyed
//   - symlinks and special files are skipped with warnings, never followed
//   - cancellation is cooperative (between files and per chunk); staging
//     files of uncommitted items are cleaned up by the job manager
package executor

import (
	"os"
	"strings"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/planner"
	"github.com/turlamai/trove/daemon/internal/resolver"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// stagingPrefix marks Trove's temporary files. Cleanup only ever removes
// names that this daemon created under this prefix — never user data.
const stagingPrefix = ".trove-stage-"

// Hooks are the job manager's callbacks into a running operation.
type Hooks struct {
	// Progress reports cumulative progress (throttled by the manager).
	Progress func(doneItems, doneBytes int64, current string)
	// Warning records a per-item warning; the operation continues.
	Warning func(text string)
	// Conflict records a pending conflict awaiting a user decision.
	Conflict func(c model.Conflict)
	// Check returns a non-nil error to cancel (cooperative).
	Check func() error
	// Decide resolves a conflicting target path: "skip"|"rename"|"overwrite"
	// or "" when undecided (pauses that item).
	Decide func(targetRel string) string
	// Staging tracks uncommitted staging files for cleanup.
	Staging func(dirRel, name string)
	// Committed maps target rel → true for committed items (resume runs).
	Committed map[string]bool
}

// Outcome summarizes one executor run.
type Outcome struct {
	Warnings  int
	Conflicts int
	Bytes     int64
	Items     int64
}

// Executor executes manifests through the resolver's syscall layer.
type Executor struct {
	res *resolver.Resolver
}

// New builds an executor.
func New(res *resolver.Resolver) *Executor { return &Executor{res: res} }

func (e *Executor) hooks(h *Hooks) *Hooks {
	if h == nil {
		return &Hooks{Check: func() error { return nil }}
	}
	if h.Check == nil {
		h.Check = func() error { return nil }
	}
	return h
}

// ---------------------------------------------------------------------------
// Instant actions (mkdir, rename) — synchronous, no job required
// ---------------------------------------------------------------------------

// Mkdir creates one directory inside the plan's target directory.
func (e *Executor) Mkdir(man *planner.Manifest) error {
	res, err := e.res.Resolve(man.TargetRoot, man.TargetDir, true)
	if err != nil {
		return err
	}
	defer res.Close()
	if err := resolver.MkdirAt(res.ParentFd, man.TargetName, 0775); err != nil {
		if os.IsExist(err) {
			return troveerr.New(troveerr.ConflictNeedsDecision, "already exists: "+man.TargetName)
		}
		return troveerr.Wrap(troveerr.IOFailure, err, "mkdir "+man.TargetName)
	}
	return nil
}

// RenameDecision is the resolved conflict policy for a rename.
type RenameDecision string

const (
	RenameSkip      RenameDecision = "skip"
	RenameOverwrite RenameDecision = "overwrite"
)

// Rename renames/moves one entry within a single root namespace.
func (e *Executor) Rename(man *planner.Manifest, decision RenameDecision) error {
	if man.SourceRoot != man.TargetRoot {
		return troveerr.New(troveerr.NamespaceMixForbidden, "rename across roots is not supported")
	}
	src, err := e.res.ResolveExists(man.SourceRoot, man.SourceRel, true)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := e.res.Resolve(man.TargetRoot, man.TargetDir, true)
	if err != nil {
		return err
	}
	defer dst.Close()

	name := man.TargetName
	if decision == "" {
		err = resolver.RenameAt(src.ParentFd, src.Name, dst.ParentFd, name, resolver.RenameNoReplace)
		if err == nil {
			return nil
		}
		if os.IsExist(err) {
			return troveerr.New(troveerr.ConflictNeedsDecision, "target exists: "+name)
		}
		return troveerr.Wrap(troveerr.IOFailure, err, "rename")
	}
	if decision == RenameSkip {
		return troveerr.New(troveerr.ConflictNeedsDecision, "target exists: "+name)
	}
	// Overwrite: type must match — never replace a directory with a file.
	if dstSt, err := resolver.FstatAt(dst.ParentFd, name); err == nil {
		if dstSt.Kind != src.Stat.Kind {
			return troveerr.New(troveerr.ConflictNeedsDecision,
				"target exists with a different type: "+name)
		}
	}
	if err := resolver.RenameAt(src.ParentFd, src.Name, dst.ParentFd, name, 0); err != nil {
		return troveerr.Wrap(troveerr.IOFailure, err, "rename")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers shared by all actions
// ---------------------------------------------------------------------------

func baseRel(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}
	return rel
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	if name == "" {
		return dir
	}
	return dir + "/" + name
}

func trimRel(rel, base string) string {
	if rel == base {
		return ""
	}
	return strings.TrimPrefix(rel, base+"/")
}

var _ = model.EntryDir
