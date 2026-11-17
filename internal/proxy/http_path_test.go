package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/syyzit/blackbox/internal/proxy"
)

// The upstream path is built from what the client sent, escaped: decoding it first
// turned %2F into a real slash, so the upstream saw a different path.
func TestHTTPKeepsEscapedPath(t *testing.T) {
	cases := []struct{ base, path, want string }{
		{"", "/mcp/a%2Fb", "/mcp/a%2Fb"},
		{"/prefix", "/a%2Fb%20c", "/prefix/a%2Fb%20c"},
		{"/pre%2Ffix/", "/x", "/pre%2Ffix/x"},
		{"", "/plain/path", "/plain/path"},
	}
	for _, tc := range cases {
		var got string
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.EscapedPath()
		}))
		h, err := proxy.NewHTTP(proxy.HTTPOptions{Upstream: up.URL + tc.base, SessionID: "s"})
		if err != nil {
			t.Fatal(err)
		}
		front := httptest.NewServer(h)

		resp, err := http.Post(front.URL+tc.path, "application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/x"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		front.Close()
		up.Close()
		if got != tc.want {
			t.Errorf("upstream %q + %q: upstream saw %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}
