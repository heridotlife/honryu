package httpapi

import (
	"testing"
)

// pdfEscape unit: PDF string metacharacters are backslash-escaped and
// non-ASCII becomes '?' (single-byte Helvetica encoding honesty).
func TestPDFEscapeMeta(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`plain`:      `plain`,
		`a(b)c`:      `a\(b\)c`,
		`back\slash`: `back\\slash`,
		"unié":       "uni?",
	}
	for in, want := range cases {
		if got := pdfEscape(in); got != want {
			t.Errorf("pdfEscape(%q) = %q, want %q", in, got, want)
		}
	}
}
