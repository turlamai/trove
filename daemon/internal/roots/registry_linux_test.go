//go:build linux

package roots

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// Simulates a remount: discovery returns a fresh root object with a new
// mount id; handles taken before the remount must fail Verify.
func TestVerifyDetectsRemount(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "s"), 0o755); err != nil {
		t.Fatal(err)
	}
	realDev := uint64(1)
	if st, err := os.Stat(dir); err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			realDev = uint64(sys.Dev)
		}
	}
	mk := func(dev uint64, mountID uint32) map[string]*Root {
		r := &Root{
			Ref: model.KindShare, ID: "s", Path: dir,
			Dev: dev, MountID: mountID,
		}
		return map[string]*Root{r.Key(): r}
	}
	current := mk(realDev, 1)
	reg := &Registry{
		roots:  current,
		loaded: time.Now(),
		discover: func() (map[string]*Root, error) {
			return current, nil
		},
	}

	handle, err := reg.Get(model.RootRef{Kind: model.KindShare, ID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Verify(handle); err != nil {
		t.Fatalf("verify should pass before remount: %v", err)
	}

	// remount: same device re-mounted under a new mount id
	current = mk(realDev, 42)
	reg.mu.Lock()
	reg.loaded = time.Time{} // force refresh
	reg.mu.Unlock()

	if err := reg.Verify(handle); troveerr.Of(err) != troveerr.MountChanged {
		t.Fatalf("expected MOUNT_CHANGED after remount, got %v", err)
	}

	// a handle taken AFTER the remount verifies fine
	fresh, err := reg.Get(model.RootRef{Kind: model.KindShare, ID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Verify(fresh); err != nil {
		t.Fatalf("fresh handle should verify: %v", err)
	}
}
