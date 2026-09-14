package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	for _, mutate := range []func(*Config){func(c *Config) { c.Parallelism = 0 }, func(c *Config) { c.Validator = "missing" }, func(c *Config) { h := c.Harnesses["codex"]; h.Env = []string{"GITHUB_TOKEN"}; c.Harnesses["codex"] = h }, func(c *Config) { h := c.Harnesses["codex"]; h.Network = "host"; c.Harnesses["codex"] = h }, func(c *Config) { h := c.Harnesses["codex"]; h.Command = []string{"codex"}; c.Harnesses["codex"] = h }, func(c *Config) { h := c.Harnesses["codex"]; h.Model = "bad\nmodel"; c.Harnesses["codex"] = h }, func(c *Config) { h := c.Harnesses["codex"]; c.Harnesses[strings.Repeat("a", 121)] = h }} {
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
func TestSizeTierSelection(t *testing.T) {
	parallelism := 2
	c := Default()
	c.Harnesses["fast"] = c.Harnesses["codex"]
	c.SizeTiers = []SizeTier{
		{Name: "small", MaxLines: 50, MaxFiles: 5, Reviewers: []string{"fast"}, Parallelism: &parallelism},
		{Name: "medium", MaxLines: 500, Reviewers: []string{"codex"}},
		{Name: "large", Reviewers: []string{"codex", "fast"}},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		lines, files int
		tier         string
		reviewers    []string
		parallelism  int
	}{
		{10, 2, "small", []string{"fast"}, 2},
		{10, 20, "medium", []string{"codex"}, 1},
		{400, 2, "medium", []string{"codex"}, 1},
		{5000, 100, "large", []string{"codex", "fast"}, 1},
	} {
		sel := c.Select(tc.lines, tc.files)
		if sel.Tier != tc.tier || !reflect.DeepEqual(sel.Reviewers, tc.reviewers) || sel.Validator != "codex" || sel.Parallelism != tc.parallelism {
			t.Fatalf("lines=%d files=%d: %+v", tc.lines, tc.files, sel)
		}
	}
	// No tier matches: top-level reviewers, validator and parallelism apply.
	c.SizeTiers = c.SizeTiers[:1]
	sel := c.Select(5000, 100)
	if sel.Tier != "" || !reflect.DeepEqual(sel.Reviewers, c.Reviewers) || sel.Validator != c.Validator || sel.Parallelism != c.Parallelism {
		t.Fatalf("fallback: %+v", sel)
	}
}

func TestSizeTierValidation(t *testing.T) {
	parallelism := 0
	for _, tiers := range [][]SizeTier{
		{{Reviewers: []string{"missing"}}},
		{{Reviewers: []string{"codex"}, Validator: "missing"}},
		{{Reviewers: []string{"codex"}, Parallelism: &parallelism}},
		{{Reviewers: []string{"codex"}, MaxLines: -1}},
		{{Name: "bad\nname", Reviewers: []string{"codex"}}},
		{{Name: "dup", Reviewers: []string{"codex"}}, {Name: "dup", Reviewers: []string{"codex"}}},
		{{Reviewers: []string{"codex"}}, {Reviewers: []string{"codex"}}}, // catch-all first swallows the rest
		{{MaxLines: 500, Reviewers: []string{"codex"}}, {MaxLines: 100, Reviewers: []string{"codex"}}},
	} {
		c := Default()
		c.SizeTiers = tiers
		if c.Validate() == nil {
			t.Fatalf("invalid tiers accepted: %+v", tiers)
		}
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
