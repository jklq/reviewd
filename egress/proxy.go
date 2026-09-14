package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const maxRequestBody = 32 << 20
const dialTimeout = 10 * time.Second
const handshakeTimeout = 15 * time.Second

type server struct {
	allow *allowlist
	creds *credentials
	ca    *caAuthority
	mu    sync.Mutex
	roots *x509.CertPool
}

func (s *server) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	switch {
	case req.Method == http.MethodConnect:
		s.handleConnect(conn, br, req)
	case !req.URL.IsAbs():
		if req.URL.Path == "/healthz" || req.URL.Path == "/readyz" {
			_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 3\r\nConnection: close\r\n\r\nok\n")
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	default:
		s.handleForward(conn, br, req)
	}
}

type peekedConn struct {
	net.Conn
	r io.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func splitAuthority(authority string) (string, string) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		return authority, ""
	}
	return host, port
}

func (s *server) deny(conn net.Conn, method, host string) {
	stdLog.Printf("deny %s %s", method, host)
	_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 10\r\nConnection: close\r\n\r\nforbidden\n")
}

func fail(w io.Writer, status string) {
	_, _ = io.WriteString(w, "HTTP/1.1 "+status+"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
}

func (s *server) handleConnect(conn net.Conn, br *bufio.Reader, req *http.Request) {
	host, port := splitAuthority(req.RequestURI)
	if port == "" {
		port = "443"
	}
	if !s.allow.Match(host) {
		s.deny(conn, req.Method, host)
		return
	}
	stdLog.Printf("allow %s %s", req.Method, host)
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), dialTimeout)
	if err != nil {
		fail(conn, "502 Bad Gateway")
		return
	}
	defer upstream.Close()
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	peek, err := br.Peek(1)
	if err != nil {
		return
	}
	if peek[0] != 0x16 {
		s.relay(conn, br, upstream)
		return
	}
	leaf, err := s.ca.leaf(host)
	if err != nil {
		return
	}
	tlsConn := tls.Server(&peekedConn{Conn: conn, r: br}, &tls.Config{Certificates: []tls.Certificate{*leaf}, MinVersion: tls.VersionTLS12})
	_ = tlsConn.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	_ = tlsConn.SetDeadline(time.Time{})
	tlsBr := bufio.NewReader(tlsConn)
	inner, err := http.ReadRequest(tlsBr)
	if err != nil {
		return
	}
	roots := s.upstreamRoots()
	tlsUpstream := tls.Client(upstream, &tls.Config{ServerName: host, RootCAs: roots, MinVersion: tls.VersionTLS12})
	_ = tlsUpstream.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := tlsUpstream.Handshake(); err != nil {
		fail(tlsConn, "502 Bad Gateway")
		return
	}
	_ = tlsUpstream.SetDeadline(time.Time{})
	s.creds.maybeRefresh()
	if err := s.roundTrip(tlsUpstream, bufio.NewReader(tlsUpstream), tlsConn, inner); err != nil {
		stdLog.Printf("connect %s error: %v", host, err)
	}
}

func (s *server) upstreamRoots() *x509.CertPool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.roots != nil {
		return s.roots
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	roots.AddCert(s.ca.cert)
	s.roots = roots
	return s.roots
}

func (s *server) handleForward(conn net.Conn, br *bufio.Reader, req *http.Request) {
	host := req.URL.Hostname()
	if !s.allow.Match(host) {
		s.deny(conn, req.Method, host)
		return
	}
	stdLog.Printf("allow %s %s", req.Method, host)
	s.creds.maybeRefresh()
	address := req.URL.Host
	if _, port, err := net.SplitHostPort(address); err != nil || port == "" {
		if req.URL.Scheme == "https" {
			address = net.JoinHostPort(req.URL.Hostname(), "443")
		} else {
			address = net.JoinHostPort(req.URL.Hostname(), "80")
		}
	}
	raw, err := net.DialTimeout("tcp", address, dialTimeout)
	if err != nil {
		fail(conn, "502 Bad Gateway")
		return
	}
	defer raw.Close()
	var upstream io.ReadWriter = raw
	if req.URL.Scheme == "https" {
		tlsUpstream := tls.Client(raw, &tls.Config{ServerName: host, RootCAs: s.upstreamRoots(), MinVersion: tls.VersionTLS12})
		_ = tlsUpstream.SetDeadline(time.Now().Add(handshakeTimeout))
		if err := tlsUpstream.Handshake(); err != nil {
			fail(conn, "502 Bad Gateway")
			return
		}
		_ = tlsUpstream.SetDeadline(time.Time{})
		upstream = tlsUpstream
	}
	reader := bufio.NewReader(upstream.(io.Reader))
	if err := s.roundTrip(upstream, reader, conn, req); err != nil {
		stdLog.Printf("forward %s error: %v", host, err)
	}
}

func (s *server) roundTrip(upstream io.ReadWriter, reader *bufio.Reader, client io.Writer, req *http.Request) error {
	pairs := s.creds.requestPairs()
	substituteHeader(req.Header, pairs)
	if req.Body != nil {
		body, err := io.ReadAll(io.LimitReader(req.Body, maxRequestBody+1))
		_ = req.Body.Close()
		if err != nil {
			fail(client, "502 Bad Gateway")
			return err
		}
		if len(body) > maxRequestBody {
			fail(client, "413 Content Too Large")
			return fmt.Errorf("request body exceeds limit")
		}
		body = substituteBytes(body, pairs)
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = nil
		req.ContentLength = int64(len(body))
		req.TransferEncoding = nil
		req.Header.Del("Transfer-Encoding")
	}
	req.RequestURI = ""
	req.Close = true
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Authenticate")
	req.Header.Del("Proxy-Connection")
	if err := req.Write(upstream); err != nil {
		return err
	}
	for {
		resp, err := http.ReadResponse(reader, req)
		if err != nil {
			return err
		}
		if resp.StatusCode >= 100 && resp.StatusCode < 200 && resp.StatusCode != http.StatusSwitchingProtocols {
			substituteHeader(resp.Header, s.creds.responsePairs())
			if err := resp.Write(client); err != nil {
				_ = resp.Body.Close()
				return err
			}
			_ = resp.Body.Close()
			continue
		}
		return s.writeResponse(client, req, resp)
	}
}

func (s *server) writeResponse(client io.Writer, req *http.Request, resp *http.Response) error {
	defer resp.Body.Close()
	pairs := s.creds.responsePairs()
	substituteHeader(resp.Header, pairs)
	resp.Close = true
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return resp.Write(client)
	}
	if req.Method != http.MethodHead && resp.ContentLength != 0 && bodyAllowed(resp.StatusCode) {
		resp.Body = io.NopCloser(substituteStream(resp.Body, pairs))
		resp.ContentLength = -1
		resp.TransferEncoding = []string{"chunked"}
		resp.Header.Del("Content-Length")
	}
	return resp.Write(client)
}

func bodyAllowed(status int) bool {
	if status >= 100 && status < 200 {
		return false
	}
	return status != http.StatusNoContent && status != http.StatusNotModified
}

func substituteHeader(header http.Header, pairs []pair) {
	for key, values := range header {
		for i, value := range values {
			header[key][i] = substituteString(value, pairs)
		}
	}
}

func (s *server) relay(client net.Conn, clientReader *bufio.Reader, upstream net.Conn) {
	reqPairs := s.creds.requestPairs()
	respPairs := s.creds.responsePairs()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, substituteStream(clientReader, reqPairs))
		if closer, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, substituteStream(upstream, respPairs))
		done <- struct{}{}
	}()
	<-done
	<-done
}
