package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spacecr8ed/blackbox/internal/policy"
	"github.com/spacecr8ed/blackbox/internal/store"
)

func cmdTrace(args []string) int {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	limit := fs.Int("limit", 20, "how many traces to list")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if fs.NArg() == 0 {
		traces, err := store.TraceList(*home, *limit)
		if err != nil {
			fmt.Fprintln(os.Stderr, "blackbox:", err)
			return 1
		}
		if len(traces) == 0 {
			fmt.Println("no traces recorded yet (trace ids come from agent sessions, e.g. OpenCode ses_…)")
			return 0
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TRACE\tEVENTS\tSESSIONS\tSTARTED")
		for _, t := range traces {
			fmt.Fprintf(w, "%s\t%d\t%d\t%s\n",
				t.ExternalID, t.Events, t.Sessions, t.StartedAt.Local().Format("2006-01-02 15:04:05"))
		}
		return flush(w)
	}

	tr, err := store.TraceFor(*home, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}

	fmt.Printf("trace    %s\n", tr.ExternalID)
	fmt.Printf("events   %d across %d session(s)\n", len(tr.Events), len(tr.Sessions))
	fmt.Printf("window   %s → %s\n",
		tr.StartedAt.Local().Format("15:04:05"), tr.EndedAt.Local().Format("15:04:05"))
	if summary := decisionSummary(tr.Decisions); summary != "" {
		fmt.Printf("policy   %s\n", summary)
	}
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "T\tSRC\tSTATUS\tTOOL\tMS")
	for _, e := range tr.Events {
		src := "mcp"
		if strings.HasPrefix(e.SessionID, "oc-") {
			src = "builtin"
		}
		offset := e.At.Sub(tr.StartedAt).Round(time.Millisecond)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n",
			offset, src, dash(e.Status), dash(e.Tool), e.DurationMS)
	}
	return flush(w)
}

func decisionSummary(decisions []policy.DecisionRecord) string {
	counts := map[string]int{}
	for _, d := range decisions {
		key := string(d.Action)
		if d.Mode != "" {
			key += " (" + string(d.Mode) + ")"
		}
		counts[key]++
	}
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	return strings.Join(parts, " · ")
}
