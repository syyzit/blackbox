package store

import (
	"testing"

	"github.com/spacecr8ed/blackbox/internal/event"
)

func TestEventsPersistAndAggregate(t *testing.T) {
	home := t.TempDir()
	sess, err := Open(home, []string{"fake-server"})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID

	emit := func(e event.Event) {
		t.Helper()
		if err := sess.Emit(e); err != nil {
			t.Fatal(err)
		}
	}

	emit(event.Event{Direction: event.ClientToServer, Method: "tools/call", Tool: "echo", ID: "1", Status: event.StatusPending, Raw: `{"id":1}`})
	emit(event.Event{
		Direction: event.ServerToClient, Method: "tools/call", Tool: "echo", ID: "1",
		Status: event.StatusOK, DurationMS: 12, Size: 100,
		Result: `{"content":[]}`, Raw: `{"id":1,"result":{}}`,
		UsageIn: 1000, UsageOut: 500, CostUSD: 0.0125,
	})
	emit(event.Event{
		Direction: event.ServerToClient, Method: "tools/call", Tool: "fail", ID: "2",
		Status: event.StatusToolError, DurationMS: 8, Size: 50, Raw: `{"id":2}`,
	})
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	meta, events, err := Find(home, id[:10])
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != id {
		t.Fatalf("resolved wrong session: %s != %s", meta.ID, id)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].Seq != 1 || events[2].Seq != 3 {
		t.Fatalf("sequence was not preserved: %+v", events)
	}
	if events[1].UsageIn != 1000 || events[1].UsageOut != 500 {
		t.Fatalf("usage was not persisted: %+v", events[1])
	}
	if events[1].Result != `{"content":[]}` || events[1].DurationMS != 12 {
		t.Fatalf("result or latency was not persisted: %+v", events[1])
	}

	stats, err := StatsFor(home, id)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Calls != 2 {
		t.Fatalf("calls = %d, want 2", stats.Calls)
	}
	if stats.Errors != 1 {
		t.Fatalf("errors = %d, want 1", stats.Errors)
	}
	if stats.TotalMS != 20 || stats.MaxMS != 12 {
		t.Fatalf("latency aggregation wrong: %+v", stats)
	}
	if stats.Bytes != 150 {
		t.Fatalf("bytes = %d, want 150", stats.Bytes)
	}
	if stats.CostUSD < 0.012 || stats.CostUSD > 0.013 {
		t.Fatalf("cost = %f, want ~0.0125", stats.CostUSD)
	}
	if len(stats.Tools) != 2 {
		t.Fatalf("expected 2 tool rows, got %+v", stats.Tools)
	}

	list, err := List(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Events != 3 {
		t.Fatalf("list did not count events: %+v", list)
	}
}
