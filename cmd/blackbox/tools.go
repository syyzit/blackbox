package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/syyzit/blackbox/internal/store"
)

// cmdTools lists tool definitions that changed since they were approved, and accepts
// them. A changed definition is reported (and blocked, with rugPullBlock) until a
// human accepts it here, so a restart never approves a rug-pull by itself.
func cmdTools(args []string) int {
	if len(args) > 0 && args[0] == "reset" {
		return toolsReset(args[1:])
	}
	accept := len(args) > 0 && args[0] == "accept"
	if accept {
		args = args[1:]
	}

	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	server := fs.String("server", "", "only this server key (default: all servers)")
	all := fs.Bool("all", false, "accept every pending change")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ledger, err := store.LoadLedger(store.LedgerPath(*home))
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	names := serverNames(*home)

	if !accept {
		changes := ledger.PendingChanges()
		if len(changes) == 0 {
			fmt.Println("no changed tool definitions")
			return 0
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SERVER\tTOOL\tAPPROVED\tNOW\tCOMMAND")
		for _, c := range changes {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Server, c.Tool, c.Approved, c.Pending, truncate(names[c.Server], 48))
		}
		if err := w.Flush(); err != nil {
			return 1
		}
		fmt.Println("\nreview the new definitions, then: blackbox tools accept -server <key> <tool>... or -all")
		return 0
	}

	if !*all && *server == "" && len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "blackbox tools accept: name a server (-server key), tool names, or pass -all")
		return 2
	}

	accepted, err := ledger.Accept(*server, fs.Args()...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	if len(accepted) == 0 {
		fmt.Println("nothing to accept")
		return 0
	}
	for _, c := range accepted {
		fmt.Printf("accepted %s %s (%s -> %s)\n", c.Server, c.Tool, c.Approved, c.Pending)
	}
	return 0
}

// serverNames maps server keys to the command of their most recent session.
func serverNames(home string) map[string]string {
	out := map[string]string{}
	metas, err := store.List(home)
	if err != nil {
		return out
	}
	for _, m := range metas {
		if _, ok := out[m.ServerKey]; !ok && m.ServerKey != "" {
			out[m.ServerKey] = strings.Join(m.Argv, " ")
		}
	}
	return out
}

// toolsReset moves a ledger aside after it has been corrupted, so the next run starts
// from an empty, trusted history instead of silently forgetting approvals.
func toolsReset(args []string) int {
	fs := flag.NewFlagSet("tools reset", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path := store.LedgerPath(*home)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Println("no ledger to reset")
		return 0
	}
	backup := path + ".corrupt-" + time.Now().Format("20060102-150405")
	if err := os.Rename(path, backup); err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	fmt.Printf("moved %s to %s\n", path, backup)
	return 0
}
