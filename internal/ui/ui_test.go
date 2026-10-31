package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/store"
)

func seed(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()

	sess, err := store.Open(home, []string{"fake-server"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Emit(event.Event{
		Direction: event.ClientToServer, Method: "tools/call", Tool: "echo", ID: "1",
		Status: event.StatusPending, Args: `{"text":"hi"}`, Raw: `{"id":1}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Emit(event.Event{
		Direction: event.ServerToClient, Method: "tools/call", Tool: "echo", ID: "1",
		Status: event.StatusOK, DurationMS: 7, Size: 20, Result: `{"ok":true}`, Raw: `{"id":1,"result":{}}`,
	}); err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	return home, id
}

func TestSessionsAndSessionAPI(t *testing.T) {
	home, id := seed(t)
	h := New(home).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if rec.Code != 200 {
		t.Fatalf("sessions status %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Sessions []store.Meta `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].Events != 2 {
		t.Fatalf("unexpected session list: %+v", list)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id[:8], nil))
	if rec.Code != 200 {
		t.Fatalf("session status %d: %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Meta   store.Meta    `json:"meta"`
		Stats  store.Stats   `json:"stats"`
		Events []event.Event `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Meta.ID != id {
		t.Fatalf("wrong session: %s", detail.Meta.ID)
	}
	if len(detail.Events) != 2 || detail.Stats.Calls != 1 {
		t.Fatalf("bad detail payload: %+v", detail)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestDiffAPI(t *testing.T) {
	home, id := seed(t)
	h := New(home).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/diff?a="+id+"&b="+id, nil))
	if rec.Code != 200 {
		t.Fatalf("diff status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Report struct {
			Identical bool `json:"identical"`
		} `json:"report"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Report.Identical {
		t.Fatal("identical session should diff clean")
	}
}

func TestExportAPI(t *testing.T) {
	home, id := seed(t)
	h := New(home).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/export", nil))
	if rec.Code != 200 {
		t.Fatalf("export status %d: %s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, id) {
		t.Fatalf("bad content disposition: %q", cd)
	}
	var bundle struct {
		Format  string        `json:"format"`
		Session store.Meta    `json:"session"`
		Events  []event.Event `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Format != store.ExportFormat || bundle.Session.ID != id || len(bundle.Events) != 2 {
		t.Fatalf("bad bundle: %+v", bundle)
	}
}

func TestSearchAPI(t *testing.T) {
	home := t.TempDir()
	sess, err := store.Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Emit(event.Event{
		Direction: event.ClientToServer, Method: "tools/call", Tool: "echo", ID: "1",
		Status: event.StatusPending, Args: `{"text":"findme"}`, Raw: `{"jsonrpc":"2.0"}`,
	}); err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	h := New(home).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=findme", nil))
	if rec.Code != 200 {
		t.Fatalf("search status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Hits []struct {
			SessionID string `json:"sessionId"`
			Seq       int64  `json:"seq"`
			Tool      string `json:"tool"`
			Snippet   string `json:"snippet"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) != 1 || out.Hits[0].SessionID != id || out.Hits[0].Tool != "echo" {
		t.Fatalf("bad hits: %+v", out.Hits)
	}
	if !strings.Contains(out.Hits[0].Snippet, "findme") {
		t.Fatalf("snippet = %q", out.Hits[0].Snippet)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=", nil))
	if rec.Code != 200 {
		t.Fatalf("empty query status %d", rec.Code)
	}
}

func TestServesIndex(t *testing.T) {
	home, _ := seed(t)
	h := New(home).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 {
		t.Fatalf("index status %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("index is empty")
	}
}
