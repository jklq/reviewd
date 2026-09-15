package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jklq/reviewd/internal/config"
	"github.com/jklq/reviewd/internal/gateway"
)

type preparedProvider struct {
	harness    config.Harness
	dockerArgs []string
	socketDir  string
}

func (d Docker) prepareProvider(ctx context.Context, r Request) (preparedProvider, func(), error) {
	p := preparedProvider{harness: r.Harness}
	noop := func() {}
	if r.Harness.Driver == "" {
		return p, noop, nil
	}
	driver, err := r.Harness.LookupDriver()
	if err != nil {
		return p, noop, err
	}
	values, err := d.Credentials.Environment(ctx, r.Harness.Credentials)
	if err != nil {
		return p, noop, err
	}
	if values == nil {
		values = map[string]string{}
	}
	for _, name := range r.Harness.Env {
		if _, ok := values[name]; ok {
			return p, noop, fmt.Errorf("duplicate credential environment %s", name)
		}
		value := os.Getenv(name)
		if value == "" {
			return p, noop, fmt.Errorf("harness requires environment variable %s", name)
		}
		values[name] = value
	}
	launch, err := driver.Prepare(r.Harness.DriverOptions(), values)
	if err != nil {
		return p, noop, err
	}
	// The sibling directory is never mounted as input or output. Only its socket
	// directory is mounted, read-only, at a fixed path inside this one container.
	dir, err := os.MkdirTemp(filepath.Dir(r.Input), ".model-")
	if err != nil {
		return p, noop, err
	}
	closeGateway, err := gateway.Start(ctx, dir, launch.Routes)
	if err != nil {
		os.RemoveAll(dir)
		return p, noop, err
	}
	close := func() { closeGateway(); _ = os.RemoveAll(dir) }
	p.socketDir = dir
	p.dockerArgs = []string{"--mount", "type=bind,src=" + dir + ",dst=/run/reviewd-model,readonly"}
	names := make([]string, 0, len(launch.Environment))
	for name := range launch.Environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p.dockerArgs = append(p.dockerArgs, "--env", name+"="+launch.Environment[name])
	}
	// No original credential names or values can reach Docker's environment.
	p.harness.Env = nil
	p.harness.Credentials = nil
	// Write only the public SDK script, not the route table or credential map.
	if err := os.WriteFile(filepath.Join(r.Input, "driver.mjs"), []byte(sdkReady+launch.Script), 0600); err != nil {
		close()
		return p, noop, err
	}
	p.harness.Command = []string{"node", "/review/driver.mjs"}
	return p, close, nil
}

// Wait for the credential-free in-container relay before starting an SDK.
const sdkReady = `for (let attempt = 0; ; attempt++) {
  try {
    const response = await fetch('http://127.0.0.1:39123/ready', { signal: AbortSignal.timeout(1000) });
    await response.arrayBuffer();
    break;
  } catch (error) {
    if (attempt >= 50) throw new Error('model relay did not start');
    await new Promise(resolve => setTimeout(resolve, 100));
  }
}
`
