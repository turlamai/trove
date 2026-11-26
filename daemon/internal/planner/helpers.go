// Small path helpers for the planner. All inputs are normalized relative
// paths (see resolver.NormalizeRelPath); these helpers never introduce or
// remove ".." segments.
package planner

import (
	"strings"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/roots"
	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// rootOf maps a typed ref to the registry root.
func (p *Planner) rootOf(ref model.RootRef) *roots.Root {
	r, err := p.reg.Get(ref)
	if err != nil {
		return nil
	}
	return r
}

func baseName(rel string) string {
	if rel == "" {
		return ""
	}
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}
	return rel
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	if name == "" {
		return dir
	}
	return dir + "/" + name
}

func trimPrefixRel(rel, prefix string) string {
	if rel == prefix {
		return ""
	}
	return strings.TrimPrefix(rel, prefix+"/")
}

func reverseItems(items []Item) {
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
}

var errNoRegistry = troveerr.New(troveerr.Internal, "registry unavailable")
