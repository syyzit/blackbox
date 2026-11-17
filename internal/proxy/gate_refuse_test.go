package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/syyzit/blackbox/internal/policy"
)

func allowAll(mode policy.Mode) *policy.Policy {
	return &policy.Policy{Default: policy.Allow, Mode: mode}
}

func TestGateRefusesWhatItCannotJudge(t *testing.T) {
	p := newPipeline(pipelineConfig{Policy: allowAll(policy.ModeEnforce), Server: "s"})

	// Even with a policy that allows everything, a frame that two parsers read
	// differently is refused: the policy would be allowing a call it has not seen.
	resp, dec, blocked := p.gate([]byte(`{"jsonrpc":"2.0","id":7,"method":"ping","Method":"tools/call","params":{"name":"x"}}`))
	if !blocked || dec == nil || dec.Action != policy.Deny {
		t.Fatalf("blocked=%v dec=%+v", blocked, dec)
	}
	var reply struct {
		ID    int `json:"id"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &reply); err != nil || reply.ID != 7 ||
		!strings.Contains(reply.Error.Message, "cannot be checked against the policy") {
		t.Fatalf("reply = %s (err %v)", resp, err)
	}

	// Input that is not JSON at all gets one error with a null id.
	resp, _, blocked = p.gate([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call"`))
	if !blocked || !strings.Contains(string(resp), `"id":null`) {
		t.Fatalf("blocked=%v reply=%s", blocked, resp)
	}

	// A notification cannot be answered, but it is still dropped.
	resp, _, blocked = p.gate([]byte(`{"jsonrpc":"2.0","method":"notifications/x","METHOD":"tools/call"}`))
	if !blocked || resp != nil {
		t.Fatalf("blocked=%v reply=%s", blocked, resp)
	}
}

func TestGateRefusesAmbiguousBatchAndAnswersEveryRequest(t *testing.T) {
	p := newPipeline(pipelineConfig{Policy: allowAll(policy.ModeEnforce), Server: "s"})
	batch := `[{"jsonrpc":"2.0","id":1,"method":"tools/list"},` +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"a","name":"b"}},` +
		`{"jsonrpc":"2.0","method":"notifications/x"}]`
	resp, _, blocked := p.gate([]byte(batch))
	if !blocked {
		t.Fatal("a batch with an ambiguous element was forwarded")
	}
	var replies []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(resp, &replies); err != nil || len(replies) != 2 || replies[0].ID != 1 || replies[1].ID != 2 {
		t.Fatalf("want an error for each of the two requests, got %s (err %v)", resp, err)
	}

	if _, _, blocked := p.gate([]byte(`[{"jsonrpc":"2.0","id":1,"method":"tools/list"},7]`)); !blocked {
		t.Fatal("a batch with an element that is not a message was forwarded")
	}
}

func TestGateLeavesNormalTrafficAlone(t *testing.T) {
	p := newPipeline(pipelineConfig{Policy: allowAll(policy.ModeEnforce), Server: "s"})
	for _, frame := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":"a","method":"tools/list"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"result":{"roots":[{"uri":"file:///a"},{"uri":"file:///b"}]}}`,
		`[{"jsonrpc":"2.0","id":1,"method":"tools/list"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`,
		"",
		"   ",
	} {
		if _, _, blocked := p.gate([]byte(frame)); blocked {
			t.Errorf("blocked a normal frame: %q", frame)
		}
	}
}

func TestGateShadowModeNeverRefuses(t *testing.T) {
	p := newPipeline(pipelineConfig{Policy: allowAll(policy.ModeShadow), Server: "s"})
	for _, frame := range []string{
		`{"jsonrpc":"2.0","id":7,"method":"ping","Method":"tools/call","params":{"name":"x"}}`,
		`{not json`,
		`[{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"a","name":"b"}}]`,
	} {
		if _, _, blocked := p.gate([]byte(frame)); blocked {
			t.Errorf("shadow mode blocked %q", frame)
		}
	}
}
