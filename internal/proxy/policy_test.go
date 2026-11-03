package proxy_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/policy"
	"github.com/syyzit/blackbox/internal/proxy"
	"github.com/syyzit/blackbox/internal/redact"
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

func shadowPolicy(tool string) *policy.Policy {
	p := denyPolicy(tool)
	p.Mode = policy.ModeShadow
	return p
}

func TestShadowPolicyRecordsWithoutBlocking(t *testing.T) {
	sink := &memSink{}
	audit := &memAudit{}
	cli, errCh := startProxiedOpts(t, proxy.Options{
		Sink: sink, Policy: shadowPolicy("fail"), Server: "fake-server",
		ServerKey: "srvkey", Ledger: newMapLedger(), Audit: audit,
	})

	res, err := cli.Call("tools/call", map[string]any{"name": "fail", "arguments": map[string]any{}})
	if err != nil {
		t.Fatalf("shadow mode must not block: %v", err)
	}
	if !strings.Contains(string(res), "intentional failure") {
		t.Fatalf("tool did not run: %s", res)
	}
	stop(t, cli, errCh)

	for _, e := range sink.snapshot() {
		if e.Direction == event.ClientToServer && e.Tool == "fail" {
			if e.Status == event.StatusDenied {
				t.Fatalf("shadow mode marked the call denied: %+v", e)
			}
			if e.Decision != string(policy.Deny) {
				t.Fatalf("intended decision not recorded: %+v", e)
			}
		}
	}
	recs := audit.snapshot()
	if len(recs) == 0 || recs[0].Mode != policy.ModeShadow {
		t.Fatalf("audit mode is wrong: %+v", recs)
	}
}

func TestAuditArgsAreRedacted(t *testing.T) {
	const secret = "sk-livekey0123456789abcdef"

	audit := &memAudit{}
	cli, errCh := startProxiedOpts(t, proxy.Options{
		Sink:      &memSink{},
		Redactor:  redact.Default(),
		Policy:    denyPolicy("echo"),
		Server:    "fake-server",
		ServerKey: "srvkey",
		Ledger:    newMapLedger(),
		Audit:     audit,
	})

	if _, err := cli.Call("tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"text": secret},
	}); err == nil {
		t.Fatal("expected the call to be denied")
	}
	stop(t, cli, errCh)

	recs := audit.snapshot()
	if len(recs) == 0 {
		t.Fatal("no audit record")
	}
	for _, r := range recs {
		if strings.Contains(r.Args, secret) {
			t.Fatalf("secret leaked into the audit log: %+v", r)
		}
	}
	if !strings.Contains(recs[0].Args, "[REDACTED") {
		t.Fatalf("audit args were not redacted: %s", recs[0].Args)
	}
}

func TestPolicyBlocksBatchContainingDeniedCall(t *testing.T) {
	sink := &memSink{}
	cli, errCh := startProxiedOpts(t, proxy.Options{
		Sink: sink, Policy: denyPolicy("fail"), Server: "fake-server", ServerKey: "srvkey",
		Ledger: newMapLedger(), Audit: &memAudit{},
	})

	// Smuggling the denied call inside a batch must not get it past the gate.
	batch := `[{"jsonrpc":"2.0","id":101,"method":"tools/call","params":{"name":"echo","arguments":{"text":"a"}}},` +
		`{"jsonrpc":"2.0","id":102,"method":"tools/call","params":{"name":"fail","arguments":{}}}]`
	if err := cli.Send([]byte(batch)); err != nil {
		t.Fatal(err)
	}
	line, err := cli.Next()
	if err != nil {
		t.Fatal(err)
	}
	var replies []struct {
		ID    int `json:"id"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &replies); err != nil {
		t.Fatalf("expected a batch of errors, got %s", line)
	}
	if len(replies) != 2 {
		t.Fatalf("expected an error for each request, got %s", line)
	}
	for _, r := range replies {
		if r.Error == nil || !strings.Contains(r.Error.Message, "blocked by blackbox policy") {
			t.Fatalf("request %d was not blocked: %s", r.ID, line)
		}
	}

	stop(t, cli, errCh)
	for _, e := range sink.snapshot() {
		if e.Direction == event.ServerToClient && !e.Synthetic {
			t.Fatalf("the server answered a blocked batch: %+v", e)
		}
	}
}

type pendingLedger struct{ *mapLedger }

func (pendingLedger) IsPending(server, tool string) bool { return tool == "echo" }

func TestRugPullFromEarlierSessionBlocksBeforeToolsList(t *testing.T) {
	cli, errCh := startProxiedOpts(t, proxy.Options{
		Sink: &memSink{}, Policy: policy.Default(), Server: "fake-server", ServerKey: "srvkey",
		Ledger: pendingLedger{newMapLedger()}, Audit: &memAudit{},
	})
	// No tools/list in this session: the pending change comes from the saved ledger.
	_, err := cli.Call("tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "x"}})
	if err == nil || !strings.Contains(err.Error(), "changed since it was last approved") {
		t.Fatalf("changed tool was not blocked from the first frame: %v", err)
	}
	stop(t, cli, errCh)
}

type sourcedStub struct{ source string }

func (s sourcedStub) Approve(policy.ApprovalRequest) bool { return true }
func (s sourcedStub) ApproveSourced(policy.ApprovalRequest) (bool, string) {
	return true, s.source
}

func TestApprovalSourceIsAudited(t *testing.T) {
	pol := &policy.Policy{Default: policy.Allow, Mode: policy.ModeEnforce, Rules: []policy.Rule{
		{Tool: "echo", Action: policy.RequireApproval, Reason: "needs a human"},
	}}
	sink := &memSink{}
	audit := &memAudit{}

	cli, errCh := startProxiedOpts(t, proxy.Options{
		Sink: sink, Policy: pol, Server: "fake-server", ServerKey: "srvkey",
		Ledger: newMapLedger(), Approver: sourcedStub{source: "ui"}, Audit: audit,
	})
	if _, err := cli.Call("tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "hi"}}); err != nil {
		t.Fatal(err)
	}
	stop(t, cli, errCh)

	recs := audit.snapshot()
	if len(recs) != 1 || !recs[0].Approved || !strings.Contains(recs[0].Reason, "approved via ui") {
		t.Fatalf("audit = %+v", recs)
	}
	for _, e := range sink.snapshot() {
		if e.Tool == "echo" && e.Direction == event.ClientToServer {
			if e.Decision != string(policy.Allow) || !strings.Contains(e.DecisionReason, "approved via ui") {
				t.Fatalf("event = %+v", e)
			}
			return
		}
	}
	t.Fatal("echo call not recorded")
}
