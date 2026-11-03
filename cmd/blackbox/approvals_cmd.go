package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/syyzit/blackbox/internal/store"
)

func cmdApprovals(args []string) int {
	fs := flag.NewFlagSet("approvals", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	all := fs.Bool("all", false, "include decided requests")
	limit := fs.Int("limit", 50, "maximum rows")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	list, err := store.ListApprovals(*home, !*all, *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	if len(list) == 0 {
		if *all {
			fmt.Println("no approvals recorded")
		} else {
			fmt.Println("no pending approvals")
		}
		return 0
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tAGE\tSTATUS\tTOOL\tSESSION\tREASON")
	now := time.Now()
	for _, a := range list {
		age := now.Sub(a.CreatedAt).Round(time.Second)
		if a.DecidedAt.IsZero() {
			decorated := fmt.Sprintf("%s (%s)", a.Status, age)
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", a.ID, age, decorated, dash(a.Tool), shortSessionID(a.SessionID), truncate(a.Reason, 40))
			continue
		}
		fmt.Fprintf(w, "%d\t%s\t%s/%s\t%s\t%s\t%s\n", a.ID, age, a.Status, a.DecidedBy, dash(a.Tool), shortSessionID(a.SessionID), truncate(a.Reason, 40))
	}
	return flush(w)
}

func cmdApprove(args []string) int { return decideApprovalCmd(args, true) }
func cmdDeny(args []string) int    { return decideApprovalCmd(args, false) }

func decideApprovalCmd(args []string, approved bool) int {
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	by := fs.String("by", "cli", "recorded source of the decision")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "blackbox: missing approval id")
		return 2
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blackbox: bad approval id %q\n", fs.Arg(0))
		return 2
	}

	if err := store.DecideApproval(*home, id, approved, *by); err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	verb := "denied"
	if approved {
		verb = "approved"
	}
	fmt.Printf("%s approval %d (by %s)\n", verb, id, *by)
	return 0
}

func shortSessionID(id string) string {
	if len(id) <= 18 {
		return id
	}
	return id[:8] + "…" + id[len(id)-6:]
}
