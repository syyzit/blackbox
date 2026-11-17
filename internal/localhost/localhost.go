// Package localhost protects servers that are meant to be reached only from this
// machine against DNS rebinding.
//
// Binding to 127.0.0.1 is not enough on its own: a web page can point its own domain
// at 127.0.0.1 and then read responses as same-origin. Those requests carry the
// attacker's domain in the Host header, so a server that only answers to IP literals
// and localhost names cannot be reached that way.
package localhost

import (
	"net"
	"net/http"
	"strings"
)

// AllowedHost reports whether a Host header names this machine directly: an IP
// literal, "localhost", or a *.localhost name (which resolvers must keep local).
// IP literals are allowed so a deliberate non-loopback bind keeps working; a
// rebinding attack always arrives with a domain name.
func AllowedHost(host string) bool {
	h := host
	if hh, port, err := net.SplitHostPort(host); err == nil {
		// SplitHostPort accepts any port text, so "127.0.0.1:80.evil.example"
		// would otherwise pass as 127.0.0.1.
		if !digits(port) {
			return false
		}
		h = hh
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	if h == "" {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	h = strings.ToLower(h)
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

func digits(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Guard rejects requests whose Host header is not a local name.
func Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !AllowedHost(r.Host) {
			http.Error(w, "blackbox: unexpected Host header (DNS rebinding protection)", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
