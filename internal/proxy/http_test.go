package proxy_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/jsonrpc"
	"github.com/syyzit/blackbox/internal/proxy"
	"github.com/syyzit/blackbox/internal/redact"
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
		for _, line := range []string{`data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1,"_meta":{"io.modelcontextprotocol/subscriptionId":"sub-1"}}}`, "", `data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":2,"_meta":{"io.modelcontextprotocol/subscriptionId":"sub-1"}}}`} {
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

	req, _ := http.NewRequest(http.MethodPost, front.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen"}`))
	req.Header.Set("Mcp-Method", "subscriptions/listen")
	req.Header.Set("Mcp-Name", "feed")
	resp, err := http.DefaultClient.Do(req)
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
		if e.Direction != event.ServerToClient || e.Method != "notifications/progress" {
			continue
		}
		streamed++
		if e.Tool != "feed" {
			t.Fatalf("streamed notification was not attributed to the subscription: %+v", e)
		}
		if e.SubscriptionID != "sub-1" {
			t.Fatalf("subscription id was not captured: %+v", e)
		}
		if strings.HasPrefix(e.Raw, "data:") {
			t.Fatalf("SSE envelope leaked into the recorded payload: %q", e.Raw)
		}
		if !json.Valid([]byte(e.Raw)) {
			t.Fatalf("recorded SSE payload is not JSON: %q", e.Raw)
		}
	}
	if streamed != 2 {
		t.Fatalf("expected 2 streamed events, got %d", streamed)
	}
}

func newHTTPFront(t *testing.T, upstream http.Handler) (*memSink, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	sink := &memSink{}
	h, err := proxy.NewHTTP(proxy.HTTPOptions{Upstream: up.URL, SessionID: "http-test", Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h)
	t.Cleanup(front.Close)
	return sink, front
}

func TestHTTPSSERecordsOnlyEventPayloads(t *testing.T) {
	sink, front := newHTTPFront(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// A comment, event/id fields, and one payload split over two data lines.
		io.WriteString(w, ": keepalive\n\n")
		io.WriteString(w, "event: message\nid: 7\n")
		io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\n")
		io.WriteString(w, "data: \"result\":{\"content\":[]}}\n\n")
	}))

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`
	resp, err := http.Post(front.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(got, []byte("event: message")) {
		t.Fatalf("stream was not forwarded unchanged: %q", got)
	}

	var frames []event.Event
	for _, e := range sink.snapshot() {
		if e.Direction == event.ServerToClient {
			frames = append(frames, e)
		}
	}
	if len(frames) != 1 {
		t.Fatalf("want 1 recorded server frame, got %d: %+v", len(frames), frames)
	}
	if frames[0].ID != "1" || frames[0].Tool != "echo" || frames[0].Status != event.StatusOK {
		t.Fatalf("multi-line event was not reassembled and matched: %+v", frames[0])
	}
}

func TestHTTPRejectsRoutingHeaderMismatch(t *testing.T) {
	called := false
	_, front := newHTTPFront(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_all","arguments":{}}}`
	req, _ := http.NewRequest(http.MethodPost, front.URL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "read_file")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || called {
		t.Fatalf("mismatched headers were forwarded: status %d, upstream called %v", resp.StatusCode, called)
	}
}

func TestHTTPKeepsClientsWithSameIDApart(t *testing.T) {
	release := make(chan struct{})
	sink, front := newHTTPFront(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg jsonrpc.Message
		_ = json.Unmarshal(body, &msg)
		if msg.ToolName() == "slow" {
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`)
	}))

	post := func(session, tool string) {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":{}}}`
		req, _ := http.NewRequest(http.MethodPost, front.URL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Mcp-Session-Id", session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// Client A's call is in flight when client B sends the same JSON-RPC id.
	done := make(chan struct{})
	go func() { post("session-a", "slow"); close(done) }()
	for len(sink.snapshot()) == 0 {
		runtime.Gosched()
	}
	post("session-b", "fast")
	close(release)
	<-done

	tools := map[string]string{} // call id -> tool of the response
	for _, e := range sink.snapshot() {
		if e.Direction == event.ServerToClient {
			tools[e.CallID] = e.Tool
		}
	}
	if len(tools) != 2 {
		t.Fatalf("responses were merged into one call: %+v", tools)
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		seen[tool] = true
	}
	if !seen["slow"] || !seen["fast"] {
		t.Fatalf("responses were attributed to the wrong client's call: %+v", tools)
	}
}
