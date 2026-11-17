package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/syyzit/blackbox/internal/store"
)

// cmdResolve records the user's answer to a built-in require_approval decision.
func cmdResolve(args []string) int {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	session := fs.String("session", "", "agent session id")
	call := fs.String("call", "", "tool call id")
	decision := fs.String("decision", "", "allow or deny")
	note := fs.String("note", "", "text appended to the decision reason")
	source := fs.String("source", "", "agent the session belongs to: opencode (default) or claude-code")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *session == "" || *call == "" || *decision == "" {
		fmt.Fprintln(os.Stderr, "blackbox resolve: -session, -call and -decision are required")
		return 2
	}

	var approved bool
	switch strings.ToLower(*decision) {
	case "allow", "approved", "yes":
		approved = true
		if *note == "" {
			*note = " (approved via permission flow)"
		}
	case "deny", "rejected", "no":
		approved = false
		if *note == "" {
			*note = " (rejected via permission flow)"
		}
	default:
		fmt.Fprintf(os.Stderr, "blackbox resolve: unknown decision %q\n", *decision)
		return 2
	}

	src, ok := store.SourceByName(*source)
	if !ok {
		fmt.Fprintf(os.Stderr, "blackbox resolve: unknown source %q\n", *source)
		return 2
	}
	if err := store.ResolveDecision(*home, src, *session, *call, approved, *note); err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	return 0
}
