// Package sandbox runs operator-defined harnesses in disposable Docker containers.
package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jklq/reviewd/internal/config"
	"github.com/jklq/reviewd/internal/credential"
	"github.com/jklq/reviewd/internal/report"
	"github.com/jklq/reviewd/internal/store"
)

type Request struct {
	Harness                           config.Harness
	Source, Base, Input, Output, Role string
	Index                             int
	Prompt                            string
}
type Runner interface {
	Run(context.Context, Request) (report.Report, error)
}
type Docker struct {
	Binary, Memory, CPUs, Owner string
	Credentials                 *credential.Manager
}

// limitedLog bounds disk use while continuing to drain the process output.
type limitedLog struct {
	w io.Writer
	n int64
}

func (l *limitedLog) Write(p []byte) (int, error) {
	n := len(p)
	if l.n <= 0 {
		return n, nil
	}
	q := p
	if int64(len(q)) > l.n {
		q = q[:l.n]
	}
	k, err := l.w.Write(q)
	l.n -= int64(k)
	return n, err
}
func (d Docker) Run(ctx context.Context, r Request) (report.Report, error) {
	var result report.Report
	for _, p := range []string{r.Source, r.Base, r.Input, r.Output, d.Binary} {
		if !filepath.IsAbs(p) {
			return result, fmt.Errorf("sandbox paths must be absolute: %s", p)
		}
	}
	if err := os.MkdirAll(r.Output, 0700); err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(r.Input, "prompt.md"), []byte(r.Prompt), 0600); err != nil {
		return result, err
	}
	prepared, closeProvider, err := d.prepareProvider(ctx, r)
	if err != nil {
		return result, err
	}
	defer closeProvider()
	r.Harness = prepared.harness
	args, err := config.Expand(r.Harness.Command, config.Variables{Prompt: r.Prompt, PromptFile: "/review/prompt.md", Workspace: "/workspace", Role: r.Role, Index: r.Index})
	if err != nil {
		return result, err
	}
	var id [12]byte
	if _, err = rand.Read(id[:]); err != nil {
		return result, err
	}
	name := "reviewd-" + hex.EncodeToString(id[:])
	uid := strconv.Itoa(os.Getuid())
	gid := strconv.Itoa(os.Getgid())
	argv := []string{"run", "--rm", "--name", name, "--label", "reviewd.managed=true", "--init", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=256", "--memory=" + d.Memory, "--cpus=" + d.CPUs, "--user", uid + ":" + gid, "--network", r.Harness.Network, "--tmpfs", "/tmp:rw,nosuid,nodev,size=512m,mode=1777", "--tmpfs", "/home/reviewd:rw,nosuid,nodev,size=1g,mode=0700,uid=" + uid + ",gid=" + gid, "--tmpfs", "/workspace:rw,nosuid,nodev,size=2g,uid=" + uid + ",gid=" + gid, "--workdir", "/workspace", "--mount", "type=bind,src=" + r.Source + ",dst=/source,readonly", "--mount", "type=bind,src=" + r.Input + ",dst=/review,readonly", "--mount", "type=bind,src=" + r.Base + ",dst=/review/base,readonly", "--tmpfs", "/output:rw,nosuid,nodev,size=8m,mode=0700,uid=" + uid + ",gid=" + gid, "--mount", "type=bind,src=" + d.Binary + ",dst=/usr/local/bin/reviewd,readonly", "--env", "HOME=/home/reviewd", "--env", "REVIEWD_OUTPUT=/output", "--env", "REVIEWD_INPUT=/review"}
	if d.Owner != "" {
		argv = append(argv, "--label", "reviewd.owner="+d.Owner)
	}
	argv = append(argv, prepared.dockerArgs...)
	values, err := d.Credentials.Environment(ctx, r.Harness.Credentials)
	if err != nil {
		return result, err
	}
	env, exported := credential.Apply(os.Environ(), values)
	for _, key := range exported {
		argv = append(argv, "--env", key)
	}
	for _, key := range r.Harness.Env {
		if _, exists := values[key]; exists {
			return result, fmt.Errorf("duplicate credential environment %s", key)
		}
		if os.Getenv(key) == "" {
			return result, fmt.Errorf("harness requires environment variable %s", key)
		}
		argv = append(argv, "--env", key)
	}
	// This shell program is fixed. All operator arguments and prompt text pass as
	// positional arguments, never interpolated as shell source. Harness output goes
	// to stderr; only the reporting CLI exports JSON on stdout.
	setup := `mkdir -p /home/reviewd && cp -a /source/. /workspace/ && rm -rf /workspace/AGENTS.md /workspace/agents.md && cp /review/AGENTS.md /workspace/AGENTS.md && cp /review/AGENTS.md /workspace/agents.md && "$@" >&2 && /usr/local/bin/reviewd agent export`
	if prepared.socketDir != "" {
		setup = `/usr/local/bin/reviewd agent model-relay >&2 & ` + setup
	}
	argv = append(argv, "--entrypoint", "/bin/sh", r.Harness.Image, "-c", setup, "reviewd-harness")
	argv = append(argv, args...)
	log, err := os.OpenFile(filepath.Join(r.Output, "harness.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return result, err
	}
	defer log.Close()
	sink := &limitedLog{log, 8 << 20}
	cmd := exec.CommandContext(ctx, "docker", argv...)
	cmd.Env = env
	transport := &reportBuffer{}
	cmd.Stdout = transport
	cmd.Stderr = sink
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
	}()
	if err = cmd.Run(); err != nil {
		return result, fmt.Errorf("%s harness failed: %w (see harness.log)", r.Role, err)
	}
	if transport.overflow {
		return result, fmt.Errorf("report transport exceeds 1 MiB")
	}
	if err = config.Decode(transport.Bytes(), &result); err != nil {
		return result, fmt.Errorf("harness did not export a valid report: %w", err)
	}
	if err = result.Validate(); err != nil {
		return result, err
	}
	if err = store.WriteJSON(filepath.Join(r.Output, "report.json"), result); err != nil {
		return result, err
	}
	return result, nil
}

// Drain stdout without letting an untrusted process allocate unbounded memory.
type reportBuffer struct {
	buffer   bytes.Buffer
	overflow bool
}

func (b *reportBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *reportBuffer) Write(p []byte) (int, error) {
	n := len(p)
	left := (1 << 20) - b.buffer.Len()
	if len(p) > left {
		b.overflow = true
		p = p[:left]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

// Cleanup removes orphan containers from this data directory before recovery.
// The daemon lock must be held. Other reviewd installations are untouched.
func (d Docker) Cleanup(ctx context.Context) error {
	if d.Owner == "" {
		return fmt.Errorf("container owner is required for cleanup")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=reviewd.owner="+d.Owner).Output()
	if err != nil {
		return fmt.Errorf("list orphan containers: %w", err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	return exec.CommandContext(ctx, "docker", append([]string{"rm", "-f"}, ids...)...).Run()
}
