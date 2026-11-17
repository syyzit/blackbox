package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/syyzit/blackbox/internal/event"
)

// A line longer than the frame limit is not held in memory: it is forwarded as it
// arrives, its event is not recorded, and one event says so.
func TestSSELineOverLimit(t *testing.T) {
	long := `data: {"jsonrpc":"2.0","method":"notifications/message","params":{"text":"` + strings.Repeat("x", 300) + `"}}`
	small := `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`
	stream := "event: message\n" + long + "\ndata: more of the same event\n\n" + "data: " + small + "\n\n"

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, stream)
	}))
	defer up.Close()
	sink := &corrSink{}
	h, err := NewHTTP(HTTPOptions{Upstream: up.URL, SessionID: "s", Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	h.rec.maxFrame = 100
	front := httptest.NewServer(h)
	defer front.Close()

	resp, err := http.Post(front.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != stream {
		t.Fatalf("stream was altered: %q", got)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	var notes, frames []event.Event
	for _, e := range sink.events {
		switch {
		case e.Direction != event.ServerToClient || e.Synthetic:
		case strings.Contains(e.Error, "not recorded"):
			notes = append(notes, e)
		default:
			frames = append(frames, e)
		}
	}
	if len(notes) != 1 || notes[0].Size != len(long) || len(notes[0].Raw) > 100+len("…") {
		t.Fatalf("over-limit line not reported once, bounded: %+v", notes)
	}
	if len(frames) != 1 || frames[0].Raw != small {
		t.Fatalf("recorded frames %+v, want only the next event", frames)
	}
}

// The parser keeps at most the limit of a line that never ends.
func TestSSEParserBoundsAnUnendedLine(t *testing.T) {
	p := sseParser{max: 100}
	for i := 0; i < 50; i++ {
		p.consume([]byte("data: " + strings.Repeat("y", 1000)))
	}
	if len(p.partial) > 100 {
		t.Fatalf("held %d bytes of one line, limit 100", len(p.partial))
	}
	items := p.end()
	if len(items) != 1 || items[0].size != 50*1006 || items[0].payload != nil {
		t.Fatalf("end of an over-limit line: %+v", items)
	}
}
