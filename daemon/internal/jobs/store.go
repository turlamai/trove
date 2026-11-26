// Package jobs runs background file operations with a persisted journal so
// task state survives page closes and daemon restarts (specification §5).
// Persistence is an atomic JSON snapshot (state.json) plus an append-only
// event journal (events.jsonl) in the configured data directory — never on
// the USB flash boot device.
package jobs

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/turlamai/trove/daemon/internal/model"
)

// Store persists job snapshots and events.
type Store struct {
	dir      string
	mu       sync.Mutex
	events   *os.File
	eventsW  *bufio.Writer
	seq      int64
}

// Open prepares the journal directory.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	return &Store{dir: dir, events: f, eventsW: bufio.NewWriterSize(f, 32*1024)}, nil
}

// Dir returns the journal directory.
func (s *Store) Dir() string { return s.dir }

// LoadState reads the last persisted snapshot.
func (s *Store) LoadState() ([]*model.Job, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var snap struct {
		Version int         `json:"version"`
		Jobs    []*model.Job `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	return snap.Jobs, nil
}

// SaveState atomically persists the job snapshot (tmp + rename + dir fsync).
func (s *Store) SaveState(jobs []*model.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := struct {
		Version int         `json:"version"`
		SavedAt time.Time   `json:"savedAt"`
		Jobs    []*model.Job `json:"jobs"`
	}{Version: 1, SavedAt: time.Now(), Jobs: jobs}
	raw, err := json.Marshal(&snap)
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, ".state.json.tmp")
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, "state.json")); err != nil {
		return err
	}
	return syncDir(s.dir)
}

// NextSeq allocates a journal sequence number.
func (s *Store) NextSeq() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// AppendEvent appends one event to the journal (buffered, fsynced on demand).
func (s *Store) AppendEvent(ev *model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := s.eventsW.Write(raw); err != nil {
		return err
	}
	if err := s.eventsW.WriteByte('\n'); err != nil {
		return err
	}
	return s.eventsW.Flush()
}

// Sync forces the journal to disk (used at shutdown and job completion).
func (s *Store) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.eventsW.Flush(); err != nil {
		return err
	}
	return s.events.Sync()
}

// Close flushes and closes the journal.
func (s *Store) Close() error {
	_ = s.Sync()
	return s.events.Close()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	// Best effort; some filesystems refuse directory fsync.
	_ = d.Sync()
	return nil
}

// ReadEvents returns events for one job with Seq > since (tail scan of the
// journal; the manager prunes so this stays small).
func (s *Store) ReadEvents(jobID string, since int64, limit int) ([]model.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(s.dir, "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []model.Event{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if line == "" {
			continue
		}
		var ev model.Event
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.JobID != jobID || ev.Seq <= since {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
