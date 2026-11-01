package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/syyzit/blackbox/internal/policy"
	"github.com/syyzit/blackbox/internal/store"
)

func cmdPolicy(args []string) int {
	if len(args) == 0 || args[0] != "test" {
		fmt.Fprintln(os.Stderr, "usage: blackbox policy test [-policy file] <session-id>")
		return 2
	}
	fs := flag.NewFlagSet("policy test", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	policyPath := fs.String("policy", "", "candidate policy file")
	failOnDeny := fs.Bool("fail-on-deny", false, "exit 1 if the policy would deny a call")
	fs.SetOutput(os.Stderr)
	flags, positional := splitFlags(fs, args[1:])
	if err := fs.Parse(flags); err != nil {
		return 2
	}
	if len(positional) == 0 {
		fmt.Fprintln(os.Stderr, "blackbox policy test: missing session id")
		return 2
	}

	pol, err := loadPolicy(*home, *policyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	meta, events, err := store.Find(*home, positional[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}

	sum := policy.EvaluateSession(pol, strings.Join(meta.Argv, " "), events)
	fmt.Printf("session  %s\n", meta.ID)
	fmt.Printf("calls    %d\n", sum.Total)
	fmt.Printf("result   allow %d · deny %d · require approval %d\n\n",
		sum.Allow, sum.Deny, sum.Approval)

	var blocked []policy.Impact
	for _, im := range sum.Impacts {
		if im.Decision != policy.Allow {
			blocked = append(blocked, im)
		}
	}
	if len(blocked) == 0 {
		fmt.Println("no recorded call would be blocked")
		return 0
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DECISION\tTOOL\tRECORDED\tARGS")
	for _, im := range blocked {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			im.Decision, dash(im.Tool), dash(im.RecordedStatus), truncate(im.Args, 64))
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}

	if *failOnDeny && sum.Deny > 0 {
		return 1
	}
	return 0
}
