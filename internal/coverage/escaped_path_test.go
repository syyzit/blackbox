package coverage

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/syyzit/blackbox/internal/event"
)

func writeCall(t *testing.T, tool string, arguments map[string]any) event.Event {
	t.Helper()
	args, err := json.Marshal(map[string]any{"name": tool, "arguments": arguments})
	if err != nil {
		t.Fatal(err)
	}
	return event.Event{
		Direction: event.ClientToServer, Method: "tools/call", Tool: tool,
		Status: event.StatusPending, Args: string(args),
		Raw: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + string(args) + `}`,
	}
}

// A path is recorded as a JSON string, where a Windows path has its backslashes
// doubled. The comparison has to be with the value, not with the JSON text.
func TestClassifyMatchesNativeAbsolutePath(t *testing.T) {
	root := t.TempDir()
	changes := []Change{{Path: "sub/b.txt", Kind: Modified}}

	abs := filepath.Join(root, "sub", "b.txt")
	rep := Classify(root, changes, []event.Event{writeCall(t, "write_file", map[string]any{"path": abs})})
	if rep.Covered != 1 {
		t.Fatalf("a write naming %s was not matched: %+v", abs, rep.Files)
	}
}

func TestNamesPathComparesDecodedValues(t *testing.T) {
	const rel = "sub/b.txt"
	const abs = `C:\repo\sub\b.txt`
	cases := []struct {
		name string
		args map[string]any
		want bool
	}{
		{"escaped absolute path", map[string]any{"path": abs}, true},
		{"absolute path with forward slashes", map[string]any{"path": "C:/repo/sub/b.txt"}, true},
		{"relative path", map[string]any{"path": rel}, true},
		{"relative path with backslashes", map[string]any{"path": `sub\b.txt`}, true},
		{"nested value", map[string]any{"edits": []any{map[string]any{"file": abs}}}, true},
		{"inside a command", map[string]any{"command": `del "` + abs + `"`}, true},
		{"another file", map[string]any{"path": `C:\repo\sub\c.txt`}, false},
		{"same name elsewhere", map[string]any{"path": `C:/repo/other/b.txt`}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			texts := callTexts([]event.Event{writeCall(t, "write_file", tc.args)})
			if got := namesPath(texts, rel, abs); got != tc.want {
				t.Fatalf("namesPath = %v, want %v", got, tc.want)
			}
		})
	}

	// Text that is not JSON is still searched as it is.
	plain := callTexts([]event.Event{{Result: "wrote " + abs}})
	if !namesPath(plain, rel, abs) {
		t.Fatal("plain-text result not searched")
	}
}
