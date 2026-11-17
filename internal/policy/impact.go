package policy

import (
	"encoding/json"

	"github.com/syyzit/blackbox/internal/event"
)

// Impact is what a candidate policy would have done to one recorded call.
type Impact struct {
	Seq            int64
	CallID         string
	Tool           string
	Args           string
	Decision       Action
	Reason         string
	RecordedStatus string
}

// ImpactSummary is the result of testing a policy against a recording.
type ImpactSummary struct {
	Total    int
	Allow    int
	Deny     int
	Approval int
	Impacts  []Impact
}

// EvaluateSession runs a candidate policy over the tool calls of a recorded session.
//
// Each logical call is evaluated once, using its first round, because that is when a
// live gate would have made the decision. The recorded status is the final outcome of
// the call, so you can see what a rule would have prevented.
func EvaluateSession(p *Policy, server string, events []event.Event) ImpactSummary {
	return evaluateSession(p, server, events, false)
}

// EvaluateExternalSession does the same for a session recorded outside the proxy (an
// editor plugin or an agent's hooks). Those events store the tool's input as Args, not
// MCP params, and that input is what the live check gave the policy; reading it as
// params would hide it from every match and paths rule.
func EvaluateExternalSession(p *Policy, server string, events []event.Event) ImpactSummary {
	return evaluateSession(p, server, events, true)
}

func evaluateSession(p *Policy, server string, events []event.Event, bareInput bool) ImpactSummary {
	finalStatus := map[string]string{}
	for _, e := range events {
		if e.Direction != event.ServerToClient || e.Status == "" || e.Status == event.StatusPending {
			continue
		}
		key := e.CallID
		if key == "" {
			key = "rpc-" + e.ID
		}
		finalStatus[key] = e.Status
	}

	seen := map[string]bool{}
	var sum ImpactSummary
	for _, e := range events {
		if e.Direction != event.ClientToServer || e.Method != "tools/call" {
			continue
		}
		key := e.CallID
		if key == "" {
			key = "rpc-" + e.ID
		}
		if seen[key] {
			continue
		}
		seen[key] = true

		req := Request{Server: server, Tool: e.Tool, Args: e.Args}
		if bareInput {
			req.Arguments = json.RawMessage(e.Args)
		}
		dec := p.Decide(req)
		sum.Total++
		switch dec.Action {
		case Allow:
			sum.Allow++
		case Deny:
			sum.Deny++
		case RequireApproval:
			sum.Approval++
		}
		sum.Impacts = append(sum.Impacts, Impact{
			Seq:            e.Seq,
			CallID:         key,
			Tool:           e.Tool,
			Args:           e.Args,
			Decision:       dec.Action,
			Reason:         dec.Reason,
			RecordedStatus: finalStatus[key],
		})
	}
	return sum
}
