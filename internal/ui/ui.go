// Package ui serves the embedded Black Box web interface and its JSON API.
package ui

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spacecr8ed/blackbox/internal/replay"
	"github.com/spacecr8ed/blackbox/internal/store"
)

//go:embed static
var embedded embed.FS

// Server holds the UI configuration.
type Server struct {
	Home string
}

// New builds a UI server for a Black Box home.
func New(home string) *Server { return &Server{Home: home} }

// Handler returns the HTTP handler for the UI and API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/sessions/", s.handleSession)
	mux.HandleFunc("/api/diff", s.handleDiff)
	mux.HandleFunc("/api/search", s.handleSearch)

	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return mux
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	sessions, err := store.List(s.Home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sessions == nil {
		sessions = []store.Meta{}
	}
	writeJSON(w, map[string]any{"sessions": sessions})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/")
	parts := strings.Split(rest, "/")
	if rest == "" || len(parts) > 2 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	if len(parts) == 2 {
		if parts[1] != "export" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		s.handleExport(w, id)
		return
	}

	meta, events, err := store.Find(s.Home, id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	stats, err := store.StatsFor(s.Home, meta.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	decisions, err := store.Decisions(s.Home, meta.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cov, err := store.CoverageFor(s.Home, meta.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"meta":      meta,
		"stats":     stats,
		"events":    events,
		"decisions": decisions,
		"coverage":  cov,
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query := r.URL.Query().Get("q")
	if strings.TrimSpace(query) == "" {
		writeJSON(w, map[string]any{"hits": []any{}})
		return
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}

	hits, err := store.Search(s.Home, store.SearchOptions{
		Query:  query,
		Since:  parseSince(r.URL.Query().Get("since")),
		Tool:   r.URL.Query().Get("tool"),
		Status: r.URL.Query().Get("status"),
		Limit:  limit,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	type hit struct {
		SessionID string    `json:"sessionId"`
		StartedAt time.Time `json:"startedAt"`
		Argv      []string  `json:"argv"`
		Seq       int64     `json:"seq"`
		At        time.Time `json:"at"`
		Status    string    `json:"status"`
		Method    string    `json:"method"`
		Tool      string    `json:"tool"`
		Snippet   string    `json:"snippet"`
	}
	out := make([]hit, 0, len(hits))
	for _, h := range hits {
		out = append(out, hit{
			SessionID: h.Session.ID,
			StartedAt: h.Session.StartedAt,
			Argv:      h.Session.Argv,
			Seq:       h.Event.Seq,
			At:        h.Event.At,
			Status:    h.Event.Status,
			Method:    h.Event.Method,
			Tool:      h.Event.Tool,
			Snippet:   store.SearchSnippet(h.Event, query),
		})
	}
	writeJSON(w, map[string]any{"hits": out})
}

func parseSince(s string) time.Duration {
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil {
			return time.Duration(n) * 24 * time.Hour
		}
	}
	if strings.HasSuffix(s, "w") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "w")); err == nil {
			return time.Duration(n) * 7 * 24 * time.Hour
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return 0
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a, b := r.URL.Query().Get("a"), r.URL.Query().Get("b")
	if a == "" || b == "" {
		writeError(w, http.StatusBadRequest, "a and b are required")
		return
	}
	metaA, eventsA, err := store.Find(s.Home, a)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	metaB, eventsB, err := store.Find(s.Home, b)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"a":      metaA,
		"b":      metaB,
		"report": replay.Diff(eventsA, eventsB),
	})
}

// Serve runs the UI until ctx is cancelled.
func Serve(ctx context.Context, home, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ServeListener(ctx, home, ln)
}

// ServeListener is Serve on an existing listener, for callers that need to pick the
// port before starting (and report the real address to the user).
func ServeListener(ctx context.Context, home string, ln net.Listener) error {
	srv := &http.Server{
		Handler:           New(home).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func (s *Server) handleExport(w http.ResponseWriter, id string) {
	bundle, err := store.Export(s.Home, id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="blackbox-`+bundle.Session.ID+`.json"`)
	writeJSON(w, bundle)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
