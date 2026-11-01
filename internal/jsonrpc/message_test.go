package jsonrpc

import (
	"encoding/json"
	"testing"
)

func TestDecodeBatch(t *testing.T) {
	msgs, isBatch := DecodeBatch([]byte(` [{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a"}}, 7, {"jsonrpc":"2.0","method":"n"}]`))
	if !isBatch || len(msgs) != 2 {
		t.Fatalf("isBatch=%v msgs=%d", isBatch, len(msgs))
	}
	if msgs[0].ToolName() != "a" || !msgs[1].IsNotification() {
		t.Fatalf("unexpected messages: %+v %+v", msgs[0], msgs[1])
	}
	if _, isBatch := DecodeBatch([]byte(`{"jsonrpc":"2.0"}`)); isBatch {
		t.Fatal("an object is not a batch")
	}
	if msgs, isBatch := DecodeBatch([]byte(`[not json`)); !isBatch || msgs != nil {
		t.Fatal("a malformed array is still a batch, with no messages")
	}
}

// FuzzDecode feeds arbitrary frames to everything the proxy calls on untrusted input.
// None of it may panic, and a frame that decodes as a single message must not also
// be a batch, or the gate could see it twice or not at all.
func FuzzDecode(f *testing.F) {
	for _, s := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":"abc","result":{"resultType":"input_required","requestState":"s1"}}`,
		`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"nope"}}`,
		`[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"}}]`,
		`{"jsonrpc":"2.0","method":"notifications/x","params":{"_meta":{"io.modelcontextprotocol/subscriptionId":"s"}}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"tools":[{"name":"t","inputSchema":{}}],"usage":{"input_tokens":5}}}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		batch, isBatch := DecodeBatch(b)
		for _, m := range batch {
			exercise(m)
		}
		m, err := Decode(b)
		if err != nil {
			return
		}
		if isBatch && json.Valid(b) {
			t.Fatalf("frame decoded as both a message and a batch: %q", b)
		}
		exercise(m)
	})
}

func exercise(m *Message) {
	_ = m.IsRequest()
	_ = m.IsNotification()
	_ = m.IsResponse()
	_ = m.IDString()
	_ = m.ToolName()
	_ = m.RequestState()
	_ = m.HasInputResponses()
	_ = m.SubscriptionID()
	_ = m.ExternalID()
	_ = m.Error.Error()
	_, _ = ParseTools(m.Result)
	_, _ = ParseResult(m.Result)
	_ = ParseUsage(m.Result)
	_ = ResultRequestState(m.Result)
}
