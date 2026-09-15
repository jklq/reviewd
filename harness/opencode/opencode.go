// Package opencode provides the official reviewd driver for the OpenCode SDK.
package opencode

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jklq/reviewd/harness"
)

//go:embed run.mjs
var script string

type Driver struct{}

func init()                                     { harness.Register("github.com/jklq/reviewd/harness/opencode", Driver{}) }
func (Driver) Validate(o harness.Options) error {
	if o.Model == "" {
		return fmt.Errorf("driver requires model")
	}
	if o.ServiceTier != "" {
		return fmt.Errorf("unsupported service_tier %q", o.ServiceTier)
	}
	return harness.ValidateOption("reasoning_effort", o.ReasoningEffort)
}
func (d Driver) Prepare(o harness.Options, values map[string]string) (harness.Launch, error) {
	if err := d.Validate(o); err != nil {
		return harness.Launch{}, err
	}
	provider, model := splitModel(o.Model)
	upstream, ok := endpoints[provider]
	if !ok {
		return harness.Launch{}, fmt.Errorf("opencode driver does not support provider %q", provider)
	}
	key, err := credential(provider, values)
	if err != nil {
		return harness.Launch{}, err
	}
	o.Model = model
	return harness.Launch{Script: script, Environment: harness.SDKEnvironment(o), Routes: []harness.Route{
		{Path: "/v1/chat/completions", ForwardHeaders: []string{"User-Agent", "X-Session-Id", "X-Session-Affinity"}, URL: upstream + "/chat/completions", Headers: http.Header{"Authorization": {"Bearer " + key}}},
	}}, nil
}

const (
	zenProvider = "opencode"
	goProvider  = "opencode-go"
)

// endpoints are fixed HTTPS destinations; credentials never select them.
var endpoints = map[string]string{
	zenProvider: "https://opencode.ai/zen/v1",
	goProvider:  "https://opencode.ai/zen/go/v1",
}

func splitModel(model string) (string, string) {
	provider, id, ok := strings.Cut(model, "/")
	if !ok {
		return zenProvider, model
	}
	return provider, id
}

func credential(provider string, values map[string]string) (string, error) {
	raw := values["OPENCODE_AUTH_JSON"]
	if raw == "" {
		return harness.RequireCredential(values, "OPENCODE_API_KEY")
	}
	if values["OPENCODE_API_KEY"] != "" {
		return "", fmt.Errorf("select either OPENCODE_AUTH_JSON or OPENCODE_API_KEY")
	}
	var auth map[string]struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if json.Unmarshal([]byte(raw), &auth) != nil {
		return "", fmt.Errorf("invalid OPENCODE_AUTH_JSON")
	}
	entry, ok := auth[provider]
	if !ok || entry.Key == "" {
		return "", fmt.Errorf("OPENCODE_AUTH_JSON has no %s credential", provider)
	}
	if entry.Type != "api" {
		return "", fmt.Errorf("%s login type %q is not supported; sign in with an API key", provider, entry.Type)
	}
	return harness.RequireCredential(map[string]string{"key": entry.Key}, "key")
}
