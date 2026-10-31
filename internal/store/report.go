package store

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReportOptions controls cross-session aggregation.
type ReportOptions struct {
	Since time.Duration
	By    string // day, tool, or server
}

// ReportRow is one aggregate bucket.
type ReportRow struct {
	Key     string
	Calls   int
	Errors  int
	Blocked int
	Would   int
	AvgMS   int64
	P95MS   int64
	MaxMS   int64
	Bytes   int64
	CostUSD float64
}

type reportAcc struct {
	calls     int
	errors    int
	blocked   int
	would     int
	durations []int64
	bytes     int64
	cost      float64
}

// Report aggregates usage across sessions.
func Report(home string, opts ReportOptions) ([]ReportRow, error) {
	by := opts.By
	if by == "" {
		by = "day"
	}
	switch by {
	case "day", "tool", "server":
	default:
		return nil, fmt.Errorf("store: report by %q (use day, tool, or server)", by)
	}

	home = Home(home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, err
	}
	db, err := openDB(filepath.Join(home, "blackbox.db"))
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var cutoff int64
	if opts.Since > 0 {
		cutoff = time.Now().Add(-opts.Since).UnixMilli()
	}

	accs := map[string]*reportAcc{}
	get := func(k string) *reportAcc {
		if accs[k] == nil {
			accs[k] = &reportAcc{}
		}
		return accs[k]
	}

	rows, err := db.Query(
		`SELECT e.at, e.tool, e.status, e.duration_ms, e.size, e.cost_usd, s.argv
		   FROM events e JOIN sessions s ON s.id = e.session_id
		  WHERE e.direction = 's2c' AND e.method = 'tools/call'
		    AND e.status IN ('ok','tool_error','error','input_required')
		    AND (? = 0 OR e.at >= ?)`, cutoff, cutoff)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			at       int64
			tool     string
			status   string
			duration int64
			size     int64
			cost     float64
			argvJSON string
		)
		if err := rows.Scan(&at, &tool, &status, &duration, &size, &cost, &argvJSON); err != nil {
			rows.Close()
			return nil, err
		}
		a := get(reportKey(by, time.UnixMilli(at), tool, decodeArgv(argvJSON)))
		a.calls++
		if status == "error" || status == "tool_error" {
			a.errors++
		}
		if duration > 0 {
			a.durations = append(a.durations, duration)
		}
		a.bytes += size
		a.cost += cost
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	blocked, err := db.Query(
		`SELECT d.at, d.tool, d.action, d.mode, s.argv
		   FROM policy_decisions d JOIN sessions s ON s.id = d.session_id
		  WHERE (? = 0 OR d.at >= ?)`, cutoff, cutoff)
	if err != nil {
		return nil, err
	}
	for blocked.Next() {
		var (
			at       int64
			tool     string
			action   string
			mode     string
			argvJSON string
		)
		if err := blocked.Scan(&at, &tool, &action, &mode, &argvJSON); err != nil {
			blocked.Close()
			return nil, err
		}
		acc := get(reportKey(by, time.UnixMilli(at), tool, decodeArgv(argvJSON)))
		switch {
		case mode == "shadow":
			if action == "deny" || action == "require_approval" {
				acc.would++
			}
		case action == "deny":
			acc.blocked++
		}
	}
	if err := blocked.Err(); err != nil {
		blocked.Close()
		return nil, err
	}
	blocked.Close()

	out := make([]ReportRow, 0, len(accs))
	for key, a := range accs {
		row := ReportRow{
			Key: key, Calls: a.calls, Errors: a.errors, Blocked: a.blocked, Would: a.would,
			Bytes: a.bytes, CostUSD: a.cost,
		}
		if len(a.durations) > 0 {
			sort.Slice(a.durations, func(i, j int) bool { return a.durations[i] < a.durations[j] })
			var sum int64
			for _, d := range a.durations {
				sum += d
			}
			row.AvgMS = sum / int64(len(a.durations))
			row.MaxMS = a.durations[len(a.durations)-1]
			idx := int(math.Ceil(0.95*float64(len(a.durations)))) - 1
			if idx < 0 {
				idx = 0
			}
			if idx >= len(a.durations) {
				idx = len(a.durations) - 1
			}
			row.P95MS = a.durations[idx]
		}
		out = append(out, row)
	}

	sort.Slice(out, func(i, j int) bool {
		if by == "day" {
			return out[i].Key > out[j].Key
		}
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func reportKey(by string, at time.Time, tool string, argv []string) string {
	switch by {
	case "tool":
		if tool == "" {
			return "(none)"
		}
		return tool
	case "server":
		if len(argv) == 0 {
			return "(unknown)"
		}
		return strings.Join(argv, " ")
	default:
		return at.Local().Format("2006-01-02")
	}
}
