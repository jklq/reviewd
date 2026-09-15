package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jklq/reviewd/internal/config"
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

func TestResolveHelper(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "reviewd-credential-codex")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	command := []string{"reviewd-credential-codex", "{{.Prompt}}"}
	resolved := resolveHelper(dir, command)
	if resolved[0] != helper || resolved[1] != "{{.Prompt}}" {
		t.Fatalf("helper not resolved: %v", resolved)
	}
	if command[0] != "reviewd-credential-codex" {
		t.Fatal("input mutated")
	}
	if got := resolveHelper(dir, []string{"python3", "-c"}); got[0] != "python3" {
		t.Fatalf("missing helper changed: %v", got)
	}
	if got := resolveHelper(dir, []string{"/usr/bin/env"}); got[0] != "/usr/bin/env" {
		t.Fatalf("explicit path changed: %v", got)
	}
}
