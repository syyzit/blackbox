// Command blackbox is a local flight recorder for AI agents.
package main

import (
	"bufio"
	"context"
	"encoding/json"
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

	"github.com/spacecr8ed/blackbox/internal/coverage"
	"github.com/spacecr8ed/blackbox/internal/policy"
	"github.com/spacecr8ed/blackbox/internal/proxy"
	"github.com/spacecr8ed/blackbox/internal/redact"
	"github.com/spacecr8ed/blackbox/internal/replay"
	"github.com/spacecr8ed/blackbox/internal/store"
	"github.com/spacecr8ed/blackbox/internal/ui"
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
	case "replay":
		return cmdReplay(args[1:])
	case "diff":
		return cmdDiff(args[1:])
	case "decisions":
		return cmdDecisions(args[1:])
	case "export":
		return cmdExport(args[1:])
	case "ui":
		return cmdUI(args[1:])
	case "init":
		return cmdInit(args[1:])
	case "prune":
		return cmdPrune(args[1:])
	case "dev":
		return cmdDev(args[1:])
	case "search":
		return cmdSearch(args[1:])
	case "report":
		return cmdReport(args[1:])
	case "policy":
		return cmdPolicy(args[1:])
	case "coverage":
		return cmdCoverage(args[1:])
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

  blackbox replay [-home dir] <session-id|prefix> [-- command...]
        Answer an agent from a recorded session, then diff the new run against it.
        With no command, list the recorded exchanges.

  blackbox diff [-home dir] <session-a> <session-b>
        Compare the tool-call sequences of two runs.

  blackbox decisions [-home dir] <session-id|prefix>
        Print the policy audit log for a session.

  blackbox export [-home dir] [-out file] <session-id|prefix>
        Write a redacted incident bundle (JSON) for a session.

  blackbox init [-dry-run] [-wrap all|name1,name2] [-config path]
        Find agent MCP configs and route selected servers through Black Box.

  blackbox prune [-older-than 30d] [-max-size 500MB] [-dry-run]
        Apply a retention policy to the local store.

  blackbox dev [-addr 127.0.0.1:8787] -- <server command>
        Record a server and serve the UI for it in one command.

  blackbox search <query> [-since 7d] [-tool name] [-status s]
        Full-text search across every recorded frame.

  blackbox report [-since 7d] [-by day|tool|server]
        Aggregate calls, errors, latency, bytes, cost and blocks.

  blackbox policy test [-policy file] <session-id>
        Ask what a candidate policy would have done to a recorded session.

  blackbox coverage [-home dir] <session-id|prefix>
        Show which file changes the recording did not account for.

  blackbox ui [-addr 127.0.0.1:8787] [-open]
        Serve the session browser and timeline UI.

  blackbox http -upstream <url> [-addr 127.0.0.1:8787]
        Proxy a streamable-HTTP MCP server and record every request.

  blackbox version

Flags for run:
  -home string              data directory (default ~/.blackbox, or $BLACKBOX_HOME)
  -policy string            policy file (default <home>/policy.json)
  -price-in float           USD per million input tokens, for reported usage
  -price-out float          USD per million output tokens, for reported usage
  -approval-timeout dur     deny an unanswered approval (default 1m)

Examples:
  blackbox run -- npx -y @modelcontextprotocol/server-filesystem ~/notes
  blackbox run -- ./bin/fakeserver
`, version)
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	policyPath := fs.String("policy", "", "policy file (default: <home>/policy.json)")
	priceIn := fs.Float64("price-in", 0, "USD per million input tokens, for reported usage")
	priceOut := fs.Float64("price-out", 0, "USD per million output tokens, for reported usage")
	approvalTimeout := fs.Duration("approval-timeout", time.Minute, "how long to wait for an approval before denying")
	watch := fs.String("watch", ".", "directory to check for unrecorded file changes")
	noWatch := fs.Bool("no-watch", false, "skip the coverage check")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	argv := fs.Args()
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "blackbox run: missing command after --")
		return 2
	}

	pol, err := loadPolicy(*home, *policyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}

	sess, err := store.Open(*home, argv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	defer sess.Close()

	var before *coverage.Snapshot
	if !*noWatch {
		before = snapshotCoverage(*watch)
	}

	fmt.Fprintf(os.Stderr, "blackbox · session %s\n", sess.Meta.ID)
	fmt.Fprintf(os.Stderr, "blackbox · recording to %s\n", filepath.Join(sess.Home, "blackbox.db"))

	err = proxy.RunStdio(context.Background(), proxy.Options{
		Argv:            argv,
		Stdin:           os.Stdin,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
		SessionID:       sess.Meta.ID,
		Sink:            sess,
		Redactor:        redact.Default(),
		PriceInPerMTok:  *priceIn,
		PriceOutPerMTok: *priceOut,
		Policy:          pol,
		Server:          strings.Join(argv, " "),
		ServerKey:       sess.Meta.ServerKey,
		Ledger:          sess.Ledger(),
		Approver:        timeoutApprover(*approvalTimeout),
		Audit:           sess,
	})
	finishCoverage(sess, before)
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
	policyPath := fs.String("policy", "", "policy file (default: <home>/policy.json)")
	approvalTimeout := fs.Duration("approval-timeout", time.Minute, "how long to wait for an approval before denying")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "blackbox http: -upstream is required")
		return 2
	}

	pol, err := loadPolicy(*home, *policyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
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
		Policy:    pol,
		Server:    *upstream,
		ServerKey: sess.Meta.ServerKey,
		Ledger:    sess.Ledger(),
		Approver:  timeoutApprover(*approvalTimeout),
		Audit:     sess,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	return 0
}

func isTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func loadPolicy(home, path string) (*policy.Policy, error) {
	if path == "" {
		candidate := filepath.Join(store.Home(home), "policy.json")
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
		}
	}
	return policy.Load(path)
}

func timeoutApprover(timeout time.Duration) policy.Approver {
	return policy.TimeoutApprover{Inner: terminalApprover{}, Timeout: timeout}
}

// terminalApprover asks on /dev/tty, never on the MCP stream, so the agent cannot
// answer its own approval prompt.
type terminalApprover struct{}

func (terminalApprover) Approve(r policy.ApprovalRequest) bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blackbox · %s needs approval but there is no terminal: denied\n", r.Tool)
		return false
	}
	defer tty.Close()

	args := r.Args
	if len(args) > 240 {
		args = args[:240] + "…"
	}
	fmt.Fprintf(tty, "\nblackbox · approval required\n  server  %s\n  tool    %s\n  args    %s\n  reason  %s\nallow? [y/N] ", r.Server, r.Tool, args, r.Reason)
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func cmdExport(args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	out := fs.String("out", "", "output file (default stdout)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "blackbox export: missing session id")
		return 2
	}

	bundle, err := store.Export(*home, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	data = append(data, '\n')

	if *out == "" {
		_, err = os.Stdout.Write(data)
	} else {
		err = os.WriteFile(*out, data, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	if *out != "" {
		fmt.Fprintf(os.Stderr, "blackbox · wrote %s (%d events, %d decisions)\n", *out, len(bundle.Events), len(bundle.Decisions))
	}
	return 0
}

func cmdDecisions(args []string) int {
	fs := flag.NewFlagSet("decisions", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "blackbox decisions: missing session id")
		return 2
	}

	recs, err := store.Decisions(*home, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	if len(recs) == 0 {
		fmt.Println("no policy decisions recorded")
		return 0
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tACTION\tAPPROVED\tTOOL\tREASON")
	for _, d := range recs {
		fmt.Fprintf(w, "%s\t%s\t%v\t%s\t%s\n",
			d.At.Local().Format("15:04:05"), d.Action, d.Approved, dash(d.Tool), truncate(d.Reason, 48))
	}
	return flush(w)
}

func cmdUI(args []string) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	addr := fs.String("addr", "127.0.0.1:8787", "listen address")
	openBrowser := fs.Bool("open", false, "open the browser")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	url := "http://" + *addr
	if strings.HasPrefix(*addr, ":") {
		url = "http://127.0.0.1" + *addr
	}
	fmt.Fprintf(os.Stderr, "blackbox · ui %s\n", url)
	if *openBrowser {
		_ = exec.Command("open", url).Start()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := ui.Serve(ctx, *home, *addr); err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	return 0
}

func cmdReplay(args []string) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "blackbox replay: missing session id")
		return 2
	}
	id := rest[0]
	var argv []string
	if len(rest) > 1 {
		if rest[1] == "--" {
			argv = rest[2:]
		} else {
			argv = rest[1:]
		}
	}

	meta, events, err := store.Find(*home, id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	eng := replay.New(events)

	if len(argv) == 0 {
		fmt.Printf("session %s · %d exchanges\n\n", meta.ID, len(eng.Exchanges()))
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "#\tMETHOD\tTOOL\tRECORDED\tARGS")
		for i, x := range eng.Exchanges() {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n",
				i+1, dash(x.Request.Method), dash(x.Request.Tool),
				dash(x.Response.Status), truncate(x.Request.Args, 64))
		}
		return flush(w)
	}

	sess, err := store.Open(*home, append([]string{"replay", meta.ID}, argv...))
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	defer sess.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(os.Stderr, "blackbox · replay %s · %d exchanges\n", meta.ID, len(eng.Exchanges()))
	fmt.Fprintf(os.Stderr, "blackbox · new session %s\n", sess.Meta.ID)

	err = replay.ServeStdio(ctx, eng, replay.Options{
		Argv:      argv,
		SessionID: sess.Meta.ID,
		Sink:      sess,
		Redactor:  redact.Default(),
		Stderr:    os.Stderr,
	})
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			fmt.Fprintln(os.Stderr, "blackbox:", err)
			return 1
		}
	}

	_ = sess.Close()
	_, replayEvents, err := store.Find(*home, sess.Meta.ID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	report := replay.Diff(events, replayEvents)
	printReport(report)
	if !report.Identical {
		return 1
	}
	return 0
}

func cmdDiff(args []string) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "blackbox diff: need two session ids")
		return 2
	}

	_, a, err := store.Find(*home, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}
	_, b, err := store.Find(*home, fs.Arg(1))
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}

	printReport(replay.Diff(a, b))
	return 0
}

func printReport(r replay.Report) {
	if r.Identical {
		fmt.Printf("identical · %d calls\n", r.CallsA)
		return
	}
	fmt.Printf("diverged · %d -> %d calls · %d changes\n", r.CallsA, r.CallsB, len(r.Changes))
	for _, c := range r.Changes {
		fmt.Printf("  %-15s %-16s %-10s %s\n", c.Kind, dash(c.Method), dash(c.Tool), c.Detail)
	}
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
	if code := flush(w); code != 0 {
		return code
	}

	if rep, err := store.CoverageFor(*home, meta.ID); err == nil && rep != nil {
		fmt.Printf("\ncoverage  %d changed · %d covered · %d unverified · %d unrecorded\n",
			rep.Changed, rep.Covered, rep.Unverified, rep.Unrecorded)
		shown := 0
		for _, f := range rep.Files {
			if f.Status == coverage.Covered {
				continue
			}
			if shown >= 8 {
				fmt.Printf("  … %d more\n", len(rep.Files)-shown+rep.More)
				break
			}
			fmt.Printf("  %-10s %s (%s)\n", f.Status, f.Path, f.Kind)
			shown++
		}
	}
	return 0
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
