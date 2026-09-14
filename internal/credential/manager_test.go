package credential

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"reviewd/internal/config"
	"reviewd/internal/store"
)

func definition(t *testing.T, state string) config.Credential {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return config.Credential{StateFile: state, Command: []string{binary, "-test.run=^TestRefreshHelper$"}, Env: []string{"REFRESH_TEST_MODE"}, Exports: []string{"HARNESS_AUTH"}}
}
func manager(t *testing.T, state string) *Manager {
	return &Manager{Definitions: map[string]config.Credential{"shared": definition(t, state)}, MinValidity: time.Minute}
}
func TestRefreshHelper(t *testing.T) {
	mode := os.Getenv("REFRESH_TEST_MODE")
	if mode == "" || os.Getenv("REVIEWD_CREDENTIAL_STATE") == "" {
		return
	}
	os.Exit(refreshHelper(mode))
}
func refreshHelper(mode string) int {
	state := os.Getenv("REVIEWD_CREDENTIAL_STATE")
	guard, err := os.OpenFile(state+".refreshing", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 9
	}
	guard.Close()
	defer os.Remove(state + ".refreshing")
	switch mode {
	case "fail":
		fmt.Fprint(os.Stderr, "private-refresh-secret")
		return 1
	case "malformed":
		fmt.Print(`{"private-refresh-secret":`)
		return 0
	case "oversized":
		fmt.Print(strings.Repeat("x", maxBytes+1))
		return 0
	case "sleep":
		time.Sleep(20 * time.Second)
		return 1
	}
	var generation int
	b, err := os.ReadFile(state)
	if err != nil || json.Unmarshal(b, &generation) != nil {
		return 2
	}
	time.Sleep(30 * time.Millisecond)
	generation++
	if err = store.WriteJSON(state, generation); err != nil {
		return 3
	}
	if mode == "rotate-fail" {
		return 1
	}
	expires := time.Now().Add(time.Hour)
	if mode == "short" {
		expires = time.Now().Add(time.Second)
	}
	env := map[string]string{"HARNESS_AUTH": fmt.Sprintf("access-%d", generation)}
	if mode == "extra" {
		env["REFRESH_SECRET"] = "private-refresh-secret"
	}
	json.NewEncoder(os.Stdout).Encode(Result{ExpiresAt: expires, Env: env})
	return 0
}

func TestCredentialEnvNamesAreOptional(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	if err := store.WriteJSON(state, 0); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REFRESH_TEST_MODE", "normal")
	_ = os.Unsetenv("TEST_ABSENT_ENV")
	def := definition(t, state)
	def.Env = []string{"REFRESH_TEST_MODE", "TEST_ABSENT_ENV"}
	m := &Manager{Definitions: map[string]config.Credential{"shared": def}, MinValidity: time.Minute}
	if _, err := m.Environment(context.Background(), []string{"shared"}); err != nil {
		t.Fatal(err)
	}
}

func TestParallelRefreshAndDurableCache(t *testing.T) {
	t.Setenv("REFRESH_TEST_MODE", "normal")
	state := filepath.Join(t.TempDir(), "state.json")
	if err := store.WriteJSON(state, 0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env, err := manager(t, state).Environment(context.Background(), []string{"shared"})
			if err != nil {
				t.Error(err)
				return
			}
			if env["HARNESS_AUTH"] != "access-1" {
				t.Error("parallel refresh rotated twice")
			}
		}()
	}
	wg.Wait()
	// Another manager (including after restart) reuses the saved result.
	env, err := manager(t, state).Environment(context.Background(), []string{"shared"})
	if err != nil || env["HARNESS_AUTH"] != "access-1" {
		t.Fatal(env, err)
	}
	files, _ := filepath.Glob(state + ".reviewd-cache-*.json")
	if len(files) != 1 {
		t.Fatal(files)
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("cache must be private", err)
	}
	// Expiring a cached access token triggers exactly one new rotation.
	b, _ := os.ReadFile(files[0])
	var cached cache
	json.Unmarshal(b, &cached)
	cached.Result.ExpiresAt = time.Now().Add(-time.Minute)
	store.WriteJSON(files[0], cached)
	env, err = manager(t, state).Environment(context.Background(), []string{"shared"})
	if err != nil || env["HARNESS_AUTH"] != "access-2" {
		t.Fatal(env, err)
	}
	// An operator login replacement invalidates even an unexpired cache.
	store.WriteJSON(state, 40)
	env, err = manager(t, state).Environment(context.Background(), []string{"shared"})
	if err != nil || env["HARNESS_AUTH"] != "access-41" {
		t.Fatal(env, err)
	}
}

func TestResolveProcess(t *testing.T) {
	state := os.Getenv("RESOLVE_TEST_STATE")
	if state == "" {
		return
	}
	env, err := manager(t, state).Environment(context.Background(), []string{"shared"})
	if err != nil || env["HARNESS_AUTH"] != "access-1" {
		t.Fatal("process refresh failed", err)
	}
}
func TestRefreshLockAcrossProcessesAndAliases(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	if err := store.WriteJSON(state, 0); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias.json")
	if err := os.Symlink(state, alias); err != nil {
		t.Fatal(err)
	}
	binary, _ := os.Executable()
	var wg sync.WaitGroup
	for _, path := range []string{state, alias, state, alias} {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			cmd := exec.Command(binary, "-test.run=^TestResolveProcess$")
			cmd.Env = append(os.Environ(), "RESOLVE_TEST_STATE="+path, "REFRESH_TEST_MODE=normal")
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%v: %s", err, b)
			}
		}(path)
	}
	wg.Wait()
	b, _ := os.ReadFile(state)
	if strings.TrimSpace(string(b)) != "1" {
		t.Fatalf("refresh raced: %s", b)
	}
}

func TestRefreshFailuresNeverReturnStaleOrSecretOutput(t *testing.T) {
	for _, mode := range []string{"fail", "malformed", "oversized", "short", "extra", "rotate-fail"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("REFRESH_TEST_MODE", mode)
			state := filepath.Join(t.TempDir(), "state.json")
			store.WriteJSON(state, 0)
			env, err := manager(t, state).Environment(context.Background(), []string{"shared"})
			if err == nil || env != nil || strings.Contains(err.Error(), "private-refresh-secret") {
				t.Fatalf("unsafe failure: %v", err)
			}
			t.Setenv("REFRESH_TEST_MODE", "normal")
			env, err = manager(t, state).Environment(context.Background(), []string{"shared"})
			if err != nil || env["HARNESS_AUTH"] == "" {
				t.Fatal("could not recover", err)
			}
			if mode == "rotate-fail" && env["HARNESS_AUTH"] != "access-2" {
				t.Fatal("lost rotated state")
			}
		})
	}
}

func TestCancellationWhileWaitingAndRefreshing(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	store.WriteJSON(state, 0)
	t.Setenv("REFRESH_TEST_MODE", "normal")
	unlock, err := lock(context.Background(), state+".reviewd.lock")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err = manager(t, state).Environment(ctx, []string{"shared"})
	cancel()
	unlock()
	if err == nil {
		t.Fatal("lock wait ignored cancellation")
	}
	t.Setenv("REFRESH_TEST_MODE", "sleep")
	ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = manager(t, state).Environment(ctx, []string{"shared"})
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("refresh ignored cancellation", err)
	}
	unlock, err = lock(context.Background(), state+".reviewd.lock")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestEnvironmentReplacement(t *testing.T) {
	env, keys := Apply([]string{"A=old", "A=older", "B=keep"}, map[string]string{"A": "new"})
	if strings.Join(env, ",") != "B=keep,A=new" || len(keys) != 1 || keys[0] != "A" {
		t.Fatal(env, keys)
	}
}
