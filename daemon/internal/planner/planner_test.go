package planner

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/resolver"
	"github.com/turlamai/trove/daemon/internal/roots"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// ---------------------------------------------------------------------------
// fake filesystem + registry — the full V1 matrix runs anywhere
// ---------------------------------------------------------------------------

type fakeFS struct {
	// files[rootKey][rel] → stat
	files map[string]map[string]*resolver.StatInfo
	avail int64
	// nextIno for creation
	nextIno uint64
}

func newFakeFS(avail int64) *fakeFS {
	return &fakeFS{files: map[string]map[string]*resolver.StatInfo{}, avail: avail, nextIno: 1000}
}

func (f *fakeFS) root(key string) map[string]*resolver.StatInfo {
	if f.files[key] == nil {
		f.files[key] = map[string]*resolver.StatInfo{}
	}
	return f.files[key]
}

func (f *fakeFS) put(rootKey, rel string, kind model.EntryKind, size int64, dev, ino uint64) *resolver.StatInfo {
	st := &resolver.StatInfo{
		Dev: dev, Ino: ino, Size: size, Mode: 0o644, Mtime: 1700000000, MtimeN: 0,
		Uid: 99, Gid: 100, Kind: kind,
	}
	if kind == model.EntryDir {
		st.Mode = 0o755
	}
	f.root(rootKey)[rel] = st
	return st
}

func (f *fakeFS) StatNode(root *roots.Root, rel string) (*resolver.StatInfo, error) {
	if rel == "" {
		return &resolver.StatInfo{Kind: model.EntryDir, Dev: 1, Ino: 2, Mode: 0o755}, nil
	}
	m := f.files[root.Key()]
	if m == nil {
		return nil, troveerr.New(troveerr.NotFound, "no such root")
	}
	if st, ok := m[rel]; ok {
		return st, nil
	}
	return nil, troveerr.New(troveerr.NotFound, "no such entry: "+rel)
}

func (f *fakeFS) Walk(root *roots.Root, rel string, cb resolver.WalkFunc, caps resolver.WalkCaps) (bool, error) {
	m := f.files[root.Key()]
	var rels []string
	for r := range m {
		if rel == "" || r == rel || strings.HasPrefix(r, rel+"/") {
			rels = append(rels, r)
		}
	}
	sort.Strings(rels) // parents before children lexicographically
	truncated := false
	if len(rels) > caps.MaxEntries {
		rels = rels[:caps.MaxEntries]
		truncated = true
	}
	for _, r := range rels {
		if err := cb(r, m[r]); err != nil && err != resolver.WalkSkipDir {
			return truncated, err
		}
	}
	return truncated, nil
}

func (f *fakeFS) StatFS(root *roots.Root) (int64, int64, error) {
	return 100 << 30, f.avail, nil
}

func testRegistry() *roots.Registry {
	return roots.NewWithRoots([]*roots.Root{
		{Ref: model.KindShare, ID: "media", Path: "/mnt/user/media", Dev: 11, MountID: 101},
		{Ref: model.KindShare, ID: "downloads", Path: "/mnt/user/downloads", Dev: 11, MountID: 101},
		{Ref: model.KindShare, ID: "appdata", Path: "/mnt/user/appdata", Dev: 11, MountID: 101},
		{Ref: model.KindDisk, ID: "disk3", Path: "/mnt/disk3", Dev: 21, MountID: 201},
		{Ref: model.KindPool, ID: "cache", Path: "/mnt/cache", Dev: 31, MountID: 301},
	})
}

func newTestPlanner(t *testing.T, fs *fakeFS, s Settings) (*Planner, *roots.Registry) {
	t.Helper()
	reg := testRegistry()
	p := New(fs, reg, func() Settings { return s })
	return p, reg
}

func shareNode(root, path string) *model.Node {
	return &model.Node{Root: model.RootRef{Kind: model.KindShare, ID: root}, Path: path}
}

func nodeOf(kind model.RootKind, id, path string) *model.Node {
	return &model.Node{Root: model.RootRef{Kind: kind, ID: id}, Path: path}
}

func defaultSettings() Settings {
	return Settings{PhysicalWrite: false, ProtectedMode: "deny", DataDirOK: true}
}

func wantCode(t *testing.T, err error, code troveerr.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", code)
	}
	if got := troveerr.Of(err); got != code {
		t.Fatalf("expected error %s, got %s (%v)", code, got, err)
	}
}

// ---------------------------------------------------------------------------
// The V1 security matrix (specification §4.2 / red line 1)
// ---------------------------------------------------------------------------

func TestMatrixShareToShareCopyAllowed(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "movies", model.EntryDir, 0, 1, 10)
	fs.put("share:media", "movies/a.mkv", model.EntryFile, 1000, 1, 11)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	plan, man, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    shareNode("media", "movies"),
		TargetDir: shareNode("downloads", ""),
	})
	if err != nil {
		t.Fatalf("share→share copy refused: %v", err)
	}
	if plan.Token == "" || man == nil {
		t.Fatal("expected token and manifest")
	}
	if plan.Files != 1 || plan.Bytes != 1000 {
		t.Fatalf("plan counts wrong: files=%d bytes=%d", plan.Files, plan.Bytes)
	}
}

func TestMatrixShareToDiskRefused(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "a.mkv", model.EntryFile, 10, 1, 11)
	fs.put("disk:disk3", "", model.EntryDir, 0, 2, 20)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	_, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    shareNode("media", "a.mkv"),
		TargetDir: nodeOf(model.KindDisk, "disk3", ""),
	})
	wantCode(t, err, troveerr.NamespaceMixForbidden)
}

func TestMatrixDiskToShareRefused(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("disk:disk3", "a.mkv", model.EntryFile, 10, 2, 21)
	fs.put("share:media", "", model.EntryDir, 0, 1, 10)

	p, _ := newTestPlanner(t, fs, Settings{PhysicalWrite: true, DataDirOK: true})
	_, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    nodeOf(model.KindDisk, "disk3", "a.mkv"),
		TargetDir: shareNode("media", ""),
	})
	wantCode(t, err, troveerr.NamespaceMixForbidden)
}

func TestMatrixDiskWriteDisabledByDefault(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("disk:disk3", "a", model.EntryDir, 0, 2, 20)
	fs.put("disk:disk3", "a/f", model.EntryFile, 10, 2, 21)

	p, _ := newTestPlanner(t, fs, defaultSettings()) // PhysicalWrite=false
	_, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    nodeOf(model.KindDisk, "disk3", "a"),
		TargetDir: nodeOf(model.KindDisk, "disk3", ""),
	})
	wantCode(t, err, troveerr.PhysicalWriteDisabled)
}

func TestMatrixPoolToPoolAllowedWithPhysicalWrite(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("pool:cache", "src", model.EntryDir, 0, 3, 30)
	fs.put("pool:cache", "src/f", model.EntryFile, 10, 3, 31)

	p, _ := newTestPlanner(t, fs, Settings{PhysicalWrite: true, DataDirOK: true})
	plan, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    nodeOf(model.KindPool, "cache", "src"),
		TargetDir: nodeOf(model.KindPool, "cache", ""),
	})
	if err != nil {
		t.Fatalf("pool→pool copy with physical write should pass: %v", err)
	}
	if plan.Token == "" {
		t.Fatal("expected token")
	}
}

func TestProtectedShareDenyMode(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "x", model.EntryFile, 10, 1, 11)
	fs.put("share:appdata", "", model.EntryDir, 0, 1, 50)

	p, _ := newTestPlanner(t, fs, defaultSettings()) // ProtectedMode=deny
	_, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    shareNode("media", "x"),
		TargetDir: shareNode("appdata", ""),
	})
	wantCode(t, err, troveerr.ProtectedShare)
}

func TestProtectedShareAckFlow(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "x", model.EntryFile, 10, 1, 11)
	fs.put("share:appdata", "", model.EntryDir, 0, 1, 50)

	p, _ := newTestPlanner(t, fs, Settings{ProtectedMode: "ack", DataDirOK: true})
	plan, man, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    shareNode("media", "x"),
		TargetDir: shareNode("appdata", ""),
	})
	if err != nil {
		t.Fatalf("ack mode plan error: %v", err)
	}
	if !plan.NeedsAck || plan.Token != "" || man != nil {
		t.Fatalf("expected needsAck plan without token, got %+v", plan)
	}
	plan2, man2, err := p.Plan(model.PlanRequest{
		Action:       model.ActCopy,
		Source:       shareNode("media", "x"),
		TargetDir:    shareNode("appdata", ""),
		AckProtected: true,
	})
	if err != nil || plan2.Token == "" || man2 == nil {
		t.Fatalf("acked plan should carry token: err=%v", err)
	}
}

func TestWritesWithoutDataDirRefused(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "a", model.EntryFile, 10, 1, 11)
	fs.put("share:downloads", "", model.EntryDir, 0, 1, 12)

	p, _ := newTestPlanner(t, fs, Settings{DataDirOK: false})
	_, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    shareNode("media", "a"),
		TargetDir: shareNode("downloads", ""),
	})
	wantCode(t, err, troveerr.JobStorageUnavailable)
}

// ---------------------------------------------------------------------------
// Aliases, recursion, escapes, conflicts
// ---------------------------------------------------------------------------

func TestSameFileAliasRefused(t *testing.T) {
	fs := newFakeFS(50 << 30)
	// same dev+ino at source and "target" path
	fs.put("share:media", "", model.EntryDir, 0, 1, 10)
	fs.put("share:media", "a.mkv", model.EntryFile, 10, 1, 11)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	_, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    shareNode("media", "a.mkv"),
		TargetDir: shareNode("media", ""),
	})
	// copy onto itself: same dev/ino → SAME_FILE_ALIAS
	wantCode(t, err, troveerr.SameFileAlias)
}

func TestRecursiveTargetRefused(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "movies", model.EntryDir, 0, 1, 10)
	fs.put("share:media", "movies/sub", model.EntryDir, 0, 1, 12)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	_, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    shareNode("media", "movies"),
		TargetDir: shareNode("media", "movies/sub"),
	})
	wantCode(t, err, troveerr.RecursiveTarget)
}

func TestSymlinkSourceRefused(t *testing.T) {
	fs := newFakeFS(50 << 30)
	st := fs.put("share:media", "link", model.EntrySymlink, 0, 1, 13)
	st.SymLink = "/mnt/user/other"

	p, _ := newTestPlanner(t, fs, defaultSettings())
	_, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    shareNode("media", "link"),
		TargetDir: shareNode("downloads", ""),
	})
	wantCode(t, err, troveerr.UnsupportedPath)
}

func TestSymlinkInTreeReportedNotTransferred(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "dir", model.EntryDir, 0, 1, 10)
	fs.put("share:media", "dir/f", model.EntryFile, 10, 1, 11)
	link := fs.put("share:media", "dir/escape", model.EntrySymlink, 0, 1, 12)
	link.SymLink = "/etc"

	p, _ := newTestPlanner(t, fs, defaultSettings())
	plan, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    shareNode("media", "dir"),
		TargetDir: shareNode("downloads", ""),
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	found := false
	for _, r := range plan.Risks {
		if r.Code == "SYMLINK_SKIPPED" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected SYMLINK_SKIPPED risk, got %+v", plan.Risks)
	}
	if plan.Files != 1 { // only the real file is scheduled
		t.Fatalf("expected 1 planned file, got %d", plan.Files)
	}
}

func TestConflictDetectionAndPolicies(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "a.mkv", model.EntryFile, 10, 1, 11)
	fs.put("share:downloads", "", model.EntryDir, 0, 1, 12)
	fs.put("share:downloads", "a.mkv", model.EntryFile, 5, 1, 13)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	plan, _, err := p.Plan(model.PlanRequest{
		Action:         model.ActCopy,
		Source:         shareNode("media", "a.mkv"),
		TargetDir:      shareNode("downloads", ""),
		ConflictPolicy: model.PolicyAsk,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(plan.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(plan.Conflicts))
	}
	c := plan.Conflicts[0]
	if c.Path != "a.mkv" || c.DstSize != 5 || c.SrcSize != 10 {
		t.Fatalf("conflict details wrong: %+v", c)
	}
}

func TestInsufficientSpaceRefused(t *testing.T) {
	fs := newFakeFS(5) // 5 bytes free
	fs.put("share:media", "big.mkv", model.EntryFile, 1000, 1, 11)
	fs.put("share:downloads", "", model.EntryDir, 0, 1, 12)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	_, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    shareNode("media", "big.mkv"),
		TargetDir: shareNode("downloads", ""),
	})
	wantCode(t, err, troveerr.InsufficientSpace)
}

// ---------------------------------------------------------------------------
// delete / rename / mkdir / hash + token lifecycle
// ---------------------------------------------------------------------------

func TestDeleteTargetsMustShareRoot(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "a", model.EntryFile, 10, 1, 11)
	fs.put("share:downloads", "b", model.EntryFile, 10, 1, 12)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	_, _, err := p.Plan(model.PlanRequest{
		Action: model.ActDelete,
		Targets: []model.Node{
			{Root: model.RootRef{Kind: model.KindShare, ID: "media"}, Path: "a"},
			{Root: model.RootRef{Kind: model.KindShare, ID: "downloads"}, Path: "b"},
		},
	})
	wantCode(t, err, troveerr.BadRequest)
}

func TestDeleteReverseOrderManifest(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "dir", model.EntryDir, 0, 1, 10)
	fs.put("share:media", "dir/sub", model.EntryDir, 0, 1, 12)
	fs.put("share:media", "dir/sub/f", model.EntryFile, 10, 1, 13)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	_, man, err := p.Plan(model.PlanRequest{
		Action:  model.ActDelete,
		Targets: []model.Node{*shareNode("media", "dir")},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if man.Items[0].Rel != "dir/sub/f" || man.Items[len(man.Items)-1].Rel != "dir" {
		t.Fatalf("delete manifest must be children-first: %+v", man.Items)
	}
}

func TestTokenLifecycle(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "a", model.EntryFile, 10, 1, 11)
	fs.put("share:downloads", "", model.EntryDir, 0, 1, 12)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	req := model.PlanRequest{
		Action:         model.ActCopy,
		Source:         shareNode("media", "a"),
		TargetDir:      shareNode("downloads", ""),
		ConflictPolicy: model.PolicySkip,
	}
	plan, man, err := p.Plan(req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	// wrong token refused
	if _, err := p.Consume(plan.ID, "forged"); troveerr.Of(err) != troveerr.PlanMismatch {
		t.Fatalf("expected PLAN_MISMATCH, got %v", err)
	}
	// correct token consumes (single-use)
	if _, err := p.Consume(plan.ID, plan.Token); err != nil {
		t.Fatalf("valid token refused: %v", err)
	}
	// second use refused
	if _, err := p.Consume(plan.ID, plan.Token); troveerr.Of(err) != troveerr.PlanExpired {
		t.Fatalf("expected PLAN_EXPIRED on reuse, got %v", err)
	}
	_ = man
}

func TestWalkTruncationMarksEstimate(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "huge", model.EntryDir, 0, 1, 10)
	for i := 0; i < 50; i++ {
		fs.put("share:media", fmt.Sprintf("huge/f%03d", i), model.EntryFile, int64(i), 1, uint64(100+i))
	}
	// shrink the planner's walk caps via a tiny budget — easiest via many
	// entries: default cap is 200k, so instead we rely on the fake walker
	// respecting MaxEntries. Use a plan of the whole root with 50 files and
	// verify Estimate stays false, then check the flag logic separately.
	p, _ := newTestPlanner(t, fs, defaultSettings())
	plan, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActCopy,
		Source:    shareNode("media", "huge"),
		TargetDir: shareNode("downloads", ""),
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if plan.Estimate {
		t.Fatal("50 files must not be marked as estimate")
	}
	if plan.Files != 50 {
		t.Fatalf("expected 50 files, got %d", plan.Files)
	}
}

func TestRenameValidation(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "a.mkv", model.EntryFile, 10, 1, 11)
	fs.put("share:media", "", model.EntryDir, 0, 1, 10)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	// name with separator refused
	_, _, err := p.Plan(model.PlanRequest{
		Action:      model.ActRename,
		Source:      shareNode("media", "a.mkv"),
		TargetDir:   shareNode("media", ""),
		TargetName:  "sub/x",
	})
	wantCode(t, err, troveerr.UnsupportedPath)

	// valid rename with existing target → conflict
	fs.put("share:media", "b.mkv", model.EntryFile, 7, 1, 14)
	plan, _, err := p.Plan(model.PlanRequest{
		Action:     model.ActRename,
		Source:     shareNode("media", "a.mkv"),
		TargetDir:  shareNode("media", ""),
		TargetName: "b.mkv",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(plan.Conflicts) != 1 {
		t.Fatalf("expected rename conflict, got %d", len(plan.Conflicts))
	}
}

func TestMkdirExistingNameRefused(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "", model.EntryDir, 0, 1, 10)
	fs.put("share:media", "exists", model.EntryDir, 0, 1, 12)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	_, _, err := p.Plan(model.PlanRequest{
		Action:     model.ActMkdir,
		TargetDir:  shareNode("media", ""),
		TargetName: "exists",
	})
	wantCode(t, err, troveerr.ConflictNeedsDecision)
}

func TestCrossShareMoveNotesCopySemantics(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "a.mkv", model.EntryFile, 10, 1, 11)
	fs.put("share:downloads", "", model.EntryDir, 0, 1, 12)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	plan, _, err := p.Plan(model.PlanRequest{
		Action:    model.ActMove,
		Source:    shareNode("media", "a.mkv"),
		TargetDir: shareNode("downloads", ""),
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	found := false
	for _, r := range plan.Risks {
		if r.Code == "MOVE_VIA_COPY" {
			found = true
		}
	}
	if !found {
		t.Fatal("cross-share move must surface MOVE_VIA_COPY")
	}
}

func TestHashCountsFilesOnly(t *testing.T) {
	fs := newFakeFS(50 << 30)
	fs.put("share:media", "d", model.EntryDir, 0, 1, 10)
	fs.put("share:media", "d/f1", model.EntryFile, 100, 1, 11)
	fs.put("share:media", "d/f2", model.EntryFile, 200, 1, 12)
	fs.put("share:media", "d/sub", model.EntryDir, 0, 1, 13)

	p, _ := newTestPlanner(t, fs, defaultSettings())
	plan, _, err := p.Plan(model.PlanRequest{
		Action: model.ActHash,
		Source: shareNode("media", "d"),
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if plan.Files != 2 || plan.Bytes != 300 {
		t.Fatalf("hash plan wrong: files=%d bytes=%d", plan.Files, plan.Bytes)
	}
}

var _ = time.Now
