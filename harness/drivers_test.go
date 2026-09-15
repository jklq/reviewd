package harness_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jklq/reviewd/harness"
	_ "github.com/jklq/reviewd/internal/providers"
)

func TestOfficialDriversKeepCredentialsOutOfLaunch(t *testing.T) {
	for _, tc := range []struct{ name, key string }{
		{"codex", "OPENAI_API_KEY"}, {"claudecode", "ANTHROPIC_API_KEY"}, {"muse", "META_API_KEY"}, {"opencode", "OPENCODE_API_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := harness.Lookup("github.com/jklq/reviewd/harness/" + tc.name)
			if err != nil {
				t.Fatal(err)
			}
			options := harness.Options{Model: "test-model"}
			if _, err := d.Prepare(options, nil); err == nil {
				t.Fatal("accepted missing credentials")
			}
			const secret = "provider-secret-123456"
			launch, err := d.Prepare(options, map[string]string{tc.key: secret})
			if err != nil {
				t.Fatal(err)
			}
			if len(launch.Routes) == 0 {
				t.Fatal("no authenticated routes")
			}
			routes, _ := json.Marshal(launch.Routes)
			if !strings.Contains(string(routes), secret) {
				t.Fatal("route missing credentials")
			}
			launch.Routes = nil
			public, _ := json.Marshal(launch)
			if strings.Contains(string(public), secret) {
				t.Fatal("credential leaked into launch")
			}
		})
	}
}

func TestOpenCodeCredentialModes(t *testing.T) {
	d, err := harness.Lookup("github.com/jklq/reviewd/harness/opencode")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "opencode-subscription-secret"
	auth := `{"opencode-go":{"type":"api","key":"opencode-subscription-secret"},"opencode":{"type":"api","key":"zen-secret"}}`
	launch, err := d.Prepare(harness.Options{Model: "opencode-go/deepseek-v4.1-flash"}, map[string]string{"OPENCODE_AUTH_JSON": auth})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Routes[0].URL != "https://opencode.ai/zen/go/v1/chat/completions" {
		t.Fatalf("wrong Go route: %s", launch.Routes[0].URL)
	}
	if launch.Environment["REVIEWD_MODEL"] != "deepseek-v4.1-flash" {
		t.Fatalf("endpoint must receive the bare model: %v", launch.Environment)
	}
	launch.Routes = nil
	public, _ := json.Marshal(launch)
	if strings.Contains(string(public), secret) {
		t.Fatal("credential leaked into launch")
	}
	// A bare or opencode/ model still selects the Zen endpoint and bare ID.
	for _, model := range []string{"deepseek-v4.1-flash", "opencode/deepseek-v4.1-flash"} {
		launch, err = d.Prepare(harness.Options{Model: model}, map[string]string{"OPENCODE_AUTH_JSON": auth})
		if err != nil {
			t.Fatal(err)
		}
		if launch.Routes[0].URL != "https://opencode.ai/zen/v1/chat/completions" || launch.Environment["REVIEWD_MODEL"] != "deepseek-v4.1-flash" {
			t.Fatalf("wrong Zen route for %q", model)
		}
	}
	// Static keys remain supported, and selecting both credentials is an error.
	if _, err = d.Prepare(harness.Options{Model: "deepseek-v4.1-flash"}, map[string]string{"OPENCODE_API_KEY": secret}); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Prepare(harness.Options{Model: "deepseek-v4.1-flash"}, map[string]string{"OPENCODE_API_KEY": secret, "OPENCODE_AUTH_JSON": auth}); err == nil {
		t.Fatal("accepted both credentials")
	}
	for _, tc := range []struct{ model, auth string }{
		{"openrouter/anthropic/claude", auth},
		{"opencode-go/deepseek-v4.1-flash", `{}`},
		{"opencode-go/deepseek-v4.1-flash", `{"opencode-go":{"type":"oauth","access":"token"}}`},
	} {
		if _, err = d.Prepare(harness.Options{Model: tc.model}, map[string]string{"OPENCODE_AUTH_JSON": tc.auth}); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsupported credentials accepted for %q: %v", tc.model, err)
		}
	}
}

func TestMuseLoginCredential(t *testing.T) {
	d, err := harness.Lookup("github.com/jklq/reviewd/harness/muse")
	if err != nil {
		t.Fatal(err)
	}
	const access, key = "muse-access-secret", "muse-api-secret"
	login := `{"providers":{"meta":{"mechanism":"oauth","access_token":"muse-access-secret","api_key":"muse-api-secret"}}}`
	launch, err := d.Prepare(harness.Options{Model: "muse-spark-1.3"}, map[string]string{"MUSE_AUTH_JSON": login})
	if err != nil {
		t.Fatal(err)
	}
	if got := launch.Routes[0].Headers.Get("Authorization"); got != "Bearer "+key {
		t.Fatalf("login account key not used: %q", got)
	}
	launch.Routes = nil
	public, _ := json.Marshal(launch)
	if strings.Contains(string(public), access) || strings.Contains(string(public), key) {
		t.Fatal("credential leaked into launch")
	}
	if _, err = d.Prepare(harness.Options{Model: "muse-spark-1.3"}, map[string]string{"MUSE_AUTH_JSON": login, "META_API_KEY": key}); err == nil {
		t.Fatal("accepted both credentials")
	}
	if _, err = d.Prepare(harness.Options{Model: "muse-spark-1.3"}, map[string]string{"MUSE_AUTH_JSON": `{"providers":{"meta":{"access_token":"only-oauth"}}}`}); err == nil {
		t.Fatal("accepted a login without an account key")
	}
}

func TestCodexAccount(t *testing.T) {
	d, _ := harness.Lookup("github.com/jklq/reviewd/harness/codex")
	launch, err := d.Prepare(harness.Options{Model: "test"}, map[string]string{"CODEX_AUTH_JSON": `{"tokens":{"access_token":"access-secret","account_id":"account","refresh_token":"never-export"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Routes[0].URL != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatal("wrong account route")
	}
	data, _ := json.Marshal(launch)
	if strings.Contains(string(data), "never-export") {
		t.Fatal("refresh token escaped")
	}
	launch.Routes = nil
	data, _ = json.Marshal(launch)
	if strings.Contains(string(data), "access-secret") {
		t.Fatal("account credential escaped")
	}
}
