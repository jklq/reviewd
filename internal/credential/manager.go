// Package credential coordinates provider-neutral refresh commands. Provider
// protocols and credential formats belong to operator configuration, not Go.
package credential

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"syscall"
	"time"

	"reviewd/internal/config"
	"reviewd/internal/store"
)

const maxBytes = 1 << 20

type Result struct {
	ExpiresAt time.Time         `json:"expires_at"`
	Env       map[string]string `json:"env"`
}
type cache struct {
	StateHash string `json:"state_hash"`
	Result    Result `json:"result"`
}
type Manager struct {
	Definitions      map[string]config.Credential
	MinValidity      time.Duration
	WebhookSecretEnv string
}

func New(c config.Config) *Manager {
	timeout, _ := time.ParseDuration(c.Timeout)
	return &Manager{Definitions: c.Credentials, MinValidity: timeout + time.Minute, WebhookSecretEnv: c.WebhookSecretEnv}
}

// Environment never exposes state files to a harness. All consumers of a login
// coordinate through a lock beside its canonical state file, across processes
// and data directories. Only refresh is serialized; review execution is parallel.
func (m *Manager) Environment(ctx context.Context, names []string) (map[string]string, error) {
	env := map[string]string{}
	for _, name := range names {
		if m == nil {
			return nil, errors.New("credential manager is not configured")
		}
		r, err := m.Resolve(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("credential %s: %w", name, err)
		}
		for key, value := range r.Env {
			if _, exists := env[key]; exists {
				return nil, errors.New("duplicate credential export")
			}
			env[key] = value
		}
	}
	return env, nil
}

func (m *Manager) Resolve(ctx context.Context, name string) (Result, error) {
	var result Result
	c, ok := m.Definitions[name]
	if !ok {
		return result, errors.New("unknown credential")
	}
	state, err := filepath.EvalSymlinks(c.StateFile)
	if err != nil {
		return result, fmt.Errorf("locate state file: %w", err)
	}
	state, err = filepath.Abs(state)
	if err != nil {
		return result, err
	}
	c.StateFile = state
	unlock, err := lock(ctx, state+".reviewd.lock")
	if err != nil {
		return result, err
	}
	defer unlock()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	hash, err := stateHash(state)
	if err != nil {
		return result, err
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "REVIEWD_CREDENTIAL_STATE=" + state, "REVIEWD_CREDENTIAL_MIN_VALIDITY=" + strconv.FormatInt(int64((m.MinValidity+time.Second-1)/time.Second), 10)}
	for _, key := range c.Env {
		if !config.AllowedEnv(key, m.WebhookSecretEnv) {
			return result, errors.New("forbidden refresh environment")
		}
		// Listed names are an allowlist of ambient variables (proxy, CA, and the
		// like), so they are forwarded only when the service has them set.
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	identity, _ := json.Marshal(struct {
		Definition  config.Credential
		Environment []string
	}{c, env})
	fingerprint := sha256.Sum256(identity)
	cachePath := state + ".reviewd-cache-" + hex.EncodeToString(fingerprint[:16]) + ".json"
	var cached cache
	if b, err := readLimited(cachePath); err == nil && config.Decode(b, &cached) == nil && cached.StateHash == hash && m.valid(c, cached.Result) == nil {
		return cached.Result, nil
	} else if err != nil && !os.IsNotExist(err) {
		return result, fmt.Errorf("read credential cache: %w", err)
	}
	// The refresh command must persist rotation in the state file before reporting
	// success. Its stdout is a secret channel, never attached to a job log.
	b, err := run(ctx, c.Command, env, filepath.Dir(state))
	if err != nil {
		return result, err
	}
	if err = config.Decode(b, &result); err != nil {
		return Result{}, errors.New("refresh command returned invalid JSON")
	}
	if err = m.valid(c, result); err != nil {
		return Result{}, err
	}
	hash, err = stateHash(state)
	if err != nil {
		return Result{}, err
	}
	if err = store.WriteJSON(cachePath, cache{StateHash: hash, Result: result}); err != nil {
		return Result{}, fmt.Errorf("persist credential cache: %w", err)
	}
	return result, nil
}

func (m *Manager) valid(c config.Credential, r Result) error {
	if !r.ExpiresAt.After(time.Now().Add(m.MinValidity)) {
		return errors.New("refreshed credentials must outlive the job timeout plus one minute")
	}
	if len(r.Env) != len(c.Exports) {
		return errors.New("refresh exports do not match configuration")
	}
	for _, key := range c.Exports {
		value, ok := r.Env[key]
		if !config.AllowedEnv(key, m.WebhookSecretEnv) || !ok || value == "" || strings.ContainsRune(value, 0) {
			return errors.New("invalid credential export")
		}
	}
	return nil
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBytes {
		return nil, errors.New("credential file exceeds 1 MiB")
	}
	return b, nil
}
func stateHash(path string) (string, error) {
	b, err := readLimited(path)
	if err != nil {
		return "", fmt.Errorf("read credential state: %w", err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func lock(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxBytes {
		return 0, errors.New("refresh output exceeds 1 MiB")
	}
	return b.Buffer.Write(p)
}
func run(ctx context.Context, args, env []string, dir string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("refresh command is missing")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var output boundedOutput
	cmd.Stdout = &output
	// Stderr may contain credentials. Discard it rather than writing harness logs.
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, errors.New("refresh command failed (output withheld)")
	}
	return output.Bytes(), nil
}

// Apply builds the Docker client's environment without duplicate keys. Values
// stay in process environment, never command arguments or persisted job metadata.
func Apply(base []string, values map[string]string) ([]string, []string) {
	env := make([]string, 0, len(base)+len(values))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := values[key]; !ok {
			env = append(env, entry)
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env, keys
}
