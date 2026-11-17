package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sleeper writes <marker>.started at once and <marker> a few seconds later, unless it
// is killed first. Run through `go run`, it is a grandchild of mcpshell.
const sleeper = `package main

import (
	"os"
	"time"
)

func main() {
	if len(os.Args) > 2 && os.Args[2] == "quick" {
		return
	}
	_ = os.WriteFile(os.Args[1]+".started", nil, 0o644)
	time.Sleep(3 * time.Second)
	_ = os.WriteFile(os.Args[1], nil, 0o644)
}
`

// A timeout kills everything the command started, not just the command: go run
// leaves the program it built running when go itself is killed.
func TestTimeoutKillsTheProcessTree(t *testing.T) {
	s := testServer(t)
	if err := os.WriteFile(filepath.Join(s.root, "sleeper.go"), []byte(sleeper), 0o644); err != nil {
		t.Fatal(err)
	}
	// Build once, so the timed run starts the program well inside its deadline.
	if text, failed := s.execute(callArgs{Command: "go", Args: []string{"run", "sleeper.go", "marker", "quick"}, TimeoutMS: 120_000}); failed {
		t.Fatalf("warm-up run failed: %s", text)
	}

	text, failed := s.execute(callArgs{Command: "go", Args: []string{"run", "sleeper.go", "marker"}, TimeoutMS: 2000})
	if !failed || !strings.Contains(text, "timed out") {
		t.Fatalf("run did not time out: %s", text)
	}
	marker := filepath.Join(s.root, "marker")
	if _, err := os.Stat(marker + ".started"); err != nil {
		t.Skip("go run did not start the program before the timeout; nothing to check")
	}
	time.Sleep(3 * time.Second) // past the moment the program would have written the marker
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the program go run started outlived the timeout")
	}
}
