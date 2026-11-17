package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/syyzit/blackbox/internal/event"
)

// Claude Code runs tools in parallel, so several hook processes append to one session
// at the same moment. Every append must land, with its own sequence numbers.
func TestAppendExternalConcurrentWriters(t *testing.T) {
	home := t.TempDir()
	const writers = 12
	now := time.Now().UTC()

	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tool := fmt.Sprintf("tool-%d", i)
			errs[i] = AppendExternal(home, ClaudeCode, "parallel", []event.Event{
				{Direction: event.ClientToServer, Method: "tools/call", Tool: tool, Status: event.StatusPending, At: now, Raw: `{}`},
				{Direction: event.ServerToClient, Method: "tools/call", Tool: tool, Status: event.StatusOK, At: now, Raw: `{}`},
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}

	_, events, err := Find(home, "cc-parallel")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2*writers {
		t.Fatalf("stored %d events, want %d", len(events), 2*writers)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d: sequence numbers are duplicated or have gaps", i, e.Seq)
		}
	}
	// One writer's request and response stay adjacent.
	for i := 0; i < len(events); i += 2 {
		if events[i].Tool != events[i+1].Tool || events[i].Direction != event.ClientToServer {
			t.Fatalf("events %d and %d are interleaved: %s, %s", i, i+1, events[i].Tool, events[i+1].Tool)
		}
	}
}

// Parallel identical calls produce identical recordings; each hook must claim a
// different one, or a recording is left out of the trace.
func TestLinkExternalCallConcurrentIdenticalCalls(t *testing.T) {
	home := t.TempDir()
	const calls = 8
	now := time.Now().UTC()

	sess, err := Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= calls; i++ {
		callID := fmt.Sprintf("call-%d", i)
		for _, e := range []event.Event{
			{Direction: event.ClientToServer, Status: event.StatusPending, Args: `{"name":"read","arguments":{"path":"a"}}`},
			{Direction: event.ServerToClient, Status: event.StatusOK},
		} {
			e.Method, e.Tool, e.CallID, e.At, e.Raw = "tools/call", "read", callID, now, `{}`
			if err := sess.Emit(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, calls)
	linked := make([]bool, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			linked[i], errs[i] = LinkExternalCall(home, "agent-session", []string{"read"}, `{"path":"a"}`,
				now.Add(-time.Minute), now.Add(time.Minute))
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil || !linked[i] {
			t.Errorf("link %d = %v, %v", i, linked[i], errs[i])
		}
	}

	_, events, err := Find(home, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.ExternalID != "agent-session" {
			t.Errorf("seq %d (%s) was not linked", e.Seq, e.CallID)
		}
	}
}
