// Package troveerr defines the daemon's error codes. Codes are part of the
// API contract (docs/api-contract.md); HTTP status codes are derived from
// them. Every failure is fail-closed: unknown situations map to codes that
// deny the operation.
package troveerr

import (
	"fmt"
	"net/http"
)

// Code is a stable error code string surfaced to the frontend, which maps it
// to user-readable text.
type Code string

const (
	Unauthorized          Code = "UNAUTHORIZED"
	CSRFInvalid           Code = "CSRF_INVALID"
	OriginRejected        Code = "ORIGIN_REJECTED"
	BadRequest            Code = "BAD_REQUEST"
	RootNotAllowed        Code = "ROOT_NOT_ALLOWED"
	RootNotFound          Code = "ROOT_NOT_FOUND"
	PathEscape            Code = "PATH_ESCAPE"
	UnsupportedPath       Code = "UNSUPPORTED_PATH"
	NamespaceMixForbidden Code = "NAMESPACE_MIX_FORBIDDEN"
	SameFileAlias         Code = "SAME_FILE_ALIAS"
	RecursiveTarget       Code = "RECURSIVE_TARGET"
	ArrayStopped          Code = "ARRAY_STOPPED"
	MountChanged          Code = "MOUNT_CHANGED"
	NotWritable           Code = "NOT_WRITABLE"
	PhysicalWriteDisabled Code = "PHYSICAL_WRITE_DISABLED"
	ProtectedShare        Code = "PROTECTED_SHARE"
	InsufficientSpace     Code = "INSUFFICIENT_SPACE"
	SourceChanged         Code = "SOURCE_CHANGED"
	ConflictNeedsDecision Code = "CONFLICT_REQUIRES_DECISION"
	UnsupportedType       Code = "UNSUPPORTED_TYPE"
	IOFailure             Code = "IO_FAILURE"
	NotFound              Code = "NOT_FOUND"
	JobStorageUnavailable Code = "JOB_STORAGE_UNAVAILABLE"
	PlanExpired           Code = "PLAN_EXPIRED"
	PlanMismatch          Code = "PLAN_MISMATCH"
	DuplicateRequest      Code = "DUPLICATE_REQUEST"
	CancelDenied          Code = "CANCEL_DENIED"
	Unsupported           Code = "UNSUPPORTED" // e.g. non-Linux build
	Internal              Code = "INTERNAL"
)

type statusMapping struct {
	code  Code
	http  int
	recov bool // recoverable — the operation may succeed after user action
}

var mappings = map[Code]statusMapping{
	Unauthorized:          {Unauthorized, http.StatusUnauthorized, false},
	CSRFInvalid:           {CSRFInvalid, http.StatusForbidden, true},
	OriginRejected:        {OriginRejected, http.StatusForbidden, false},
	BadRequest:            {BadRequest, http.StatusBadRequest, false},
	RootNotAllowed:        {RootNotAllowed, http.StatusForbidden, false},
	RootNotFound:          {RootNotFound, http.StatusNotFound, false},
	PathEscape:            {PathEscape, http.StatusBadRequest, false},
	UnsupportedPath:       {UnsupportedPath, http.StatusBadRequest, false},
	NamespaceMixForbidden: {NamespaceMixForbidden, http.StatusUnprocessableEntity, false},
	SameFileAlias:         {SameFileAlias, http.StatusUnprocessableEntity, false},
	RecursiveTarget:       {RecursiveTarget, http.StatusUnprocessableEntity, false},
	ArrayStopped:          {ArrayStopped, http.StatusConflict, true},
	MountChanged:          {MountChanged, http.StatusConflict, true},
	NotWritable:           {NotWritable, http.StatusForbidden, true},
	PhysicalWriteDisabled: {PhysicalWriteDisabled, http.StatusForbidden, true},
	ProtectedShare:        {ProtectedShare, http.StatusForbidden, true},
	InsufficientSpace:     {InsufficientSpace, http.StatusInsufficientStorage, true},
	SourceChanged:         {SourceChanged, http.StatusConflict, true},
	ConflictNeedsDecision: {ConflictNeedsDecision, http.StatusConflict, true},
	UnsupportedType:       {UnsupportedType, http.StatusUnprocessableEntity, true},
	IOFailure:             {IOFailure, http.StatusInternalServerError, true},
	NotFound:              {NotFound, http.StatusNotFound, false},
	JobStorageUnavailable: {JobStorageUnavailable, http.StatusServiceUnavailable, true},
	PlanExpired:           {PlanExpired, http.StatusConflict, true},
	PlanMismatch:          {PlanMismatch, http.StatusConflict, true},
	DuplicateRequest:      {DuplicateRequest, http.StatusConflict, true},
	CancelDenied:          {CancelDenied, http.StatusConflict, false},
	Unsupported:           {Unsupported, http.StatusNotImplemented, false},
	Internal:              {Internal, http.StatusInternalServerError, false},
}

// E is an error carrying a Trove code.
type E struct {
	Code    Code
	Detail  string // technical detail for logs; may mention paths
	Wrapped error
}

func (e *E) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Detail)
	}
	return string(e.Code)
}

func (e *E) Unwrap() error { return e.Wrapped }

// New builds a coded error.
func New(c Code, detail string) *E { return &E{Code: c, Detail: detail} }

// Wrap wraps an underlying error with a code.
func Wrap(c Code, err error, detail string) *E {
	return &E{Code: c, Detail: detail, Wrapped: err}
}

// Of returns the Trove code of err (or Internal).
func Of(err error) Code {
	if e, ok := err.(*E); ok {
		return e.Code
	}
	return Internal
}

// HTTPStatus maps a code to its HTTP status.
func HTTPStatus(c Code) int {
	if m, ok := mappings[c]; ok {
		return m.http
	}
	return http.StatusInternalServerError
}

// Recoverable reports whether a retry or user action can resolve the error.
func Recoverable(c Code) bool {
	if m, ok := mappings[c]; ok {
		return m.recov
	}
	return false
}
