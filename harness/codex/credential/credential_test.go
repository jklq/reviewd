package credential

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func accessToken(t *testing.T, expires time.Time) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{"exp": expires.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
}

func writeLogin(t *testing.T, dir, access, refresh string) string {
	t.Helper()
	state := filepath.Join(dir, "auth.json")
	login, err := json.Marshal(map[string]any{
		"auth_mode":    "chatgpt",
		"last_refresh": "2000-01-01T00:00:00Z",
		"tokens":       map[string]any{"access_token": access, "account_id": "account", "refresh_token": refresh},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(state, login, 0600); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRefreshProjectsValidLoginWithoutRotation(t *testing.T) {
	expires := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	state := writeLogin(t, t.TempDir(), accessToken(t, expires), "refresh-secret")
	t.Setenv("PATH", t.TempDir())
	out, got, err := Refresh(context.Background(), state, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(expires) {
		t.Fatalf("expiry %s, want %s", got, expires)
	}
	var exported map[string]any
	if json.Unmarshal(out, &exported) != nil {
		t.Fatal("export is not JSON")
	}
	tokens, _ := exported["tokens"].(map[string]any)
	if tokens["refresh_token"] != "" || tokens["access_token"] == "" || tokens["account_id"] != "account" {
		t.Fatalf("bad export: %v", tokens)
	}
	// The state file keeps the refresh token; only the export is access-only.
	onDisk, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if json.Unmarshal(onDisk, &stored) != nil {
		t.Fatal("state file corrupted")
	}
	if stored["tokens"].(map[string]any)["refresh_token"] != "refresh-secret" {
		t.Fatal("state file modified")
	}
}

func TestRefreshRotatesThroughCodexAppServer(t *testing.T) {
	expires := time.Now().Add(time.Minute).Truncate(time.Second)
	state := writeLogin(t, t.TempDir(), accessToken(t, expires), "old-refresh")
	fakeCodex(t)
	if _, _, err := Refresh(context.Background(), state, time.Hour); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if json.Unmarshal(onDisk, &stored) != nil {
		t.Fatal("state file corrupted")
	}
	if stored["tokens"].(map[string]any)["refresh_token"] != "rotated-refresh" {
		t.Fatal("rotation was not persisted")
	}
}

func TestRefreshRejectsUnsupportedState(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Refresh(context.Background(), filepath.Join(dir, "state.json"), time.Minute); err == nil {
		t.Fatal("accepted a state file not named auth.json")
	}
	state := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(state, []byte(`{"auth_mode":"api_key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Refresh(context.Background(), state, time.Minute); err == nil {
		t.Fatal("accepted an API key login")
	}
}

// fakeCodex installs a `codex` executable that acts as the test process itself.
func fakeCodex(t *testing.T) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nexec " + binary + " -test.run=^TestCodexAppServerHelper$\n"
	if err = os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_CODEX", "1")
}

func TestCodexAppServerHelper(t *testing.T) {
	dir := os.Getenv("CODEX_HOME")
	if dir == "" || os.Getenv("FAKE_CODEX") == "" {
		return
	}
	requests := json.NewDecoder(os.Stdin)
	responses := json.NewEncoder(os.Stdout)
	for {
		var message struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if requests.Decode(&message) != nil {
			break
		}
		switch message.Method {
		case "initialize":
			_ = responses.Encode(map[string]any{"id": *message.ID, "result": map[string]any{}})
		case "account/read":
			refreshed := time.Now().Add(2 * time.Hour)
			login, _ := json.Marshal(map[string]any{
				"auth_mode": "chatgpt",
				"tokens":    map[string]any{"access_token": accessToken(t, refreshed), "account_id": "account", "refresh_token": "rotated-refresh"},
			})
			if err := os.WriteFile(filepath.Join(dir, "auth.json"), login, 0600); err != nil {
				t.Fatal(err)
			}
			_ = responses.Encode(map[string]any{"id": *message.ID, "result": map[string]any{"account": map[string]any{"type": "chatgpt"}}})
			return
		}
	}
	t.Fatal("codex app server protocol violated")
}
