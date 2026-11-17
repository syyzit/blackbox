package localhost

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAllowedHost(t *testing.T) {
	allowed := []string{
		"127.0.0.1", "127.0.0.1:8787", "localhost", "localhost:8787", "LOCALHOST:1",
		"[::1]:8787", "::1", "app.localhost:8787", "192.168.1.20:8787", "localhost.",
	}
	for _, h := range allowed {
		if !AllowedHost(h) {
			t.Errorf("AllowedHost(%q) = false, want true", h)
		}
	}
	denied := []string{
		"", "evil.example", "evil.example:8787", "localhost.evil.example",
		"127.0.0.1.nip.io:8787", "attacker.com:8787",
	}
	for _, h := range denied {
		if AllowedHost(h) {
			t.Errorf("AllowedHost(%q) = true, want false", h)
		}
	}
}

func TestGuardRejectsRebindingHost(t *testing.T) {
	h := Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://rebind.attacker.com:8787/api/sessions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("rebinding host got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/sessions", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("local host got %d", rec.Code)
	}
}

func FuzzAllowedHost(f *testing.F) {
	for _, s := range []string{"127.0.0.1:8787", "[::1]:1", "evil.example", "localhost.", "[", ":"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, host string) {
		if AllowedHost(host) && AllowedHost(host+".evil.example") {
			t.Fatalf("a public domain under %q was allowed", host)
		}
	})
}

func TestGuardChecksOrigin(t *testing.T) {
	cases := map[string]int{
		"":                              http.StatusOK, // not a browser
		"http://127.0.0.1:8787":         http.StatusOK,
		"http://localhost:5173":         http.StatusOK,
		"http://[::1]:8787":             http.StatusOK,
		"https://evil.example":          http.StatusForbidden,
		"http://127.0.0.1.evil.example": http.StatusForbidden,
		"null":                          http.StatusForbidden,
	}
	g := Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for origin, want := range cases {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8787/", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Origin %q: status %d, want %d", origin, w.Code, want)
		}
	}
}
