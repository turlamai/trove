// Package shares manages Trove share links: token-gated download links for
// single files, optionally password-protected and expiring.
//
// Security model (docs/threat-model.md §V1.5):
//   - tokens are 256-bit random (base64url, 43 chars) — unguessable
//   - links are download-only: the public listener serves exactly one file
//     resolved through the same typed-root + containment checks as the
//     authenticated API; no listing, no directory traversal, no HTML
//   - optional password (SHA-256, constant-time compare)
//   - optional expiry; expired links resolve to 410 Gone
//   - the listener is disabled by default and must be enabled in settings
//   - every download is logged to the daemon log (audit)
package shares

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// Share is one share-link record.
type Share struct {
	Token        string       `json:"token"` // 43-char base64url
	Root         model.RootRef `json:"root"`
	RelPath      string       `json:"relPath"`
	Name         string       `json:"name"` // display basename
	CreatedAt    time.Time    `json:"createdAt"`
	ExpiresAt    *time.Time   `json:"expiresAt,omitempty"` // nil = no expiry
	PasswordHash string       `json:"passwordHash,omitempty"` // hex sha256
	Downloads    int64        `json:"downloads"`
	LastDownload *time.Time   `json:"lastDownload,omitempty"`
	Note         string       `json:"note,omitempty"`
}

// Store persists share records (JSON, atomic tmp+rename).
type Store struct {
	mu     sync.Mutex
	path   string // <dataDir>/shares.json
	shares map[string]*Share
}

// OpenStore loads (and prunes expired entries from) the share store.
// The storage directory is created on demand — a fresh install has no
// <dataDir>/trove-shares/ yet, and the first Create would otherwise fail
// with ENOENT on the tmp+rename persist.
func OpenStore(dataDir string) (*Store, error) {
	s := &Store{path: filepath.Join(dataDir, "shares.json"), shares: map[string]*Share{}}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var list []*Share
	if json.Unmarshal(raw, &list) == nil {
		for _, sh := range list {
			if sh.Token != "" && !expired(sh) {
				s.shares[sh.Token] = sh
			}
		}
	}
	return s, nil
}

func expired(sh *Share) bool {
	return sh.ExpiresAt != nil && time.Now().After(*sh.ExpiresAt)
}

func (s *Store) persistLocked() error {
	list := make([]*Share, 0, len(s.shares))
	for _, v := range s.shares {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// NewToken generates a 256-bit URL-safe token.
func NewToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// HashPassword returns the hex SHA-256 of a password (casual gate, not an
// auth secret — the link token is the primary credential).
func HashPassword(pw string) string {
	sum := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(sum[:])
}

// CheckPassword verifies a supplied password against the record's hash.
func (sh *Share) CheckPassword(pw string) bool {
	if sh.PasswordHash == "" {
		return pw == ""
	}
	if pw == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sh.PasswordHash), []byte(HashPassword(pw))) == 1
}

// Create adds a share record and persists.
func (s *Store) Create(root model.RootRef, relPath, name string, expiresAt *time.Time, password string, note string) (*Share, error) {
	token, err := NewToken()
	if err != nil {
		return nil, troveerr.Wrap(troveerr.Internal, err, "token")
	}
	sh := &Share{
		Token: token, Root: root, RelPath: relPath, Name: name,
		CreatedAt: time.Now().UTC(), ExpiresAt: expiresAt, Note: note,
	}
	if password != "" {
		sh.PasswordHash = HashPassword(password)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shares[token] = sh
	if err := s.persistLocked(); err != nil {
		delete(s.shares, token)
		return nil, err
	}
	return sh, nil
}

// List returns all non-expired records sorted by creation time.
func (s *Store) List() []*Share {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Share, 0, len(s.shares))
	for _, v := range s.shares {
		if !expired(v) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Resolve validates a token (+ optional password) and returns the record.
func (s *Store) Resolve(token, password string) (*Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh, ok := s.shares[token]
	if !ok {
		return nil, troveerr.New(troveerr.NotFound, "share link not found")
	}
	if expired(sh) {
		delete(s.shares, token)
		_ = s.persistLocked()
		return nil, troveerr.New(troveerr.NotFound, "share link expired")
	}
	if !sh.CheckPassword(password) {
		return nil, troveerr.New(troveerr.CSRFInvalid, "wrong password")
	}
	return sh, nil
}

// RecordDownload increments the download counter.
func (s *Store) RecordDownload(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh, ok := s.shares[token]; ok {
		sh.Downloads++
		now := time.Now().UTC()
		sh.LastDownload = &now
		_ = s.persistLocked()
	}
}

// Revoke removes a share link.
func (s *Store) Revoke(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.shares[token]; !ok {
		return troveerr.New(troveerr.NotFound, "share link not found")
	}
	delete(s.shares, token)
	return s.persistLocked()
}
