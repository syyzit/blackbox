package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/syyzit/blackbox/internal/coverage"
	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/store"
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

func TestCoverageInSessionAPI(t *testing.T) {
	home := t.TempDir()
	sess, err := store.Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.SaveCoverage(coverage.Report{
		Root: "/repo", Changed: 1, Unrecorded: 1,
		Files: []coverage.Class{{Path: "a.go", Kind: coverage.Modified, Status: coverage.Unrecorded}},
	}); err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	h := New(home).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id, nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Coverage *coverage.Report `json:"coverage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Coverage == nil || out.Coverage.Unrecorded != 1 || out.Coverage.Files[0].Path != "a.go" {
		t.Fatalf("coverage missing from the API: %+v", out.Coverage)
	}
}

func TestTraceAPI(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)

	sess, err := store.Open(home, []string{"mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Emit(event.Event{
		Direction: event.ClientToServer, Method: "tools/call", Tool: "list_directory",
		Status: event.StatusPending, At: now, ExternalID: "ses_ui", Raw: `{}`,
	}); err != nil {
		t.Fatal(err)
	}
	id := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	if err := store.AppendExternal(home, "ses_ui", []string{"opencode", "builtins"}, []event.Event{{
		Direction: event.ClientToServer, Method: "tools/call", Tool: "shell",
		Status: event.StatusPending, At: now.Add(time.Second), Raw: `{}`,
	}}); err != nil {
		t.Fatal(err)
	}

	h := New(home).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/traces", nil))
	if rec.Code != 200 {
		t.Fatalf("traces status %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Traces []store.TraceSummary `json:"traces"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Traces) != 1 || list.Traces[0].ExternalID != "ses_ui" || list.Traces[0].Events != 2 {
		t.Fatalf("bad trace list: %+v", list.Traces)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/traces/ses_ui", nil))
	if rec.Code != 200 {
		t.Fatalf("trace status %d: %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Trace store.Trace `json:"trace"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Trace.Events) != 2 || len(detail.Trace.Sessions) != 2 {
		t.Fatalf("bad trace: %+v", detail.Trace)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id, nil))
	var session struct {
		TraceID string `json:"traceId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.TraceID != "ses_ui" {
		t.Fatalf("session trace id = %q", session.TraceID)
	}
}

func TestApprovalsAPI(t *testing.T) {
	home := t.TempDir()
	sess, err := store.Open(home, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	session := sess.Meta.ID
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateApproval(home, store.Approval{
		SessionID: session, Tool: "echo", Args: `{"text":"hi"}`, Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := New(home).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/approvals", nil))
	if rec.Code != 200 {
		t.Fatalf("list status %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Approvals []store.Approval `json:"approvals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Approvals) != 1 || list.Approvals[0].ID != id {
		t.Fatalf("approvals = %+v", list.Approvals)
	}

	body := strings.NewReader(`{"decision":"allow","by":"ui"}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/approvals/"+strconv.FormatInt(id, 10), body))
	if rec.Code != 200 {
		t.Fatalf("decide status %d: %s", rec.Code, rec.Body.String())
	}
	got, err := store.ApprovalByID(home, id)
	if err != nil || got.Status != store.ApprovalApproved || got.DecidedBy != "ui" {
		t.Fatalf("decided = %+v, %v", got, err)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/approvals/"+strconv.FormatInt(id, 10), strings.NewReader(`{"decision":"deny"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("second decision status %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/approvals/"+strconv.FormatInt(id, 10), strings.NewReader(`{"decision":"maybe"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad decision status %d", rec.Code)
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
