package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTemplateArgumentsRemainLiteral(t *testing.T) {
	prompt := "quote ' \" $(touch /tmp/pwned) `id`\nsecond line"
	got, err := Expand([]string{"harness", "--prompt", "{{.Prompt}}"}, Variables{Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"harness", "--prompt", prompt}) {
		t.Fatal(got)
	}
	if _, err = Expand([]string{"{{.Missing}}"}, Variables{}); err == nil {
		t.Fatal("unknown template accepted")
	}
}

func TestTemplateUsageDetection(t *testing.T) {
	withCommand := func(command ...string) Config {
		c := Default()
		h := c.Harnesses["codex"]
		h.Command = command
		c.Harnesses["codex"] = h
		return c
	}
	if err := withCommand("agent", `{{ printf "%s" .Prompt }}`).Validate(); err != nil {
		t.Fatalf("functional prompt template rejected: %v", err)
	}
	if err := withCommand("agent", "{{/* {{.Prompt}} */}}").Validate(); err == nil {
		t.Fatal("comment-only prompt token accepted")
	}
}

func TestCredentialConfiguration(t *testing.T) {
	c := Default()
	for _, mutate := range []func(*Config){
		func(c *Config) {
			h := c.Harnesses["codex"]
			h.Credentials = []string{"missing"}
			c.Harnesses["codex"] = h
		},
		func(c *Config) {
			h := c.Harnesses["codex"]
			h.Env = []string{"CODEX_AUTH_JSON"}
			c.Harnesses["codex"] = h
		},
		func(c *Config) {
			h := c.Harnesses["codex"]
			h.Credentials = append(h.Credentials, h.Credentials[0])
			c.Harnesses["codex"] = h
		},
		func(c *Config) {
			d := c.Credentials["codex_account"]
			d.Exports = []string{"REVIEWD_WEBHOOK_SECRET"}
			c.Credentials["codex_account"] = d
		},
		func(c *Config) {
			d := c.Credentials["codex_account"]
			d.Env = []string{"GITHUB_TOKEN"}
			c.Credentials["codex_account"] = d
		},
		func(c *Config) {
			d := c.Credentials["codex_account"]
			d.Command = nil
			c.Credentials["codex_account"] = d
		},
	} {
		copy := Default()
		mutate(&copy)
		if copy.Validate() == nil {
			t.Fatal("invalid credentials accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "reviewd.json")
	if err := os.WriteFile(path, defaultJSON, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Credentials["codex_account"].StateFile != filepath.Join(filepath.Dir(path), c.Credentials["codex_account"].StateFile) {
		t.Fatal("credential path not relative to config")
	}
	// Existing environment-only harnesses do not inherit account refresh defaults.
	if err = os.WriteFile(path, []byte(`{"harnesses":{"custom":{"image":"custom","command":["custom","{{.Prompt}}"],"env":["CUSTOM_AUTH"],"network":"none"}},"reviewers":["custom"],"validator":"custom"}`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil || len(loaded.Credentials) != 0 {
		t.Fatal("legacy config inherited credentials", err)
	}
}
func TestConfigRejectsUnsafeSettings(t *testing.T) {
	for _, mutate := range []func(*Config){func(c *Config) { c.Parallelism = 0 }, func(c *Config) { c.Validator = "missing" }, func(c *Config) { h := c.Harnesses["codex"]; h.Env = []string{"GITHUB_TOKEN"}; c.Harnesses["codex"] = h }, func(c *Config) { h := c.Harnesses["codex"]; h.Network = "host"; c.Harnesses["codex"] = h }, func(c *Config) { h := c.Harnesses["codex"]; h.Command = []string{"codex"}; c.Harnesses["codex"] = h }, func(c *Config) { h := c.Harnesses["codex"]; h.Model = "bad\nmodel"; c.Harnesses["codex"] = h }} {
		c := Default()
		mutate(&c)
		if c.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestStrictDecode(t *testing.T) {
	for _, s := range []string{`{"unknown":true}`, `{} {}`} {
		var c Config
		if Decode([]byte(s), &c) == nil {
			t.Fatal(s)
		}
	}
}
