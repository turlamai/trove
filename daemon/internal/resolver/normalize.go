// Package resolver turns (RootRef, relativePath) requests into verified file
// handles. normalize.go holds the portable, unit-testable string layer: it is
// the first line of defense against path traversal and malformed input. The
// syscall layer (Linux only) enforces containment with openat2(RESOLVE_BENEATH).
package resolver

import (
	"strings"
	"unicode/utf8"

	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// MaxPathLen bounds a relative path (bytes, UTF-8). Linux PATH_MAX is 4096
// for the whole absolute path; we leave generous room for the mount prefix.
const MaxPathLen = 3800

// MaxNameLen bounds a single component (255 bytes is the classic limit).
const MaxNameLen = 255

// NormalizeRelPath validates and normalizes a client-supplied relative path.
//
// It rejects — rather than repairs — anything suspicious:
//   - empty, absolute, or drive-letter forms
//   - any "." / ".." segment (clients must send clean paths; "a/./b" is
//     rejected, not silently cleaned, so nothing is ever re-interpreted)
//   - NUL bytes, invalid UTF-8, control characters in names
//   - path separators in components, Windows backslash forms
//   - components that are exactly "..", "..&something", or contain a
//     leading-trailing space/dot trick used by some filesystems
//
// The returned path has no leading/trailing slash and no empty segments.
func NormalizeRelPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if len(p) > MaxPathLen {
		return "", troveerr.New(troveerr.UnsupportedPath, "path too long")
	}
	if !utf8.ValidString(p) {
		return "", troveerr.New(troveerr.UnsupportedPath, "path is not valid UTF-8")
	}
	if strings.ContainsRune(p, 0) {
		return "", troveerr.New(troveerr.UnsupportedPath, "path contains NUL byte")
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", troveerr.New(troveerr.PathEscape, "absolute paths are not accepted")
	}
	if strings.Contains(p, `\`) {
		return "", troveerr.New(troveerr.UnsupportedPath, "backslash is not a path separator")
	}
	// Windows drive letters ("C:/...") and UNC roots ("//server/...").
	if len(p) >= 2 && p[1] == ':' {
		return "", troveerr.New(troveerr.UnsupportedPath, "drive-letter paths are not accepted")
	}
	if strings.HasPrefix(p, "//") {
		return "", troveerr.New(troveerr.PathEscape, "UNC paths are not accepted")
	}

	var b strings.Builder
	for seg := range strings.SplitSeq(p, "/") {
		if err := checkSegment(seg); err != nil {
			return "", err
		}
		if seg == "" {
			continue // collapse "a//b" — separator repetition is not an escape
		}
		if b.Len() > 0 {
			b.WriteByte('/')
		}
		b.WriteString(seg)
	}
	out := b.String()
	if len(out) > MaxPathLen {
		return "", troveerr.New(troveerr.UnsupportedPath, "path too long after normalization")
	}
	return out, nil
}

func checkSegment(seg string) error {
	switch seg {
	case "", ".":
		return nil
	case "..":
		return troveerr.New(troveerr.PathEscape, "parent-directory segments are not accepted")
	}
	if len(seg) > MaxNameLen {
		return troveerr.New(troveerr.UnsupportedPath, "name too long")
	}
	if seg == "." {
		return nil
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if c < 0x20 || c == 0x7f {
			return troveerr.New(troveerr.UnsupportedPath, "control characters in names are not accepted")
		}
	}
	return nil
}

// NormalizeName validates a single component (rename target, mkdir name,
// auto-rename suffixes). Slashes are rejected so a name can never become a
// path.
func NormalizeName(name string) (string, error) {
	if name == "" {
		return "", troveerr.New(troveerr.BadRequest, "empty name")
	}
	if strings.ContainsAny(name, "/\\") {
		return "", troveerr.New(troveerr.UnsupportedPath, "name must not contain separators")
	}
	if strings.ContainsRune(name, 0) {
		return "", troveerr.New(troveerr.UnsupportedPath, "name contains NUL byte")
	}
	if !utf8.ValidString(name) {
		return "", troveerr.New(troveerr.UnsupportedPath, "name is not valid UTF-8")
	}
	if name == "." || name == ".." {
		return "", troveerr.New(troveerr.UnsupportedPath, "reserved name")
	}
	if err := checkSegment(name); err != nil {
		return "", err
	}
	if len(name) > MaxNameLen {
		return "", troveerr.New(troveerr.UnsupportedPath, "name too long")
	}
	return name, nil
}

// IsInside returns true when child==parent or child lies below parent.
// Both inputs must already be normalized.
func IsInside(parent, child string) bool {
	if parent == "" {
		return true
	}
	if child == parent {
		return true
	}
	return strings.HasPrefix(child, parent+"/")
}
