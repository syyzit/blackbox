package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/syyzit/blackbox/internal/coverage"
	"github.com/syyzit/blackbox/internal/proxy"
	"github.com/syyzit/blackbox/internal/redact"
	"github.com/syyzit/blackbox/internal/store"
	"github.com/syyzit/blackbox/internal/ui"
)

// cmdDev runs an MCP server and serves the UI for it in one command.
func cmdDev(args []string) int {
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	addr := fs.String("addr", "127.0.0.1:8787", "UI listen address (the next free port is used)")
	policyPath := fs.String("policy", "", "policy file (default: <home>/policy.json)")
	priceIn := fs.Float64("price-in", 0, "USD per million input tokens, for reported usage")
	priceOut := fs.Float64("price-out", 0, "USD per million output tokens, for reported usage")
	approvalTimeout := fs.Duration("approval-timeout", time.Minute, "how long to wait for an approval before denying")
	open := fs.Bool("open", true, "open the browser")
	keepUI := fs.Bool("keep-ui", true, "keep serving the UI after the server exits")
	watch := fs.String("watch", ".", "directory to check for unrecorded file changes")
	noWatch := fs.Bool("no-watch", false, "skip the coverage check")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	argv := fs.Args()
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "blackbox dev: missing command after --")
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

	ln, url, err := listenWithFallback(*addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	uiErr := make(chan error, 1)
	go func() { uiErr <- ui.ServeListener(ctx, *home, ln) }()

	sessionURL := url + "/?session=" + sess.Meta.ID
	fmt.Fprintf(os.Stderr, "blackbox · session %s\n", sess.Meta.ID)
	fmt.Fprintf(os.Stderr, "blackbox · ui %s\n", sessionURL)
	if *open {
		openBrowser(sessionURL)
	}

	runErr := proxy.RunStdio(ctx, proxy.Options{
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

	code := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			fmt.Fprintln(os.Stderr, "blackbox:", runErr)
			code = 1
		}
	}

	if stats, err := store.StatsFor(*home, sess.Meta.ID); err == nil {
		fmt.Fprintf(os.Stderr, "blackbox · %d calls · %d errors · %.0fms avg · %s\n",
			stats.Calls, stats.Errors, stats.AvgMS, humanBytes(stats.Bytes))
	}

	if *keepUI && isTTY() {
		fmt.Fprintf(os.Stderr, "blackbox · session saved, ui still at %s (ctrl-c to stop)\n", url)
		select {
		case <-ctx.Done():
		case err := <-uiErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintln(os.Stderr, "blackbox:", err)
				return 1
			}
		}
	}
	return code
}

// listenWithFallback binds addr, or the next free port if it is taken, so a stale
// `blackbox ui` never stops the dev loop.
func listenWithFallback(addr string) (net.Listener, string, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", fmt.Errorf("bad -addr %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, "", fmt.Errorf("bad -addr %q: %w", addr, err)
	}

	for i := 0; i < 20; i++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port+i)))
		if err != nil {
			continue
		}
		actual := ln.Addr().(*net.TCPAddr)
		browseHost := host
		if browseHost == "" || browseHost == "0.0.0.0" || browseHost == "::" {
			browseHost = "127.0.0.1"
		}
		return ln, "http://" + net.JoinHostPort(browseHost, strconv.Itoa(actual.Port)), nil
	}
	return nil, "", fmt.Errorf("no free port near %s", addr)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
