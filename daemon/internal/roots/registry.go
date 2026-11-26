// Package roots maintains the registry of allowed storage roots. A root is a
// mount-backed directory with an identity: its mount id, device id, filesystem
// type and source. Operations re-verify the identity before touching data, so
// a disk swap, unmount or re-mount between plan and execution is detected
// (fail closed with MOUNT_CHANGED).
package roots

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// Root is one allowed storage root.
type Root struct {
	Ref      model.RootKind
	ID       string // share name, disk name or pool name
	Path     string // absolute mount path, e.g. /mnt/user/media
	MountID  uint32 // from /proc/self/mountinfo
	Dev      uint64 // st_dev of the mount
	FSType   string
	Source   string // mount source device
	ReadOnly bool   // mounted read-only
}

// Key returns the registry key for a root.
func (r *Root) Key() string { return fmt.Sprintf("%s:%s", r.Ref, r.ID) }

// RefModel returns the wire representation.
func (r *Root) RefModel() model.RootRef { return model.RootRef{Kind: r.Ref, ID: r.ID} }

// ProtectedShares are high-risk system shares: writes are refused (or need an
// explicit acknowledgement when the admin enables ack mode in settings).
var ProtectedShares = []string{"appdata", "domains", "system", "isos"}

// Registry holds the current root set.
type Registry struct {
	mu     sync.RWMutex
	roots  map[string]*Root
	loaded time.Time

	// discover refreshes the root set. Overridden in tests; the Linux
	// build installs the mountinfo scanner.
	discover func() (map[string]*Root, error)
}

// New creates a registry and performs the first discovery.
func New() *Registry {
	reg := &Registry{roots: map[string]*Root{}}
	reg.discover = discoverPlatform
	_ = reg.Refresh()
	return reg
}

// NewWithRoots builds a registry from an explicit root list (tests, dev mode).
func NewWithRoots(rs []*Root) *Registry {
	m := map[string]*Root{}
	for _, r := range rs {
		m[r.Key()] = r
	}
	return &Registry{
		roots:  m,
		loaded: time.Now(),
		discover: func() (map[string]*Root, error) {
			return m, nil
		},
	}
}

// Refresh re-scans mounts. Discovery failure keeps the previous set (fail
// closed: Verify will reject roots whose identity can no longer be confirmed).
func (reg *Registry) Refresh() error {
	m, err := reg.discover()
	if err != nil {
		return err
	}
	reg.mu.Lock()
	reg.roots = m
	reg.loaded = time.Now()
	reg.mu.Unlock()
	return nil
}

// Get returns the root for a typed ref, or a coded error.
func (reg *Registry) Get(ref model.RootRef) (*Root, error) {
	if ref.ID == "" || !validID(ref.ID) {
		return nil, troveerr.New(troveerr.RootNotFound, "invalid root id")
	}
	switch ref.Kind {
	case model.KindShare, model.KindDisk, model.KindPool:
	default:
		return nil, troveerr.New(troveerr.RootNotAllowed, "unknown root kind "+string(ref.Kind))
	}
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	r, ok := reg.roots[fmt.Sprintf("%s:%s", ref.Kind, ref.ID)]
	if !ok {
		return nil, troveerr.New(troveerr.RootNotFound, "root not mounted: "+ref.String())
	}
	return r, nil
}

// List returns all roots sorted by (kind, id).
func (reg *Registry) List() []*Root {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]*Root, 0, len(reg.roots))
	for _, r := range reg.roots {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ref != out[j].Ref {
			return out[i].Ref < out[j].Ref
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Verify re-checks a root's mount identity, three ways: the registry must
// still know the root, its snapshot must match the handle the caller holds
// (plan-time identity), and a live stat of the trusted mount path must agree
// with the snapshot (catches unmount/remount between refreshes).
func (reg *Registry) Verify(r *Root) error {
	reg.mu.RLock()
	stale := time.Since(reg.loaded) > 2*time.Second
	reg.mu.RUnlock()
	if stale {
		if err := reg.Refresh(); err != nil {
			return troveerr.Wrap(troveerr.MountChanged, err, "mount table refresh failed")
		}
	}
	reg.mu.RLock()
	cur, ok := reg.roots[r.Key()]
	if ok {
		if fi, err := os.Stat(r.Path); err == nil {
			if st, ok2 := fi.Sys().(*syscall.Stat_t); ok2 && uint64(st.Dev) != cur.Dev {
				reg.mu.RUnlock()
				return troveerr.New(troveerr.MountChanged,
					fmt.Sprintf("device identity changed for %s (dev %d→%d)",
						r.Key(), cur.Dev, uint64(st.Dev)))
			}
		} else {
			reg.mu.RUnlock()
			return troveerr.New(troveerr.MountChanged, "root path no longer exists: "+r.Path)
		}
	}
	defer reg.mu.RUnlock()
	if !ok {
		return troveerr.New(troveerr.MountChanged, "root no longer mounted: "+r.Key())
	}
	if cur.MountID != r.MountID || cur.Dev != r.Dev || cur.Path != r.Path {
		return troveerr.New(troveerr.MountChanged,
			fmt.Sprintf("mount identity changed for %s (id %d→%d dev %d→%d)",
				r.Key(), r.MountID, cur.MountID, r.Dev, cur.Dev))
	}
	return nil
}

// IsProtected reports whether a share root is in the protected list.
func (reg *Registry) IsProtected(r *Root) bool {
	if r.Ref != model.KindShare {
		return false
	}
	for _, p := range ProtectedShares {
		if strings.EqualFold(p, r.ID) {
			return true
		}
	}
	return false
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c == '/' || c == '\\' || c < 0x20 || c == 0x7f {
			return false
		}
	}
	return id != "." && id != ".." && !strings.HasPrefix(id, ".")
}
