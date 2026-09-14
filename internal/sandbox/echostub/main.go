package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "stub:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("stub", flag.ContinueOnError)
	httpAddr := fs.String("http", ":8080", "plain HTTP listen address")
	httpsAddr := fs.String("https", ":8443", "TLS listen address")
	certFile := fs.String("cert", "", "PEM bundle with the server certificate and key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *certFile == "" {
		return fmt.Errorf("a certificate bundle is required")
	}
	cert, err := tls.LoadX509KeyPair(*certFile, *certFile)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := r.Header.Get("X-Token")
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		fmt.Printf("AUTH=%s TOKEN=%s BODY=%s\n", auth, token, body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"auth": auth, "token": token, "body": string(body)})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	failed := make(chan error, 2)
	go func() { failed <- http.ListenAndServe(*httpAddr, mux) }()
	go func() {
		srv := &http.Server{Addr: *httpsAddr, Handler: mux, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}
		failed <- srv.ListenAndServeTLS("", "")
	}()
	fmt.Println("stub listening")
	return <-failed
}
