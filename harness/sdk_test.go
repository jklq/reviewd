package harness_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jklq/reviewd/harness"
)

func TestSDKContracts(t *testing.T) {
	if os.Getenv("REVIEWD_SDK_TEST") != "1" {
		t.Skip("set REVIEWD_SDK_TEST=1 with built provider images")
	}
	tag := os.Getenv("REVIEWD_SDK_IMAGE_TAG")
	if tag == "" {
		tag = "local"
	}
	fixture, err := filepath.Abs("testdata/sdk-contract.mjs")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		key     string
		options harness.Options
		expect  []string
	}{
		{"codex", "OPENAI_API_KEY", harness.Options{Model: "gpt-5.6-luna", ReasoningEffort: "high", ServiceTier: "fast"}, []string{`"service_tier":"priority"`}},
		{"claudecode", "ANTHROPIC_API_KEY", harness.Options{Model: "claude-sonnet-4-6"}, nil},
		{"muse", "META_API_KEY", harness.Options{Model: "muse-spark-1.3", ReasoningEffort: "xhigh"}, nil},
		{"opencode", "OPENCODE_API_KEY", harness.Options{Model: "opencode-go/deepseek-v4.1-flash", ReasoningEffort: "max"}, []string{`"reasoning_effort":"max"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := harness.Lookup("github.com/jklq/reviewd/harness/" + tc.name)
			if err != nil {
				t.Fatal(err)
			}
			launch, err := d.Prepare(tc.options, map[string]string{tc.key: "server-only-test-key"})
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(t.TempDir(), "driver.mjs")
			if err := os.WriteFile(script, []byte(launch.Script), 0644); err != nil {
				t.Fatal(err)
			}
			for i := range launch.Routes {
				launch.Routes[i].Headers = nil
			}
			routes, _ := json.Marshal(launch.Routes)
			args := []string{"run", "--rm", "--init", "--network", "none", "--user", "1000:1000", "--memory", "1g", "--cpus", "1", "--tmpfs", "/home/reviewd:uid=1000,gid=1000", "--tmpfs", "/workspace:uid=1000,gid=1000", "--tmpfs", "/review:uid=1000,gid=1000", "-v", fixture + ":/fixture.mjs:ro", "-v", script + ":/driver.mjs:ro", "--env", "REVIEWD_TEST_ROUTES=" + string(routes)}
			if len(tc.expect) > 0 {
				args = append(args, "--env", "REVIEWD_TEST_EXPECT="+strings.Join(tc.expect, "\n"))
			}
			for name, value := range launch.Environment {
				args = append(args, "--env", name+"="+value)
			}
			args = append(args, "reviewd-"+tc.name+":"+tag, "node", "/fixture.mjs")
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
				t.Fatalf("SDK contract: %v\n%s", err, output)
			} else {
				t.Log(string(output))
			}
		})
	}
}
