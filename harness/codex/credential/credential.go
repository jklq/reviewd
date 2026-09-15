// Package credential refreshes a Codex ChatGPT account login for reviewd's
// shared credential contract. It is the Codex provider's reference refresh
// implementation and is deliberately not imported by the reviewd binary.
package credential

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// slack renews slightly before the required remaining validity, so a token
// cannot expire between refresh and the model call it was prepared for.
const slack = time.Minute

// Refresh returns access-only login JSON for stateFile and the access token's
// expiry. When less than minValidity plus slack remains, it asks the installed
// Codex CLI to rotate the login through its app server, which persists the
// rotated refresh token in the state file.
func Refresh(ctx context.Context, stateFile string, minValidity time.Duration) ([]byte, time.Time, error) {
	if filepath.Base(stateFile) != "auth.json" {
		return nil, time.Time{}, errors.New("codex login state file must be named auth.json")
	}
	auth, tokens, access, err := readLogin(stateFile)
	if err != nil {
		return nil, time.Time{}, err
	}
	expires, err := expiry(access)
	if err != nil {
		return nil, time.Time{}, err
	}
	if !expires.After(time.Now().Add(minValidity + slack)) {
		if err = rotate(ctx, filepath.Dir(stateFile)); err != nil {
			return nil, time.Time{}, err
		}
		if auth, tokens, access, err = readLogin(stateFile); err != nil {
			return nil, time.Time{}, err
		}
		if expires, err = expiry(access); err != nil {
			return nil, time.Time{}, err
		}
		if !expires.After(time.Now().Add(minValidity)) {
			return nil, time.Time{}, errors.New("codex did not extend the login's validity")
		}
	}
	tokens["refresh_token"] = ""
	auth["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(auth)
	if err != nil {
		return nil, time.Time{}, err
	}
	return b, expires.UTC(), nil
}

// readLogin returns the raw login, its tokens, and its access token. Unknown
// fields are preserved so exports stay faithful to the provider's format.
func readLogin(path string) (map[string]any, map[string]any, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read login state: %w", err)
	}
	var auth map[string]any
	if json.Unmarshal(b, &auth) != nil {
		return nil, nil, "", errors.New("invalid login state")
	}
	if auth["auth_mode"] != "chatgpt" {
		return nil, nil, "", errors.New("a ChatGPT account login is required")
	}
	tokens, ok := auth["tokens"].(map[string]any)
	if !ok {
		return nil, nil, "", errors.New("login state has no tokens")
	}
	access, ok := tokens["access_token"].(string)
	if !ok || access == "" {
		return nil, nil, "", errors.New("login state has no access token")
	}
	return auth, tokens, access, nil
}

// expiry reads the standard JWT exp claim without verifying the signature.
// The access token is opaque to reviewd; only its documented shape is used.
func expiry(access string) (time.Time, error) {
	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, errors.New("access token is not a JWT")
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}, errors.New("access token has no expiry")
	}
	return time.Unix(claims.Exp, 0), nil
}

// rotate runs one JSON-RPC exchange with the Codex app server. The server owns
// the refresh protocol and writes the rotated login beside CODEX_HOME.
func rotate(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "codex",
		"-c", `cli_auth_credentials_store="file"`,
		"-c", `forced_login_method="chatgpt"`,
		"app-server")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CODEX_HOME="+dir)
	// Stderr may contain login material; never forward or log it.
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start codex app server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()
	requests := json.NewEncoder(stdin)
	responses := json.NewDecoder(stdout)
	send := func(message map[string]any) error {
		if err := requests.Encode(message); err != nil {
			return errors.New("codex app server ended during refresh")
		}
		return nil
	}
	if err = send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "reviewd-credential-codex", "version": "1"}}}); err != nil {
		return err
	}
	if _, err = response(responses, 1); err != nil {
		return err
	}
	if err = send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return err
	}
	if err = send(map[string]any{"id": 2, "method": "account/read", "params": map[string]any{"refreshToken": true}}); err != nil {
		return err
	}
	result, err := response(responses, 2)
	if err != nil {
		return err
	}
	account, _ := result["account"].(map[string]any)
	if account["type"] != "chatgpt" {
		return errors.New("codex requires a ChatGPT account login")
	}
	return nil
}

// response reads until the reply with id arrives, skipping notifications.
func response(decoder *json.Decoder, id int) (map[string]any, error) {
	for {
		var message struct {
			ID     *int           `json:"id"`
			Error  map[string]any `json:"error"`
			Result map[string]any `json:"result"`
		}
		if err := decoder.Decode(&message); err != nil {
			return nil, errors.New("codex ended before the account refresh completed")
		}
		if message.ID == nil || *message.ID != id {
			continue
		}
		if message.Error != nil {
			return nil, errors.New("codex account refresh failed")
		}
		return message.Result, nil
	}
}
