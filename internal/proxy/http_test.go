package proxy_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/jsonrpc"
	"github.com/spacecr8ed/blackbox/internal/proxy"
	"github.com/spacecr8ed/blackbox/internal/redact"
)

func TestHTTPProxyForwardsAndRecords(t *testing.T) {
	var gotMethod, gotName string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Header.Get("Mcp-Method")
		gotName = r.Header.Get("Mcp-Name")

		body, _ := io.ReadAll(r.Body)
		var msg jsonrpc.Message
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if msg.Method != "tools/call" {
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","content":[{"type":"text","text":"ok"}]}}`, string(msg.ID))
	}))
	defer upstream.Close()

	sink := &memSink{}
	h, err := proxy.NewHTTP(proxy.HTTPOptions{
		Upstream:  upstream.URL,
		SessionID: "http-test",
		Sink:      sink,
		Redactor:  redact.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	body := `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`
	resp, err := http.Post(front.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !bytes.Contains(got, []byte(`"ok"`)) {
		t.Fatalf("unexpected response %d: %s", resp.StatusCode, got)
	}

	if gotMethod != "tools/call" || gotName != "echo" {
		t.Fatalf("upstream did not receive routing headers: method=%q name=%q", gotMethod, gotName)
	}

	events := sink.snapshot()
	var sawRequest, sawResponse bool
	for _, e := range events {
		if e.SessionID != "http-test" {
			t.Fatalf("event missing session id: %+v", e)
		}
		if e.Direction == event.ClientToServer && e.ID == "42" && e.Tool == "echo" {
			sawRequest = true
			if e.Status != event.StatusPending {
				t.Fatalf("request status = %q", e.Status)
			}
		}
		if e.Direction == event.ServerToClient && e.ID == "42" {
			sawResponse = true
			if e.Method != "tools/call" || e.Status != event.StatusOK {
				t.Fatalf("response not correlated: %+v", e)
			}
			if e.DurationMS < 0 {
				t.Fatalf("negative duration: %+v", e)
			}
		}
	}
	if !sawRequest || !sawResponse {
		t.Fatalf("missing request/response events: %+v", events)
	}
}

func TestHTTPProxyUsesHeaderRouting(t *testing.T) {
	var gotMethod, gotName string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Header.Get("Mcp-Method")
		gotName = r.Header.Get("Mcp-Name")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jsonrpc":"2.0","id":9,"result":{"resultType":"complete","tools":[]}}`)
	}))
	defer upstream.Close()

	sink := &memSink{}
	h, err := proxy.NewHTTP(proxy.HTTPOptions{Upstream: upstream.URL, SessionID: "hdr", Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	// The body carries no method; routing must come from the headers.
	req, _ := http.NewRequest(http.MethodPost, front.URL, strings.NewReader(`{}`))
	req.Header.Set("Mcp-Method", "tools/list")
	req.Header.Set("Mcp-Name", "echo")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if gotMethod != "tools/list" || gotName != "echo" {
		t.Fatalf("headers were not forwarded: method=%q name=%q", gotMethod, gotName)
	}

	for _, e := range sink.snapshot() {
		if e.Direction != event.ClientToServer {
			continue
		}
		if e.Method != "tools/list" || e.Tool != "echo" {
			t.Fatalf("recorded event ignored header routing: %+v", e)
		}
		return
	}
	t.Fatal("no client event recorded")
}

func TestHTTPProxyStreamsSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		for _, line := range []string{`data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`, "", `data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":2}}`} {
			io.WriteString(w, line+"\n")
			fl.Flush()
		}
	}))
	defer upstream.Close()

	sink := &memSink{}
	h, err := proxy.NewHTTP(proxy.HTTPOptions{Upstream: upstream.URL, SessionID: "sse", Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	resp, err := http.Post(front.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(body, []byte(`"progress":2`)) {
		t.Fatalf("stream was truncated: %s", body)
	}

	var streamed int
	for _, e := range sink.snapshot() {
		if e.Direction == event.ServerToClient && strings.Contains(e.Raw, "progress") {
			streamed++
		}
	}
	if streamed != 2 {
		t.Fatalf("expected 2 streamed events, got %d", streamed)
	}
}
