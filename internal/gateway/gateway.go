// Package gateway keeps upstream authentication outside the harness container.
package gateway

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jklq/reviewd/harness"
)

// Start exposes only a private Unix socket. The directory must be outside all
// review inputs/outputs, at the same host path when using Docker Compose.
func Start(ctx context.Context, dir string, routes []harness.Route) (func(), error) {
	handler, err := NewHandler(routes)
	if err != nil {
		return nil, err
	}
	// Bind through a directory descriptor so long data paths do not exceed
	// Linux's 108-byte Unix-socket address limit. Docker uses the short mount path.
	directory, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	listener, err := net.Listen("unix", fmt.Sprintf("/proc/self/fd/%d/model.sock", directory.Fd()))
	if err != nil {
		return nil, err
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Chmod(filepath.Join(dir, "model.sock"), 0600); err != nil {
		listener.Close()
		return nil, err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0), BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = server.Serve(listener) }()
	return func() {
		_ = server.Close()
		handler.transport.CloseIdleConnections()
		_ = os.Remove(filepath.Join(dir, "model.sock"))
	}, nil
}

type Handler struct {
	routes    map[string]harness.Route
	transport *http.Transport
	slots     chan struct{}
}

// NewHandler validates the trusted routing table. No forwarding proxy, arbitrary
// URL, redirect, incoming authentication header, or query is ever forwarded.
func NewHandler(routes []harness.Route) (*Handler, error) {
	h := &Handler{slots: make(chan struct{}, 4), routes: map[string]harness.Route{}, transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 2 * time.Minute, DisableCompression: true, MaxConnsPerHost: 8}}
	if len(routes) == 0 {
		return nil, fmt.Errorf("driver has no model routes")
	}
	for _, route := range routes {
		if route.Method == "" {
			route.Method = http.MethodPost
		}
		if route.Method != http.MethodPost && route.Method != http.MethodGet {
			return nil, fmt.Errorf("invalid driver route method")
		}
		u, err := url.Parse(route.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(route.Path, "/") {
			return nil, fmt.Errorf("invalid driver model route")
		}
		if _, exists := h.routes[route.Path]; exists {
			return nil, fmt.Errorf("duplicate driver model route")
		}
		route.Headers = route.Headers.Clone()
		route.ForwardHeaders = slices.Clone(route.ForwardHeaders)
		for _, name := range route.ForwardHeaders {
			switch strings.ToLower(name) {
			case "authorization", "x-api-key", "proxy-authorization", "host", "connection", "cookie", "content-length", "transfer-encoding", "content-encoding", "forwarded", "x-forwarded-host", "x-forwarded-for", "x-forwarded-proto":
				return nil, fmt.Errorf("driver cannot forward authentication or routing headers")
			}
		}
		h.routes[route.Path] = route
	}
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		http.Error(w, "model gateway busy", 429)
		return
	}
	route, ok := h.routes[r.URL.Path]
	if !ok || r.Method != route.Method || (r.URL.RawQuery != "" && r.URL.RawQuery != route.Query) || r.URL.RawPath != "" || r.URL.IsAbs() || r.Host == "" {
		http.Error(w, "model route denied", http.StatusForbidden)
		return
	}
	// Bound input before contacting the upstream. Do not forward client headers:
	// even Connection-nominated or proxy headers must not influence authentication.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
	if err != nil {
		http.Error(w, "model request too large", http.StatusRequestEntityTooLarge)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), route.Method, route.URL, strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, "model request failed", 502)
		return
	}
	request.URL.RawQuery = r.URL.RawQuery
	if route.Headers != nil {
		request.Header = route.Headers.Clone()
	}
	// Protocol metadata only. Never copy caller authentication, routing, cookies,
	// proxy headers, content encoding, or Connection-nominated headers.
	for _, name := range route.ForwardHeaders {
		if value := r.Header.Get(name); value != "" {
			request.Header.Set(name, value)
		}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream, application/json")
	response, err := h.transport.RoundTrip(request)
	if err != nil {
		http.Error(w, "model upstream unavailable", 502)
		return
	}
	defer response.Body.Close()
	// Errors can echo authorization; never expose their body, headers or location.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status := response.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		http.Error(w, "model upstream rejected request", status)
		return
	}
	contentType := response.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") && !strings.HasPrefix(contentType, "application/json") {
		http.Error(w, "invalid model response", 502)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(response.StatusCode)
	// Streaming is essential for SDKs. Only the model payload crosses the boundary;
	// authentication, cookies, redirects and diagnostic headers never do.
	buffer := make([]byte, 32<<10)
	reader := io.LimitReader(response.Body, 128<<20)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
