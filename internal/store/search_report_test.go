package store

import (
	"testing"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/policy"
)

func TestSearchAcrossSessions(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC()

	s1, err := Open(home, []string{"alpha-server"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Emit(event.Event{
		Direction: event.ClientToServer, Method: "tools/call", Tool: "read_file", ID: "1",
		Status: event.StatusPending, At: now, Args: `{"path":"needlealpha.txt"}`,
		Raw: `{"jsonrpc":"2.0","id":1}`,
	}); err != nil {
		t.Fatal(err)
	}
	oneID := s1.Meta.ID
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(home, []string{"beta-server"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Emit(event.Event{
		Direction: event.ServerToClient, Method: "tools/call", Tool: "write_file", ID: "1",
		Status: event.StatusToolError, At: now, Error: "permission denied needlebeta",
		Raw: `{"jsonrpc":"2.0","id":1}`,
	}); err != nil {
		t.Fatal(err)
	}
	twoID := s2.Meta.ID
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}

	hits, err := Search(home, SearchOptions{Query: "needlealpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Session.ID != oneID || hits[0].Event.Tool != "read_file" {
		t.Fatalf("unexpected hits: %+v", hits)
	}

	hits, err = Search(home, SearchOptions{Query: "needlebeta", Tool: "write_file", Status: event.StatusToolError})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Session.ID != twoID {
		t.Fatalf("filtered search failed: %+v", hits)
	}
	if snippet := SearchSnippet(hits[0].Event, "needlebeta"); snippet == "" {
		t.Fatal("empty snippet")
	}

	// Cross-field: method and tool are indexed too.
	hits, err = Search(home, SearchOptions{Query: "read_file"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("tool name search failed: %+v", hits)
	}

	if _, err := Search(home, SearchOptions{Query: "   "}); err == nil {
		t.Fatal("empty query should fail")
	}
}

func TestSearchDropsDeletedSessions(t *testing.T) {
	home := t.TempDir()
	sess, err := Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Emit(event.Event{
		Direction: event.ClientToServer, Method: "tools/call", Tool: "echo",
		Status: event.StatusPending, At: time.Now().UTC(), Args: `{"note":"goneaway"}`,
		Raw: `{"note":"goneaway"}`,
	}); err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	if hits, _ := Search(home, SearchOptions{Query: "goneaway"}); len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %+v", hits)
	}
	backdate(t, home, id, time.Now().Add(-48*time.Hour))
	if _, err := Prune(home, PruneOptions{OlderThan: time.Hour}); err != nil {
		t.Fatal(err)
	}
	hits, err := Search(home, SearchOptions{Query: "goneaway"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("search index kept deleted rows: %+v", hits)
	}
}

func TestReportAggregates(t *testing.T) {
	home := t.TempDir()
	day1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

	sess, err := Open(home, []string{"srv", "--flag"})
	if err != nil {
		t.Fatal(err)
	}
	emit := func(at time.Time, tool, status string, ms int64, size int, cost float64) {
		t.Helper()
		if err := sess.Emit(event.Event{
			Direction: event.ServerToClient, Method: "tools/call", Tool: tool,
			Status: status, At: at, DurationMS: ms, Size: size, CostUSD: cost,
		}); err != nil {
			t.Fatal(err)
		}
	}
	emit(day1, "echo", event.StatusOK, 10, 100, 0.01)
	emit(day1, "echo", event.StatusToolError, 20, 100, 0.01)
	emit(day1, "echo", event.StatusOK, 30, 100, 0.01)
	emit(day2, "add", event.StatusOK, 40, 200, 0.02)
	if err := sess.Record(policy.DecisionRecord{
		Server: "srv", Tool: "echo", Action: policy.Deny, Reason: "test", At: day1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Record(policy.DecisionRecord{
		Server: "srv", Tool: "echo", Action: policy.RequireApproval, Mode: policy.ModeShadow,
		Reason: "would prompt", At: day1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	byDay, err := Report(home, ReportOptions{By: "day"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byDay) != 2 {
		t.Fatalf("expected 2 days, got %+v", byDay)
	}
	if byDay[0].Key != "2026-01-02" || byDay[0].Calls != 1 {
		t.Fatalf("day ordering/count wrong: %+v", byDay)
	}
	d1 := byDay[1]
	if d1.Key != "2026-01-01" || d1.Calls != 3 || d1.Errors != 1 || d1.Blocked != 1 || d1.Would != 1 {
		t.Fatalf("day 1 aggregate wrong: %+v", d1)
	}
	if d1.AvgMS != 20 || d1.MaxMS != 30 || d1.P95MS != 30 {
		t.Fatalf("latency aggregate wrong: %+v", d1)
	}
	if d1.Bytes != 300 {
		t.Fatalf("bytes = %d", d1.Bytes)
	}
	if d1.CostUSD < 0.029 || d1.CostUSD > 0.031 {
		t.Fatalf("cost = %f", d1.CostUSD)
	}

	byTool, err := Report(home, ReportOptions{By: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byTool) != 2 || byTool[0].Key != "echo" || byTool[0].Calls != 3 {
		t.Fatalf("tool report wrong: %+v", byTool)
	}

	byServer, err := Report(home, ReportOptions{By: "server"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byServer) != 1 || byServer[0].Key != "srv --flag" || byServer[0].Calls != 4 {
		t.Fatalf("server report wrong: %+v", byServer)
	}

	if _, err := Report(home, ReportOptions{By: "nonsense"}); err == nil {
		t.Fatal("expected an error for an unknown grouping")
	}
}
