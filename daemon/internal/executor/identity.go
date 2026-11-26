// Source identity verification: entries are transferred only when the live
// inode still matches the planned snapshot.
package executor

import (
	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/planner"
	"github.com/turlamai/trove/daemon/internal/resolver"
)

// checkIdentity compares a live source entry with its planned snapshot.
// A replaced inode or a changed file (move safety, spec T12) is skipped with
// a warning by default, or aborts when SourceChangedPolicy=abort.
func (e *Executor) checkIdentity(item *planner.Item, st *resolver.StatInfo, srcRel string,
	man *planner.Manifest, h *Hooks) error {

	if st.Dev != item.Dev || st.Ino != item.Ino {
		switch man.SourcePolic {
		case model.SourceAbort:
			return abortSourceChanged(srcRel)
		default:
			h.Warning("source replaced since planning — kept, not transferred: " + srcRel)
			return resolver.WalkSkipDir
		}
	}
	if item.Kind == model.EntryFile && st.Kind == model.EntryFile {
		if st.Size != item.Size || st.Mtime != item.MtimeS || int64(st.MtimeN) != item.MtimeNs {
			switch man.SourcePolic {
			case model.SourceAbort:
				return abortSourceChanged(srcRel)
			default:
				h.Warning("source modified during transfer — kept, not transferred: " + srcRel)
				return resolver.WalkSkipDir
			}
		}
	}
	return nil
}
