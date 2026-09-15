package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunProjectsStateWithoutAlteringIt(t *testing.T) {
	state := filepath.Join(t.TempDir(), "auth.json")
	login := `{"providers":{"meta":{"mechanism":"oauth","access_token":"access-secret"}}}`
	if err := os.WriteFile(state, []byte(login), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REVIEWD_CREDENTIAL_STATE", state)
	var stdout bytes.Buffer
	if err := run(&stdout, []string{"MUSE_AUTH_JSON"}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		ExpiresAt time.Time         `json:"expires_at"`
		Env       map[string]string `json:"env"`
	}
	if json.Unmarshal(stdout.Bytes(), &result) != nil {
		t.Fatalf("invalid contract output: %s", stdout.String())
	}
	if result.Env["MUSE_AUTH_JSON"] != login {
		t.Fatal("state altered by projection")
	}
	if !result.ExpiresAt.After(time.Now().Add(24 * time.Hour)) {
		t.Fatal("projection must outlive a job")
	}
}

func TestRunRejectsUnusableInput(t *testing.T) {
	t.Setenv("REVIEWD_CREDENTIAL_STATE", filepath.Join(t.TempDir(), "missing.json"))
	if err := run(&bytes.Buffer{}, []string{"MUSE_AUTH_JSON"}); err == nil {
		t.Fatal("accepted a missing state file")
	}
	state := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(state, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REVIEWD_CREDENTIAL_STATE", state)
	if err := run(&bytes.Buffer{}, []string{"MUSE_AUTH_JSON"}); err == nil {
		t.Fatal("accepted a non-JSON login state")
	}
	if err := run(&bytes.Buffer{}, nil); err == nil {
		t.Fatal("accepted a missing export name")
	}
}
