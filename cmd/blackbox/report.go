package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spacecr8ed/blackbox/internal/store"
)

func cmdReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	since := fs.String("since", "7d", "window to report on (e.g. 7d, 30d, 0 for all)")
	by := fs.String("by", "day", "group by day, tool, or server")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	age, err := parseAge(*since)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 2
	}

	rows, err := store.Report(*home, store.ReportOptions{Since: age, By: *by})
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Println("no calls in this window")
		return 0
	}

	label := "KEY"
	switch *by {
	case "day":
		label = "DAY"
	case "tool":
		label = "TOOL"
	case "server":
		label = "SERVER"
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\tCALLS\tERRORS\tBLOCKED\tWOULD\tAVG\tP95\tMAX\tBYTES\tCOST\n", label)
	var totals store.ReportRow
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%dms\t%dms\t%dms\t%s\t$%.3f\n",
			truncate(r.Key, 40), r.Calls, r.Errors, r.Blocked, r.Would,
			r.AvgMS, r.P95MS, r.MaxMS, humanBytes(r.Bytes), r.CostUSD)
		totals.Calls += r.Calls
		totals.Errors += r.Errors
		totals.Blocked += r.Blocked
		totals.Would += r.Would
		totals.Bytes += r.Bytes
		totals.CostUSD += r.CostUSD
	}
	fmt.Fprintf(w, "TOTAL\t%d\t%d\t%d\t%d\t\t\t\t%s\t$%.3f\n",
		totals.Calls, totals.Errors, totals.Blocked, totals.Would, humanBytes(totals.Bytes), totals.CostUSD)
	return flush(w)
}
