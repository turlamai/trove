// Package model defines the shared types of the Trove daemon: typed storage
// roots, directory entries, plans and jobs. All file operations are addressed
// by (RootRef, relativePath) pairs — the daemon never accepts arbitrary
// absolute paths from the frontend.
package model

import "time"

// RootKind is the namespace type of a storage root. Mixing kinds inside one
// operation is refused by design (Unraid user shares and disk/pool paths can
// be two views of the same data).
type RootKind string

const (
	KindShare RootKind = "share" // /mnt/user/<name> — logical view
	KindDisk  RootKind = "disk"  // /mnt/diskN      — physical view
	KindPool  RootKind = "pool"  // /mnt/<pool>     — physical view
)

// RootRef identifies an allowed storage root. It is the only path authority
// the frontend may present.
type RootRef struct {
	Kind RootKind `json:"kind"`
	ID   string   `json:"id"` // share name, disk name or pool name
}

func (r RootRef) String() string { return string(r.Kind) + ":" + r.ID }

// EntryKind classifies a directory entry.
type EntryKind string

const (
	EntryDir     EntryKind = "dir"
	EntryFile    EntryKind = "file"
	EntrySymlink EntryKind = "symlink"
	EntryOther   EntryKind = "other" // fifo, socket, device — never copied
)

// Entry is one item of a directory listing. Sizes of entries the daemon
// cannot stat are reported as -1 ("unknown"), never guessed.
type Entry struct {
	Name    string    `json:"name"`
	Kind    EntryKind `json:"kind"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"` // e.g. "rwxr-xr-x"
	Mtime   time.Time `json:"mtime"`
	Owner   string    `json:"owner,omitempty"`
	Group   string    `json:"group,omitempty"`
	Target  string    `json:"target,omitempty"` // symlink target (display only)
	DirFile bool      `json:"dirFile,omitempty"`
}

// Action is a planned file operation.
type Action string

const (
	ActCopy   Action = "copy"
	ActMove   Action = "move"
	ActDelete Action = "delete"
	ActRename Action = "rename"
	ActMkdir  Action = "mkdir"
	ActHash   Action = "hash"
)

// ConflictPolicy decides what happens when the target already exists.
type ConflictPolicy string

const (
	PolicyAsk       ConflictPolicy = "ask"       // pause the job, resolve per item
	PolicySkip      ConflictPolicy = "skip"
	PolicyRename    ConflictPolicy = "rename"    // auto-suffix " (n)"
	PolicyOverwrite ConflictPolicy = "overwrite" // replace after full verification
)

// SourceChangedPolicy decides what happens when a source file changed on disk
// since planning (or during a move, before source deletion).
type SourceChangedPolicy string

const (
	SourceSkip  SourceChangedPolicy = "skip" // keep source, warn (default)
	SourceAbort SourceChangedPolicy = "abort"
)

// VerifyLevel controls copy verification before commit.
type VerifyLevel string

const (
	VerifySizeMeta VerifyLevel = "size-and-metadata"
	VerifySHA256   VerifyLevel = "sha256"
)

// Node is a typed path inside a root.
type Node struct {
	Root RootRef `json:"root"`
	Path string  `json:"path"` // relative, no leading/trailing slash; "" = root dir
}

// PlanRequest is the input to the planner (dry run).
type PlanRequest struct {
	Action         Action              `json:"action"`
	Source         *Node               `json:"source,omitempty"`
	Targets        []Node              `json:"targets,omitempty"` // delete: entries to remove
	TargetDir      *Node               `json:"targetDir,omitempty"` // copy/move destination directory
	TargetName     string              `json:"targetName,omitempty"` // rename/mkdir
	ConflictPolicy ConflictPolicy      `json:"conflictPolicy,omitempty"`
	SourcePolicy   SourceChangedPolicy `json:"sourcePolicy,omitempty"`
	Verify         VerifyLevel         `json:"verify,omitempty"`
	// Purge bypasses the trash for a delete: permanent removal.
	Purge bool `json:"purge,omitempty"`
	// AckProtected confirms an intentional write into a protected share
	// (appdata/domains/system/...) when the plugin settings allow it.
	AckProtected bool `json:"ackProtected,omitempty"`
	// ClientRequestID is the caller's idempotency key.
	ClientRequestID string `json:"clientRequestId,omitempty"`
}

// Conflict is a name collision found during planning or execution.
type Conflict struct {
	Path        string    `json:"path"` // target-relative path of the collision
	SrcSize     int64     `json:"srcSize"`
	SrcMtime    time.Time `json:"srcMtime"`
	DstSize     int64     `json:"dstSize"`
	DstMtime    time.Time `json:"dstMtime"`
	Resolved    string    `json:"resolved,omitempty"` // decision when resolved
	ResolvedAll bool      `json:"resolvedAll,omitempty"`
}

// Risk describes a non-blocking warning attached to a plan.
type Risk struct {
	Code string `json:"code"`
	Note string `json:"note"`
}

// Plan is the planner's verdict. Submit requires the server-generated token.
type Plan struct {
	ID          string     `json:"id"`
	Action      Action     `json:"action"`
	Source      *Node      `json:"source,omitempty"`
	Targets     []Node     `json:"targets,omitempty"` // delete targets (retry bookkeeping)
	TargetDir   *Node      `json:"targetDir,omitempty"`
	TargetName  string     `json:"targetName,omitempty"`
	Policy      ConflictPolicy `json:"conflictPolicy,omitempty"`
	Items       int64      `json:"items"`         // files+dirs discovered (may be partial)
	Files       int64      `json:"files"`
	Dirs        int64      `json:"dirs"`
	Bytes       int64      `json:"bytes"`         // total source bytes
	Estimate    bool       `json:"estimate"`      // true when the walk hit scan limits
	Conflicts   []Conflict `json:"conflicts,omitempty"`
	Risks       []Risk     `json:"risks,omitempty"`
	Trash       bool       `json:"trash,omitempty"` // delete will move to trash
	NeedsAck    bool       `json:"needsAck,omitempty"` // protected share write
	Token       string     `json:"token,omitempty"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	NamespaceOK bool       `json:"namespaceOk"`
}

// JobState is the job state machine. States follow the specification:
// PLANNED → QUEUED → RUNNING → VERIFYING → COMPLETED plus failure/cancel
// branches. NeedsAttention waits for user decisions (conflicts).
type JobState string

const (
	JobQueued         JobState = "queued"
	JobRunning        JobState = "running"
	JobVerifying      JobState = "verifying"
	JobCompleted      JobState = "completed"
	JobCancelRequested JobState = "cancel_requested"
	JobCancelled      JobState = "cancelled"
	JobFailed         JobState = "failed"           // final
	JobRetryable      JobState = "failed_retryable" // can be retried
	JobNeedsAttention JobState = "needs_attention"  // conflicts / source changed
)

// Job is one background operation, persisted in the daemon data directory.
type Job struct {
	ID          string          `json:"id"`
	ClientReqID string          `json:"clientRequestId,omitempty"`
	Action      Action          `json:"action"`
	State       JobState        `json:"state"`
	CreatedAt   time.Time       `json:"createdAt"`
	StartedAt   *time.Time      `json:"startedAt,omitempty"`
	EndedAt     *time.Time      `json:"endedAt,omitempty"`
	Plan        *Plan           `json:"plan,omitempty"`
	// Progress
	TotalItems  int64  `json:"totalItems"`
	DoneItems   int64  `json:"doneItems"`
	TotalBytes  int64  `json:"totalBytes"`
	DoneBytes   int64  `json:"doneBytes"`
	CurrentFile string `json:"currentFile,omitempty"`
	SpeedBPS    float64 `json:"speedBps,omitempty"`
	ETASeconds  float64 `json:"etaSeconds,omitempty"`
	// Outcomes
	ErrorCode  string     `json:"errorCode,omitempty"`
	Message    string     `json:"message,omitempty"`
	Warnings   []string   `json:"warnings,omitempty"`
	Conflicts  []Conflict `json:"conflicts,omitempty"` // pending (needs_attention)
	Hashes     []HashSpec `json:"hashes,omitempty"`    // hash action results
	EventsSeq  int64      `json:"eventsSeq"`
	Cancelable bool       `json:"cancelable"`
	Pausable   bool       `json:"pausable"` // always false in V1
}

// HashSpec is one computed digest of the hash action.
type HashSpec struct {
	Rel    string `json:"rel"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Event is a journal record for one job transition or progress checkpoint.
type Event struct {
	Seq   int64     `json:"seq"`
	JobID string    `json:"jobId"`
	At    time.Time `json:"at"`
	Type  string    `json:"type"` // state | progress | warning | error | conflict | info
	State JobState  `json:"state,omitempty"`
	Code  string    `json:"code,omitempty"`
	Text  string    `json:"text,omitempty"`
}

// Capabilities is reported by GET /capabilities.
type Capabilities struct {
	Version      string   `json:"version"`
	APIVersion   string   `json:"apiVersion"`
	ReadOK       bool     `json:"readOk"`
	WriteOK      bool     `json:"writeOk"`
	ReadOnlyWhy  string   `json:"readOnlyWhy,omitempty"`
	DataDir      string   `json:"dataDir,omitempty"`
	DataDirOK    bool     `json:"dataDirOk"`
	ExpertMode   bool     `json:"expertMode"`
	PhysicalWrite bool    `json:"physicalWrite"`
	Concurrency  int      `json:"concurrency"`
	Openat2      bool     `json:"openat2"`
	ArrayState   string   `json:"arrayState,omitempty"`
	Platform     string   `json:"platform"`
	Actions      []string `json:"actions"`
}
