package plugin_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jklq/reviewd/harness"
	"github.com/jklq/reviewd/harness/plugin"
)

func TestExecutableDriver(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "driver")
	build := exec.Command("go", "build", "-o", binary, "./cmd/reviewd-driver-codex")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	defer plugin.Close()
	d, err := plugin.Open("github.com/jklq/reviewd/harness/codex", binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(harness.Options{}); err == nil {
		t.Fatal("plugin accepted missing model")
	}
	launch, err := d.Prepare(harness.Options{Model: "test"}, map[string]string{"OPENAI_API_KEY": "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if len(launch.Routes) != 2 || launch.Routes[0].Headers.Get("Authorization") != "Bearer test-key" {
		t.Fatal("RPC lost driver result")
	}
	if strings.Contains(launch.Script, "test-key") {
		t.Fatal("credential leaked")
	}
	again, err := plugin.Open("github.com/jklq/reviewd/harness/codex", binary)
	if err != nil || d != again {
		t.Fatal("did not reuse plugin process")
	}
	if _, err := plugin.Open("missing/driver", binary); err == nil {
		t.Fatal("wrong driver name accepted")
	}
	plugin.Close()
	if _, err := d.Prepare(harness.Options{Model: "test"}, nil); err == nil {
		t.Fatal("closed plugin remains usable")
	}
}
