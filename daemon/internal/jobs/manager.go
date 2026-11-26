// Manager: queues, runs and journals background jobs. Concurrency is
// serialized by default (one large write job at a time); cancel is
// cooperative; conflicts pause a job in needs_attention until the user
// resolves them.
package jobs

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/turlamai/trove/daemon/internal/executor"
	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/planner"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// Manager runs jobs.
type Manager struct {
	mu    sync.Mutex
	cond  *sync.Cond
	jobs  map[string]*runtime
	order []string // insertion order for FIFO
	byReq map[string]string
	seq   int64

	exec   *executor.Executor
	store  *Store
	sem    chan struct{}
	stopping bool

	// decisions for jobs in needs_attention: jobID → target rel → action
	decided map[string]map[string]string
}

type runtime struct {
	job      *model.Job
	manifest *planner.Manifest
	cancel   bool
	// current staging file (for cleanup on abort)
	staging    *executor.StagingRef
	committed  map[string]bool
	lastProg   time.Time
	hashOffset int
}

// NewManager creates the manager. Concurrency is clamped to [1,4].
func NewManager(exec *executor.Executor, store *Store, concurrency int) *Manager {
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 4 {
		concurrency = 4
	}
	m := &Manager{
		jobs:    map[string]*runtime{},
		byReq:   map[string]string{},
		exec:    exec,
		store:   store,
		sem:     make(chan struct{}, concurrency),
		decided: map[string]map[string]string{},
	}
	m.cond = sync.NewCond(&m.mu)
	go m.loop()
	return m
}

// LoadState restores persisted jobs at startup. Non-terminal jobs become
// failed_retryable: their in-memory manifests are gone, so a retry re-plans
// from the recorded source/target (never resumes blind).
func (m *Manager) LoadState() error {
	jobs, err := m.store.LoadState()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range jobs {
		rt := &runtime{job: j, committed: map[string]bool{}}
		switch j.State {
		case model.JobQueued, model.JobRunning, model.JobVerifying, model.JobCancelRequested,
			model.JobNeedsAttention:
			j.State = model.JobRetryable
			j.Message = "interrupted by daemon restart; retry re-plans and re-verifies"
			j.Cancelable = false
		}
		m.jobs[j.ID] = rt
		m.order = append(m.order, j.ID)
		if j.ClientReqID != "" {
			m.byReq[j.ClientReqID] = j.ID
		}
	}
	_ = m.persistLocked()
	return nil
}

// Submit validates the plan token and enqueues a job. Idempotent per
// clientRequestId.
func (m *Manager) Submit(man *planner.Manifest, plan *model.Plan, clientReqID string) (*model.Job, error) {
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return nil, troveerr.New(troveerr.NotWritable, "daemon is shutting down")
	}
	if clientReqID != "" {
		if id, ok := m.byReq[clientReqID]; ok {
			m.mu.Unlock()
			return m.jobs[id].job, troveerr.New(troveerr.DuplicateRequest, "clientRequestId already submitted")
		}
	}
	now := time.Now()
	job := &model.Job{
		ID:          newJobID(),
		ClientReqID: clientReqID,
		Action:      man.Action,
		State:       model.JobQueued,
		CreatedAt:   now,
		Plan:        plan,
		TotalItems:  int64(len(man.Items)),
		TotalBytes:  man.TotalBytes,
		Cancelable:  true,
	}
	rt := &runtime{job: job, manifest: man, committed: map[string]bool{}}
	m.jobs[job.ID] = rt
	m.order = append(m.order, job.ID)
	if clientReqID != "" {
		m.byReq[clientReqID] = job.ID
	}
	m.mu.Unlock()

	m.emit(rt, "state", "", "job queued")
	m.cond.Signal()
	return job, nil
}

// Get returns a job snapshot.
func (m *Manager) Get(id string) (*model.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.jobs[id]
	if !ok {
		return nil, troveerr.New(troveerr.NotFound, "no such job")
	}
	return rt.job, nil
}

// List returns jobs newest first.
func (m *Manager) List(limit int) []*model.Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.Job, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.jobs[m.order[i]].job)
	}
	return out
}

// Cancel requests cooperative cancellation.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.jobs[id]
	if !ok {
		return troveerr.New(troveerr.NotFound, "no such job")
	}
	switch rt.job.State {
	case model.JobQueued:
		rt.job.State = model.JobCancelled
		now := time.Now()
		rt.job.EndedAt = &now
		rt.job.Message = "cancelled before start"
	case model.JobRunning, model.JobVerifying:
		rt.cancel = true
		rt.job.State = model.JobCancelRequested
	case model.JobNeedsAttention:
		rt.job.State = model.JobCancelled
		now := time.Now()
		rt.job.EndedAt = &now
		rt.job.Message = "cancelled with unresolved conflicts; copied data remains, source intact"
	default:
		return troveerr.New(troveerr.CancelDenied, "job is not cancelable in state "+string(rt.job.State))
	}
	m.emitLocked(rt, "state", "", "cancel requested")
	_ = m.persistLocked()
	m.cond.Signal()
	return nil
}

// Resolve applies user decisions to a paused job and resumes it.
func (m *Manager) Resolve(id string, decisions map[string]string) error {
	m.mu.Lock()
	rt, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return troveerr.New(troveerr.NotFound, "no such job")
	}
	if rt.job.State != model.JobNeedsAttention {
		m.mu.Unlock()
		return troveerr.New(troveerr.CancelDenied, "job is not awaiting decisions")
	}
	d := m.decided[id]
	if d == nil {
		d = map[string]string{}
		m.decided[id] = d
	}
	for path, res := range decisions {
		switch res {
		case string(model.PolicySkip), string(model.PolicyRename), string(model.PolicyOverwrite):
			d[path] = res
		default:
			m.mu.Unlock()
			return troveerr.New(troveerr.BadRequest, "unknown resolution "+res)
		}
	}
	// Drop resolved conflicts from the pending list.
	pending := rt.job.Conflicts[:0]
	for _, c := range rt.job.Conflicts {
		if d[c.Path] == "" {
			pending = append(pending, c)
		}
	}
	rt.job.Conflicts = pending
	if len(pending) == 0 {
		rt.job.State = model.JobQueued
	}
	m.mu.Unlock()
	m.emit(rt, "state", "", "conflicts resolved; resumed")
	m.cond.Signal()
	return nil
}

// Retry re-plans a retryable job and submits a fresh one (the old record
// stays as history). Returns the new job.
func (m *Manager) Retry(id string, replan func(model.PlanRequest) (*planner.Manifest, *model.Plan, error)) (*model.Job, error) {
	m.mu.Lock()
	rt, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return nil, troveerr.New(troveerr.NotFound, "no such job")
	}
	if rt.job.State != model.JobRetryable {
		m.mu.Unlock()
		return nil, troveerr.New(troveerr.CancelDenied, "job is not retryable")
	}
	old := rt.job
	m.mu.Unlock()

	req := planRequestOf(old)
	man, plan, err := replan(req)
	if err != nil {
		return nil, err
	}
	return m.Submit(man, plan, old.ClientReqID+"#r"+fmt.Sprint(time.Now().UnixNano()%1000000))
}

func planRequestOf(j *model.Job) model.PlanRequest {
	req := model.PlanRequest{
		Action:         j.Action,
		ConflictPolicy: model.PolicySkip,
		Verify:         model.VerifySizeMeta,
	}
	if j.Plan != nil {
		req.Source = j.Plan.Source
		req.TargetDir = j.Plan.TargetDir
		req.Targets = j.Plan.Targets
		req.TargetName = j.Plan.TargetName
		if j.Plan.Policy != "" {
			req.ConflictPolicy = j.Plan.Policy
		}
	}
	return req
}

// Close stops accepting work and waits briefly for running jobs.
func (m *Manager) Close() {
	m.mu.Lock()
	m.stopping = true
	m.cond.Broadcast()
	m.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		busy := false
		for _, rt := range m.jobs {
			if rt.job.State == model.JobRunning || rt.job.State == model.JobVerifying {
				busy = true
			}
		}
		m.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = m.store.Sync()
}

// ---------------------------------------------------------------------------
// worker loop
// ---------------------------------------------------------------------------

// loop dispatches queued jobs. One unlock per lock, strictly: a double
// unlock throws a fatal error that kills the whole daemon (this exact bug
// shipped once and was caught by real-machine acceptance).
func (m *Manager) loop() {
	for {
		m.mu.Lock()
		id, ok := m.nextQueuedLocked()
		if !ok {
			m.cond.Wait()
			m.mu.Unlock()
			continue
		}
		m.mu.Unlock()
		m.sem <- struct{}{}
		go func(id string) {
			defer func() { <-m.sem }()
			m.run(id)
			// a finished job may unblock the next queued one
			m.mu.Lock()
			m.cond.Signal()
			m.mu.Unlock()
		}(id)
	}
}

func (m *Manager) nextQueuedLocked() (string, bool) {
	for _, id := range m.order {
		if m.jobs[id].job.State == model.JobQueued {
			return id, true
		}
	}
	return "", false
}

func (m *Manager) run(id string) {
	// A panic in an executor must never take the daemon down: capture it
	// into the job record (message + stack) so it surfaces in the UI.
	defer func() {
		if r := recover(); r != nil {
			m.mu.Lock()
			rt, ok := m.jobs[id]
			if ok {
				now := time.Now()
				rt.job.EndedAt = &now
				rt.job.State = model.JobFailed
				rt.job.ErrorCode = string(troveerr.Internal)
				rt.job.Message = fmt.Sprintf("executor panic: %v", r)
				rt.job.CurrentFile = ""
				m.mu.Unlock()
				m.emit(rt, "error", string(troveerr.Internal), rt.job.Message)
				_ = m.store.Sync()
				return
			}
			m.mu.Unlock()
		}
	}()
	m.mu.Lock()
	rt, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	now := time.Now()
	rt.job.StartedAt = &now
	rt.job.State = model.JobRunning
	rt.job.CurrentFile = ""
	man := rt.manifest
	m.mu.Unlock()
	m.emit(rt, "state", "", "job started")

	var runErr error
	var outcome *executor.Outcome

	switch man.Action {
	case model.ActCopy, model.ActMove:
		outcome, runErr = m.exec.Transfer(man, m.hooks(rt))
	case model.ActDelete:
		outcome, runErr = m.exec.Delete(man, m.hooks(rt))
	case model.ActHash:
		outcome, runErr = m.exec.Hash(man, m.hooks(rt), func(hr executor.HashResult) {
			m.mu.Lock()
			rt.job.Hashes = append(rt.job.Hashes, model.HashSpec{
				Rel: hr.Rel, Size: hr.Size, SHA256: hr.SHA256})
			m.mu.Unlock()
		})
	default:
		runErr = troveerr.New(troveerr.Internal, "unrunnable action "+string(man.Action))
	}

	// Staging leftovers on failure/cancel.
	m.mu.Lock()
	pending := []executor.StagingRef{}
	if rt.staging != nil && rt.staging.Name != "" {
		pending = append(pending, *rt.staging)
	}
	rt.staging = nil
	state := rt.job.State
	m.mu.Unlock()
	if runErr != nil || state == model.JobCancelRequested {
		m.exec.CleanupStaging(pending)
	}

	m.finish(rt, outcome, runErr)
}

func (m *Manager) hooks(rt *runtime) *executor.Hooks {
	return &executor.Hooks{
		Progress: func(dItems, dBytes int64, current string) {
			m.mu.Lock()
			rt.job.DoneItems += dItems
			rt.job.DoneBytes += dBytes
			if current != "" {
				rt.job.CurrentFile = current
			}
			updateSpeed(rt)
			// Throttled persistence: at most every 500 ms.
			if time.Since(rt.lastProg) > 500*time.Millisecond {
				rt.lastProg = time.Now()
				_ = m.persistLocked()
			}
			m.mu.Unlock()
		},
		Warning: func(text string) {
			m.mu.Lock()
			rt.job.Warnings = append(rt.job.Warnings, text)
			if len(rt.job.Warnings) > 100 {
				rt.job.Warnings = rt.job.Warnings[1:]
			}
			m.mu.Unlock()
			m.emit(rt, "warning", "", text)
		},
		Conflict: func(c model.Conflict) {
			m.mu.Lock()
			rt.job.Conflicts = append(rt.job.Conflicts, c)
			m.mu.Unlock()
			m.emit(rt, "conflict", "", "conflict at "+c.Path)
		},
		Check: func() error {
			m.mu.Lock()
			defer m.mu.Unlock()
			if rt.cancel || m.stopping {
				return troveerr.New(troveerr.CancelDenied, "cancelled")
			}
			return nil
		},
		Decide: func(targetRel string) string {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.decided[rt.job.ID][targetRel]
		},
		Staging: func(dirRel, name string) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if name == "" {
				rt.staging = nil
				return
			}
			rt.staging = &executor.StagingRef{
				Root:   rt.manifest.TargetRoot,
				DirRel: dirRel,
				Name:   name,
			}
		},
		Committed: rt.committed,
	}
}

func updateSpeed(rt *runtime) {
	// Speed/ETA are computed by the API layer from deltas; keep fields stable.
}

func (m *Manager) finish(rt *runtime, outcome *executor.Outcome, runErr error) {
	m.mu.Lock()
	now := time.Now()
	rt.job.EndedAt = &now
	if runErr != nil {
		if troveerr.Of(runErr) == troveerr.CancelDenied {
			rt.job.State = model.JobCancelled
			rt.job.Message = "cancelled"
		} else if troveerr.Of(runErr) == troveerr.SourceChanged {
			rt.job.State = model.JobNeedsAttention
			rt.job.ErrorCode = string(troveerr.SourceChanged)
		} else if len(rt.job.Conflicts) > 0 {
			rt.job.State = model.JobNeedsAttention
			rt.job.ErrorCode = string(troveerr.ConflictNeedsDecision)
		} else {
			code := troveerr.Of(runErr)
			if code == troveerr.Internal || code == troveerr.IOFailure {
				rt.job.State = model.JobFailed
			} else {
				rt.job.State = model.JobRetryable
			}
			rt.job.ErrorCode = string(code)
			rt.job.Message = runErr.Error()
		}
	} else {
		rt.job.State = model.JobCompleted
		if outcome != nil && outcome.Warnings > 0 {
			rt.job.Message = "completed with warnings"
		} else {
			rt.job.Message = ""
		}
	}
	rt.job.CurrentFile = ""
	if outcome != nil {
		rt.job.DoneItems = outcome.Items
		rt.job.DoneBytes = outcome.Bytes
	}
	rt.cancel = false
	m.mu.Unlock()
	_ = m.store.Sync()
	if runErr != nil {
		m.emit(rt, "error", string(troveerr.Of(runErr)), runErr.Error())
	}
	m.emit(rt, "state", "", "finished")
}

// Events returns recent events for a job with seq > since.
func (m *Manager) Events(jobID string, since int64, limit int) ([]model.Event, error) {
	return m.store.ReadEvents(jobID, since, limit)
}

// emit (locked variant) records one event.
func (m *Manager) emitLocked(rt *runtime, typ, code, text string) {
	m.seq++
	ev := model.Event{
		Seq:   m.seq,
		JobID: rt.job.ID,
		At:    time.Now(),
		Type:  typ,
		Code:  code,
		Text:  text,
		State: rt.job.State,
	}
	_ = m.store.AppendEvent(&ev)
	rt.job.EventsSeq = m.seq
}

// emit locks and records.
func (m *Manager) emit(rt *runtime, typ, code, text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emitLocked(rt, typ, code, text)
	_ = m.persistLocked()
}

func (m *Manager) persistLocked() error {
	list := make([]*model.Job, 0, len(m.order))
	for _, id := range m.order {
		list = append(list, m.jobs[id].job)
	}
	return m.store.SaveState(list)
}

// Prune removes terminal jobs older than 30 days (housekeeping on start).
func (m *Manager) Prune() {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().AddDate(0, 0, -30)
	kept := m.order[:0]
	for _, id := range m.order {
		rt := m.jobs[id]
		if isTerminal(rt.job.State) && rt.job.EndedAt != nil && rt.job.EndedAt.Before(cutoff) {
			delete(m.jobs, id)
			if rt.job.ClientReqID != "" {
				delete(m.byReq, rt.job.ClientReqID)
			}
			continue
		}
		kept = append(kept, id)
	}
	m.order = kept
}

func isTerminal(s model.JobState) bool {
	switch s {
	case model.JobCompleted, model.JobCancelled, model.JobFailed, model.JobRetryable:
		return true
	}
	return false
}

func newJobID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("job_%d_%s", time.Now().UnixMilli(), base64.RawURLEncoding.EncodeToString(b[:]))
}

var _ = sort.Strings
var _ = os.Getpid
var _ = errors.New
var _ = strings.Contains
