package store

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/syyzit/blackbox/internal/event"
)

func appendOne(t *testing.T, home string, src Source, session, text string) {
	t.Helper()
	if err := AppendExternal(home, src, session, []event.Event{{
		Direction: event.ClientToServer, Method: "tools/call", Tool: "echo",
		Status: event.StatusPending, At: time.Now().UTC(), Args: text, Raw: `{}`,
	}}); err != nil {
		t.Fatal(err)
	}
}

// An id prefix is literal text: % and _ in it are not wildcards.
func TestFindTreatsPrefixLiterally(t *testing.T) {
	home := t.TempDir()
	appendOne(t, home, ClaudeCode, "aXb-1", `{"text":"one"}`)

	for _, prefix := range []string{"%", "_____", "%b-1", "cc-a_b", `cc-aXb\`, "c%"} {
		if meta, _, err := Find(home, prefix); err == nil {
			t.Errorf("Find(%q) resolved to %s", prefix, meta.ID)
		}
	}
	for _, prefix := range []string{"cc-aXb-1", "cc-aXb", "aXb", "aXb-1"} {
		if meta, _, err := Find(home, prefix); err != nil || meta.ID != "cc-aXb-1" {
			t.Errorf("Find(%q) = %s, %v", prefix, meta.ID, err)
		}
	}

	// With a real underscore in an id, the underscore matches only itself.
	appendOne(t, home, ClaudeCode, "a_b-2", `{"text":"two"}`)
	if meta, _, err := Find(home, "cc-a_b"); err != nil || meta.ID != "cc-a_b-2" {
		t.Errorf("Find(cc-a_b) = %s, %v", meta.ID, err)
	}
}

// A complete id wins over longer ids that start with it, however many there are.
func TestFindPrefersExactID(t *testing.T) {
	home := t.TempDir()
	for _, id := range []string{"abc0", "abc1", "abc2", "abc3", "abc4", "abc"} {
		appendOne(t, home, ClaudeCode, id, `{}`)
	}
	if meta, _, err := Find(home, "cc-abc"); err != nil || meta.ID != "cc-abc" {
		t.Fatalf("Find(cc-abc) = %s, %v", meta.ID, err)
	}
	if _, _, err := Find(home, "cc-ab"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("Find(cc-ab) = %v, want ambiguous", err)
	}
}

func TestTraceForPrefersExactIDAndLiteralPrefix(t *testing.T) {
	home := t.TempDir()
	for _, id := range []string{"abc0", "abc1", "abc", "x_y"} {
		appendOne(t, home, ClaudeCode, id, `{}`)
	}

	tr, err := TraceFor(home, "abc")
	if err != nil || tr.ExternalID != "abc" {
		t.Fatalf("TraceFor(abc) = %+v, %v", tr, err)
	}
	if _, err := TraceFor(home, "ab"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("TraceFor(ab) = %v, want ambiguous", err)
	}
	if tr, err := TraceFor(home, "abc0"); err != nil || tr.ExternalID != "abc0" {
		t.Fatalf("TraceFor(abc0) = %+v, %v", tr, err)
	}
	for _, prefix := range []string{"%", "x%", "____", "_bc0"} {
		if tr, err := TraceFor(home, prefix); err == nil {
			t.Errorf("TraceFor(%q) resolved to %s", prefix, tr.ExternalID)
		}
	}
	if tr, err := TraceFor(home, "x_"); err != nil || tr.ExternalID != "x_y" {
		t.Errorf("TraceFor(x_) = %+v, %v", tr, err)
	}
}

func TestSearchSessionFilterIsLiteral(t *testing.T) {
	home := t.TempDir()
	appendOne(t, home, ClaudeCode, "aXb-1", `{"text":"findme"}`)

	for prefix, want := range map[string]int{"": 1, "cc-aXb": 1, "%": 0, "cc-a_b": 0, "__": 0} {
		hits, err := Search(home, SearchOptions{Query: "findme", SessionID: prefix})
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != want {
			t.Errorf("session filter %q: %d hits, want %d", prefix, len(hits), want)
		}
	}
}

func TestSearchSnippet(t *testing.T) {
	// U+023A is two bytes and lowercases to a three-byte rune, so an index found in
	// the lowercased text does not fit the original.
	grows := strings.Repeat("Ⱥ", 100)
	cases := []struct {
		name, field, query, want string
	}{
		{"plain", "the needle here", "needle", "the needle here"},
		{"case-insensitive", "the NEEDLE here", " needle ", "the NEEDLE here"},
		{"lowercase longer than original", grows + " needle", "needle", "…" + strings.Repeat("Ⱥ", 20) + " needle"},
		{"match on a folded rune", "xx ȺBC yy", "ⱥbc", "xx ȺBC yy"},
		{"multi-byte text is not cut mid-rune", strings.Repeat("é", 60) + "needle" + strings.Repeat("é", 60),
			"needle", "…" + strings.Repeat("é", 20) + "needle" + strings.Repeat("é", 37) + "…"},
		{"newlines flattened", "a\nneedle\nb", "needle", "a needle b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SearchSnippet(event.Event{Result: tc.field}, tc.query)
			if got != tc.want {
				t.Fatalf("snippet = %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("snippet is not valid UTF-8: %q", got)
			}
		})
	}

	// No field contains the query: fall back to the result, as before.
	if got := SearchSnippet(event.Event{Result: "res", Args: "args"}, "absent"); got != "res" {
		t.Fatalf("fallback = %q", got)
	}
}
