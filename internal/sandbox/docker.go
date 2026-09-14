// Package sandbox runs operator-defined harnesses in disposable Docker containers.
package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"reviewd/internal/config"
	"reviewd/internal/credential"
	"reviewd/internal/report"
	"reviewd/internal/store"
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
	Egress                      *config.Egress
	ConfigFile                  string
}

var ErrEgressLeak = errors.New("egress sentinel found in harness output; report withheld")

const sentinelPrefix = "reviewd-sentinel-"
const egressProxyAddr = "http://reviewd-egress:8080"
const egressCAPath = "/review/egress-ca.pem"

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
	args, err := config.Expand(r.Harness.Command, config.Variables{Prompt: r.Prompt, PromptFile: "/review/prompt.md", Workspace: "/workspace", Role: r.Role, Index: r.Index})
	if err != nil {
		return result, err
	}
	var id [12]byte
	if _, err = rand.Read(id[:]); err != nil {
		return result, err
	}
	name := "reviewd-" + hex.EncodeToString(id[:])
	values, err := d.Credentials.Environment(ctx, r.Harness.Credentials)
	if err != nil {
		return result, err
	}
	if r.Harness.Egress {
		return d.runEgress(ctx, r, args, values, name)
	}
	uid := strconv.Itoa(os.Getuid())
	gid := strconv.Itoa(os.Getgid())
	p, err := planDockerRun(planRequest{
		harness: r.Harness,
		args:    args, uid: uid, gid: gid,
		harnessName: name,
		memory:      d.Memory, cpus: d.CPUs, owner: d.Owner, binary: d.Binary,
		source: r.Source, base: r.Base, input: r.Input,
		values: values, environ: os.Environ(),
	})
	if err != nil {
		return result, err
	}
	return execHarness(ctx, r, p.harnessArgv, p.harnessEnv, name, nil)
}

func (d Docker) runEgress(ctx context.Context, r Request, args []string, values map[string]string, harnessName string) (report.Report, error) {
	var result report.Report
	if d.Egress == nil {
		return result, errors.New("harness requires egress but no egress block is configured")
	}
	if d.ConfigFile == "" {
		return result, errors.New("harness requires egress but no config file is configured")
	}
	if d.Egress.CAFile == "" {
		return result, errors.New("egress ca_file is required")
	}
	if err := os.WriteFile(filepath.Join(r.Input, "egress-ca.pem"), nil, 0600); err != nil {
		return result, err
	}
	names := make([]string, 0, len(values))
	for key := range values {
		names = append(names, key)
	}
	sort.Strings(names)
	sentinels, err := newSentinels(names)
	if err != nil {
		return result, err
	}
	var raw [6]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return result, err
	}
	proxyName := "reviewd-egress-" + hex.EncodeToString(raw[:])
	if _, err = rand.Read(raw[:]); err != nil {
		return result, err
	}
	network := "reviewd-job-" + hex.EncodeToString(raw[:])
	seenDirs := map[string]bool{}
	var stateDirs []string
	seenAmbient := map[string]bool{}
	var ambient []string
	if d.Credentials != nil {
		for _, cname := range r.Harness.Credentials {
			def, ok := d.Credentials.Definitions[cname]
			if !ok {
				continue
			}
			if dir := filepath.Dir(def.StateFile); !seenDirs[dir] {
				seenDirs[dir] = true
				stateDirs = append(stateDirs, dir)
			}
			for _, key := range def.Env {
				if !seenAmbient[key] {
					seenAmbient[key] = true
					ambient = append(ambient, key)
				}
			}
		}
	}
	sort.Strings(stateDirs)
	sort.Strings(ambient)
	uid := strconv.Itoa(os.Getuid())
	gid := strconv.Itoa(os.Getgid())
	p, err := planDockerRun(planRequest{
		harness: r.Harness, egress: d.Egress, configFile: d.ConfigFile,
		stateDirs: stateDirs, proxyAmbient: ambient,
		args: args, uid: uid, gid: gid,
		harnessName: harnessName, proxyName: proxyName, internalNetwork: network,
		memory: d.Memory, cpus: d.CPUs, owner: d.Owner, binary: d.Binary,
		source: r.Source, base: r.Base, input: r.Input,
		values: values, sentinels: sentinels, environ: os.Environ(),
	})
	if err != nil {
		return result, err
	}
	netArgs := []string{"network", "create", "--internal", "--label", "reviewd.managed=true"}
	if d.Owner != "" {
		netArgs = append(netArgs, "--label", "reviewd.owner="+d.Owner)
	}
	netArgs = append(netArgs, network)
	if out, err := exec.CommandContext(ctx, "docker", netArgs...).CombinedOutput(); err != nil {
		return result, fmt.Errorf("create egress network: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "docker", "network", "rm", network).Run()
	}()
	proxyCmd := exec.CommandContext(ctx, "docker", p.proxyArgv...)
	proxyCmd.Env = p.proxyEnv
	if out, err := proxyCmd.CombinedOutput(); err != nil {
		return result, fmt.Errorf("start egress proxy: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", proxyName).Run()
	}()
	if out, err := exec.CommandContext(ctx, "docker", "network", "connect", "--alias", "reviewd-egress", network, proxyName).CombinedOutput(); err != nil {
		return result, fmt.Errorf("connect egress proxy: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if err := waitProxyHealthy(ctx, proxyName); err != nil {
		return result, err
	}
	return execHarness(ctx, r, p.harnessArgv, p.harnessEnv, harnessName, sentinels)
}

func execHarness(ctx context.Context, r Request, argv, env []string, name string, sentinels map[string]string) (report.Report, error) {
	var result report.Report
	logPath := filepath.Join(r.Output, "harness.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
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
		if leakErr := checkSentinels(transport.Bytes(), logPath, sentinels, r.Role, r.Index); leakErr != nil {
			return result, leakErr
		}
		return result, fmt.Errorf("%s harness failed: %w (see harness.log)", r.Role, err)
	}
	if transport.overflow {
		return result, fmt.Errorf("report transport exceeds 1 MiB")
	}
	if err = checkSentinels(transport.Bytes(), logPath, sentinels, r.Role, r.Index); err != nil {
		return result, err
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

func newSentinels(names []string) (map[string]string, error) {
	sentinels := make(map[string]string, len(names))
	for _, name := range names {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, err
		}
		sentinels[name] = sentinelPrefix + hex.EncodeToString(raw[:])
	}
	return sentinels, nil
}

func checkSentinels(transcript []byte, logPath string, sentinels map[string]string, role string, index int) error {
	if len(sentinels) == 0 {
		return nil
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		return fmt.Errorf("read harness log for egress scan: %w", err)
	}
	for _, sentinel := range sentinels {
		if sentinel == "" {
			continue
		}
		if bytes.Contains(transcript, []byte(sentinel)) || bytes.Contains(logBytes, []byte(sentinel)) {
			return fmt.Errorf("%s %d: %w", role, index, ErrEgressLeak)
		}
	}
	return nil
}

func waitProxyHealthy(ctx context.Context, name string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		running, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}", name).Output()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("inspect egress proxy: %w", err)
		}
		if strings.TrimSpace(string(running)) != "true" {
			return fmt.Errorf("egress proxy exited before becoming healthy%s", proxyDiagnosis(ctx, name))
		}
		out, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", name).Output()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("inspect egress proxy: %w", err)
		}
		switch strings.TrimSpace(string(out)) {
		case "healthy":
			return nil
		case "none":
			return errors.New("egress proxy image has no healthcheck")
		case "unhealthy":
			return fmt.Errorf("egress proxy failed its healthcheck%s", proxyDiagnosis(ctx, name))
		}
		if time.Now().After(deadline) {
			return errors.New("egress proxy did not become healthy within 30s")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func proxyDiagnosis(ctx context.Context, name string) string {
	exit, _ := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.ExitCode}}", name).Output()
	logs, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", "5", name).CombinedOutput()
	detail := strings.TrimSpace("exit " + strings.TrimSpace(string(exit)) + " " + strings.TrimSpace(string(logs)))
	if detail == "exit 0" || detail == "exit" {
		return ""
	}
	if len(detail) > 500 {
		detail = detail[:500]
	}
	return ": " + detail
}

type planRequest struct {
	harness         config.Harness
	egress          *config.Egress
	configFile      string
	stateDirs       []string
	proxyAmbient    []string
	args            []string
	uid, gid        string
	harnessName     string
	proxyName       string
	internalNetwork string
	memory, cpus    string
	owner, binary   string
	source, base    string
	input           string
	values          map[string]string
	sentinels       map[string]string
	environ         []string
}

type planResult struct {
	harnessArgv []string
	harnessEnv  []string
	proxyArgv   []string
	proxyEnv    []string
}

func planDockerRun(p planRequest) (planResult, error) {
	var out planResult
	network := p.harness.Network
	egress := p.harness.Egress
	if egress {
		if p.egress == nil {
			return out, errors.New("harness requires egress but no egress block is configured")
		}
		if p.egress.CAFile == "" {
			return out, errors.New("egress ca_file is required")
		}
		if p.configFile == "" {
			return out, errors.New("harness requires egress but no config file is configured")
		}
		if p.internalNetwork == "" {
			return out, errors.New("harness requires egress but no internal network was created")
		}
		network = p.internalNetwork
	}
	argv := []string{"run", "--rm", "--name", p.harnessName, "--label", "reviewd.managed=true", "--init", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=256", "--memory=" + p.memory, "--cpus=" + p.cpus, "--user", p.uid + ":" + p.gid, "--network", network, "--tmpfs", "/tmp:rw,nosuid,nodev,size=512m,mode=1777", "--tmpfs", "/home/reviewd:rw,nosuid,nodev,size=1g,mode=0700,uid=" + p.uid + ",gid=" + p.gid, "--tmpfs", "/workspace:rw,nosuid,nodev,size=2g,uid=" + p.uid + ",gid=" + p.gid, "--workdir", "/workspace", "--mount", "type=bind,src=" + p.source + ",dst=/source,readonly", "--mount", "type=bind,src=" + p.input + ",dst=/review,readonly", "--mount", "type=bind,src=" + p.base + ",dst=/review/base,readonly", "--tmpfs", "/output:rw,nosuid,nodev,size=8m,mode=0700,uid=" + p.uid + ",gid=" + p.gid, "--mount", "type=bind,src=" + p.binary + ",dst=/usr/local/bin/reviewd,readonly"}
	if egress {
		argv = append(argv, "--mount", "type=bind,src="+p.egress.CAFile+",dst="+egressCAPath+",readonly")
	}
	argv = append(argv, "--env", "HOME=/home/reviewd", "--env", "REVIEWD_OUTPUT=/output", "--env", "REVIEWD_INPUT=/review")
	if p.owner != "" {
		argv = append(argv, "--label", "reviewd.owner="+p.owner)
	}
	keys := make([]string, 0, len(p.values))
	for key := range p.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if egress {
		for _, key := range keys {
			sentinel, ok := p.sentinels[key]
			if !ok || sentinel == "" {
				return out, fmt.Errorf("harness requires sentinel for credential export %s", key)
			}
			argv = append(argv, "--env", key+"="+sentinel)
		}
		out.harnessEnv = append([]string{}, p.environ...)
	} else {
		var exported []string
		out.harnessEnv, exported = credential.Apply(p.environ, p.values)
		for _, key := range exported {
			argv = append(argv, "--env", key)
		}
	}
	ambient := map[string]string{}
	for _, entry := range p.environ {
		key, value, _ := strings.Cut(entry, "=")
		ambient[key] = value
	}
	for _, key := range p.harness.Env {
		if _, exists := p.values[key]; exists {
			return out, fmt.Errorf("duplicate credential environment %s", key)
		}
		if ambient[key] == "" {
			return out, fmt.Errorf("harness requires environment variable %s", key)
		}
		argv = append(argv, "--env", key)
	}
	if egress {
		argv = append(argv, "--env", "HTTPS_PROXY="+egressProxyAddr, "--env", "HTTP_PROXY="+egressProxyAddr, "--env", "NO_PROXY=", "--env", "REVIEWD_EGRESS_CA="+egressCAPath)
	}
	// This shell program is fixed. All operator arguments and prompt text pass as
	// positional arguments, never interpolated as shell source. Harness output goes
	// to stderr; only the reporting CLI exports JSON on stdout.
	setup := `mkdir -p /home/reviewd && cp -a /source/. /workspace/ && rm -f /workspace/AGENTS.md /workspace/agents.md && cp /review/AGENTS.md /workspace/AGENTS.md && cp /review/AGENTS.md /workspace/agents.md && "$@" >&2 && /usr/local/bin/reviewd agent export`
	argv = append(argv, "--entrypoint", "/bin/sh", p.harness.Image, "-c", setup, "reviewd-harness")
	argv = append(argv, p.args...)
	out.harnessArgv = argv
	if egress {
		sentinelJSON, err := json.Marshal(p.sentinels)
		if err != nil {
			return out, err
		}
		proxy := []string{"run", "-d", "--rm", "--name", p.proxyName, "--label", "reviewd.managed=true", "--label", "reviewd.role=egress"}
		if p.owner != "" {
			proxy = append(proxy, "--label", "reviewd.owner="+p.owner)
		}
		proxy = append(proxy, "--user", p.uid+":"+p.gid, "--network", p.egress.Network)
		if p.egress.Network != "bridge" && p.egress.Network != "none" {
			proxy = append(proxy, "--network-alias", "reviewd-egress")
		}
		proxy = append(proxy, "--mount", "type=bind,src="+p.configFile+",dst="+p.configFile+",readonly")
		proxy = append(proxy, "--mount", "type=bind,src="+p.egress.CAFile+",dst="+p.egress.CAFile+",readonly")
		for _, dir := range p.stateDirs {
			proxy = append(proxy, "--mount", "type=bind,src="+dir+",dst="+dir)
		}
		for _, key := range keys {
			proxy = append(proxy, "--env", key)
		}
		proxy = append(proxy, "--env", "REVIEWD_SENTINELS="+string(sentinelJSON))
		for _, key := range p.proxyAmbient {
			if ambient[key] == "" {
				continue
			}
			proxy = append(proxy, "--env", key)
		}
		proxy = append(proxy, "--env", "PATH", "--env", "HOME")
		proxy = append(proxy, p.egress.Image)
		proxy = append(proxy, p.egress.Command...)
		out.proxyArgv = proxy
		out.proxyEnv, _ = credential.Apply(p.environ, p.values)
	}
	return out, nil
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
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=reviewd.role=egress", "--filter", "label=reviewd.owner="+d.Owner).Output()
	if err != nil {
		return fmt.Errorf("list orphan egress proxies: %w", err)
	}
	if ids := strings.Fields(string(out)); len(ids) > 0 {
		if err := exec.CommandContext(ctx, "docker", append([]string{"rm", "-f"}, ids...)...).Run(); err != nil {
			return fmt.Errorf("remove orphan egress proxies: %w", err)
		}
	}
	out, err = exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=reviewd.owner="+d.Owner).Output()
	if err != nil {
		return fmt.Errorf("list orphan containers: %w", err)
	}
	if ids := strings.Fields(string(out)); len(ids) > 0 {
		if err := exec.CommandContext(ctx, "docker", append([]string{"rm", "-f"}, ids...)...).Run(); err != nil {
			return fmt.Errorf("remove orphan containers: %w", err)
		}
	}
	out, err = exec.CommandContext(ctx, "docker", "network", "ls", "-q", "--filter", "label=reviewd.managed=true", "--filter", "label=reviewd.owner="+d.Owner).Output()
	if err != nil {
		return fmt.Errorf("list orphan networks: %w", err)
	}
	if ids := strings.Fields(string(out)); len(ids) == 0 {
		return nil
	} else if err := exec.CommandContext(ctx, "docker", append([]string{"network", "rm"}, ids...)...).Run(); err != nil {
		return fmt.Errorf("remove orphan networks: %w", err)
	}
	return nil
}
