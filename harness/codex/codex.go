// Package codex provides the official reviewd driver for the Codex SDK.
package codex

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/jklq/reviewd/harness"
	"net/http"
)

//go:embed run.mjs
var script string

type Driver struct{}

func init() { harness.Register("github.com/jklq/reviewd/harness/codex", Driver{}) }
func (Driver) Validate(o harness.Options) error {
	return harness.ValidateOptions(o, "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "persistent")
}
func (d Driver) Prepare(o harness.Options, values map[string]string) (harness.Launch, error) {
	if err := d.Validate(o); err != nil {
		return harness.Launch{}, err
	}
	headers := http.Header{}
	upstream := "https://api.openai.com/v1"
	if raw := values["CODEX_AUTH_JSON"]; raw != "" {
		if values["OPENAI_API_KEY"] != "" {
			return harness.Launch{}, fmt.Errorf("select either CODEX_AUTH_JSON or OPENAI_API_KEY")
		}
		var auth struct {
			Tokens struct {
				AccessToken string `json:"access_token"`
				AccountID   string `json:"account_id"`
			} `json:"tokens"`
		}
		if json.Unmarshal([]byte(raw), &auth) != nil {
			return harness.Launch{}, fmt.Errorf("invalid CODEX_AUTH_JSON")
		}
		token, err := harness.RequireCredential(map[string]string{"access_token": auth.Tokens.AccessToken}, "access_token")
		if err != nil {
			return harness.Launch{}, err
		}
		headers.Set("Authorization", "Bearer "+token)
		if auth.Tokens.AccountID != "" {
			headers.Set("ChatGPT-Account-ID", auth.Tokens.AccountID)
		}
		headers.Set("originator", "codex_cli_rs")
		upstream = "https://chatgpt.com/backend-api/codex"
	} else {
		key, err := harness.RequireCredential(values, "OPENAI_API_KEY")
		if err != nil {
			return harness.Launch{}, err
		}
		headers.Set("Authorization", "Bearer "+key)
	}
	return harness.Launch{Script: script, Environment: harness.SDKEnvironment(o), Routes: []harness.Route{
		{Path: "/responses", ForwardHeaders: []string{"User-Agent", "X-Codex-Beta-Features", "X-Client-Request-Id", "X-Codex-Turn-Metadata", "Session-Id", "Thread-Id"}, URL: upstream + "/responses", Headers: headers},
		{Path: "/responses/compact", ForwardHeaders: []string{"User-Agent", "X-Codex-Beta-Features", "X-Client-Request-Id", "X-Codex-Turn-Metadata", "Session-Id", "Thread-Id"}, URL: upstream + "/responses/compact", Headers: headers},
	}}, nil
}
