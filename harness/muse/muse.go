// Package muse provides the official reviewd driver for the Muse Code SDK.
package muse

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jklq/reviewd/harness"
)

//go:embed run.mjs
var script string

type Driver struct{}

func init() { harness.Register("github.com/jklq/reviewd/harness/muse", Driver{}) }
func (Driver) Validate(o harness.Options) error {
	return harness.ValidateOptions(o, "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra")
}
func (d Driver) Prepare(o harness.Options, values map[string]string) (harness.Launch, error) {
	if err := d.Validate(o); err != nil {
		return harness.Launch{}, err
	}
	token, err := credential(values)
	if err != nil {
		return harness.Launch{}, err
	}
	env := harness.SDKEnvironment(o)
	env["META_API_KEY"] = "reviewd-placeholder"
	return harness.Launch{Script: script, Environment: env, Routes: []harness.Route{
		{Method: "GET", Path: "/muse-code/models", ForwardHeaders: []string{"User-Agent", "X-Meta-Ai-Gateway-Session-Id"}, URL: "https://api.meta.ai/muse-code/models", Headers: http.Header{"Authorization": {"Bearer " + token}, "X-Client-Id": {"tbh:exec"}}},
		{Path: "/v1/responses", ForwardHeaders: []string{"User-Agent", "X-Meta-Ai-Gateway-Session-Id"}, URL: "https://api.meta.ai/v1/responses", Headers: http.Header{"Authorization": {"Bearer " + token}, "X-Client-Id": {"tbh:exec"}}},
	}}, nil
}

// credential returns the account key minted by `muse login`, not its OAuth access token.
func credential(values map[string]string) (string, error) {
	raw := values["MUSE_AUTH_JSON"]
	if raw == "" {
		return harness.RequireCredential(values, "META_API_KEY")
	}
	if values["META_API_KEY"] != "" {
		return "", fmt.Errorf("select either MUSE_AUTH_JSON or META_API_KEY")
	}
	var auth struct {
		Providers struct {
			Meta struct {
				APIKey string `json:"api_key"`
			} `json:"meta"`
		} `json:"providers"`
	}
	if json.Unmarshal([]byte(raw), &auth) != nil {
		return "", fmt.Errorf("invalid MUSE_AUTH_JSON")
	}
	key := auth.Providers.Meta.APIKey
	if key == "" {
		return "", fmt.Errorf("MUSE_AUTH_JSON has no meta account key; run muse login")
	}
	return harness.RequireCredential(map[string]string{"api_key": key}, "api_key")
}
