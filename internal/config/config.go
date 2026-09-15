// Package config defines the operator-owned execution policy. Repository content
// can never select commands, images, credentials, or container mounts.
package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	"reviewd/internal/report"
)

type Harness struct {
	Image       string   `json:"image"`
	Command     []string `json:"command"`
	Model       string   `json:"model,omitempty"` // Declared model, recorded in reviews when the agent does not report one.
	Env         []string `json:"env,omitempty"`   // Names explicitly forwarded from the operator environment.
	Network     string   `json:"network"`
	Credentials []string `json:"credentials,omitempty"`
	Egress      bool     `json:"egress,omitempty"`
}

type Egress struct {
	Image   string   `json:"image"`
	Command []string `json:"command"`
	Network string   `json:"network"`
	CAFile  string   `json:"ca_file"`
}

// Credential commands are trusted operator programs. They update their durable
// state file and return only the short-lived environment needed by harnesses.
type Credential struct {
	StateFile string   `json:"state_file"`
	Command   []string `json:"command"`
	Env       []string `json:"env,omitempty"`
	Exports   []string `json:"exports"`
}

// SizeTier routes PRs within its bounds to its own harness set. Zero bounds
// are unlimited; empty Validator and Parallelism inherit the top-level values.
type SizeTier struct {
	Name        string   `json:"name,omitempty"`
	MaxLines    int      `json:"max_lines,omitempty"`
	MaxFiles    int      `json:"max_files,omitempty"`
	Reviewers   []string `json:"reviewers"`
	Validator   string   `json:"validator,omitempty"`
	Parallelism *int     `json:"parallelism,omitempty"`
}

func (t SizeTier) label(i int) string {
	if t.Name != "" {
		return t.Name
	}
	return fmt.Sprintf("tier-%d", i+1)
}

// Selection is the effective reviewer set for one PR: either the first
// matching size tier or, when none matches, the top-level defaults.
type Selection struct {
	Tier        string
	Reviewers   []string
	Validator   string
	Parallelism int
}

// Select returns the first size tier matching the PR's changed lines
// (additions plus deletions) and changed-file count.
func (c Config) Select(lines, files int) Selection {
	if lines < 0 {
		lines = 0
	}
	if files < 0 {
		files = 0
	}
	for i, t := range c.SizeTiers {
		if (t.MaxLines <= 0 || lines <= t.MaxLines) && (t.MaxFiles <= 0 || files <= t.MaxFiles) {
			sel := Selection{Tier: t.label(i), Reviewers: t.Reviewers, Validator: t.Validator, Parallelism: c.Parallelism}
			if sel.Validator == "" {
				sel.Validator = c.Validator
			}
			if t.Parallelism != nil {
				sel.Parallelism = *t.Parallelism
			}
			return sel
		}
	}
	return Selection{Reviewers: c.Reviewers, Validator: c.Validator, Parallelism: c.Parallelism}
}

// covers reports whether bound a (0 = unlimited) admits every value bound b admits.
func covers(a, b int) bool {
	return a <= 0 || (b > 0 && a >= b)
}

type Config struct {
	Listen               string                `json:"listen"`
	AgentBinary          string                `json:"agent_binary,omitempty"`
	DataDir              string                `json:"data_dir"`
	AppID                int64                 `json:"app_id"`
	PrivateKeyFile       string                `json:"private_key_file"`
	WebhookSecretEnv     string                `json:"webhook_secret_env"`
	Egress               *Egress               `json:"egress,omitempty"`
	ConfigFile           string                `json:"-"`
	Harnesses            map[string]Harness    `json:"harnesses"`
	Credentials          map[string]Credential `json:"credentials,omitempty"`
	Reviewers            []string              `json:"reviewers"`
	Parallelism          int                   `json:"parallelism"`
	Validator            string                `json:"validator"`
	SizeTiers            []SizeTier            `json:"size_tiers,omitempty"`
	Workers              int                   `json:"workers"`
	Timeout              string                `json:"timeout"`
	Memory               string                `json:"memory"`
	CPUs                 string                `json:"cpus"`
	MaxFindings          int                   `json:"max_findings"`
	MinFindingConfidence float64               `json:"min_finding_confidence"`
	Policy               string                `json:"policy"`
	AllowForks           bool                  `json:"allow_forks"`
}

//go:embed defaults.json
var defaultJSON []byte

func Default() Config {
	var c Config
	if err := json.Unmarshal(defaultJSON, &c); err != nil {
		panic("invalid embedded default configuration: " + err.Error())
	}
	return c
}
func Load(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	// The file owns the harness set; JSON unmarshalling merges into maps, so
	// clear the default entry to avoid resurrecting a harness the operator removed.
	c.Harnesses = nil
	c.Credentials = nil
	if err = Decode(b, &c); err != nil {
		return c, err
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return c, err
	}
	if !filepath.IsAbs(c.DataDir) {
		c.DataDir = filepath.Join(base, c.DataDir)
	}
	if !filepath.IsAbs(c.PrivateKeyFile) {
		c.PrivateKeyFile = filepath.Join(base, c.PrivateKeyFile)
	}
	if c.AgentBinary != "" && !filepath.IsAbs(c.AgentBinary) {
		c.AgentBinary = filepath.Join(base, c.AgentBinary)
	}
	for name, credential := range c.Credentials {
		if !filepath.IsAbs(credential.StateFile) {
			credential.StateFile = filepath.Join(base, credential.StateFile)
		}
		c.Credentials[name] = credential
	}
	if c.Egress != nil && c.Egress.CAFile != "" && !filepath.IsAbs(c.Egress.CAFile) {
		c.Egress.CAFile = filepath.Join(base, c.Egress.CAFile)
	}
	if abs, err := filepath.Abs(path); err == nil {
		c.ConfigFile = abs
	}
	return c, c.Validate()
}
func Decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

var name = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func AllowedEnv(env, webhookSecret string) bool {
	return name.MatchString(env) && env != webhookSecret && !strings.HasPrefix(env, "GITHUB_") && !strings.HasPrefix(env, "REVIEWD_") && env != "HOME" && env != "PATH" && env != "DOCKER_HOST" && env != "DOCKER_CONTEXT"
}

func validNetwork(network string) bool {
	return network == "bridge" || network == "none" || strings.HasPrefix(network, "reviewd-")
}

func (c Config) Validate() error {
	if c.Listen == "" || c.DataDir == "" || c.PrivateKeyFile == "" || !name.MatchString(c.WebhookSecretEnv) {
		return errors.New("listen, data_dir, private_key_file and webhook_secret_env are required")
	}
	if c.Workers < 1 || c.Workers > 32 || c.Parallelism < 1 || c.Parallelism > 16 {
		return errors.New("workers must be 1..32 and parallelism 1..16")
	}
	if len(c.Reviewers) == 0 {
		return errors.New("at least one reviewer is required")
	}
	if c.MaxFindings < 1 || c.MaxFindings > 50 || c.MinFindingConfidence < 0.5 || c.MinFindingConfidence > 1 {
		return errors.New("max_findings must be 1..50 and min_finding_confidence 0.5..1")
	}
	if d, err := time.ParseDuration(c.Timeout); err != nil || d < time.Second || d > 2*time.Hour {
		return errors.New("timeout must be 1s..2h")
	}
	if !regexp.MustCompile(`^[1-9][0-9]*[mg]$`).MatchString(c.Memory) || !regexp.MustCompile(`^[1-9][0-9]*(\.[0-9]+)?$`).MatchString(c.CPUs) {
		return errors.New("memory must be e.g. 2g; cpus must be positive")
	}
	for n, credential := range c.Credentials {
		if !name.MatchString(n) || credential.StateFile == "" || len(credential.Command) == 0 || credential.Command[0] == "" || len(credential.Exports) == 0 {
			return fmt.Errorf("invalid credential %q", n)
		}
		seen := map[string]bool{}
		for _, env := range credential.Exports {
			if !AllowedEnv(env, c.WebhookSecretEnv) || seen[env] {
				return fmt.Errorf("%s: invalid or duplicate credential export %q", n, env)
			}
			seen[env] = true
		}
		for _, env := range credential.Env {
			if !AllowedEnv(env, c.WebhookSecretEnv) {
				return fmt.Errorf("%s: forbidden refresh environment %q", n, env)
			}
		}
	}
	for _, n := range append(append([]string{}, c.Reviewers...), c.Validator) {
		if _, ok := c.Harnesses[n]; !ok {
			return fmt.Errorf("unknown harness %q", n)
		}
	}
	names := map[string]bool{}
	for i, t := range c.SizeTiers {
		if err := report.ValidateAttribution("size tier", t.Name); err != nil {
			return err
		}
		if t.Name != "" {
			if names[t.Name] {
				return fmt.Errorf("duplicate size tier %q", t.Name)
			}
			names[t.Name] = true
		}
		if t.MaxLines < 0 || t.MaxFiles < 0 {
			return fmt.Errorf("size tier %q: max_lines and max_files must be >= 0", t.label(i))
		}
		if len(t.Reviewers) == 0 {
			return fmt.Errorf("size tier %q: at least one reviewer is required", t.label(i))
		}
		for _, n := range t.Reviewers {
			if _, ok := c.Harnesses[n]; !ok {
				return fmt.Errorf("size tier %q: unknown harness %q", t.label(i), n)
			}
		}
		if t.Validator != "" {
			if _, ok := c.Harnesses[t.Validator]; !ok {
				return fmt.Errorf("size tier %q: unknown harness %q", t.label(i), t.Validator)
			}
		}
		if t.Parallelism != nil && (*t.Parallelism < 1 || *t.Parallelism > 16) {
			return fmt.Errorf("size tier %q: parallelism must be 1..16", t.label(i))
		}
	}
	for j := range c.SizeTiers {
		for i := 0; i < j; i++ {
			if covers(c.SizeTiers[i].MaxLines, c.SizeTiers[j].MaxLines) && covers(c.SizeTiers[i].MaxFiles, c.SizeTiers[j].MaxFiles) {
				return fmt.Errorf("size tier %q is unreachable: tier %q matches every PR it would match", c.SizeTiers[j].label(j), c.SizeTiers[i].label(i))
			}
		}
	}
	for n, h := range c.Harnesses {
		exports := map[string]bool{}
		for _, credentialName := range h.Credentials {
			credential, ok := c.Credentials[credentialName]
			if !ok {
				return fmt.Errorf("%s: unknown credential %q", n, credentialName)
			}
			for _, env := range credential.Exports {
				if exports[env] {
					return fmt.Errorf("%s: duplicate credential export %q", n, env)
				}
				exports[env] = true
			}
		}
		if !name.MatchString(n) || h.Image == "" || strings.HasPrefix(h.Image, "-") || len(h.Command) == 0 || h.Command[0] == "" {
			return fmt.Errorf("invalid harness %q", n)
		}
		if err := report.ValidateAttribution("model", h.Model); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		// The harness name is stamped into every review's attribution, so a
		// name the report rejects must fail here rather than on every run.
		if err := report.ValidateAttribution("harness", n); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		if h.Egress {
			if h.Network != "" {
				return fmt.Errorf("%s: network must be empty when egress is enabled", n)
			}
			if c.Egress == nil {
				return fmt.Errorf("%s: egress requires the global egress block", n)
			}
		} else if !validNetwork(h.Network) {
			return fmt.Errorf("%s: network must be bridge, none or reviewd-*", n)
		}
		// Usage is behavioral: a command must react to the prompt or the prompt
		// file. This accepts any valid template expression and rejects a token
		// that is merely mentioned in a comment.
		base := Variables{Prompt: "reviewd-prompt-a", PromptFile: "/review/prompt.md", Workspace: "/workspace", Role: "reviewer"}
		expanded, err := Expand(h.Command, base)
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		otherPrompt := base
		otherPrompt.Prompt = "reviewd-prompt-b"
		promptVariant, err := Expand(h.Command, otherPrompt)
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		otherFile := base
		otherFile.PromptFile = "/review/other.md"
		fileVariant, err := Expand(h.Command, otherFile)
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		if slices.Equal(expanded, promptVariant) && slices.Equal(expanded, fileVariant) {
			return fmt.Errorf("%s: command needs {{.Prompt}} or {{.PromptFile}}", n)
		}
		for _, env := range h.Env {
			if !AllowedEnv(env, c.WebhookSecretEnv) || exports[env] {
				return fmt.Errorf("%s: forbidden environment name %q", n, env)
			}
		}
	}
	if c.Egress != nil {
		if c.Egress.Image == "" || strings.HasPrefix(c.Egress.Image, "-") || len(c.Egress.Command) == 0 || c.Egress.Command[0] == "" {
			return errors.New("invalid egress block: image and command are required")
		}
		if !validNetwork(c.Egress.Network) {
			return errors.New("egress: network must be bridge, none or reviewd-*")
		}
		if c.Egress.CAFile == "" {
			return errors.New("egress: ca_file is required")
		}
	}
	return nil
}

type Variables struct {
	Prompt, PromptFile, Workspace, Role string
	Index                               int
}

func Expand(command []string, v Variables) ([]string, error) {
	out := make([]string, len(command))
	for i, arg := range command {
		t, err := template.New("arg").Option("missingkey=error").Parse(arg)
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		if err = t.Execute(&b, v); err != nil {
			return nil, err
		}
		out[i] = b.String()
	}
	return out, nil
}
