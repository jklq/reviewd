package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "egress:", err)
		os.Exit(1)
	}
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("use egress serve or egress healthcheck")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "healthcheck":
		return healthcheck(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var allows stringList
	listen := fs.String("listen", ":8080", "proxy listen address")
	fs.Var(&allows, "allow", "allowed destination host, repeatable, comma-separated, *.example.com matches subdomains")
	configPath := fs.String("config", "", "mounted reviewd config for credential refresh")
	caPath := fs.String("ca", "", "CA bundle override, defaults to the config ca_file")
	reviewdBin := fs.String("reviewd", "reviewd", "reviewd binary for credential refresh")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	sentinels, err := loadSentinels()
	if err != nil {
		return err
	}
	allow := parseAllow(flatten(allows), os.Getenv("REVIEWD_EGRESS_ALLOW"))
	caFile := *caPath
	if caFile == "" && *configPath != "" {
		caFile, err = configCAFile(*configPath)
		if err != nil {
			return err
		}
	}
	if caFile == "" {
		return fmt.Errorf("a CA bundle is required: pass --ca or a --config with ca_file")
	}
	authority, err := loadCA(caFile)
	if err != nil {
		return err
	}
	creds, err := newCredentials(sentinels, *configPath, *reviewdBin)
	if err != nil {
		return err
	}
	s := &server{
		allow: allow,
		creds: creds,
		ca:    authority,
	}
	stdLog.Printf("listening on %s with %d allowed hosts and %d credential exports", *listen, len(allow.entries), len(sentinels))
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	s.serveListener(ln)
	return nil
}

const maxProxyConns = 32

func (s *server) serveListener(ln net.Listener) {
	slots := make(chan struct{}, maxProxyConns)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			stdLog.Printf("accept: %v", err)
			continue
		}
		select {
		case slots <- struct{}{}:
			go func() {
				defer func() { <-slots }()
				s.handle(conn)
			}()
		default:
			stdLog.Printf("accept: too many connections")
			_ = conn.Close()
		}
	}
}

func flatten(lists []string) []string {
	var out []string
	for _, item := range lists {
		out = append(out, strings.Split(item, ",")...)
	}
	return out
}

func healthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	listen := fs.String("listen", ":8080", "proxy listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	_, port, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("proxy not ready: %s", resp.Status)
	}
	return nil
}
