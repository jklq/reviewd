package gateway

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jklq/reviewd/harness"
)

func TestGatewayBoundary(t *testing.T) {
	const secret = "server-only-provider-secret"
	calls := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/fixed" || r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("X-Evil") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected upstream request")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) == "redirect" {
			w.Header().Set("Location", "https://attacker.example/"+secret)
			w.WriteHeader(307)
			return
		}
		if string(body) == "error" {
			w.Header().Set("X-Secret", secret)
			w.WriteHeader(401)
			_, _ = w.Write([]byte(secret))
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Secret", secret)
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: second\n\n")
	}))
	defer upstream.Close()
	handler, err := NewHandler([]harness.Route{{Path: "/responses", URL: upstream.URL + "/fixed", Headers: http.Header{"Authorization": {"Bearer " + secret}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler.transport = upstream.Client().Transport.(*http.Transport)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/responses", "ok", 200}, {"POST", "/responses", "error", 401}, {"POST", "/responses", "redirect", 502},
		{"GET", "/responses", "", 403}, {"CONNECT", "//attacker.example:443", "", 403},
		{"POST", "/responses?url=https://attacker.example", "", 403},
		{"POST", "/%72esponses", "", 403}, {"POST", "/responses/../admin", "", 403},
		{"POST", "https://attacker.example/responses", "", 403},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer attacker")
		r.Header.Set("X-Evil", "yes")
		r.Header.Set("Cookie", "steal=true")
		r.Header.Set("Connection", "Authorization, X-Api-Key")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, w.Code, tc.status)
		}

		if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Header().Get("X-Secret"), secret) || w.Header().Get("Location") != "" {
			t.Fatal("credential escaped")
		}
	}
	if calls != 3 {
		t.Fatalf("denied requests contacted upstream: %d", calls)
	}
}

func TestGatewayRouteWithoutHeaders(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("caller authentication reached upstream")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	defer upstream.Close()
	handler, err := NewHandler([]harness.Route{{Path: "/responses", URL: upstream.URL + "/responses"}})
	if err != nil {
		t.Fatal(err)
	}
	handler.transport = upstream.Client().Transport.(*http.Transport)
	request := httptest.NewRequest("POST", "/responses", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer attacker")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 200 || w.Body.String() != "{}" {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
}

func TestGatewayCancellationAndSocketCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	close, err := Start(ctx, dir, []harness.Route{{Path: "/responses", URL: "https://api.example/responses"}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", dir+"/model.sock")
	}}}
	response, err := client.Get("http://model/forbidden")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal(response.Status)
	}
	close()
	if _, err := client.Get("http://model/forbidden"); err == nil {
		t.Fatal("gateway still serves after close")
	}
}
