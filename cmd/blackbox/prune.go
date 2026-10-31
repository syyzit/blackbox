package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spacecr8ed/blackbox/internal/store"
)

func cmdPrune(args []string) int {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	home := fs.String("home", "", "data directory")
	older := fs.String("older-than", "", "delete sessions older than this (e.g. 30d, 720h)")
	maxSize := fs.String("max-size", "", "keep the store under this size (e.g. 500MB)")
	dryRun := fs.Bool("dry-run", false, "report without deleting")
	vacuum := fs.Bool("vacuum", true, "compact the database after deleting")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	age, err := parseAge(*older)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 2
	}
	size, err := parseSize(*maxSize)
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 2
	}
	if age == 0 && size == 0 {
		fmt.Fprintln(os.Stderr, "blackbox prune: give -older-than, -max-size, or both")
		return 2
	}

	res, err := store.Prune(*home, store.PruneOptions{
		OlderThan: age, MaxBytes: size, DryRun: *dryRun, Vacuum: *vacuum,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "blackbox:", err)
		return 1
	}

	if len(res.Sessions) == 0 {
		fmt.Printf("nothing to delete · %s on disk\n", humanBytes(res.Before))
		return 0
	}

	verb := "deleted"
	if *dryRun {
		verb = "would delete"
	}
	fmt.Printf("%s %d session(s) · %d events · %s of payload\n\n",
		verb, len(res.Sessions), res.Events, humanBytes(res.Bytes))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTARTED\tEVENTS\tSIZE\tCOMMAND")
	for _, s := range res.Sessions {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n",
			s.ID, s.StartedAt.Local().Format("2006-01-02 15:04"), s.Events,
			humanBytes(res.Sizes[s.ID]), truncate(strings.Join(s.Argv, " "), 48))
	}
	_ = w.Flush()

	if *dryRun {
		fmt.Printf("\n(dry run, nothing deleted; %s on disk)\n", humanBytes(res.Before))
		return 0
	}
	fmt.Printf("\nfile %s -> %s", humanBytes(res.Before), humanBytes(res.After))
	if res.Vacuumed {
		fmt.Print(" (vacuumed)")
	}
	fmt.Println()
	return 0
}

func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && strings.HasSuffix(s, "d") {
		return time.Duration(n) * 24 * time.Hour, nil
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(s, "w")); err == nil && strings.HasSuffix(s, "w") {
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad -older-than %q: use 30d, 12h, or 90m", s)
	}
	return d, nil
}

func parseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	units := []struct {
		suffix string
		mult   float64
	}{
		{"KB", 1 << 10}, {"MB", 1 << 20}, {"GB", 1 << 30}, {"TB", 1 << 40},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"B", 1},
	}
	for _, u := range units {
		if !strings.HasSuffix(s, u.suffix) {
			continue
		}
		num := strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
		n, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return 0, fmt.Errorf("bad size %q", s)
		}
		return int64(n * u.mult), nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad size %q: use 500MB, 2GB, or bytes", s)
	}
	return n, nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
