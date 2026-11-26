// Package planner is the pure decision layer of the daemon: given a typed
// operation request it validates the namespace matrix, protected shares,
// same-file aliases, recursive targets, conflicts and space — and produces a
// Plan with a short-lived HMAC confirmation token. It never touches the
// filesystem for writing; it reads through an injectable FSView so the whole
// matrix is unit-testable (tests carry the specification's V1 matrix).
package planner

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/resolver"
	"github.com/turlamai/trove/daemon/internal/roots"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// Settings are the policy inputs the daemon feeds in (from its config).
type Settings struct {
	PhysicalWrite bool   // allow writes into disk/pool roots (V1 default false)
	ProtectedMode string // "deny" (default) | "ack" (writes need explicit ack)
	DataDirOK     bool   // job persistence available — writes refuse without it
	Concurrency   int
}

// Item is one manifest entry: a node to copy/move/delete, in execution
// order (parents before children).
type Item struct {
	Rel     string
	Kind    model.EntryKind
	Size    int64
	Mode    uint32
	MtimeS  int64
	MtimeNs int64
	Dev     uint64
	Ino     uint64
	Nlink   uint64
	SymLink string
}

// Manifest is the server-side execution script for an approved plan. It is
// never sent to the client; the client only sees the public Plan.
type Manifest struct {
	PlanID      string
	Action      model.Action
	SourceRoot  model.RootRef
	SourceRel   string
	TargetRoot  model.RootRef
	TargetDir   string // target directory (relative inside TargetRoot)
	TargetName  string
	Items       []Item
	DeleteTargets []model.Node `json:"-"` // delete action retry metadata
	Policy      model.ConflictPolicy
	SourcePolic model.SourceChangedPolicy
	Verify      model.VerifyLevel
	TotalBytes  int64
	Files       int64
	Dirs        int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// canonical is the exact byte material the token binds.
type canonical struct {
	PlanID     string          `json:"id"`
	Action     model.Action    `json:"action"`
	SourceRoot model.RootRef   `json:"sr"`
	SourceRel  string          `json:"srel"`
	TargetRoot model.RootRef   `json:"tr,omitempty"`
	TargetDir  string          `json:"td,omitempty"`
	TargetName string          `json:"tn,omitempty"`
	Items      int             `json:"n"`
	Bytes      int64           `json:"b"`
	Policy     string          `json:"p,omitempty"`
	ExpiresAt  time.Time       `json:"exp"`
	Extra      json.RawMessage `json:"x,omitempty"`
}

type planEntry struct {
	manifest *Manifest
	plan     *model.Plan
}

// Planner validates operations and issues plan tokens.
type Planner struct {
	fs       resolver.FSView
	reg      *roots.Registry
	settings func() Settings
	secret   []byte

	mu    sync.Mutex
	plans map[string]planEntry // planID → entry
}

// New creates a planner. settings is called on every Plan so config changes
// apply immediately.
func New(fs resolver.FSView, reg *roots.Registry, settings func() Settings) *Planner {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	return &Planner{
		fs:       fs,
		reg:      reg,
		settings: settings,
		secret:   secret,
		plans:    map[string]planEntry{},
	}
}

// Plan executes the validation matrix. It returns the public plan (with a
// token when fully approved) and the internal manifest.
func (p *Planner) Plan(req model.PlanRequest) (*model.Plan, *Manifest, error) {
	if err := validateRequest(req); err != nil {
		return nil, nil, err
	}
	s := p.settings()

	plan := &model.Plan{
		ID:          newID(),
		Action:      req.Action,
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(10 * time.Minute),
		NamespaceOK: true,
	}

	// Global gate: background writes require persisted job storage. Without
	// a data directory the daemon degrades to read-only browsing (spec T20).
	switch req.Action {
	case model.ActCopy, model.ActMove, model.ActDelete, model.ActHash:
		if !s.DataDirOK {
			return plan, nil, troveerr.New(troveerr.JobStorageUnavailable,
				"no persistent data directory configured; write jobs are disabled")
		}
	}

	var man *Manifest
	var err error
	switch req.Action {
	case model.ActCopy, model.ActMove:
		man, err = p.planTransfer(req, plan, s)
	case model.ActDelete:
		man, err = p.planDelete(req, plan, s)
	case model.ActRename:
		man, err = p.planRename(req, plan, s)
	case model.ActMkdir:
		man, err = p.planMkdir(req, plan, s)
	case model.ActHash:
		man, err = p.planHash(req, plan)
	}
	if err != nil {
		return plan, nil, err
	}

	// Protected-share acknowledgement handling.
	if plan.NeedsAck && !req.AckProtected {
		return plan, nil, nil // plan returned without token; UI must re-ack
	}

	plan.Token = p.sign(plan, man)
	man.PlanID = plan.ID
	man.ExpiresAt = plan.ExpiresAt
	p.store(plan, man)
	return plan, man, nil
}

func validateRequest(req model.PlanRequest) error {
	if req.Source == nil && req.Action != model.ActDelete && req.Action != model.ActMkdir {
		return troveerr.New(troveerr.BadRequest, "missing source")
	}
	needTarget := req.Action == model.ActCopy || req.Action == model.ActMove ||
		req.Action == model.ActRename || req.Action == model.ActMkdir
	if needTarget && req.TargetDir == nil {
		return troveerr.New(troveerr.BadRequest, "missing target directory")
	}
	if req.Action == model.ActDelete && len(req.Targets) == 0 {
		return troveerr.New(troveerr.BadRequest, "delete requires targets")
	}
	if len(req.Targets) > 1000 {
		return troveerr.New(troveerr.BadRequest, "too many targets")
	}
	pol := req.ConflictPolicy
	switch pol {
	case "", model.PolicyAsk, model.PolicySkip, model.PolicyRename, model.PolicyOverwrite:
	default:
		return troveerr.New(troveerr.BadRequest, "unknown conflict policy")
	}
	switch req.Verify {
	case "", model.VerifySizeMeta, model.VerifySHA256:
	default:
		return troveerr.New(troveerr.BadRequest, "unknown verify level")
	}
	return nil
}

// checkNamespaces enforces the V1 security matrix:
//
//	share↔share          allowed
//	disk↔disk, pool↔pool expert mode only, gated by PhysicalWrite
//	share↔disk/pool      refused unconditionally (two views of one data)
//
// It also applies the protected-share policy and returns per-root info.
func (p *Planner) checkNamespaces(kinds []model.RootKind, refs []model.RootRef, write bool, ack bool, s Settings) error {
	kind := kinds[0]
	for _, k := range kinds {
		if k != kind {
			return troveerr.New(troveerr.NamespaceMixForbidden,
				"logical share and physical disk/pool views must not be mixed in one operation")
		}
	}
	if kind != model.KindShare && !s.PhysicalWrite {
		return troveerr.New(troveerr.PhysicalWriteDisabled,
			"writes into physical disk/pool views are disabled by default")
	}
	if write && kind == model.KindShare {
		for _, ref := range refs {
			if root, err := p.reg.Get(ref); err == nil && p.reg.IsProtected(root) {
				switch {
				case s.ProtectedMode == "ack" && !ack:
					return errNeedsAck
				case s.ProtectedMode == "ack" && ack:
					// admin enabled ack mode and the user acknowledged
				default:
					return troveerr.New(troveerr.ProtectedShare,
						"share "+ref.ID+" is protected (appdata/domains/system/isos); writes are refused")
				}
			}
		}
	}
	return nil
}

var errNeedsAck = troveerr.New(troveerr.ProtectedShare, "acknowledgement required")

func (p *Planner) planTransfer(req model.PlanRequest, plan *model.Plan, s Settings) (*Manifest, error) {
	src, dst := *req.Source, *req.TargetDir
	if req.Action == model.ActMove && src.Root == dst.Root && src.Path == dst.Path {
		return nil, troveerr.New(troveerr.SameFileAlias, "source equals target directory")
	}
	if err := p.checkNamespaces(
		[]model.RootKind{src.Root.Kind, dst.Root.Kind},
		[]model.RootRef{src.Root, dst.Root}, true, req.AckProtected, s); err != nil {
		if err == errNeedsAck {
			plan.NeedsAck = true
			plan.Risks = append(plan.Risks, model.Risk{
				Code: "PROTECTED_SHARE", Note: "writes into a protected system share"})
			return nil, nil
		}
		plan.NamespaceOK = false
		return nil, err
	}

	srcStat, err := p.fs.StatNode(p.rootOf(src.Root), src.Path)
	if err != nil {
		return nil, err
	}
	if srcStat.Kind == model.EntrySymlink {
		return nil, troveerr.New(troveerr.UnsupportedPath,
			"symbolic links are not transferred; operate on the link target instead")
	}
	if srcStat.Kind != model.EntryDir && srcStat.Kind != model.EntryFile {
		return nil, troveerr.New(troveerr.UnsupportedType, "source is a special file")
	}
	dstStat, err := p.fs.StatNode(p.rootOf(dst.Root), dst.Path)
	if err != nil {
		return nil, err
	}
	if dstStat.Kind != model.EntryDir {
		return nil, troveerr.New(troveerr.NotWritable, "target is not a directory")
	}

	// Recursive-target guard: a directory must not be copied/moved into
	// itself or one of its descendants (same root only).
	if src.Root == dst.Root && resolver.IsInside(src.Path, dst.Path) {
		return nil, troveerr.New(troveerr.RecursiveTarget,
			"target directory lies inside the source tree")
	}

	policy := req.ConflictPolicy
	if policy == "" {
		policy = model.PolicyAsk
	}
	verify := req.Verify
	if verify == "" {
		verify = model.VerifySizeMeta
	}
	man := &Manifest{
		Action:      req.Action,
		SourceRoot:  src.Root,
		SourceRel:   src.Path,
		TargetRoot:  dst.Root,
		TargetDir:   dst.Path,
		Policy:      policy,
		SourcePolic: req.SourcePolicy,
		Verify:      verify,
		Items:       []Item{{ // the source node itself, parents before children
			Rel: src.Path, Kind: srcStat.Kind, Size: srcStat.Size, Mode: srcStat.Mode,
			MtimeS: srcStat.Mtime, MtimeNs: srcStat.MtimeN, Dev: srcStat.Dev, Ino: srcStat.Ino,
		}},
		CreatedAt: time.Now(),
	}

	// Same-root subtree walk; single file needs no walk.
	if srcStat.Kind == model.EntryDir {
		truncated, err := p.fs.Walk(p.rootOf(src.Root), src.Path, func(rel string, st *resolver.StatInfo) error {
			if rel == src.Path {
				return nil
			}
			man.Items = append(man.Items, Item{
				Rel: rel, Kind: st.Kind, Size: st.Size, Mode: st.Mode,
				MtimeS: st.Mtime, MtimeNs: st.MtimeN, Dev: st.Dev, Ino: st.Ino, SymLink: st.SymLink,
			})
			return nil
		}, resolver.DefaultWalkCaps)
		if err != nil {
			return nil, err
		}
		plan.Estimate = truncated
		if truncated {
			plan.Risks = append(plan.Risks, model.Risk{
				Code: "SCAN_TRUNCATED", Note: "source tree is very large; counts are partial"})
		}
	}

	// Aggregate + risk classification.
	base := src.Path
	for i := range man.Items {
		it := man.Items[i]
		switch it.Kind {
		case model.EntryFile:
			plan.Files++
			plan.Bytes += it.Size
			if it.Size < 0 {
				plan.Bytes -= it.Size
			}
			if it.Nlink > 1 {
				plan.Risks = append(plan.Risks, model.Risk{
					Code: "HARDLINK_SPLIT", Note: "hard-linked files become independent copies"})
			}
		case model.EntryDir:
			plan.Dirs++
		case model.EntrySymlink:
			plan.Risks = append(plan.Risks, model.Risk{
				Code: "SYMLINK_SKIPPED", Note: "symbolic links are not transferred"})
		case model.EntryOther:
			plan.Risks = append(plan.Risks, model.Risk{
				Code: "UNSUPPORTED_TYPE", Note: "special files (fifo/socket/device) are skipped"})
		}
		if it.Rel != base && it.Kind == model.EntryDir {
			plan.Items++ // dirs beyond the root count once
		}
	}
	plan.Items = int64(len(man.Items))
	plan.Source = &src
	plan.TargetDir = &dst

	// Cross-root move (share A → share B) is a copy-verify-delete sequence.
	if req.Action == model.ActMove && src.Root != dst.Root {
		plan.Risks = append(plan.Risks, model.Risk{
			Code: "MOVE_VIA_COPY", Note: "cross-share move runs as copy → verify → safe delete of source"})
	}

	// Conflict scan against the destination.
	dstBase := joinRel(dst.Path, baseName(src.Path))
	if err := p.scanConflicts(dst.Root, dstBase, man, plan); err != nil {
		return nil, err
	}

	// Same-file alias: identical inode at destination (single-file case or
	// directory root alias).
	if src.Root == dst.Root && srcStat.Kind == model.EntryFile {
		dstRel := joinRel(dst.Path, baseName(src.Path))
		if st, err := p.fs.StatNode(p.rootOf(dst.Root), dstRel); err == nil {
			if st.Dev == srcStat.Dev && st.Ino == srcStat.Ino {
				return nil, troveerr.New(troveerr.SameFileAlias,
					"source and target resolve to the same file")
			}
		}
	}

	// Space preflight (copy and cross-root move only).
	if req.Action == model.ActCopy || src.Root != dst.Root {
		if _, avail, err := p.fs.StatFS(p.rootOf(dst.Root)); err == nil {
			if avail < plan.Bytes {
				return nil, troveerr.New(troveerr.InsufficientSpace,
					fmt.Sprintf("target has %d bytes free, need %d", avail, plan.Bytes))
			}
		}
		// StatFS failure is not fatal here; the executor re-checks.
	}

	man.TotalBytes = plan.Bytes
	man.Files = plan.Files
	man.Dirs = plan.Dirs
	return man, nil
}

func (p *Planner) planDelete(req model.PlanRequest, plan *model.Plan, s Settings) (*Manifest, error) {
	refs := make([]model.RootRef, 0, len(req.Targets))
	kinds := make([]model.RootKind, 0, len(req.Targets))
	for _, t := range req.Targets {
		refs = append(refs, t.Root)
		kinds = append(kinds, t.Root.Kind)
	}
	if err := p.checkNamespaces(kinds, refs, true, req.AckProtected, s); err != nil {
		if err == errNeedsAck {
			plan.NeedsAck = true
			plan.Risks = append(plan.Risks, model.Risk{
				Code: "PROTECTED_SHARE", Note: "deletes inside a protected system share"})
			return nil, nil
		}
		plan.NamespaceOK = false
		return nil, err
	}

	man := &Manifest{Action: model.ActDelete, CreatedAt: time.Now(), DeleteTargets: req.Targets}
	root := req.Targets[0].Root
	for _, t := range req.Targets {
		if t.Root != root {
			return nil, troveerr.New(troveerr.BadRequest, "delete targets must share one root")
		}
		st, err := p.fs.StatNode(p.rootOf(t.Root), t.Path)
		if err != nil {
			return nil, err
		}
		if st.Kind == model.EntrySymlink {
			man.Items = append(man.Items, Item{Rel: t.Path, Kind: st.Kind, SymLink: st.SymLink})
			continue
		}
		man.Items = append(man.Items, Item{
			Rel: t.Path, Kind: st.Kind, Size: st.Size, Mode: st.Mode,
			MtimeS: st.Mtime, MtimeNs: st.MtimeN, Dev: st.Dev, Ino: st.Ino,
		})
		if st.Kind == model.EntryDir {
			_, err := p.fs.Walk(p.rootOf(t.Root), t.Path, func(rel string, sst *resolver.StatInfo) error {
				if rel == t.Path {
					return nil
				}
				man.Items = append(man.Items, Item{
					Rel: rel, Kind: sst.Kind, Size: sst.Size, Mode: sst.Mode,
					MtimeS: sst.Mtime, MtimeNs: sst.MtimeN, Dev: sst.Dev, Ino: sst.Ino, SymLink: sst.SymLink,
				})
				return nil
			}, resolver.DefaultWalkCaps)
			if err != nil {
				return nil, err
			}
		}
	}
	// Execution order: children before parents — reverse the parent-first list.
	reverseItems(man.Items)
	for i := range man.Items {
		switch man.Items[i].Kind {
		case model.EntryFile:
			plan.Files++
			plan.Bytes += man.Items[i].Size
		case model.EntryDir:
			plan.Dirs++
		}
	}
	plan.Items = int64(len(man.Items))
	plan.TargetDir = &model.Node{Root: root}
	plan.Targets = req.Targets
	man.TotalBytes = plan.Bytes
	man.Files = plan.Files
	man.Dirs = plan.Dirs
	plan.Risks = append(plan.Risks, model.Risk{
		Code: "DELETE_NO_TRASH", Note: "deletion is permanent in V1; there is no trash bin"})
	return man, nil
}

func (p *Planner) planRename(req model.PlanRequest, plan *model.Plan, s Settings) (*Manifest, error) {
	src, dst := *req.Source, *req.TargetDir
	name, err := resolver.NormalizeName(req.TargetName)
	if err != nil {
		return nil, err
	}
	if err := p.checkNamespaces(
		[]model.RootKind{src.Root.Kind, dst.Root.Kind},
		[]model.RootRef{src.Root, dst.Root}, true, req.AckProtected, s); err != nil {
		if err == errNeedsAck {
			plan.NeedsAck = true
			return nil, nil
		}
		plan.NamespaceOK = false
		return nil, err
	}
	st, err := p.fs.StatNode(p.rootOf(src.Root), src.Path)
	if err != nil {
		return nil, err
	}
	if st.Kind != model.EntryDir && st.Kind != model.EntryFile {
		return nil, troveerr.New(troveerr.UnsupportedType, "rename supports files and directories only")
	}
	dstStat, err := p.fs.StatNode(p.rootOf(dst.Root), dst.Path)
	if err != nil || dstStat.Kind != model.EntryDir {
		return nil, troveerr.New(troveerr.NotWritable, "rename target parent must be a directory")
	}
	target := joinRel(dst.Path, name)
	if src.Root == dst.Root && target == src.Path {
		return nil, troveerr.New(troveerr.SameFileAlias, "rename source equals target")
	}
	if src.Root == dst.Root && resolver.IsInside(src.Path, target) && st.Kind == model.EntryDir {
		return nil, troveerr.New(troveerr.RecursiveTarget, "cannot rename a directory into itself")
	}
	policy := req.ConflictPolicy
	if policy == "" {
		policy = model.PolicyAsk
	}
	man := &Manifest{
		Action: req.Action, SourceRoot: src.Root, SourceRel: src.Path,
		TargetRoot: dst.Root, TargetDir: dst.Path, TargetName: name,
		Policy: policy, Items: []Item{{
			Rel: src.Path, Kind: st.Kind, Size: st.Size, Mode: st.Mode,
			MtimeS: st.Mtime, MtimeNs: st.MtimeN, Dev: st.Dev, Ino: st.Ino,
		}},
		CreatedAt: time.Now(),
	}
	plan.Source = &src
	plan.TargetDir = &dst
	plan.TargetName = name
	plan.Policy = policy
	plan.Items = 1
	if tf, err := p.fs.StatNode(p.rootOf(dst.Root), target); err == nil {
		plan.Conflicts = append(plan.Conflicts, model.Conflict{
			Path: target, SrcSize: st.Size,
			SrcMtime: time.Unix(st.Mtime, int64(st.MtimeN)),
			DstSize:  tf.Size, DstMtime: time.Unix(tf.Mtime, int64(tf.MtimeN)),
		})
	}
	return man, nil
}

func (p *Planner) planMkdir(req model.PlanRequest, plan *model.Plan, s Settings) (*Manifest, error) {
	dst := *req.TargetDir
	name, err := resolver.NormalizeName(req.TargetName)
	if err != nil {
		return nil, err
	}
	if err := p.checkNamespaces([]model.RootKind{dst.Root.Kind}, []model.RootRef{dst.Root}, true, req.AckProtected, s); err != nil {
		if err == errNeedsAck {
			plan.NeedsAck = true
			return nil, nil
		}
		plan.NamespaceOK = false
		return nil, err
	}
	dstStat, err := p.fs.StatNode(p.rootOf(dst.Root), dst.Path)
	if err != nil || dstStat.Kind != model.EntryDir {
		return nil, troveerr.New(troveerr.NotWritable, "parent must be an existing directory")
	}
	target := joinRel(dst.Path, name)
	if _, err := p.fs.StatNode(p.rootOf(dst.Root), target); err == nil {
		return nil, troveerr.New(troveerr.ConflictNeedsDecision, "target already exists: "+target)
	}
	man := &Manifest{
		Action: model.ActMkdir, TargetRoot: dst.Root, TargetDir: dst.Path,
		TargetName: name, CreatedAt: time.Now(),
	}
	plan.TargetDir = &dst
	plan.Items = 1
	return man, nil
}

func (p *Planner) planHash(req model.PlanRequest, plan *model.Plan) (*Manifest, error) {
	src := *req.Source
	st, err := p.fs.StatNode(p.rootOf(src.Root), src.Path)
	if err != nil {
		return nil, err
	}
	man := &Manifest{
		Action: model.ActHash, SourceRoot: src.Root, SourceRel: src.Path,
		Items: []Item{{
			Rel: src.Path, Kind: st.Kind, Size: st.Size, Mode: st.Mode,
			MtimeS: st.Mtime, MtimeNs: st.MtimeN, Dev: st.Dev, Ino: st.Ino,
		}},
		CreatedAt: time.Now(),
	}
	if st.Kind == model.EntryDir {
		truncated, err := p.fs.Walk(p.rootOf(src.Root), src.Path, func(rel string, sst *resolver.StatInfo) error {
			if rel == src.Path {
				return nil
			}
			man.Items = append(man.Items, Item{
				Rel: rel, Kind: sst.Kind, Size: sst.Size, Mode: sst.Mode,
				MtimeS: sst.Mtime, MtimeNs: sst.MtimeN, Dev: sst.Dev, Ino: sst.Ino,
			})
			return nil
		}, resolver.DefaultWalkCaps)
		if err != nil {
			return nil, err
		}
		plan.Estimate = truncated
	}
	files := 0
	for _, it := range man.Items {
		if it.Kind == model.EntryFile {
			files++
			plan.Bytes += it.Size
		}
	}
	if files == 0 {
		return nil, troveerr.New(troveerr.UnsupportedType, "nothing to hash")
	}
	plan.Items = int64(files)
	plan.Files = int64(files)
	plan.Source = &src
	man.TotalBytes = plan.Bytes
	man.Files = int64(files)
	return man, nil
}

// scanConflicts walks the destination prefix of a transfer and records every
// existing name.
func (p *Planner) scanConflicts(dstRoot model.RootRef, dstBase string, man *Manifest, plan *model.Plan) error {
	root := p.rootOf(dstRoot)
	for i := range man.Items {
		it := man.Items[i]
		sub := ""
		if it.Rel != man.SourceRel {
			sub = trimPrefixRel(it.Rel, man.SourceRel)
		}
		target := joinRel(dstBase, sub)
		st, err := p.fs.StatNode(root, target)
		if err != nil {
			continue // does not exist — fine
		}
		plan.Conflicts = append(plan.Conflicts, model.Conflict{
			Path:     target,
			SrcSize:  it.Size,
			SrcMtime: time.Unix(it.MtimeS, it.MtimeNs),
			DstSize:  st.Size,
			DstMtime: time.Unix(st.Mtime, int64(st.MtimeN)),
		})
	}
	return nil
}

// store keeps the manifest for the TTL window.
func (p *Planner) store(plan *model.Plan, man *Manifest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// opportunistic pruning
	now := time.Now()
	for id, e := range p.plans {
		if now.After(e.manifest.ExpiresAt) {
			delete(p.plans, id)
		}
	}
	if len(p.plans) > 256 {
		oldestID := ""
		var oldest time.Time
		for id, e := range p.plans {
			if oldest.IsZero() || e.manifest.CreatedAt.Before(oldest) {
				oldest = e.manifest.CreatedAt
				oldestID = id
			}
		}
		delete(p.plans, oldestID)
	}
	p.plans[plan.ID] = planEntry{manifest: man, plan: plan}
}

// Consume validates a plan token, returns the manifest and forgets it
// (single-use), enforcing single submission per confirmation.
func (p *Planner) Consume(planID, token string) (*Manifest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.plans[planID]
	if !ok {
		return nil, troveerr.New(troveerr.PlanExpired, "unknown or expired plan; plan again")
	}
	if time.Now().After(e.manifest.ExpiresAt) {
		delete(p.plans, planID)
		return nil, troveerr.New(troveerr.PlanExpired, "plan expired; plan again")
	}
	want := p.sign(e.plan, e.manifest)
	if !hmac.Equal([]byte(want), []byte(token)) {
		return nil, troveerr.New(troveerr.PlanMismatch, "confirmation token mismatch")
	}
	delete(p.plans, planID)
	return e.manifest, nil
}

// sign produces the HMAC token binding plan identity to manifest content.
func (p *Planner) sign(plan *model.Plan, man *Manifest) string {
	c := canonical{
		PlanID: plan.ID, Action: plan.Action,
		Items: len(man.Items), Bytes: man.TotalBytes,
		Policy: string(man.Policy), ExpiresAt: plan.ExpiresAt,
	}
	if man.SourceRoot.ID != "" {
		c.SourceRoot = man.SourceRoot
		c.SourceRel = man.SourceRel
	}
	if man.TargetRoot.ID != "" {
		c.TargetRoot = man.TargetRoot
		c.TargetDir = man.TargetDir
		c.TargetName = man.TargetName
	}
	raw, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, p.secret)
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("pln_%d_%s", time.Now().UnixMilli(), base64.RawURLEncoding.EncodeToString(b[:]))
}

var _ = binary.BigEndian
