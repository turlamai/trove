// Manager unifies the share store and the public listener behind one type
// for the API layer.
package shares

import (
	"sync"
	"time"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/resolver"
)

// Manager owns the share store and the public listener.
type Manager struct {
	mu    sync.Mutex
	res   *resolver.Resolver
	store *Store
	serv  *PublicServer
}

// NewManager wires a manager. res is used by the public listener to resolve
// and serve shared files through containment-checked fds.
func NewManager(res *resolver.Resolver, store *Store, audit func(format string, args ...any)) *Manager {
	m := &Manager{res: res, store: store}
	m.serv = NewPublicServer(res, store, audit)
	return m
}

// Reconfigure starts/stops the public listener per settings.
func (m *Manager) Reconfigure(cfg PublicConfig) {
	m.mu.Lock()
	serv := m.serv
	m.mu.Unlock()
	serv.Reconfigure(cfg)
}

// List returns active links.
func (m *Manager) List() []*Share {
	return m.store.List()
}

// Create adds a link.
func (m *Manager) Create(root model.RootRef, relPath, name string, expiresAt *time.Time, password, note string) (*Share, error) {
	return m.store.Create(root, relPath, name, expiresAt, password, note)
}

// Resolve validates a token for download.
func (m *Manager) Resolve(token, password string) (*Share, error) {
	return m.store.Resolve(token, password)
}

// RecordDownload counts a download.
func (m *Manager) RecordDownload(token string) {
	m.store.RecordDownload(token)
}

// Revoke removes a link.
func (m *Manager) Revoke(token string) error {
	return m.store.Revoke(token)
}
