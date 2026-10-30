package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spacecr8ed/blackbox/internal/event"
	"github.com/spacecr8ed/blackbox/internal/jsonrpc"
	"github.com/spacecr8ed/blackbox/internal/redact"
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
}

// HTTP is a reverse proxy for MCP over streamable HTTP.
//
// It uses the 2026-07-28 header routing fields (Mcp-Method / Mcp-Name) when present
// and falls back to parsing the JSON-RPC body, so it works with older clients too.
type HTTP struct {
	upstream *url.URL
	client   *http.Client
	rec      *pipeline
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
		rec: newPipeline(o.SessionID, o.Sink, o.Redactor, o.PriceInPerMTok, o.PriceOutPerMTok),
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

	method, name := routeFields(r.Header, body)
	if len(body) > 0 {
		h.rec.record(event.ClientToServer, body, routeHint{method: method, name: name})
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

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		h.streamSSE(w, resp.Body)
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	h.rec.record(event.ServerToClient, bytes.TrimSpace(respBody), routeHint{})
	_, _ = w.Write(respBody)
}

func (h *HTTP) streamSSE(w http.ResponseWriter, body io.Reader) {
	flusher, _ := w.(http.Flusher)
	br := bufio.NewReaderSize(body, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = w.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
			h.rec.record(event.ServerToClient, bytes.TrimSpace(line), routeHint{})
		}
		if err != nil {
			return
		}
	}
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
		Addr:              addr,
		Handler:           h,
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
