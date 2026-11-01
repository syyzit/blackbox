package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/jsonrpc"
	"github.com/syyzit/blackbox/internal/localhost"
	"github.com/syyzit/blackbox/internal/policy"
	"github.com/syyzit/blackbox/internal/redact"
)

// HTTPOptions configures a streamable-HTTP MCP proxy.
type HTTPOptions struct {
	// Upstream is the base URL of the real MCP server.
	Upstream string
	// SessionID is stamped onto every recorded event.
	SessionID string
	// Sink receives recorded events. May be nil to disable recording.
	Sink event.Sink
	// Redactor strips secrets from recorded payloads. Nil means no redaction.
	Redactor *redact.Redactor
	// PriceInPerMTok and PriceOutPerMTok convert reported token usage into USD.
	PriceInPerMTok  float64
	PriceOutPerMTok float64

	// Policy gates tool calls. Nil records without enforcing.
	Policy *policy.Policy
	// Server is the human-readable server identity used by policy matching.
	Server string
	// ServerKey is the stable fingerprint used by the tool-definition ledger.
	ServerKey string
	// Ledger tracks tool definitions across sessions.
	Ledger Ledger
	// Approver asks a human about require_approval calls.
	Approver policy.Approver
	// Audit persists policy decisions.
	Audit policy.Auditor
}

// HTTP is a reverse proxy for MCP over streamable HTTP.
//
// It uses the 2026-07-28 header routing fields (Mcp-Method / Mcp-Name) when present
// and falls back to parsing the JSON-RPC body, so it works with older clients too.
type HTTP struct {
	upstream  *url.URL
	client    *http.Client
	rec       *pipeline
	exchanges atomic.Uint64 // numbers each request/response exchange for correlation
}

// NewHTTP builds an HTTP proxy.
func NewHTTP(o HTTPOptions) (*HTTP, error) {
	u, err := url.Parse(o.Upstream)
	if err != nil {
		return nil, fmt.Errorf("proxy: parse upstream: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("proxy: upstream must be http(s), got %q", u.Scheme)
	}
	return &HTTP{
		upstream: u,
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
				MaxIdleConnsPerHost:   8,
				ResponseHeaderTimeout: 0, // tools may be slow; the caller controls the deadline
			},
		},
		rec: newPipeline(pipelineConfig{
			SessionID: o.SessionID,
			Sink:      o.Sink,
			Redactor:  o.Redactor,
			PriceIn:   o.PriceInPerMTok,
			PriceOut:  o.PriceOutPerMTok,
			Policy:    o.Policy,
			Server:    o.Server,
			ServerKey: o.ServerKey,
			Ledger:    o.Ledger,
			Approver:  o.Approver,
			Audit:     o.Audit,
		}),
	}, nil
}

// ServeHTTP proxies one request to the upstream MCP server.
func (h *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, "blackbox: reading request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	_ = r.Body.Close()
	body = bytes.TrimSpace(body)

	// The routing headers are recorded as authoritative and forwarded upstream, while
	// policy reads the body. If they disagree, the recording would name one tool while
	// another one runs, so refuse the request instead of guessing which is true.
	if reason := routeMismatch(r.Header, body); reason != "" {
		http.Error(w, "blackbox: "+reason, http.StatusBadRequest)
		return
	}

	method, name := routeFields(r.Header, body)
	hint := routeHint{
		method:  method,
		name:    name,
		scope:   fmt.Sprintf("req-%d", h.exchanges.Add(1)),
		session: r.Header.Get("Mcp-Session-Id"),
	}
	respHint := routeHint{scope: hint.scope, session: hint.session}

	reply, dec, blocked := h.rec.gate(body)
	if blocked {
		h.rec.record(event.ClientToServer, body, hint, dec, false)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		if len(reply) > 0 {
			h.rec.record(event.ServerToClient, reply, respHint, dec, true)
			_, _ = w.Write(reply)
		}
		return
	}
	if len(body) > 0 {
		h.rec.record(event.ClientToServer, body, hint, dec, false)
	}

	target := *h.upstream
	target.Path = singleJoin(h.upstream.Path, r.URL.Path)
	target.RawQuery = r.URL.RawQuery

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "blackbox: building upstream request: "+err.Error(), http.StatusBadGateway)
		return
	}
	copyHeaders(req.Header, r.Header)
	// Make routing explicit for the upstream and for anything watching in between.
	if method != "" {
		req.Header.Set("Mcp-Method", method)
	}
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		http.Error(w, "blackbox: upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.Header().Del("Content-Length") // the body may be redacted/re-encoded downstream
	w.WriteHeader(resp.StatusCode)

	// A server that assigns a session on initialize names it in the response; replies
	// to server requests later in this exchange belong to that session.
	if respHint.session == "" {
		respHint.session = resp.Header.Get("Mcp-Session-Id")
	}

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		sseHint := respHint
		sseHint.name = name
		h.streamSSE(w, resp.Body, sseHint)
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	if trimmed := bytes.TrimSpace(respBody); len(trimmed) > 0 {
		h.rec.record(event.ServerToClient, trimmed, respHint, nil, false)
	}
	_, _ = w.Write(respBody)
}

// routeMismatch reports when the Mcp-Method / Mcp-Name headers name a different
// method or tool than the JSON-RPC body. A body that cannot be parsed as a single
// message is left to the upstream server.
func routeMismatch(hdr http.Header, body []byte) string {
	hMethod, hName := hdr.Get("Mcp-Method"), hdr.Get("Mcp-Name")
	if hMethod == "" && hName == "" {
		return ""
	}
	msg, err := jsonrpc.Decode(body)
	if err != nil || msg.Method == "" {
		if _, isBatch := jsonrpc.DecodeBatch(body); isBatch {
			return "Mcp-Method/Mcp-Name headers cannot describe a batch"
		}
		return ""
	}
	if hMethod != "" && hMethod != msg.Method {
		return fmt.Sprintf("Mcp-Method header %q does not match body method %q", hMethod, msg.Method)
	}
	if hName != "" && msg.Method == "tools/call" && hName != msg.ToolName() {
		return fmt.Sprintf("Mcp-Name header %q does not match body tool %q", hName, msg.ToolName())
	}
	return ""
}

// routeFields resolves Mcp-Method and Mcp-Name, preferring headers.
func routeFields(h http.Header, body []byte) (method, name string) {
	method = h.Get("Mcp-Method")
	name = h.Get("Mcp-Name")
	if method != "" && name != "" {
		return method, name
	}
	msg, err := jsonrpc.Decode(body)
	if err != nil {
		return method, name
	}
	if method == "" {
		method = msg.Method
	}
	if name == "" {
		name = msg.ToolName()
	}
	return method, name
}

// RunHTTP serves the proxy until ctx is cancelled.
func RunHTTP(ctx context.Context, addr string, o HTTPOptions) error {
	h, err := NewHTTP(o)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr: addr,
		// Without this, a web page could rebind its domain to 127.0.0.1 and call
		// tools through the proxy as if it were the agent.
		Handler:           localhost.Guard(h),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("proxy: listen %s: %w", addr, err)
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	}
}

var hopHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"content-length":      true,
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		if hopHeaders[strings.ToLower(k)] {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func singleJoin(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case strings.HasSuffix(a, "/") && strings.HasPrefix(b, "/"):
		return a + b[1:]
	case !strings.HasSuffix(a, "/") && !strings.HasPrefix(b, "/"):
		return a + "/" + b
	default:
		return a + b
	}
}
