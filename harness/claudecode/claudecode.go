// Package claudecode provides the official reviewd driver for the Claude Agent SDK.
package claudecode

import (
	_ "embed"
	"fmt"
	"net/http"

	"github.com/jklq/reviewd/harness"
)

//go:embed run.mjs
var script string

type Driver struct{}

func init() { harness.Register("github.com/jklq/reviewd/harness/claudecode", Driver{}) }
func (Driver) Validate(o harness.Options) error {
	if o.ServiceTier != "" {
		return fmt.Errorf("unsupported service_tier %q", o.ServiceTier)
	}
	return harness.ValidateOptions(o, "low", "medium", "high", "max")
}
func (d Driver) Prepare(o harness.Options, values map[string]string) (harness.Launch, error) {
	if err := d.Validate(o); err != nil {
		return harness.Launch{}, err
	}
	key, err := harness.RequireCredential(values, "ANTHROPIC_API_KEY")
	if err != nil {
		return harness.Launch{}, err
	}
	headers := http.Header{"X-Api-Key": {key}, "Anthropic-Version": {"2023-06-01"}}
	env := harness.SDKEnvironment(o)
	env["ANTHROPIC_BASE_URL"] = env["REVIEWD_MODEL_URL"]
	env["ANTHROPIC_API_KEY"] = "reviewd-placeholder"
	env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
	return harness.Launch{Script: script, Environment: env, Routes: []harness.Route{
		{Path: "/v1/messages", Query: "beta=true", ForwardHeaders: []string{"User-Agent", "Anthropic-Beta"}, URL: "https://api.anthropic.com/v1/messages", Headers: headers},
		{Path: "/v1/messages/count_tokens", Query: "beta=true", ForwardHeaders: []string{"User-Agent", "Anthropic-Beta"}, URL: "https://api.anthropic.com/v1/messages/count_tokens", Headers: headers},
	}}, nil
}
