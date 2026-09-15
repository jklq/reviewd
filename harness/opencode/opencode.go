// Package opencode provides the official reviewd driver for the OpenCode SDK.
// It routes OpenAI-compatible chat completions to fixed OpenCode Zen endpoints:
// the pay-as-you-go API (bare or opencode/ model) or the Go subscription
// (opencode-go/ model).
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
func (Driver) Validate(o harness.Options) error { return harness.ValidateOptions(o) }
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
	// The endpoint expects the bare model ID; the provider prefix selects it.
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

// splitModel uses OpenCode's provider/model convention. A bare model selects
// Zen, preserving bare Zen model IDs in existing configuration.
func splitModel(model string) (string, string) {
	provider, id, ok := strings.Cut(model, "/")
	if !ok {
		return zenProvider, model
	}
	return provider, id
}

// credential returns the selected provider's API key from either a projected
// OpenCode auth.json or the service environment.
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
	// The CLI stores key logins as type "api"; OAuth logins carry refreshable
	// tokens and would need a refresh helper before this driver can use them.
	if entry.Type != "api" {
		return "", fmt.Errorf("%s login type %q is not supported; sign in with an API key", provider, entry.Type)
	}
	return harness.RequireCredential(map[string]string{"key": entry.Key}, "key")
}
