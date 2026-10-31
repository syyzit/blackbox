package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spacecr8ed/blackbox/internal/store"
)

func cmdSearch(args []string) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	since := fs.String("since", "", "only frames newer than this (e.g. 7d)")
	tool := fs.String("tool", "", "only this tool")
	status := fs.String("status", "", "only this status (ok, error, tool_error, denied)")
	session := fs.String("session", "", "only sessions whose id starts with this")
	limit := fs.Int("limit", 50, "maximum hits")
	fs.SetOutput(os.Stderr)
	flags, positional := splitFlags(fs, args)
	if err := fs.Parse(flags); err != nil {
		return 2
	}
	query := strings.Join(positional, " ")
	if strings.TrimSpace(query) == "" {
		fmt.Fprintln(os.Stderr, "blackbox search: missing query")
		return 2
	}
	age, err := parseAge(*since)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 2
	}

	hits, err := store.Search(*home, store.SearchOptions{
		Query: query, Since: age, Tool: *tool, Status: *status,
		SessionID: *session, Limit: *limit,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	if len(hits) == 0 {
		fmt.Println("no matches")
		return 0
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SESSION\tSEQ\tTIME\tSTATUS\tTOOL\tMATCH")
	for _, h := range hits {
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n",
			h.Session.ID, h.Event.Seq,
			h.Event.At.Local().Format("01-02 15:04"),
			dash(h.Event.Status), dash(h.Event.Tool),
			truncate(store.SearchSnippet(h.Event, query), 72))
	}
	return flush(w)
}
