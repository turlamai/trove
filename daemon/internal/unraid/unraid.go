// Package unraid reads read-only system state (array state, version) from
// the emhttp var file. It is an isolated adapter: version-sensitive formats
// never leak outside this package, and every reader fails soft to "unknown"
// so the daemon degrades to mount-presence decisions rather than guessing.
package unraid

import (
	"os"
	"strings"
)

// State is the small slice of emhttp state the daemon uses.
type State struct {
	Version string // Unraid version string
	MDState string // "Started" | "Stopped" | ...
	FSState string // internal fs state copy
}

// ReadVarIni parses /var/local/emhttp/var.ini (KEY="value" lines).
func ReadVarIni(path string) State {
	st := State{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.Trim(strings.TrimSpace(line[i+1:]), "\"")
		switch k {
		case "version":
			st.Version = v
		case "mdState":
			st.MDState = v
		case "fsState":
			st.FSState = v
		}
	}
	return st
}

// ArrayStarted reports whether the array reports Started. Unknown states
// are reported as false but callers must treat "unknown" separately.
func (s State) ArrayStarted() bool { return strings.EqualFold(s.MDState, "Started") }
