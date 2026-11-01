// Command fakeserver runs the deterministic fake MCP server used by the demo.
package main

import (
	"fmt"
	"os"

	"github.com/syyzit/blackbox/internal/mcptest"
)

func main() {
	if err := mcptest.NewServer().Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "fakeserver:", err)
		os.Exit(1)
	}
}
