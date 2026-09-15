package gateway

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"time"
)

// Relay runs inside the container and cannot select an upstream.
func Relay() error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/run/reviewd-model/model.sock")
	}}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.Out.URL.Scheme = "http"
		r.Out.URL.Host = "model"
	}, Transport: transport, FlushInterval: -1, ErrorLog: log.New(io.Discard, "", 0), ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { http.Error(w, "model gateway unavailable", 502) }}
	return (&http.Server{Addr: "127.0.0.1:39123", Handler: proxy, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}).ListenAndServe()
}
