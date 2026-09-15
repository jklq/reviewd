package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"reviewd/internal/config"
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
