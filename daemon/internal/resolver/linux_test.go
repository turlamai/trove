//go:build linux

// Kernel-containment tests on a real filesystem: symlink escape, ".."
// traversal, listing, and the openat2 probe. These are the safety-critical
// tests for the syscall layer and run on Linux CI (and Unraid itself).
package resolver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/roots"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

func setupTree(t *testing.T) (*Resolver, *roots.Root) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "share")
	if err := os.MkdirAll(filepath.Join(root, "dir", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "sub", "f.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// escape symlink inside the share
	if err := os.Symlink(base, filepath.Join(root, "dir", "escape")); err != nil {
		t.Fatal(err)
	}
	// in-root symlink
	if err := os.Symlink("dir", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	// outside victim file
	if err := os.WriteFile(filepath.Join(base, "victim.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := roots.NewWithRoots([]*roots.Root{{
		Ref: model.KindShare, ID: "s", Path: root, Dev: 1, MountID: 1,
	}})
	if !New(reg).OpenAT2Supported() {
		t.Skip("kernel lacks openat2")
	}
	return New(reg), reg.List()[0]
}

func TestContainmentTraversalRefused(t *testing.T) {
	r, root := setupTree(t)
	ref := root.RefModel()

	if _, err := r.Resolve(ref, "../victim.txt", false); troveerr.Of(err) != troveerr.PathEscape {
		t.Fatalf("expected PATH_ESCAPE, got %v", err)
	}
	// A symlink inside the tree pointing outside must NOT resolve out.
	if _, err := r.Resolve(ref, "dir/escape/victim.txt", false); err == nil {
		t.Fatal("symlink escape must be refused")
	} else if troveerr.Of(err) != troveerr.PathEscape && troveerr.Of(err) != troveerr.IOFailure {
		t.Fatalf("unexpected error class: %v", err)
	}
	// Absolute path refused by the string layer before the kernel sees it.
	if _, err := r.Resolve(ref, "/etc/passwd", false); troveerr.Of(err) != troveerr.PathEscape {
		t.Fatalf("expected PATH_ESCAPE, got %v", err)
	}
}

func TestResolveInsideRoot(t *testing.T) {
	r, root := setupTree(t)
	ref := root.RefModel()

	res, err := r.ResolveExists(ref, "dir/sub/f.txt", false)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	defer res.Close()
	if res.Stat.Kind != model.EntryFile || res.Stat.Size != 5 {
		t.Fatalf("wrong stat: %+v", res.Stat)
	}

	// root itself
	res2, err := r.Resolve(ref, "", false)
	if err != nil || !res2.IsDir() {
		t.Fatalf("root resolve failed: %v", err)
	}
	res2.Close()
}

func TestWriteRequiresMountVerification(t *testing.T) {
	r, root := setupTree(t)
	// identity stays → verify ok
	if err := r.Registry().Verify(root); err != nil {
		t.Fatalf("verify should pass: %v", err)
	}
	// a root the registry no longer knows → MOUNT_CHANGED
	ghost := *root
	ghost.ID = "gone"
	if err := r.Registry().Verify(&ghost); troveerr.Of(err) != troveerr.MountChanged {
		t.Fatalf("expected MOUNT_CHANGED for unknown root, got %v", err)
	}
}

func TestListDirEntries(t *testing.T) {
	r, root := setupTree(t)
	res, err := r.ResolveExists(root.RefModel(), "dir", false)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	fd, err := OpenDirNoFollow(res.ParentFd, res.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer CloseFD(fd)
	entries, err := ListDir(fd)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["sub"] || !names["escape"] {
		t.Fatalf("missing entries: %v", names)
	}
}

func TestOpenat2Probe(t *testing.T) {
	r, _ := setupTree(t)
	if !r.OpenAT2Supported() {
		t.Fatal("probe should succeed on Linux 5.6+")
	}
}
