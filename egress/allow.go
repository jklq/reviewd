package main

import (
	"log"
	"os"
	"strings"
)

var stdLog = log.New(os.Stderr, "egress: ", log.LstdFlags)

type allowlist struct {
	entries []string
}

func parseAllow(parts []string, env string) *allowlist {
	seen := map[string]bool{}
	var entries []string
	add := func(entry string) {
		entry = strings.ToLower(strings.TrimSpace(entry))
		entry = strings.TrimSuffix(entry, ".")
		if entry == "" || seen[entry] {
			return
		}
		seen[entry] = true
		entries = append(entries, entry)
	}
	for _, part := range parts {
		add(part)
	}
	for _, part := range strings.Split(env, ",") {
		add(part)
	}
	return &allowlist{entries: entries}
}

func (a *allowlist) Match(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return false
	}
	for _, entry := range a.entries {
		if strings.HasPrefix(entry, "*.") {
			base := entry[2:]
			if host == base || strings.HasSuffix(host, "."+base) {
				return true
			}
			continue
		}
		if host == entry {
			return true
		}
	}
	return false
}
