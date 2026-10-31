package replay

import (
	"fmt"

	"github.com/spacecr8ed/blackbox/internal/event"
)

// Report is the behavioral diff between two recorded runs.
type Report struct {
	Identical bool         `json:"identical"`
	CallsA    int          `json:"callsA"`
	CallsB    int          `json:"callsB"`
	Changes   []Divergence `json:"changes"`
}

type call struct {
	ev     event.Event
	key    string
	result string
}

func calls(events []event.Event) []call {
	results := map[string]string{}
	for _, e := range events {
		if e.Direction == event.ServerToClient && e.ID != "" {
			results[e.ID] = e.Result
		}
	}

	out := make([]call, 0, len(events))
	position := map[string]int{}
	for _, e := range events {
		if e.Direction != event.ClientToServer || e.Method == "" || e.Status == event.StatusNotify {
			continue
		}
		// One logical call can span several MRTR rounds. The last round carries the
		// final arguments and the response, so it replaces the earlier rounds in place.
		group := e.CallID
		if group == "" {
			group = "rpc-" + e.ID
		}
		c := call{
			ev:     e,
			key:    e.Method + "\x00" + e.Tool + "\x00" + canonical(e.Args),
			result: results[e.ID],
		}
		if i, ok := position[group]; ok {
			out[i] = c
			continue
		}
		position[group] = len(out)
		out = append(out, c)
	}
	return out
}

// Diff compares the client-side call sequence of two runs.
//
// It answers the question a flight recorder exists for: given the same task twice,
// where did the agent behave differently? Matching is by method, tool, and canonical
// arguments; response payloads are compared for matched calls.
func Diff(a, b []event.Event) Report {
	ca, cb := calls(a), calls(b)
	rep := Report{CallsA: len(ca), CallsB: len(cb)}

	n, m := len(ca), len(cb)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if ca[i].key == cb[j].key {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	matchedA := make([]bool, n)
	matchedB := make([]bool, m)
	matches := make([][2]int, 0, n)
	for i, j := 0, 0; i < n && j < m; {
		if ca[i].key == cb[j].key {
			matches = append(matches, [2]int{i, j})
			matchedA[i], matchedB[j] = true, true
			i++
			j++
			continue
		}
		if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}

	keysB := make(map[string]bool, m)
	for _, c := range cb {
		keysB[c.key] = true
	}
	keysA := make(map[string]bool, n)
	for _, c := range ca {
		keysA[c.key] = true
	}

	for i, c := range ca {
		if matchedA[i] {
			continue
		}
		kind := "missing"
		detail := "call was not made in run B"
		if keysB[c.key] {
			kind = "reordered"
			detail = "same call, different position"
		}
		rep.Changes = append(rep.Changes, Divergence{
			Kind: kind, Index: c.ev.Seq, Method: c.ev.Method, Tool: c.ev.Tool, Detail: detail,
		})
	}
	for j, c := range cb {
		if matchedB[j] || keysA[c.key] {
			continue
		}
		rep.Changes = append(rep.Changes, Divergence{
			Kind: "extra", Index: c.ev.Seq, Method: c.ev.Method, Tool: c.ev.Tool,
			Detail: "call was made only in run B",
		})
	}

	for _, pair := range matches {
		a, b := ca[pair[0]], cb[pair[1]]
		if a.result == "" || b.result == "" {
			continue
		}
		if canonical(a.result) != canonical(b.result) {
			rep.Changes = append(rep.Changes, Divergence{
				Kind: "result_changed", Index: b.ev.Seq, Method: b.ev.Method, Tool: b.ev.Tool,
				Detail: fmt.Sprintf("response to %s differed", label(b.ev)),
			})
		}
	}

	rep.Identical = len(rep.Changes) == 0
	return rep
}

func label(e event.Event) string {
	if e.Tool != "" {
		return e.Tool
	}
	if e.Method != "" {
		return e.Method
	}
	return "call"
}
