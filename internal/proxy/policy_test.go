package proxy_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/policy"
	"github.com/spacecr8ed/blackbox/internal/proxy"
)

type memAudit struct {
	mu   sync.Mutex
	recs []policy.DecisionRecord
}

func (m *memAudit) Record(r policy.DecisionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, r)
	return nil
}

func (m *memAudit) snapshot() []policy.DecisionRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]policy.DecisionRecord(nil), m.recs...)
}

type stubApprover struct {
	allow bool
	calls int
}

func (s *stubApprover) Approve(policy.ApprovalRequest) bool {
	s.calls++
	return s.allow
}

type mapLedger struct {
	mu   sync.Mutex
	seen map[string]string
}

func newMapLedger() *mapLedger { return &mapLedger{seen: map[string]string{}} }

func (l *mapLedger) Observe(server, tool, hash string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := server + "|" + tool
	prev, ok := l.seen[key]
	l.seen[key] = hash
	return ok && prev != hash
}

func denyPolicy(tool string) *policy.Policy {
	return &policy.Policy{Default: policy.Allow, RugPullBlock: true, Rules: []policy.Rule{
		{Tool: tool, Action: policy.Deny, Reason: "blocked in test"},
	}}
}

func TestPolicyDeniesAndAudits(t *testing.T) {
	sink := &memSink{}
	audit := &memAudit{}

	cli, errCh := startProxiedOpts(t, proxy.Options{
		Sink:      sink,
		Policy:    denyPolicy("fail"),
		Server:    "fake-server",
		ServerKey: "srvkey",
		Ledger:    newMapLedger(),
		Audit:     audit,
	})

	_, err := cli.Call("tools/call", map[string]any{
		"name":      "fail",
		"arguments": map[string]any{},
	})
	if err == nil {
		t.Fatal("denied call should return a JSON-RPC error")
	}
	if !strings.Contains(err.Error(), "blocked by blackbox policy") {
		t.Fatalf("unexpected error: %v", err)
	}

	// An allowed tool still works in the same session.
	if _, err := cli.Call("tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"text": "still allowed"},
	}); err != nil {
		t.Fatalf("allowed call failed: %v", err)
	}

	stop(t, cli, errCh)

	var denied, synthetic bool
	for _, e := range sink.snapshot() {
		if e.Status == event.StatusDenied && e.Direction == event.ClientToServer && e.Tool == "fail" {
			denied = true
		}
		if e.Synthetic && e.Status == event.StatusDenied {
			synthetic = true
		}
	}
	if !denied || !synthetic {
		t.Fatalf("denied call was not recorded with its synthetic response: %+v", sink.snapshot())
	}

	recs := audit.snapshot()
	if len(recs) == 0 || recs[0].Action != policy.Deny || recs[0].Tool != "fail" {
		t.Fatalf("denied decision was not audited first: %+v", recs)
	}
}

func TestApprovalGate(t *testing.T) {
	pol := &policy.Policy{Default: policy.Allow, RugPullBlock: true, Rules: []policy.Rule{
		{Tool: "echo", Action: policy.RequireApproval, Reason: "echo needs a human"},
	}}

	// Denied approval.
	denier := &stubApprover{allow: false}
	sink := &memSink{}
	cli, errCh := startProxiedOpts(t, proxy.Options{
		Sink: sink, Policy: pol, Server: "fake-server", ServerKey: "srvkey",
		Ledger: newMapLedger(), Approver: denier, Audit: &memAudit{},
	})
	if _, err := cli.Call("tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "x"}}); err == nil {
		t.Fatal("unapproved call should be denied")
	}
	stop(t, cli, errCh)
	if denier.calls != 1 {
		t.Fatalf("approver called %d times, want 1", denier.calls)
	}

	// Approved.
	accepter := &stubApprover{allow: true}
	sink2 := &memSink{}
	cli2, errCh2 := startProxiedOpts(t, proxy.Options{
		Sink: sink2, Policy: pol, Server: "fake-server", ServerKey: "srvkey",
		Ledger: newMapLedger(), Approver: accepter, Audit: &memAudit{},
	})
	res, err := cli2.Call("tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "approved run"}})
	if err != nil {
		t.Fatalf("approved call failed: %v", err)
	}
	if !strings.Contains(string(res), "approved run") {
		t.Fatalf("unexpected result: %s", res)
	}
	stop(t, cli2, errCh2)
	if accepter.calls != 1 {
		t.Fatalf("approver called %d times, want 1", accepter.calls)
	}
}

func TestRugPullBlocksChangedTool(t *testing.T) {
	ledger := newMapLedger()
	// Pretend a previous run approved a different definition for echo.
	ledger.seen["srvkey|echo"] = "olderhash0000000"

	sink := &memSink{}
	audit := &memAudit{}
	cli, errCh := startProxiedOpts(t, proxy.Options{
		Sink: sink, Policy: policy.Default(), Server: "fake-server", ServerKey: "srvkey",
		Ledger: ledger, Audit: audit,
	})

	if _, err := cli.Call("tools/list", map[string]any{}); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	_, err := cli.Call("tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "x"}})
	if err == nil {
		t.Fatal("call to a changed tool should be blocked")
	}
	if !strings.Contains(err.Error(), "changed since it was last approved") {
		t.Fatalf("unexpected error: %v", err)
	}

	stop(t, cli, errCh)

	var flagged bool
	for _, e := range sink.snapshot() {
		for _, d := range e.ToolDefs {
			if d.Name == "echo" && d.Changed {
				flagged = true
			}
		}
	}
	if !flagged {
		t.Fatal("changed tool definition was not flagged in the recording")
	}
	if len(audit.snapshot()) == 0 {
		t.Fatal("rug-pull denial was not audited")
	}
}
