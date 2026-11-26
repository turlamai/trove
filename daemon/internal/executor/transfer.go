// Transfers (copy and move): staging → fsync → verify → atomic commit, with
// the safe source-delete phase for cross-filesystem moves.
//
// fd semantics of resolver.Resolve(r, rel): ParentFd is the verified parent
// directory of the final component and Name the component itself. Directory
// bodies are therefore opened explicitly with OpenDirNoFollow before any
// operation inside them.
package executor

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"time"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/planner"
	"github.com/turlamai/trove/daemon/internal/resolver"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

const copyChunk = 1 << 20 // 1 MiB

// Transfer executes a copy or move manifest.
func (e *Executor) Transfer(man *planner.Manifest, h *Hooks) (*Outcome, error) {
	h = e.hooks(h)
	if h.Committed == nil {
		h.Committed = map[string]bool{}
	}
	out := &Outcome{}

	srcBase := baseRel(man.SourceRel)
	dstBase := joinRel(man.TargetDir, srcBase)

	srcRoot, err := e.res.RootOf(man.SourceRoot)
	if err != nil {
		return out, err
	}
	dstRoot, err := e.res.RootOf(man.TargetRoot)
	if err != nil {
		return out, err
	}
	if err := e.res.Registry().Verify(srcRoot); err != nil {
		return out, err
	}
	if err := e.res.Registry().Verify(dstRoot); err != nil {
		return out, err
	}

	sameDev := srcRoot.Dev == dstRoot.Dev
	needsCopy := man.Action == model.ActCopy || !sameDev
	if needsCopy {
		if _, avail, err := resolver.StatFSAt(dstRoot.Path); err == nil && avail < man.TotalBytes {
			return out, troveerr.New(troveerr.InsufficientSpace,
				"target filesystem has insufficient space")
		}
	}

	srcRes, err := e.res.ResolveExists(man.SourceRoot, man.SourceRel, false)
	if err != nil {
		return out, err
	}
	defer srcRes.Close()

	dstRes, err := e.res.Resolve(man.TargetRoot, man.TargetDir, true)
	if err != nil {
		return out, err
	}
	defer dstRes.Close()
	if !dstRes.Exists || !dstRes.IsDir() {
		return out, troveerr.New(troveerr.NotWritable, "target directory is missing")
	}
	// Open the target directory body (final component: no symlink follow).
	dstDirFd, err := resolver.OpenResolvedDir(dstRes)
	if err != nil {
		return out, troveerr.Wrap(troveerr.IOFailure, err, "open target dir "+man.TargetDir)
	}
	defer resolver.CloseFD(dstDirFd)

	byRel := make(map[string]planner.Item, len(man.Items))
	for i := range man.Items {
		byRel[man.Items[i].Rel] = man.Items[i]
	}

	// Root item first (parents before children).
	rootItem, planned := byRel[man.SourceRel]
	if !planned {
		return out, troveerr.New(troveerr.PlanMismatch, "plan lost its root item")
	}
	if err := e.checkIdentity(&rootItem, &srcRes.Stat, man.SourceRel, man, h); err != nil {
		return out, err
	}
	rootDst := dstBase
	switch srcRes.Stat.Kind {
	case model.EntryDir:
		if err := e.mkTargetDir(man.TargetRoot, rootDst, &rootItem, h, out); err != nil {
			return out, err
		}
	case model.EntryFile:
		n, err := e.transferEntry(srcRes, &rootItem, man.TargetRoot, dstDirFd, rootDst, man, h, out, sameDev)
		if err != nil {
			return out, err
		}
		out.Items++
		out.Bytes += n
	default:
		return out, troveerr.New(troveerr.UnsupportedType, "unsupported source type")
	}

	// Children (source directories only).
	if srcRes.IsDir() {
		_, err = e.res.Walk(srcRes.Root, srcRes.Rel, func(rel string, st *resolver.StatInfo) error {
			if rel == man.SourceRel {
				return nil
			}
			if err := h.Check(); err != nil {
				return err
			}
			dstRel := joinRel(dstBase, trimRel(rel, srcBase))
			if h.Committed[dstRel] {
				return nil
			}
			item, planned := byRel[rel]
			if !planned {
				// Appeared after planning — never transferred silently.
				h.Warning("skipped (appeared after planning): " + rel)
				out.Warnings++
				return nil
			}
			if err := e.checkIdentity(&item, st, rel, man, h); err != nil {
				return err
			}
			switch st.Kind {
			case model.EntryDir:
				return e.mkTargetDir(man.TargetRoot, dstRel, &item, h, out)
			case model.EntryFile:
				cres, err := e.res.ResolveExists(man.SourceRoot, rel, false)
				if err != nil {
					return err
				}
				defer cres.Close()
				n, err := e.transferEntry(cres, &item, man.TargetRoot, dstDirFd, dstRel, man, h, out, sameDev)
				if err != nil {
					return err
				}
				out.Items++
				out.Bytes += n
				return nil
			case model.EntrySymlink:
				h.Warning("skipped symbolic link: " + rel)
				out.Warnings++
				return nil
			default:
				h.Warning("skipped special file: " + rel)
				out.Warnings++
				return nil
			}
		}, resolver.DefaultWalkCaps)
		if err != nil {
			return out, err
		}
	}

	// Move phase 2: cross-filesystem moves delete the verified source tree.
	// Same-device moves were atomic renames — nothing left to delete.
	if man.Action == model.ActMove && !sameDev {
		if err := e.deleteSourceTree(man, h); err != nil {
			if errors.Is(err, ErrSourceKept) {
				return out, troveerr.New(troveerr.SourceChanged,
					"copy verified but parts of the source changed and were kept")
			}
			return out, err
		}
	}
	return out, nil
}

// mkTargetDir creates one destination directory (or merges into an existing
// one). The final component is created through a containment-verified parent
// descriptor, so a symlink swap in the destination cannot redirect it.
func (e *Executor) mkTargetDir(root model.RootRef, dstRel string, item *planner.Item,
	h *Hooks, out *Outcome) error {

	res, err := e.res.Resolve(root, dstRel, true)
	if err != nil {
		return err
	}
	defer res.Close()
	if res.Exists {
		if res.Stat.Kind == model.EntryDir {
			return nil // merge — compatible, no data loss
		}
		h.Warning("target exists and is not a directory — subtree skipped: " + dstRel)
		out.Warnings++
		return resolver.WalkSkipDir
	}
	mode := uint32(item.Mode) & 0775
	if mode == 0 {
		mode = 0755
	}
	if err := resolver.MkdirAt(res.ParentFd, res.Name, mode); err != nil {
		if os.IsExist(err) {
			return nil // raced creation is a merge
		}
		return troveerr.Wrap(troveerr.IOFailure, err, "mkdir "+dstRel)
	}
	return nil
}

// transferEntry moves or copies one file entry into (dstRoot, dstDirFd,
// dstRel). Returns bytes transferred.
func (e *Executor) transferEntry(srcRes *resolver.Resolved, item *planner.Item,
	dstRoot model.RootRef, dstDirFd int, dstRel string,
	man *planner.Manifest, h *Hooks, out *Outcome, sameDev bool) (int64, error) {

	dstName := baseRel(dstRel)

	// Same-filesystem move: atomic per-entry rename, no copy phase.
	if man.Action == model.ActMove && sameDev {
		err := resolver.RenameAt(srcRes.ParentFd, srcRes.Name, dstDirFd, dstName,
			resolver.RenameNoReplace)
		if os.IsExist(err) {
			resolved, ok := e.resolveConflict(dstRel, dstName, &srcRes.Stat, dstDirFd, man, h, out)
			if !ok {
				return 0, nil
			}
			if resolved == dstName {
				// overwrite decision: atomic replace
				err = resolver.RenameAt(srcRes.ParentFd, srcRes.Name, dstDirFd, dstName, 0)
			} else {
				err = resolver.RenameAt(srcRes.ParentFd, srcRes.Name, dstDirFd, resolved,
					resolver.RenameNoReplace)
			}
		}
		if err != nil {
			return 0, troveerr.Wrap(troveerr.IOFailure, err, "move "+srcRes.Rel)
		}
		h.Committed[dstRel] = true
		h.Progress(1, srcRes.Stat.Size, srcRes.Rel)
		return srcRes.Stat.Size, nil
	}

	// Copy path.
	sfd, err := resolver.OpenFileNoFollow(srcRes.ParentFd, srcRes.Name, os.O_RDONLY, 0)
	if err != nil {
		return 0, troveerr.Wrap(troveerr.IOFailure, err, "open source "+srcRes.Rel)
	}
	defer resolver.CloseFD(sfd)

	target := dstName
	if _, err := resolver.FstatAt(dstDirFd, dstName); err == nil {
		resolved, ok := e.resolveConflict(dstRel, dstName, &srcRes.Stat, dstDirFd, man, h, out)
		if !ok {
			return 0, nil
		}
		target = resolved
	}

	stagingName := stagingPrefix + dstName + "." + shortID()
	tfd, err := resolver.CreateFileExclusive(dstDirFd, stagingName, uint32(item.Mode)&0777)
	if err != nil {
		return 0, troveerr.Wrap(troveerr.IOFailure, err, "create staging in "+dstRel)
	}
	h.Staging(dstRelDir(dstRel), stagingName)
	committed := false
	defer func() {
		if tfd >= 0 {
			resolver.CloseFD(tfd)
		}
		if !committed {
			e.removeStaging(dstRoot, dstRelDir(dstRel), stagingName)
		}
	}()

	n, err := e.pump(tfd, sfd, srcRes.Stat.Size, man.Verify, h, srcRes.Rel)
	if err != nil {
		return n, err
	}

	// Verify the staged bytes before any commit.
	vst, err := resolver.FstatAt(dstDirFd, stagingName)
	if err != nil {
		return n, troveerr.Wrap(troveerr.IOFailure, err, "stat staging "+dstRel)
	}
	if vst.Size != srcRes.Stat.Size {
		return n, troveerr.New(troveerr.IOFailure, "staged size mismatch for "+dstRel)
	}
	if err := resolver.SetTimesFD(tfd, &srcRes.Stat); err != nil {
		h.Warning("could not preserve timestamps: " + dstRel)
		out.Warnings++
	}
	_ = resolver.FchownFD(tfd, srcRes.Stat.Uid, srcRes.Stat.Gid)
	if err := resolver.FsyncFD(tfd); err != nil {
		return n, troveerr.Wrap(troveerr.IOFailure, err, "fsync staging "+dstRel)
	}
	resolver.CloseFD(tfd)
	tfd = -1

	// Commit — atomic rename; NOREPLACE re-checks the race window.
	err = resolver.RenameAt(dstDirFd, stagingName, dstDirFd, target, resolver.RenameNoReplace)
	if os.IsExist(err) {
		// A conflicting target appeared between pre-check and commit.
		resolved, ok := e.resolveConflict(dstRel, dstName, &srcRes.Stat, dstDirFd, man, h, out)
		if !ok {
			e.removeStaging(dstRoot, dstRelDir(dstRel), stagingName)
			h.Staging("", "")
			return n, nil
		}
		if resolved == dstName {
			// overwrite decision: atomic replace of the fully verified file
			err = resolver.RenameAt(dstDirFd, stagingName, dstDirFd, dstName, 0)
		} else {
			err = resolver.RenameAt(dstDirFd, stagingName, dstDirFd, resolved, resolver.RenameNoReplace)
		}
	}
	if err != nil {
		return n, troveerr.Wrap(troveerr.IOFailure, err, "commit "+dstRel)
	}
	committed = true
	h.Staging("", "") // released
	h.Committed[dstRel] = true
	h.Progress(1, n, srcRes.Rel)
	return n, nil
}

// dstRelDir returns the directory part of a target rel (where staging files
// live).
func dstRelDir(dstRel string) string {
	for i := len(dstRel) - 1; i >= 0; i-- {
		if dstRel[i] == '/' {
			return dstRel[:i]
		}
	}
	return ""
}

// resolveConflict applies policy/decision for an existing target and returns
// the target name to use (ok=false → item skipped or left pending).
func (e *Executor) resolveConflict(dstRel, dstName string, st *resolver.StatInfo,
	dstDirFd int, man *planner.Manifest, h *Hooks, out *Outcome) (string, bool) {

	decide := ""
	if h.Decide != nil {
		decide = h.Decide(dstRel)
	}
	policy := man.Policy
	if decide != "" {
		policy = model.ConflictPolicy(decide)
	}

	c := model.Conflict{Path: dstRel, SrcSize: st.Size, SrcMtime: unixTime(st.Mtime, int64(st.MtimeN))}
	if existing, err := resolver.FstatAt(dstDirFd, dstName); err == nil {
		c.DstSize = existing.Size
		c.DstMtime = unixTime(existing.Mtime, existing.MtimeN)
		// Type mismatch (file ↔ directory) is never overwritable.
		if st.Kind != existing.Kind {
			c.Resolved = "type-mismatch"
			h.Conflict(c)
			out.Conflicts++
			out.Warnings++
			h.Warning("target has a different type — kept, not overwritten: " + dstRel)
			return "", false
		}
	}

	switch policy {
	case model.PolicyOverwrite:
		c.Resolved = string(model.PolicyOverwrite)
		out.Conflicts++
		return dstName, true
	case model.PolicySkip:
		c.Resolved = string(model.PolicySkip)
		h.Conflict(c)
		out.Conflicts++
		out.Warnings++
		h.Warning("skipped existing target: " + dstRel)
		return "", false
	case model.PolicyRename:
		c.Resolved = string(model.PolicyRename)
		out.Conflicts++
		return uniqueName(dstDirFd, dstName), true
	default: // ask
		if decide == "" {
			c.Resolved = ""
			h.Conflict(c) // stays pending; the job pauses afterwards
			out.Conflicts++
			return "", false
		}
		// A decided-but-unknown value is treated conservatively as skip.
		c.Resolved = string(model.PolicySkip)
		h.Conflict(c)
		out.Conflicts++
		return "", false
	}
}

// pump copies exactly size bytes src→dst, reporting byte progress.
func (e *Executor) pump(dst, src int, size int64, verify model.VerifyLevel, h *Hooks, label string) (int64, error) {
	var done int64
	if verify != model.VerifySHA256 {
		n, err := resolver.CopyRange(dst, src, size)
		done = n
		if n > 0 {
			h.Progress(0, n, label)
		}
		if err == nil {
			return done, nil
		}
		if err != io.EOF && err != io.ErrNoProgress {
			return done, troveerr.Wrap(troveerr.IOFailure, err, "copy "+label)
		}
		// Kernel path unsupported or short — continue in userspace from n.
		if n > 0 && n < size {
			if _, err := resolver.SeekFD(src, n); err != nil {
				return done, troveerr.Wrap(troveerr.IOFailure, err, "seek "+label)
			}
		}
	}

	buf := make([]byte, copyChunk)
	for done < size {
		if err := h.Check(); err != nil {
			return done, err
		}
		want := size - done
		if want > copyChunk {
			want = copyChunk
		}
		nr, er := readFull(src, buf[:want])
		if nr > 0 {
			if err := resolver.WriteAllFD(dst, buf[:nr]); err != nil {
				return done, troveerr.Wrap(troveerr.IOFailure, err, "write "+label)
			}
			done += int64(nr)
			h.Progress(0, int64(nr), label)
		}
		if er == io.EOF {
			break
		}
		if er != nil {
			return done, troveerr.Wrap(troveerr.IOFailure, er, "read "+label)
		}
	}
	if done != size {
		return done, troveerr.New(troveerr.IOFailure, "short read "+label)
	}
	return done, nil
}

// deleteSourceTree removes planned source entries after a verified copy,
// children first, re-checking identity per item.
func (e *Executor) deleteSourceTree(man *planner.Manifest, h *Hooks) error {
	kept := false
	items := man.Items
	for i := len(items) - 1; i >= 0; i-- {
		if err := h.Check(); err != nil {
			return err
		}
		it := items[i]
		res, err := e.res.Resolve(man.SourceRoot, it.Rel, true)
		if err != nil {
			if troveerr.Of(err) == troveerr.NotFound {
				continue
			}
			return err
		}
		st, err := resolver.FstatAt(res.ParentFd, res.Name)
		if err != nil {
			res.Close()
			if os.IsNotExist(err) {
				continue
			}
			return troveerr.Wrap(troveerr.IOFailure, err, "re-check source "+it.Rel)
		}
		if st.Dev != it.Dev || st.Ino != it.Ino {
			res.Close()
			h.Warning("source changed after copy — not deleted: " + it.Rel)
			kept = true
			continue
		}
		err = deleteOneEntry(res, st, it, h)
		res.Close()
		if err != nil {
			if errors.Is(err, ErrSourceKept) {
				kept = true
				continue
			}
			return err
		}
	}
	if kept {
		return ErrSourceKept
	}
	return nil
}

func deleteOneEntry(res *resolver.Resolved, st *resolver.StatInfo, it planner.Item, h *Hooks) error {
	switch st.Kind {
	case model.EntryDir:
		if err := resolver.RmdirAt(res.ParentFd, res.Name); err != nil {
			if resolver.ENOTEmpty(err) {
				h.Warning("directory gained new content after copy — left intact: " + it.Rel)
				return ErrSourceKept
			}
			return troveerr.Wrap(troveerr.IOFailure, err, "rmdir source "+it.Rel)
		}
	case model.EntryFile, model.EntrySymlink:
		if err := resolver.UnlinkAt(res.ParentFd, res.Name); err != nil && !os.IsNotExist(err) {
			return troveerr.Wrap(troveerr.IOFailure, err, "unlink source "+it.Rel)
		}
	default:
		h.Warning("special file left in place: " + it.Rel)
		return ErrSourceKept
	}
	return nil
}

// removeStaging deletes one leftover staging file (best-effort).
func (e *Executor) removeStaging(root model.RootRef, dirRel, name string) {
	if name == "" {
		return
	}
	var res *resolver.Resolved
	var err error
	if dirRel == "" {
		// Staging file at the root itself.
		if res, err = e.res.Resolve(root, name, true); err != nil {
			return
		}
		defer res.Close()
		_ = resolver.UnlinkAt(res.ParentFd, res.Name)
		return
	}
	if res, err = e.res.Resolve(root, dirRel, true); err != nil {
		return
	}
	defer res.Close()
	dirFd, err := resolver.OpenDirNoFollow(res.ParentFd, res.Name)
	if err != nil {
		return
	}
	defer resolver.CloseFD(dirFd)
	_ = resolver.UnlinkAt(dirFd, name)
}

// CleanupStaging removes all pending staging files of an aborted job.
func (e *Executor) CleanupStaging(pending []StagingRef) {
	for _, p := range pending {
		e.removeStaging(p.Root, p.DirRel, p.Name)
	}
}

// StagingRef identifies one uncommitted staging file.
type StagingRef struct {
	Root   model.RootRef
	DirRel string
	Name   string
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func shortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// uniqueName finds "name (2)", "name (3)", … not present in dir.
func uniqueName(dirfd int, name string) string {
	ext := ""
	base := name
	if i := lastDot(name); i > 0 {
		base, ext = name[:i], name[i:]
	}
	for n := 1; n < 1000; n++ {
		cand := base + " (" + itoa(n) + ")" + ext
		if _, err := resolver.FstatAt(dirfd, cand); err != nil {
			return cand
		}
	}
	return base + " (" + shortID() + ")" + ext
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func unixTime(sec, nsec int64) time.Time {
	return time.Unix(sec, nsec)
}

func readFull(fd int, b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := resolver.ReadFD(fd, b[total:])
		total += n
		if err != nil {
			if err == io.EOF && total > 0 {
				return total, nil
			}
			return total, err
		}
		if n == 0 {
			if total > 0 {
				return total, nil
			}
			return 0, io.EOF
		}
	}
	return total, nil
}
