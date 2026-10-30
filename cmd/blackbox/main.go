// Command blackbox is a local flight recorder for AI agents.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spacecr8ed/blackbox/internal/proxy"
	"github.com/spacecr8ed/blackbox/internal/redact"
	"github.com/spacecr8ed/blackbox/internal/store"
)

const version = "0.1.0"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:])
	case "sessions", "ls":
		return cmdSessions(args[1:])
	case "show":
		return cmdShow(args[1:])
	case "stats":
		return cmdStats(args[1:])
	case "http":
		return cmdHTTP(args[1:])
	case "version", "--version", "-v":
		fmt.Println("blackbox", version)
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "blackbox: unknown command %q\n\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `blackbox %s - a flight recorder for AI agents

Usage:
  blackbox run [flags] -- <command> [args...]
        Start an MCP server and record every frame that crosses its stdio.

  blackbox sessions [-home dir]
        List recorded sessions, newest first.

  blackbox show [-home dir] <session-id|prefix>
        Print the recorded timeline for a session.

  blackbox stats [-home dir] <session-id|prefix>
        Print latency, error and cost aggregates for a session.

  blackbox http -upstream <url> [-addr 127.0.0.1:8787]
        Proxy a streamable-HTTP MCP server and record every request.

  blackbox version

Flags for run:
  -home string   data directory (default ~/.blackbox, or $BLACKBOX_HOME)

Examples:
  blackbox run -- npx -y @modelcontextprotocol/server-filesystem ~/notes
  blackbox run -- ./bin/fakeserver
`, version)
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	argv := fs.Args()
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "blackbox run: missing command after --")
		return 2
	}

	sess, err := store.Open(*home, argv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	defer sess.Close()

	fmt.Fprintf(os.Stderr, "blackbox · session %s\n", sess.Meta.ID)
	fmt.Fprintf(os.Stderr, "blackbox · recording to %s\n", filepath.Join(sess.Home, "blackbox.db"))

	err = proxy.RunStdio(context.Background(), proxy.Options{
		Argv:      argv,
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		SessionID: sess.Meta.ID,
		Sink:      sess,
		Redactor:  redact.Default(),
	})
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	return 0
}

func cmdSessions(args []string) int {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	metas, err := store.List(*home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	if len(metas) == 0 {
		fmt.Println("no sessions recorded yet")
		return 0
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTARTED\tEVENTS\tCOMMAND")
	for _, m := range metas {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n",
			m.ID,
			m.StartedAt.Local().Format("2006-01-02 15:04:05"),
			m.Events,
			truncate(strings.Join(m.Argv, " "), 64),
		)
	}
	return flush(w)
}

func cmdHTTP(args []string) int {
	fs := flag.NewFlagSet("http", flag.ContinueOnError)
	upstream := fs.String("upstream", "", "upstream MCP server base URL")
	addr := fs.String("addr", "127.0.0.1:8787", "listen address")
	home := fs.String("home", "", "data directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "blackbox http: -upstream is required")
		return 2
	}

	sess, err := store.Open(*home, []string{"http-proxy", *upstream})
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	defer sess.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(os.Stderr, "blackbox · session %s\n", sess.Meta.ID)
	fmt.Fprintf(os.Stderr, "blackbox · %s -> %s\n", *addr, *upstream)

	err = proxy.RunHTTP(ctx, *addr, proxy.HTTPOptions{
		Upstream:  *upstream,
		SessionID: sess.Meta.ID,
		Sink:      sess,
		Redactor:  redact.Default(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	return 0
}

func cmdStats(args []string) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "blackbox stats: missing session id")
		return 2
	}

	stats, err := store.StatsFor(*home, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}

	fmt.Printf("calls    %d\n", stats.Calls)
	fmt.Printf("errors   %d\n", stats.Errors)
	fmt.Printf("latency  total %dms · avg %.1fms · max %dms\n", stats.TotalMS, stats.AvgMS, stats.MaxMS)
	fmt.Printf("bytes    %d\n", stats.Bytes)
	fmt.Printf("cost     $%.4f\n", stats.CostUSD)
	if len(stats.Tools) > 0 {
		fmt.Println()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TOOL\tCALLS\tERRORS\tAVG\tMAX")
		for _, t := range stats.Tools {
			fmt.Fprintf(w, "%s\t%d\t%d\t%.1fms\t%dms\n", t.Tool, t.Calls, t.Errors, t.AvgMS, t.MaxMS)
		}
		return flush(w)
	}
	return 0
}

func cmdShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "blackbox show: missing session id")
		return 2
	}

	meta, events, err := store.Find(*home, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}

	fmt.Printf("session  %s\n", meta.ID)
	fmt.Printf("started  %s\n", meta.StartedAt.Local().Format(time.RFC3339))
	fmt.Printf("command  %s\n", strings.Join(meta.Argv, " "))
	fmt.Printf("events   %d\n\n", len(events))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SEQ\tT\tDIR\tSTATUS\tMS\tMETHOD\tTOOL\tID\tFRAME")
	for _, e := range events {
		arrow := "->"
		if e.Direction == "s2c" {
			arrow = "<-"
		}
		t := e.At.Sub(meta.StartedAt).Round(time.Millisecond)
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			e.Seq, t, arrow,
			dash(e.Status),
			e.DurationMS,
			dash(e.Method), dash(e.Tool), dash(e.ID),
			truncate(redact.Default().String(e.Raw), 64))
		if e.ToolDefs != nil {
			for _, d := range e.ToolDefs {
				flag := ""
				if d.Changed {
					flag = "  ** CHANGED **"
				}
				fmt.Fprintf(w, "\t\t\t\t\t\t\to\t%s\t%s%s\n", d.Name, d.Hash, flag)
			}
		}
	}
	return flush(w)
}

func flush(w *tabwriter.Writer) int {
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	return 0
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
