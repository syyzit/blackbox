package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spacecr8ed/blackbox/internal/mcpconfig"
)

// cmdInit discovers agent MCP configs and routes selected servers through Black Box.
func cmdInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "show the rewrite without writing")
	yes := fs.Bool("yes", false, "write without prompting")
	wrap := fs.String("wrap", "", "'all', or a comma-separated list of server names")
	only := fs.String("config", "", "only this config file")
	command := fs.String("command", "", "command to insert (default: this binary)")
	dir := fs.String("dir", "", "project directory to scan (default: working directory)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	bin := *command
	if bin == "" {
		if exe, err := os.Executable(); err == nil {
			bin = exe
		} else {
			bin = "blackbox"
		}
	}

	wd := *dir
	if wd == "" {
		wd, _ = os.Getwd()
	}

	locs := mcpconfig.Locations(wd)
	if *only != "" {
		b, err := os.ReadFile(*only)
		if err != nil {
			fmt.Fprintln(os.Stderr, "blackbox:", err)
			return 1
		}
		style, err := mcpconfig.DetectStyle(b)
		if err != nil {
			fmt.Fprintln(os.Stderr, "blackbox:", err)
			return 1
		}
		locs = []mcpconfig.Location{{Path: *only, Client: "custom", Style: style}}
	}

	type entry struct {
		cfg *mcpconfig.Config
		srv mcpconfig.Server
	}
	var found []entry
	for _, loc := range locs {
		if _, err := os.Stat(loc.Path); err != nil {
			continue
		}
		cfg, err := mcpconfig.Load(loc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "blackbox · skipping %s: %v\n", loc.Path, err)
			continue
		}
		servers, err := cfg.Servers()
		if err != nil {
			fmt.Fprintf(os.Stderr, "blackbox · skipping %s: %v\n", loc.Path, err)
			continue
		}
		for _, srv := range servers {
			found = append(found, entry{cfg: cfg, srv: srv})
		}
	}

	if len(found) == 0 {
		fmt.Println("no MCP servers found in known config files")
		return 0
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CONFIG\tCLIENT\tSERVER\tSTATE\tCOMMAND")
	for _, e := range found {
		state := "available"
		switch {
		case e.srv.Remote:
			state = "remote"
		case e.srv.Wrapped:
			state = "wrapped"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			e.cfg.Path, e.cfg.Client, e.srv.Name, state, truncate(strings.Join(e.srv.Command, " "), 64))
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}

	if *wrap == "" {
		fmt.Println()
		fmt.Println("nothing changed. wrap with:")
		fmt.Println("  blackbox init -wrap all")
		fmt.Println("  blackbox init -wrap name1,name2")
		return 0
	}

	selected := map[string]bool{}
	if *wrap == "all" {
		for _, e := range found {
			if !e.srv.Remote && !e.srv.Wrapped {
				selected[e.srv.Name] = true
			}
		}
	} else {
		for _, name := range strings.Split(*wrap, ",") {
			if name = strings.TrimSpace(name); name != "" {
				selected[name] = true
			}
		}
	}

	changed := map[*mcpconfig.Config]bool{}
	matched := map[string]bool{}
	fmt.Println()
	for _, e := range found {
		if e.srv.Remote || e.srv.Wrapped || !selected[e.srv.Name] {
			continue
		}
		before, after, err := e.cfg.WrapWith(e.srv.Name, bin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "blackbox · %s: %v\n", e.srv.Name, err)
			continue
		}
		matched[e.srv.Name] = true
		changed[e.cfg] = true
		fmt.Printf("+ %s\n  %s\n    %s\n      -> %s\n", e.cfg.Path, e.srv.Name, before, after)
	}

	for name := range selected {
		if !matched[name] {
			fmt.Fprintf(os.Stderr, "blackbox · no wrappable server named %q\n", name)
			return 1
		}
	}
	if len(changed) == 0 {
		fmt.Println("nothing to do")
		return 0
	}

	if *dryRun {
		fmt.Printf("\ndry run: %d file(s) would be written, nothing changed\n", len(changed))
		return 0
	}
	if !*yes {
		if !isTTY() {
			fmt.Fprintln(os.Stderr, "blackbox · refusing to write without -yes in a non-interactive session")
			return 1
		}
		fmt.Print("\nwrite these changes? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))
		if line != "y" && line != "yes" {
			fmt.Println("aborted")
			return 1
		}
	}

	cfgs := make([]*mcpconfig.Config, 0, len(changed))
	for cfg := range changed {
		cfgs = append(cfgs, cfg)
	}
	sort.Slice(cfgs, func(i, j int) bool { return cfgs[i].Path < cfgs[j].Path })
	for _, cfg := range cfgs {
		backup, err := cfg.Write()
		if err != nil {
			fmt.Fprintln(os.Stderr, "blackbox:", err)
			return 1
		}
		fmt.Printf("wrote %s (backup %s)\n", cfg.Path, backup)
	}
	return 0
}
