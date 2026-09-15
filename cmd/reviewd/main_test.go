package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"reviewd/internal/config"
	"reviewd/internal/credential"
)

func TestInitUsesDefaultAndCustomHarnessConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviewd.json")
	if err := run([]string{"init", "--config", path, "--app-id", "123"}); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	defaults := config.Default()
	if !reflect.DeepEqual(c.Harnesses, defaults.Harnesses) {
		t.Fatal("init changed the default harness")
	}
	if err = run([]string{"init", "--config", path}); err == nil {
		t.Fatal("overwrote existing configuration")
	}
	custom := filepath.Join(t.TempDir(), "reviewd.json")
	if err = run([]string{"init", "--config", custom, "--image", "custom:local", "--command", `["custom","{{.Prompt}}"]`, "--harness-env", "CUSTOM_AUTH"}); err != nil {
		t.Fatal(err)
	}
	c, err = config.Load(custom)
	if err != nil {
		t.Fatal(err)
	}
	h := c.Harnesses[c.Reviewers[0]]
	if h.Image != "custom:local" || !reflect.DeepEqual(h.Command, []string{"custom", "{{.Prompt}}"}) || !reflect.DeepEqual(h.Env, []string{"CUSTOM_AUTH"}) {
		t.Fatal("custom harness not preserved")
	}
}

func credentialTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "login.json")
	if err := os.WriteFile(state, []byte("token"), 0600); err != nil {
		t.Fatal(err)
	}
	doc := `{"harnesses":{"custom":{"image":"custom:local","command":["custom","{{.Prompt}}"],"network":"none"}},"reviewers":["custom"],"validator":"custom","credentials":{"shared":{"state_file":"` + state + `","exports":["HARNESS_AUTH"],"command":["sh","-c","printf '%s' '{\"expires_at\":\"2035-01-01T00:00:00Z\",\"env\":{\"HARNESS_AUTH\":\"test-access\"}}'"]}}}`
	path := filepath.Join(dir, "reviewd.json")
	if err := os.WriteFile(path, []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := f()
	_ = w.Close()
	os.Stdout = old
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), runErr
}

func TestCredentialEnvPrintsManagerResult(t *testing.T) {
	path := credentialTestConfig(t)
	out, err := captureStdout(t, func() error {
		return run([]string{"credential", "env", "--config", path, "shared"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var printed credential.Result
	if err := json.Unmarshal([]byte(out), &printed); err != nil {
		t.Fatalf("stdout is not a result: %q %v", out, err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := credential.New(c).Resolve(context.Background(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(printed, want) {
		t.Fatalf("got %+v want %+v", printed, want)
	}
	if printed.Env["HARNESS_AUTH"] != "test-access" {
		t.Fatalf("unexpected env: %+v", printed.Env)
	}
}

func TestCredentialEnvFailures(t *testing.T) {
	path := credentialTestConfig(t)
	for _, args := range [][]string{
		{"credential", "env", "--config", path, "missing"},
		{"credential", "env", "--config", path},
		{"credential", "env", "--config", filepath.Join(t.TempDir(), "absent.json"), "shared"},
		{"credential", "bogus", "--config", path, "shared"},
	} {
		if out, err := captureStdout(t, func() error { return run(args) }); err == nil {
			t.Fatalf("%v accepted: %q", args, out)
		} else if out != "" {
			t.Fatalf("%v wrote to stdout: %q", args, out)
		}
	}
	bad := filepath.Join(t.TempDir(), "reviewd.json")
	if err := os.WriteFile(bad, []byte(`{"harnesses":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error {
		return run([]string{"credential", "env", "--config", bad, "shared"})
	}); err == nil {
		t.Fatal("invalid config accepted")
	}
}
