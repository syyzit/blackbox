package jsonrpc

import (
	"strings"
	"testing"
)

func TestDecodeReadsExactKeys(t *testing.T) {
	// Each frame carries a decoy spelled with different capitals. encoding/json would
	// let the decoy win; a server that reads keys exactly never sees it.
	m, err := Decode([]byte(`{"jsonrpc":"2.0","id":3,"ID":99,"method":"tools/call","Method":"ping",
		"params":{"name":"fail","Name":"echo"},"Params":{"name":"other"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Method != "tools/call" || m.IDString() != "3" || m.ToolName() != "fail" {
		t.Fatalf("method=%q id=%q tool=%q", m.Method, m.IDString(), m.ToolName())
	}
}

func TestDecodeToleratesWhatServersTolerate(t *testing.T) {
	for name, frame := range map[string]string{
		"byte order mark":   "\xef\xbb\xbf" + `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"}}`,
		"jsonrpc as number": `{"jsonrpc":2.0,"id":1,"method":"tools/call","params":{"name":"x"}}`,
		"null params id":    `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"},"error":null}`,
	} {
		m, err := Decode([]byte(frame))
		if err != nil || m.ToolName() != "x" {
			t.Errorf("%s: tool=%v err=%v", name, m, err)
		}
	}
	if _, err := Decode([]byte(`{"method":7}`)); err == nil {
		t.Error("a method that is not a string must not decode as a frame without a method")
	}
	if _, err := Decode([]byte(`[1,2]`)); err == nil {
		t.Error("an array is not a single frame")
	}
}

func TestParseToolsReadsExactKeys(t *testing.T) {
	tools, err := ParseTools([]byte(`{"tools":[{"name":"echo","description":"real","Description":"decoy","inputSchema":{"a":1},"InputSchema":{}}]}`))
	if err != nil || len(tools) != 1 {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
	if tools[0].Description != "real" || string(tools[0].InputSchema) != `{"a":1}` {
		t.Fatalf("read the decoy: %+v", tools[0])
	}
}

func TestAmbiguity(t *testing.T) {
	call := func(extra string) string {
		return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read","arguments":{"path":"/tmp/x"}` + extra + `}}`
	}
	clear := []string{
		call(``),
		call(`,"_meta":{"k":"v"}`),
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}`,
		// The same key in different objects is not a repeat.
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":{"a":{"id":1},"b":{"id":2},"list":[{"id":1},{"id":2}]}}}`,
		// Capitals only matter for fields the protocol defines; tools name their own.
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":{"Name":"a","name":"b","ID":1}}}`,
		"\xef\xbb\xbf" + call(``),
	}
	for _, frame := range clear {
		if reason := Ambiguity([]byte(frame)); reason != "" {
			t.Errorf("refused a normal frame (%s): %s", reason, frame)
		}
	}

	ambiguous := map[string]string{
		`{"jsonrpc":"2.0","id":1,"method":"ping","Method":"tools/call","params":{"name":"x"}}`:                 "Method",
		`{"jsonrpc":"2.0","id":1,"ID":2,"method":"ping"}`:                                                      "ID",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"},"Params":{"name":"y"}}`:           "Params",
		call(`,"Arguments":{"path":"/etc/passwd"}`):                                                            "Arguments",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","NAME":"y"}}`:                      "NAME",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","method":"ping","params":{"name":"x"}}`:                 "repeats",
		call(`,"arguments":{"path":"/etc/passwd"}`):                                                            "repeats",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":{"a":{"p":1,"p":2}}}}`: "repeats",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":7}}`:                                   "not a string",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":["x"]}`:                                        "not an object",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`:                                                       "not an object",
		`{"jsonrpc":"2.0","id":1,"method":["tools/call"]}`:                                                     "not a string",
		`"tools/call"`: "not a JSON-RPC message",
		`{not json`:    "not a JSON-RPC message",
	}
	for frame, want := range ambiguous {
		if reason := Ambiguity([]byte(frame)); !strings.Contains(reason, want) {
			t.Errorf("Ambiguity = %q, want it to mention %q: %s", reason, want, frame)
		}
	}
}
