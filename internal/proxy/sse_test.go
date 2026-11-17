package proxy

import (
	"bytes"
	"testing"
)

func feed(stream []byte) [][]byte {
	var p sseParser
	var out [][]byte
	for _, l := range bytes.SplitAfter(stream, []byte("\n")) {
		if payload, ok := p.line(l); ok {
			out = append(out, payload)
		}
	}
	if payload, ok := p.flush(); ok {
		out = append(out, payload)
	}
	return out
}

func FuzzSSEParser(f *testing.F) {
	f.Add([]byte("event: message\nid: 1\ndata: {\"a\":1}\n\n: keepalive\n\n"), []byte(`{"jsonrpc":"2.0"}`))
	f.Add([]byte("data\ndata:x\r\n\r\ndata: [DONE]\n\n"), []byte(`x`))
	f.Fuzz(func(t *testing.T, stream, payload []byte) {
		_ = feed(stream) // arbitrary streams must not panic

		// A single-line payload framed as one event comes back as itself.
		if bytes.ContainsAny(payload, "\r\n") {
			return
		}
		want := bytes.TrimSpace(payload)
		if len(want) == 0 || bytes.Equal(want, []byte("[DONE]")) {
			return
		}
		framed := append(append([]byte("event: message\ndata: "), payload...), "\n\n"...)
		got := feed(framed)
		if len(got) != 1 || !bytes.Equal(got[0], want) {
			t.Fatalf("payload %q came back as %q", payload, got)
		}
	})
}
