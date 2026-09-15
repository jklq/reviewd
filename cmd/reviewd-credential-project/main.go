// Command reviewd-credential-project exports a provider CLI's JSON login state.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// projection credits long validity to state that does not rotate.
const validity = 10 * 365 * 24 * time.Hour

func main() {
	if err := run(os.Stdout, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "reviewd-credential-project:", err)
		os.Exit(1)
	}
}

func run(stdout io.Writer, args []string) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("usage: reviewd-credential-project EXPORT_NAME")
	}
	state := os.Getenv("REVIEWD_CREDENTIAL_STATE")
	if state == "" {
		return errors.New("REVIEWD_CREDENTIAL_STATE is required")
	}
	b, err := os.ReadFile(state)
	if err != nil {
		return fmt.Errorf("read login state: %w", err)
	}
	if !json.Valid(b) {
		return errors.New("login state is not valid JSON")
	}
	return json.NewEncoder(stdout).Encode(struct {
		ExpiresAt string            `json:"expires_at"`
		Env       map[string]string `json:"env"`
	}{time.Now().Add(validity).UTC().Format(time.RFC3339), map[string]string{args[0]: string(b)}})
}
