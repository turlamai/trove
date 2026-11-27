// Trash: same-filesystem deletion for user shares. A delete with
// Manifest.Trash renames targets into `<shareRoot>/.trove-trash/` (atomic,
// instant, recoverable) and records original paths in a JSONL sidecar.
// Restore renames back (conflict-safe); purge deletes permanently through
// the normal children-first delete machinery (Manifest.Trash=false).
package executor

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/planner"
	"github.com/turlamai/trove/daemon/internal/resolver"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// trashDir is created at the root of each user share. Dot-prefixed dirs are
// excluded from SMB/user-share listings by Unraid convention, so trash is
// invisible to clients but fully accessible to the daemon.
const trashDir = ".trove-trash"

// trashSidecar records trash entries: one JSON object per line.
const trashSidecar = "manifest.jsonl"

// TrashRecord is one entry of a share's trash sidecar.
type TrashRecord struct {
	Name      string `json:"name"`
	OriginRel string `json:"originRel"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
	DeletedAt string `json:"deletedAt"`
}

// TrashEntryInfo is the API view of one trash entry.
type TrashEntryInfo struct {
	Name      string `json:"name"`
	OriginRel string `json:"originRel"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
	DeletedAt string `json:"deletedAt"`
}

// DeleteWithTrash moves the planned top-level targets into the share's
// trash directory. Directories move atomically as a whole (in trash mode
// the planner emits only top-level items — no walk). Identity checks run
// per target; anything that changed since planning is skipped with a
// warning and kept.
func (e *Executor) DeleteWithTrash(man *planner.Manifest, h *Hooks,
	records func([]TrashRecord)) (*Outcome, error) {

	h = e.hooks(h)
	out := &Outcome{}
	if len(man.Items) == 0 {
		return out, troveerr.New(troveerr.PlanMismatch, "delete plan lost its items")
	}
	root, err := e.res.RootOf(man.SourceRoot)
	if err != nil {
		return out, err
	}
	if err := e.res.Registry().Verify(root); err != nil {
		return out, err
	}
	if err := e.ensureTrashDir(root.RefModel()); err != nil {
		return out, err
	}
	tres, err := e.res.Resolve(man.SourceRoot, trashDir, true)
	if err != nil {
		return out, err
	}
	tfd, err := resolver.OpenResolvedDir(tres)
	if err != nil {
		tres.Close()
		return out, troveerr.Wrap(troveerr.IOFailure, err, "open trash dir")
	}

	var recordsOut []TrashRecord
	cleanup := func() {
		resolver.CloseFD(tfd)
		tres.Close()
	}

	for i := range man.Items {
		if err := h.Check(); err != nil {
			cleanup()
			return out, err
		}
		it := man.Items[i]
		res, err := e.res.Resolve(man.SourceRoot, it.Rel, true)
		if err != nil {
			if troveerr.Of(err) == troveerr.NotFound {
				h.Warning("already gone: " + it.Rel)
				continue
			}
			cleanup()
			return out, err
		}
		st, err := resolver.FstatAt(res.ParentFd, res.Name)
		if err != nil {
			res.Close()
			if os.IsNotExist(err) {
				continue
			}
			cleanup()
			return out, troveerr.Wrap(troveerr.IOFailure, err, "stat "+it.Rel)
		}
		if st.Dev != it.Dev || st.Ino != it.Ino {
			res.Close()
			h.Warning("changed since planning — not deleted: " + it.Rel)
			out.Warnings++
			continue
		}
		kind := st.Kind
		stSize := st.Size
		parentFd := res.ParentFd
		name := res.Name

		trashName := trashEntryName(it.Rel, time.Now().UTC())
		if err := resolver.RenameAt(parentFd, name, tfd, trashName, resolver.RenameNoReplace); err != nil {
			if os.IsExist(err) {
				trashName = resolver.UniqueName(tfd, trashName)
				err = resolver.RenameAt(parentFd, name, tfd, trashName, 0)
			}
			if err != nil {
				res.Close()
				cleanup()
				return out, troveerr.Wrap(troveerr.IOFailure, err, "trash "+it.Rel)
			}
		}
		res.Close()
		recordsOut = append(recordsOut, TrashRecord{
			Name:      trashName,
			OriginRel: it.Rel,
			Kind:      string(kind),
			Size:      stSize,
			DeletedAt: time.Now().UTC().Format(time.RFC3339),
		})
		out.Items++
		if kind == model.EntryFile {
			out.Bytes += stSize
		}
		h.Progress(1, stSize, it.Rel)
	}
	cleanup()

	if len(recordsOut) > 0 {
		if err := e.appendTrashSidecar(root.RefModel(), recordsOut); err != nil {
			h.Warning("trash entries moved but the sidecar record failed: "+err.Error())
			out.Warnings++
		}
	}
	if records != nil && len(recordsOut) > 0 {
		records(recordsOut)
	}
	return out, nil
}

// ensureTrashDir creates .trove-trash (mode 0700) if missing, fail-closed
// on anything unexpected (e.g. a non-directory in the way).
func (e *Executor) ensureTrashDir(root model.RootRef) error {
	res, err := e.res.Resolve(root, trashDir, true)
	if err != nil {
		return err
	}
	defer res.Close()
	if res.Exists {
		if res.Stat.Kind == model.EntryDir {
			return nil
		}
		return troveerr.New(troveerr.NotWritable, ".trove-trash exists and is not a directory")
	}
	if err := resolver.MkdirAt(res.ParentFd, res.Name, 0700); err != nil && !os.IsExist(err) {
		return troveerr.Wrap(troveerr.IOFailure, err, "create trash dir")
	}
	return nil
}

// trashEntryName derives a collision-resistant trash name:
// <utc-timestamp>-<original-basename>.
func trashEntryName(originRel string, at time.Time) string {
	base := originRel
	if i := strings.LastIndexByte(originRel, '/'); i >= 0 {
		base = originRel[i+1:]
	}
	return at.Format("20060102T150405") + "-" + base
}

// appendTrashSidecar appends records to the share trash's manifest.jsonl.
func (e *Executor) appendTrashSidecar(root model.RootRef, records []TrashRecord) error {
	res, err := e.res.Resolve(root, trashDir, true)
	if err != nil {
		return err
	}
	defer res.Close()
	dfd, err := resolver.OpenResolvedDir(res)
	if err != nil {
		return err
	}
	defer resolver.CloseFD(dfd)
	sfd, err := resolver.OpenFileNoFollow(dfd, trashSidecar,
		os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer resolver.CloseFD(sfd)
	var sb strings.Builder
	for i := range records {
		raw, err := json.Marshal(records[i])
		if err != nil {
			continue
		}
		sb.Write(raw)
		sb.WriteByte('\n')
	}
	return resolver.WriteAllFD(sfd, []byte(sb.String()))
}

// readSidecarAll loads the sidecar records of one share's trash. A missing
// sidecar is an empty record set, not an error.
func (e *Executor) readSidecarAll(tres *resolver.Resolved) ([]TrashRecord, error) {
	dfd, err := resolver.OpenResolvedDir(tres)
	if err != nil {
		return nil, err
	}
	defer resolver.CloseFD(dfd)
	sfd, err := resolver.OpenFileNoFollow(dfd, trashSidecar, os.O_RDONLY, 0)
	if err != nil {
		return nil, nil
	}
	defer resolver.CloseFD(sfd)
	raw, err := resolver.ReadAllFD(sfd)
	if err != nil {
		return nil, err
	}
	out := []TrashRecord{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r TrashRecord
		if json.Unmarshal([]byte(line), &r) == nil && r.Name != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

// TrashList returns a share's trash entries (sidecar records unioned with
// on-disk names; disk names without a record still surface).
func (e *Executor) TrashList(root model.RootRef) ([]TrashRecord, []TrashEntryInfo, error) {
	tres, err := e.res.Resolve(root, trashDir, false)
	if err != nil {
		if troveerr.Of(err) == troveerr.NotFound {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer tres.Close()
	if !tres.IsDir() {
		return nil, nil, nil
	}
	records, err := e.readSidecarAll(tres)
	if err != nil {
		return nil, nil, err
	}
	byName := map[string]TrashRecord{}
	for i := range records {
		byName[records[i].Name] = records[i]
	}
	dirFd, err := resolver.OpenResolvedDir(tres)
	if err != nil {
		return nil, nil, err
	}
	defer resolver.CloseFD(dirFd)
	entries, err := resolver.ListDir(dirFd)
	if err != nil {
		return nil, nil, err
	}
	infos := []TrashEntryInfo{}
	for i := range entries {
		en := entries[i]
		if en.Name == trashSidecar {
			continue
		}
		r, ok := byName[en.Name]
		origin, deletedAt := "", ""
		if ok {
			origin = r.OriginRel
			deletedAt = r.DeletedAt
		}
		infos = append(infos, TrashEntryInfo{
			Name:      en.Name,
			OriginRel: origin,
			Kind:      string(en.Stat.Kind),
			Size:      en.Stat.Size,
			DeletedAt: deletedAt,
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return records, infos, nil
}

// TrashRestore moves one entry back to its recorded origin (or the share
// root when the origin directory no longer exists). Name conflicts resolve
// with the standard auto-rename. Returns the restored name.
func (e *Executor) TrashRestore(root model.RootRef, name string) (string, error) {
	if name == "" || strings.Contains(name, "/") || strings.HasPrefix(name, ".") {
		return "", troveerr.New(troveerr.BadRequest, "invalid trash entry name")
	}
	tres, err := e.res.Resolve(root, trashDir, true)
	if err != nil {
		return "", err
	}
	tfd, err := resolver.OpenResolvedDir(tres)
	if err != nil {
		tres.Close()
		return "", troveerr.Wrap(troveerr.IOFailure, err, "open trash dir")
	}
	records, err := e.readSidecarAll(tres)
	originRel := ""
	if err == nil {
		for i := range records {
			if records[i].Name == name {
				originRel = records[i].OriginRel
			}
		}
	}
	parentRel, originName := "", name
	if originRel != "" {
		if i := strings.LastIndexByte(originRel, '/'); i >= 0 {
			parentRel = originRel[:i]
			originName = originRel[i+1:]
		} else {
			originName = originRel
		}
	}
	pres, err := e.res.Resolve(root, parentRel, true)
	if err != nil {
		resolver.CloseFD(tfd)
		tres.Close()
		return "", err
	}
	ofd, err := resolver.OpenResolvedDir(pres)
	if err != nil {
		pres.Close()
		resolver.CloseFD(tfd)
		tres.Close()
		return "", err
	}
	if !pres.Exists || !pres.IsDir() {
		resolver.CloseFD(ofd)
		pres.Close()
		resolver.CloseFD(tfd)
		tres.Close()
		return "", troveerr.New(troveerr.NotFound,
			"origin directory no longer exists; restore manually or purge")
	}
	if err := resolver.RenameAt(tfd, name, ofd, originName, resolver.RenameNoReplace); err != nil {
		if !os.IsExist(err) {
			resolver.CloseFD(ofd)
			pres.Close()
			resolver.CloseFD(tfd)
			tres.Close()
			return "", troveerr.Wrap(troveerr.IOFailure, err, "restore "+name)
		}
		// name conflict inside origin → auto-rename (keep both)
		alt := resolver.UniqueName(ofd, originName)
		if err2 := resolver.RenameAt(tfd, name, ofd, alt, 0); err2 != nil {
			resolver.CloseFD(ofd)
			pres.Close()
			resolver.CloseFD(tfd)
			tres.Close()
			return "", troveerr.Wrap(troveerr.IOFailure, err2, "restore "+name)
		}
		originName = alt
	}
	resolver.CloseFD(ofd)
	pres.Close()
	resolver.CloseFD(tfd)
	tres.Close()
	if err := e.removeTrashRecord(root, name); err != nil {
		return originName, nil // restored; stale sidecar line is harmless
	}
	return originName, nil
}

// removeTrashRecord rewrites the sidecar without one record (atomic tmp +
// rename inside the trash dir).
func (e *Executor) removeTrashRecord(root model.RootRef, trashName string) error {
	tres, err := e.res.Resolve(root, trashDir, true)
	if err != nil {
		return err
	}
	defer tres.Close()
	records, err := e.readSidecarAll(tres)
	if err != nil {
		return err
	}
	kept := records[:0]
	for i := range records {
		if records[i].Name != trashName {
			kept = append(kept, records[i])
		}
	}
	dfd, err := resolver.OpenResolvedDir(tres)
	if err != nil {
		return err
	}
	defer resolver.CloseFD(dfd)
	tmp := trashSidecar + ".tmp"
	sfd, err := resolver.OpenFileNoFollow(dfd, tmp,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	var sb strings.Builder
	for i := range kept {
		raw, err := json.Marshal(kept[i])
		if err != nil {
			continue
		}
		sb.Write(raw)
		sb.WriteByte('\n')
	}
	if err := resolver.WriteAllFD(sfd, []byte(sb.String())); err != nil {
		resolver.CloseFD(sfd)
		return err
	}
	resolver.CloseFD(sfd)
	return resolver.RenameAt(dfd, tmp, dfd, trashSidecar, 0)
}
