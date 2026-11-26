//go:build linux

// Executor semantics on a real filesystem: staging commit, conflict
// policies, verified move with source delete, and delete ordering with
// leftovers.
package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/planner"
	"github.com/turlamai/trove/daemon/internal/resolver"
	"github.com/turlamai/trove/daemon/internal/roots"
)

func testEnv(t *testing.T) (*Executor, string, string) {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	dst := filepath.Join(base, "dst")
	for _, d := range []string{src, dst} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	reg := roots.NewWithRoots([]*roots.Root{
		{Ref: model.KindShare, ID: "src", Path: src, Dev: 1, MountID: 1},
		{Ref: model.KindShare, ID: "dst", Path: dst, Dev: 1, MountID: 1},
	})
	return New(resolver.New(reg)), src, dst
}

func shareRef(id string) model.RootRef { return model.RootRef{Kind: model.KindShare, ID: id} }

func write(t *testing.T, base, rel, content string) {
	t.Helper()
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func noHooks() *Hooks { return &Hooks{Committed: map[string]bool{}} }

func buildManifest(t *testing.T, e *Executor, action model.Action,
	srcRoot, srcRel, dstRootID, dstDir string, items []planner.Item) *planner.Manifest {
	t.Helper()
	return &planner.Manifest{
		Action:     action,
		SourceRoot: shareRef(srcRoot),
		SourceRel:  srcRel,
		TargetRoot: shareRef(dstRootID),
		TargetDir:  dstDir,
		Policy:     model.PolicySkip,
		Verify:     model.VerifySizeMeta,
		Items:      items,
	}
}

func statItem(t *testing.T, e *Executor, rootID, rel string, kind model.EntryKind) planner.Item {
	t.Helper()
	root, err := e.res.RootOf(shareRef(rootID))
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.res.StatNode(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	return planner.Item{
		Rel: rel, Kind: kind, Size: st.Size, Mode: st.Mode,
		MtimeS: st.Mtime, MtimeNs: int64(st.MtimeN), Dev: st.Dev, Ino: st.Ino,
	}
}

func assertNoStagingLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, en := range entries {
		if strings.Contains(en.Name(), stagingPrefix) {
			t.Fatalf("staging leftover: %s", en.Name())
		}
	}
}

func TestCopyFileStagingCommit(t *testing.T) {
	e, src, dst := testEnv(t)
	write(t, src, "a.mkv", "0123456789")
	man := buildManifest(t, e, model.ActCopy, "src", "a.mkv", "dst", "",
		[]planner.Item{statItem(t, e, "src", "a.mkv", model.EntryFile)})

	out, err := e.Transfer(man, noHooks())
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "a.mkv"))
	if err != nil || string(got) != "0123456789" {
		t.Fatalf("copy content wrong: %q err=%v", got, err)
	}
	if out.Items != 1 || out.Bytes != 10 {
		t.Fatalf("outcome wrong: %+v", out)
	}
	assertNoStagingLeftovers(t, dst)
}

func TestCopyConflictSkipAndRename(t *testing.T) {
	e, src, dst := testEnv(t)
	write(t, src, "a.txt", "new-content")
	write(t, dst, "a.txt", "old")

	man := buildManifest(t, e, model.ActCopy, "src", "a.txt", "dst", "",
		[]planner.Item{statItem(t, e, "src", "a.txt", model.EntryFile)})
	man.Policy = model.PolicySkip

	if _, err := e.Transfer(man, noHooks()); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "a.txt")); string(got) != "old" {
		t.Fatalf("skip policy must keep target: %q", got)
	}
	assertNoStagingLeftovers(t, dst)

	man2 := buildManifest(t, e, model.ActCopy, "src", "a.txt", "dst", "",
		[]planner.Item{statItem(t, e, "src", "a.txt", model.EntryFile)})
	man2.Policy = model.PolicyRename
	if _, err := e.Transfer(man2, noHooks()); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "a (1).txt")); err != nil {
		t.Fatalf("rename policy should create a (1).txt: %v", err)
	}
}

func TestCopyConflictOverwrite(t *testing.T) {
	e, src, dst := testEnv(t)
	write(t, src, "a.txt", "new-content")
	write(t, dst, "a.txt", "old")

	man := buildManifest(t, e, model.ActCopy, "src", "a.txt", "dst", "",
		[]planner.Item{statItem(t, e, "src", "a.txt", model.EntryFile)})
	man.Policy = model.PolicyOverwrite
	if _, err := e.Transfer(man, noHooks()); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "a.txt")); string(got) != "new-content" {
		t.Fatalf("overwrite policy must replace target: %q", got)
	}
	assertNoStagingLeftovers(t, dst)
}

func TestMoveSameDevAtomic(t *testing.T) {
	e, src, dst := testEnv(t)
	write(t, src, "mv.txt", "payload")

	man := buildManifest(t, e, model.ActMove, "src", "mv.txt", "dst", "",
		[]planner.Item{statItem(t, e, "src", "mv.txt", model.EntryFile)})
	if _, err := e.Transfer(man, noHooks()); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "mv.txt")); !os.IsNotExist(err) {
		t.Fatal("source must be gone after same-device move")
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "mv.txt")); string(got) != "payload" {
		t.Fatalf("moved content wrong: %q", got)
	}
}

func TestMoveSourceChangedNeverDeleted(t *testing.T) {
	e, src, dst := testEnv(t)
	write(t, src, "m.txt", "original")
	write(t, dst, "placeholder", "x")

	// Force the copy+verify+delete path by faking a different device id on
	// the destination root (registry-local; no real mount change needed).
	srcRoot, _ := e.res.RootOf(shareRef("src"))
	dstRoot, _ := e.res.RootOf(shareRef("dst"))
	dstRoot.Dev = srcRoot.Dev + 1

	it := statItem(t, e, "src", "m.txt", model.EntryFile)
	it.Ino = it.Ino + 5000 // planned identity no longer matches live file
	man := buildManifest(t, e, model.ActMove, "src", "m.txt", "dst", "", []planner.Item{it})

	out, err := e.Transfer(man, noHooks())
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	_ = out
	// identity mismatch → file skipped, never deleted
	if _, err := os.Stat(filepath.Join(src, "m.txt")); err != nil {
		t.Fatalf("changed source must remain: %v", err)
	}
	assertNoStagingLeftovers(t, dst)
}

func TestDeleteChildrenFirstAndLeftovers(t *testing.T) {
	e, src, _ := testEnv(t)
	write(t, src, "tree/sub/f.txt", "x")
	write(t, src, "tree/newfile.txt", "appeared after planning")

	man := buildManifest(t, e, model.ActDelete, "src", "tree", "src", "",
		[]planner.Item{
			statItem(t, e, "src", "tree/sub/f.txt", model.EntryFile),
			statItem(t, e, "src", "tree/sub", model.EntryDir),
			statItem(t, e, "src", "tree", model.EntryDir),
		})
	// planner delivers children-first; ensure the order encodes it
	man.Items = []planner.Item{
		man.Items[0], // tree/sub/f.txt
		man.Items[1], // tree/sub
		man.Items[2], // tree
	}
	if _, err := e.Delete(man, noHooks()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "tree/newfile.txt")); err != nil {
		t.Fatal("unplanned new file must survive")
	}
	if _, err := os.Stat(filepath.Join(src, "tree")); err != nil {
		t.Fatal("directory with leftovers must remain")
	}
	if _, err := os.Stat(filepath.Join(src, "tree/sub")); !os.IsNotExist(err) {
		t.Fatal("planned subtree should be gone")
	}
}

func TestHashComputesDigest(t *testing.T) {
	e, src, _ := testEnv(t)
	write(t, src, "h.txt", "hello trove")
	man := buildManifest(t, e, model.ActHash, "src", "h.txt", "src", "",
		[]planner.Item{statItem(t, e, "src", "h.txt", model.EntryFile)})
	man.Action = model.ActHash

	var got HashResult
	out, err := e.Hash(man, noHooks(), func(hr HashResult) { got = hr })
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if out.Items != 1 || got.SHA256 == "" {
		t.Fatalf("hash outcome wrong: %+v %+v", out, got)
	}
	// sha256("hello trove")
	const want = "4ac3abb9a4c3dd80d400b4b7c2d4654a2cb7d38f16e024a8bd34b62ca4d68943"
	if got.SHA256 != want {
		t.Fatalf("digest mismatch: got %s want %s", got.SHA256, want)
	}
}
