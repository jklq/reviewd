package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSentinel = "reviewd-sentinel-test0000000000000000000000"
const testReal = "real-credential-value"

func testServer(t *testing.T, caPath string, allow *allowlist, sentinels, reals map[string]string) string {
	t.Helper()
	authority, err := loadCA(caPath)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{allow: allow, creds: &credentials{sentinels: sentinels, reals: reals, items: map[string]*credItem{}}, ca: authority}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go s.serveListener(ln)
	return ln.Addr().String()
}

func proxyClient(proxyAddr string, tlsConfig *tls.Config) *http.Client {
	proxyURL, _ := url.Parse("http://" + proxyAddr)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: tlsConfig}}
}

func TestForwardSubstitutesBothDirections(t *testing.T) {
	var gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("X-Echo", "token "+testReal)
		_, _ = fmt.Fprint(w, "upstream saw "+testReal)
	}))
	defer upstream.Close()
	host, _, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	caPath, _ := testCA(t)
	proxyAddr := testServer(t, caPath, parseAllow([]string{host}, ""), map[string]string{"HARNESS_AUTH": testSentinel}, map[string]string{"HARNESS_AUTH": testReal})
	req, _ := http.NewRequest("POST", upstream.URL+"/echo", strings.NewReader("login "+testSentinel))
	req.Header.Set("Authorization", "Bearer "+testSentinel)
	resp, err := proxyClient(proxyAddr, nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if gotAuth != "Bearer "+testReal || gotBody != "login "+testReal {
		t.Fatalf("upstream got auth=%q body=%q", gotAuth, gotBody)
	}
	if strings.Contains(gotAuth+gotBody, testSentinel) {
		t.Fatal("sentinel reached upstream")
	}
	if strings.Contains(string(body)+resp.Header.Get("X-Echo"), testReal) {
		t.Fatalf("real reached client: body=%q echo=%q", body, resp.Header.Get("X-Echo"))
	}
	if !strings.Contains(string(body), testSentinel) || !strings.Contains(resp.Header.Get("X-Echo"), testSentinel) {
		t.Fatalf("response not scrubbed: body=%q echo=%q", body, resp.Header.Get("X-Echo"))
	}
}

func TestConnectMITMSubstitutesTLS(t *testing.T) {
	var gotAuth string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, "secret "+testReal)
	})
	caPath, pool := testCA(t)
	authority, err := loadCA(caPath)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := authority.leaf("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewUnstartedServer(handler)
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{*leaf}}
	upstream.StartTLS()
	defer upstream.Close()
	proxyAddr := testServer(t, caPath, parseAllow([]string{"127.0.0.1"}, ""), map[string]string{"HARNESS_AUTH": testSentinel}, map[string]string{"HARNESS_AUTH": testReal})
	client := proxyClient(proxyAddr, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	req, _ := http.NewRequest("GET", upstream.URL+"/echo", nil)
	req.Header.Set("Authorization", "Bearer "+testSentinel)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if gotAuth != "Bearer "+testReal {
		t.Fatalf("upstream got auth=%q", gotAuth)
	}
	if strings.Contains(string(body), testReal) || !strings.Contains(string(body), testSentinel) {
		t.Fatalf("response not scrubbed: %q", body)
	}
	if !strings.Contains(resp.TLS.PeerCertificates[0].Issuer.CommonName, "reviewd egress test CA") {
		t.Fatal("client did not see a minted leaf")
	}
}

func TestConcurrentForward(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "secret "+testReal)
	}))
	defer upstream.Close()
	host, _, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	caPath, _ := testCA(t)
	proxyAddr := testServer(t, caPath, parseAllow([]string{host}, ""), map[string]string{"HARNESS_AUTH": testSentinel}, map[string]string{"HARNESS_AUTH": testReal})
	client := proxyClient(proxyAddr, nil)
	done := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() {
			req, _ := http.NewRequest("GET", upstream.URL+"/echo", nil)
			req.Header.Set("Authorization", "Bearer "+testSentinel)
			resp, err := client.Do(req)
			if err != nil {
				done <- err
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if strings.Contains(string(body), testReal) {
				done <- fmt.Errorf("real reached client")
				return
			}
			done <- nil
		}()
	}
	for i := 0; i < 16; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestDenyAndHealth(t *testing.T) {
	caPath, _ := testCA(t)
	proxyAddr := testServer(t, caPath, parseAllow([]string{"allowed.test"}, ""), map[string]string{}, map[string]string{})
	resp, err := proxyClient(proxyAddr, nil).Get("http://blocked.test/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("blocked host status=%d", resp.StatusCode)
	}
	get := func(path string) (int, string) {
		conn, err := net.Dial("tcp", proxyAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, _ = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", path)
		b, _ := io.ReadAll(conn)
		return len(b), string(b)
	}
	if _, body := get("/healthz"); !strings.Contains(body, "200 OK") {
		t.Fatalf("healthz: %q", body)
	}
	if _, body := get("/elsewhere"); !strings.Contains(body, "404") {
		t.Fatalf("unknown path: %q", body)
	}
}

func TestRefreshRotatesValues(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "reviewd")
	body := `#!/bin/sh
printf '%s' '{"expires_at":"2035-01-01T00:00:00Z","env":{"HARNESS_AUTH":"rotated-real"}}'
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "reviewd.json")
	if err := os.WriteFile(configPath, []byte(`{"credentials":{"acct":{"exports":["HARNESS_AUTH"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_AUTH", "initial-real")
	creds, err := newCredentials(map[string]string{"HARNESS_AUTH": testSentinel}, configPath, script)
	if err != nil {
		t.Fatal(err)
	}
	pairs := creds.requestPairs()
	if len(pairs) != 1 || pairs[0].old != testSentinel || pairs[0].new != "rotated-real" {
		t.Fatalf("pairs=%+v", pairs)
	}
	back := creds.responsePairs()
	if len(back) != 1 || back[0].old != "rotated-real" || back[0].new != testSentinel {
		t.Fatalf("reverse pairs=%+v", back)
	}
}

func TestSentinelLoading(t *testing.T) {
	t.Setenv("REVIEWD_SENTINELS", `{"A":"s1"}`)
	m, err := loadSentinels()
	if err != nil || m["A"] != "s1" {
		t.Fatalf("load: %v %+v", err, m)
	}
	t.Setenv("REVIEWD_SENTINELS", `{"A":`)
	if _, err := loadSentinels(); err == nil {
		t.Fatal("invalid sentinels accepted")
	}
	t.Setenv("HARNESS_AUTH", "")
	if _, err := newCredentials(map[string]string{"HARNESS_AUTH": testSentinel}, "", "reviewd"); err == nil {
		t.Fatal("missing real value accepted")
	}
}
