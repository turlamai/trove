package executor

import (
	"errors"
	"time"

	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// ErrSourceKept signals that a move completed its copies but had to keep
// (parts of) the source tree — the job enters needs_attention.
var ErrSourceKept = errors.New("source kept after verified copy")

func abortSourceChanged(rel string) error {
	return troveerr.New(troveerr.SourceChanged, "source changed since planning: "+rel)
}

func nowPointer() *time.Time {
	t := time.Now()
	return &t
}
