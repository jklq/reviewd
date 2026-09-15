package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunEmitsAccessOnlyContract(t *testing.T) {
	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	claims, err := json.Marshal(map[string]any{"exp": expires.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "auth.json")
	login, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token":  "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature",
			"account_id":    "account",
			"refresh_token": "refresh-secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(state, login, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REVIEWD_CREDENTIAL_STATE", state)
	t.Setenv("REVIEWD_CREDENTIAL_MIN_VALIDITY", "60")
	t.Setenv("PATH", t.TempDir())
	var stdout bytes.Buffer
	if err = run(&stdout); err != nil {
		t.Fatal(err)
	}
	var result struct {
		ExpiresAt time.Time         `json:"expires_at"`
		Env       map[string]string `json:"env"`
	}
	if json.Unmarshal(stdout.Bytes(), &result) != nil || !result.ExpiresAt.Equal(expires) || result.Env["CODEX_AUTH_JSON"] == "" {
		t.Fatalf("invalid contract output: %s", stdout.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte("refresh-secret")) {
		t.Fatal("refresh token exported")
	}
}

func TestRunRequiresMinValidity(t *testing.T) {
	t.Setenv("REVIEWD_CREDENTIAL_MIN_VALIDITY", "")
	if err := run(io.Discard); err == nil {
		t.Fatal("accepted missing min validity")
	}
}
