package policy

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestElideKeepsBothEnds(t *testing.T) {
	if got := Elide("short", 10, 10); got != "short" {
		t.Fatalf("short text changed: %q", got)
	}

	// A long harmless argument must not push the one that matters out of view.
	s := `{"a":"` + strings.Repeat("x", 5000) + `","command":"rm -rf /"}`
	got := Elide(s, 400, 200)
	if !strings.HasPrefix(got, `{"a":"xxx`) || !strings.HasSuffix(got, `"command":"rm -rf /"}`) {
		t.Fatalf("lost an end: %.60q … %q", got, got[len(got)-40:])
	}
	if !strings.Contains(got, "bytes not shown") || len(got) > 700 {
		t.Fatalf("no notice, or not shortened (%d bytes)", len(got))
	}

	// Cuts land between characters, not inside one.
	multi := strings.Repeat("é", 400)
	if got := Elide(multi, 101, 101); !utf8.ValidString(got) {
		t.Fatalf("split a multi-byte character: %q", got)
	}
}
