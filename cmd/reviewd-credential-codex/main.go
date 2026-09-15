// Command reviewd-credential-codex refreshes the Codex ChatGPT account login
// configured as a reviewd credential. It implements the shared refresh command
// contract documented in docs/configuration.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/jklq/reviewd/harness/codex/credential"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "reviewd-credential-codex:", err)
		os.Exit(1)
	}
}

func run(stdout io.Writer) error {
	seconds, err := strconv.Atoi(os.Getenv("REVIEWD_CREDENTIAL_MIN_VALIDITY"))
	if err != nil || seconds < 0 {
		return errors.New("REVIEWD_CREDENTIAL_MIN_VALIDITY must be a non-negative number of seconds")
	}
	auth, expires, err := credential.Refresh(context.Background(), os.Getenv("REVIEWD_CREDENTIAL_STATE"), time.Duration(seconds)*time.Second)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(struct {
		ExpiresAt string            `json:"expires_at"`
		Env       map[string]string `json:"env"`
	}{expires.Format(time.RFC3339), map[string]string{"CODEX_AUTH_JSON": string(auth)}})
}
