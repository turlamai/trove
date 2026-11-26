package resolver

import (
	"strings"
	"testing"

	"github.com/turlamai/trove/daemon/internal/troveerr"
)

// TestNormalizeRelPath covers the traversal and malformed-input matrix.
// Every rejection must be a rejection — nothing is silently "cleaned".
func TestNormalizeRelPath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr troveerr.Code
	}{
		// valid
		{in: "", want: ""},
		{in: "a", want: "a"},
		{in: "a/b/c.mkv", want: "a/b/c.mkv"},
		{in: "a//b", want: "a/b"}, // separator collapse is not an escape
		{in: "电影/4K (2014).mkv", want: "电影/4K (2014).mkv"},
		{in: "a b/c.d e", want: "a b/c.d e"},

		// traversal attempts
		{in: "..", wantErr: troveerr.PathEscape},
		{in: "../etc/passwd", wantErr: troveerr.PathEscape},
		{in: "a/../b", wantErr: troveerr.PathEscape},
		{in: "a/..", wantErr: troveerr.PathEscape},
		{in: "....//", want: "...."}, // "...." is a valid name, trailing empty dropped
		{in: "..%2f", want: "..%2f"}, // encoded stays literal; decode is the HTTP layer's job

		// absolute forms
		{in: "/etc/passwd", wantErr: troveerr.PathEscape},
		{in: "/", wantErr: troveerr.PathEscape},
		{in: "//server/share", wantErr: troveerr.PathEscape},
		{in: "\\temp", wantErr: troveerr.PathEscape},

		// windows forms
		{in: "C:/Windows", wantErr: troveerr.UnsupportedPath},
		{in: "a\\b", wantErr: troveerr.UnsupportedPath},

		// invalid bytes
		{in: "a\x00b", wantErr: troveerr.UnsupportedPath},
		{in: "a\x01b", wantErr: troveerr.UnsupportedPath},
		{in: "a\x7fb", wantErr: troveerr.UnsupportedPath},
		{in: "\xff\xfe", wantErr: troveerr.UnsupportedPath},

		// length
		{in: strings.Repeat("a", 250), want: strings.Repeat("a", 250)},
		{in: strings.Repeat("a", 256), wantErr: troveerr.UnsupportedPath}, // >255 component
		{in: strings.Repeat("a/", 2000), wantErr: troveerr.UnsupportedPath},
	}
	for _, tc := range cases {
		got, err := NormalizeRelPath(tc.in)
		if tc.wantErr != "" {
			if err == nil {
				t.Errorf("NormalizeRelPath(%q) = %q, want error %s", tc.in, got, tc.wantErr)
				continue
			}
			if code := troveerr.Of(err); code != tc.wantErr {
				t.Errorf("NormalizeRelPath(%q) error = %s, want %s", tc.in, code, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeRelPath(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeRelPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	valid := []string{"movie.mkv", "新目录", "a b c", ".hidden"}
	for _, n := range valid {
		if _, err := NormalizeName(n); err != nil {
			t.Errorf("NormalizeName(%q) unexpected error: %v", n, err)
		}
	}
	// ".. " (trailing space) is a legal Linux filename, not a parent
	// reference — the kernel treats it literally, so acceptance is correct.
	invalid := []string{"", ".", "..", "a/b", "a\\b", "a\x00"}
	for _, n := range invalid {
		if _, err := NormalizeName(n); err == nil {
			t.Errorf("NormalizeName(%q) accepted, want rejection", n)
		}
	}
}

func TestIsInside(t *testing.T) {
	cases := []struct {
		parent, child string
		want          bool
	}{
		{"", "anything", true},
		{"a", "a", true},
		{"a", "a/b", true},
		{"a", "a/b/c", true},
		{"a", "ab", false},     // prefix without separator
		{"a", "a../b", false},  // lookalike
		{"a/b", "a", false},
	}
	for _, tc := range cases {
		if got := IsInside(tc.parent, tc.child); got != tc.want {
			t.Errorf("IsInside(%q, %q) = %v, want %v", tc.parent, tc.child, got, tc.want)
		}
	}
}
