package sandbox_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reviewd/internal/config"
	"reviewd/internal/credential"
	"reviewd/internal/report"
	"reviewd/internal/review"
	"reviewd/internal/sandbox"
	"reviewd/internal/timing"
)

// Runs real containers and the actual agent CLI, with a deterministic harness.
// It verifies fan-out, validator hand-off, literal template arguments, mounts,
// credentials isolation and structured output without any paid model calls.
func TestDockerPipeline(t *testing.T) {
	if os.Getenv("REVIEWD_DOCKER_TEST") != "1" {
		t.Skip("set REVIEWD_DOCKER_TEST=1 to run Docker integration")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "reviewd")
	build := exec.Command("go", "build", "-o", binary, "./cmd/reviewd")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", b, err)
	}
	ctx, timings := timing.New(context.Background())
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if b, err := exec.CommandContext(ctx, "docker", "pull", "alpine:3.21").CombinedOutput(); err != nil {
		t.Fatalf("pull: %s %v", b, err)
	}
	head := filepath.Join(dir, "head")
	base := filepath.Join(dir, "base")
	for _, p := range []string{head, base} {
		if err = os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(head, "store.go"), []byte("package store\nfunc save() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := `set -eu
 test -s /workspace/AGENTS.md
 test -s /workspace/agents.md
 test -s /review/context.json
 test -z "${GITHUB_TOKEN:-}"
 test -z "${REVIEWD_CREDENTIAL_STATE:-}"
 test -z "${REVIEWD_WEBHOOK_SECRET:-}"
 test "$HARNESS_AUTH" = 'test-access'
 test ! -S /var/run/docker.sock
 grep " /output " /proc/mounts | grep -q tmpfs
 test "$1" = 'literal $(touch /output/injected)'
 if touch /review/cannot-write 2>/dev/null; then exit 42; fi
 if touch /source/cannot-write 2>/dev/null; then exit 43; fi
 if [ "$2" = validator ]; then
   test -s /review/candidates.json
   grep -q 'Persist updates' /review/candidates.json
 fi
 reviewd agent init
 reviewd agent finding --path store.go --line 2 --side RIGHT --priority 2 --confidence 0.98 --title 'Handle persistence failures' --body 'Disk failures lose acknowledged updates. Propagate the write error.' --evidence 'The save return value is ignored.'
 cat >/tmp/overview.json <<'JSON'
{"summary":"Persist updates","confidence":2,"reasons":["Write failures lose updates."],"important_files":[{"path":"store.go","description":"Adds persistence."}],"sequence_diagram":"sequenceDiagram\n Caller->>Store: Save"}
JSON
 reviewd agent overview --file /tmp/overview.json
 reviewd agent submit
 `
	c := config.Default()
	c.Parallelism = 3
	t.Setenv("HARNESS_AUTH", "stale-inherited-value")
	t.Setenv("REVIEWD_WEBHOOK_SECRET", "server-only-secret")
	state := filepath.Join(dir, "login.json")
	if err := os.WriteFile(state, []byte("initial-refresh-token"), 0600); err != nil {
		t.Fatal(err)
	}
	c.Credentials = map[string]config.Credential{"shared": {StateFile: state, Exports: []string{"HARNESS_AUTH"}, Command: []string{"sh", "-c", `set -eu
test -z "${REVIEWD_WEBHOOK_SECRET:-}"
printf 'refresh\n' >> "$REVIEWD_CREDENTIAL_STATE.count"
printf 'rotated-refresh-token' > "$REVIEWD_CREDENTIAL_STATE.next"
mv "$REVIEWD_CREDENTIAL_STATE.next" "$REVIEWD_CREDENTIAL_STATE"
printf '%s' '{"expires_at":"2035-01-01T00:00:00Z","env":{"HARNESS_AUTH":"test-access"}}'
`}}}
	c.Harnesses = map[string]config.Harness{"mock": {Image: "alpine:3.21", Network: "none", Credentials: []string{"shared"}, Command: []string{"sh", "-c", script, "mock", "literal $(touch /output/injected)", "{{.Role}}", "{{.PromptFile}}"}}}
	c.Reviewers = []string{"mock"}
	c.Validator = "mock"
	owner := sha256.Sum256([]byte(dir))
	runner := sandbox.Docker{Binary: binary, Memory: "256m", CPUs: "1", Owner: hex.EncodeToString(owner[:16]), Credentials: credential.New(c)}
	engine := review.Engine{Config: c, Runner: runner}
	r, err := engine.Run(ctx, dir, review.Context{Repo: "o/r", Number: 1}, []report.ChangedFile{{Filename: "store.go", Patch: "@@ -1,2 +1,2 @@\n package store\n-func old() {}\n+func save() {}", Additions: 1, Deletions: 1}})
	if err != nil {
		_ = filepath.Walk(dir, func(p string, i os.FileInfo, err error) error {
			if strings.HasSuffix(p, "harness.log") {
				b, _ := os.ReadFile(p)
				t.Log(string(b))
			}
			return nil
		})
		t.Fatal(err)
	}
	if len(r.Findings) != 1 || r.MergeReady() {
		t.Fatal(r)
	}
	refreshes, err := os.ReadFile(state + ".count")
	if err != nil || string(refreshes) != "refresh\n" {
		t.Fatalf("parallel reviewers did not share refresh: %q %v", refreshes, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "reviewer-0", "output", "injected")); !os.IsNotExist(err) {
		t.Fatal("template executed shell content")
	}
	for _, stage := range []string{"reviewer-0", "reviewer-1", "reviewer-2", "validator-0"} {
		if _, err = os.Stat(filepath.Join(dir, stage, "output", "report.json")); err != nil {
			t.Fatal(err)
		}
	}
	for _, stage := range []string{"reviewer-0", "reviewer-1", "reviewer-2", "validator-0"} {
		for _, phase := range []string{"credentials", "container_start", "workspace_copy", "harness", "report_export", "cleanup"} {
			found := false
			for _, span := range timings.Spans() {
				if span.Name == stage+"/"+phase {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing timing %s/%s", stage, phase)
			}
		}
	}
	// A killed/timed-out harness must be removed and cannot be treated as success.
	input := filepath.Join(dir, "timeout-input")
	os.MkdirAll(filepath.Join(input, "base"), 0700)
	os.WriteFile(filepath.Join(input, "AGENTS.md"), []byte("review"), 0600)
	short, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	_, err = runner.Run(short, sandbox.Request{Harness: config.Harness{Image: "alpine:3.21", Network: "none", Command: []string{"sleep", "60"}}, Source: head, Base: base, Input: input, Output: filepath.Join(dir, "timeout-output"), Role: "reviewer"})
	if err == nil {
		t.Fatal("timeout accepted")
	}

	// An interrupted daemon's orphan is cleaned only by its own owner label.
	orphan, err := exec.CommandContext(ctx, "docker", "run", "-d", "--label", "reviewd.owner="+runner.Owner, "alpine:3.21", "sleep", "60").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", strings.TrimSpace(string(orphan))).Run() })
	if err = runner.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	left, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=reviewd.owner="+runner.Owner).Output()
	if err != nil || strings.TrimSpace(string(left)) != "" {
		t.Fatalf("orphan remained: %s %v", left, err)
	}
}
