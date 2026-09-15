package sandbox

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jklq/reviewd/harness"
	"github.com/jklq/reviewd/internal/config"
)

type boundaryDriver struct{}

func (boundaryDriver) Validate(harness.Options) error { return nil }
func (boundaryDriver) Prepare(o harness.Options, values map[string]string) (harness.Launch, error) {
	script := `import fs from 'node:fs';
import { execFileSync } from 'node:child_process';
const canary = ['provider-canary','987654'].join('-');
if (process.env.TEST_PROVIDER_KEY || process.env.GITHUB_TOKEN) throw new Error('credential name exposed');
for (const p of ['/proc/self/environ', '/proc/1/environ', '/review/driver.mjs', '/review/prompt.md']) {
  if (fs.readFileSync(p, 'utf8').includes(canary)) throw new Error('credential exposed');
}
for (const pid of fs.readdirSync('/proc')) {
  if (!/^[0-9]+$/.test(pid)) continue;
  for (const name of ['environ', 'cmdline']) {
    let data = '';
    try { data = fs.readFileSync('/proc/' + pid + '/' + name, 'utf8'); } catch {}
    if (data.includes(canary)) throw new Error('credential exposed in /proc/' + pid + '/' + name);
  }
}
for (const path of ['/wrong', '/responses?url=https://attacker.example', '/%72esponses']) {
  const r = await fetch(process.env.REVIEWD_MODEL_URL + path, {method:'POST', body:'{}'});
  if (r.status !== 403) throw new Error('unsafe model route accepted');
}
if (process.env.REVIEWD_MODEL === 'wait') await new Promise(resolve => setTimeout(resolve, 60000));
fs.writeFileSync('/tmp/report.json', JSON.stringify({summary:'Boundary verified', confidence:5, reasons:[], findings:[], important_files:[], sequence_diagram:'sequenceDiagram\n A->>B: Review'}));
execFileSync('/usr/local/bin/reviewd', ['agent','submit','--file','/tmp/report.json'], {stdio:'inherit'});
`
	return harness.Launch{Script: script, Environment: harness.SDKEnvironment(o), Routes: []harness.Route{{Path: "/responses", URL: "https://unreachable.invalid/responses", Headers: http.Header{"Authorization": {"Bearer " + values["TEST_PROVIDER_KEY"]}}}}}, nil
}

func TestProviderDockerIsolation(t *testing.T) {
	if os.Getenv("REVIEWD_DOCKER_TEST") != "1" {
		t.Skip("set REVIEWD_DOCKER_TEST=1")
	}
	harness.Register("test/boundary", boundaryDriver{})
	dir := t.TempDir()
	binary := filepath.Join(dir, "reviewd")
	build := exec.Command("go", "build", "-o", binary, "./cmd/reviewd")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	if output, err := exec.Command("docker", "pull", "node:22-bookworm-slim").CombinedOutput(); err != nil {
		t.Fatalf("pull: %v %s", err, output)
	}
	t.Setenv("TEST_PROVIDER_KEY", "provider-canary-987654")
	t.Setenv("GITHUB_TOKEN", "github-canary")
	request := Request{Harness: config.Harness{Driver: "test/boundary", Model: "test", Image: "node:22-bookworm-slim", Network: "none", Env: []string{"TEST_PROVIDER_KEY"}}, Source: filepath.Join(dir, "head"), Base: filepath.Join(dir, "base"), Input: filepath.Join(dir, "input"), Output: filepath.Join(dir, "output"), Prompt: "Inspect credential isolation", Role: "reviewer"}
	for _, p := range []string{request.Source, request.Base, request.Input, filepath.Join(request.Input, "base")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(request.Input, "AGENTS.md"), []byte("Review"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.Input, "files.json"), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	docker := Docker{Binary: binary, Memory: "512m", CPUs: "1", Owner: "provider-test"}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := docker.Run(ctx, request)
	if err != nil {
		log, _ := os.ReadFile(filepath.Join(request.Output, "harness.log"))
		t.Fatalf("run: %v\n%s", err, log)
	}
	if result.Summary != "Boundary verified" {
		t.Fatal(result)
	}
	checkCleanup := func() {
		entries, _ := filepath.Glob(filepath.Join(dir, ".model-*"))
		if len(entries) != 0 {
			t.Fatal("model socket directory retained", entries)
		}
		log, _ := os.ReadFile(filepath.Join(request.Output, "harness.log"))
		if strings.Contains(string(log), "provider-canary-987654") {
			t.Fatal("credential logged")
		}
	}
	checkCleanup()
	request.Harness.Model = "wait"
	timeout, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if _, err := docker.Run(timeout, request); err == nil {
		t.Fatal("timeout ignored")
	}
	checkCleanup()
}
