package policy

import "github.com/syyzit/blackbox/internal/event"

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

		dec := p.Decide(Request{Server: server, Tool: e.Tool, Args: e.Args})
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
