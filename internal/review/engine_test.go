package review

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jklq/reviewd/internal/config"
	"github.com/jklq/reviewd/internal/report"
	"github.com/jklq/reviewd/internal/sandbox"
)

type fakeRunner struct {
	mu                                 sync.Mutex
	active, max, reviewers, validators int
	fail                               bool
}

func (f *fakeRunner) Run(ctx context.Context, r sandbox.Request) (report.Report, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.max {
		f.max = f.active
	}
	if r.Role == "reviewer" {
		f.reviewers++
	} else {
		f.validators++
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
	if f.fail && r.Role == "reviewer" {
		return report.Report{}, errors.New("harness failed")
	}
	if r.Role == "reviewer" {
		time.Sleep(30 * time.Millisecond)
	} else {
		b, err := os.ReadFile(filepath.Join(r.Input, "candidates.json"))
		if err != nil || !strings.Contains(string(b), "Change intent") {
			return report.Report{}, errors.New("missing candidates")
		}
	}
	n := 4
	return report.Report{Summary: "Change intent", Confidence: &n, Reasons: []string{"Live dependency not tested"}, SequenceDiagram: "sequenceDiagram\n A->>B: Change"}, nil
}

// scriptedRunner records every role/harness attempt and fails or degrades the
// report for named pairs, so tests can stage provider outages.
type scriptedRunner struct {
	mu      sync.Mutex
	calls   []string
	fail    map[string]error
	invalid map[string]bool
}

func (s *scriptedRunner) Run(_ context.Context, r sandbox.Request) (report.Report, error) {
	key := r.Role + "/" + r.Harness.Model
	s.mu.Lock()
	s.calls = append(s.calls, key)
	s.mu.Unlock()
	if err := s.fail[key]; err != nil {
		return report.Report{}, err
	}
	confidence := 4
	result := report.Report{Summary: "Change intent", Confidence: &confidence, Reasons: []string{"Live dependency not tested"}, SequenceDiagram: "sequenceDiagram\n A->>B: Change"}
	if !s.invalid[key] {
		result.ImportantFiles = []report.File{{Path: "a.go", Description: "Changed file."}}
	}
	return result, nil
}

func (s *scriptedRunner) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.calls...)
}

var changedFiles = []report.ChangedFile{{Filename: "a.go", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new", Additions: 1, Deletions: 1}}

// fallbackConfig selects codex with a "backup" harness as its fallback.
func fallbackConfig(t *testing.T, parallelism int) config.Config {
	t.Helper()
	c := config.Default()
	c.SizeTiers = nil // exercise top-level reviewer/validator/parallelism selection
	c.Parallelism = parallelism
	backup := c.Harnesses["codex"]
	backup.Model = "backup-model"
	c.Harnesses["backup"] = backup
	c.Fallbacks = map[string][]string{"codex": {"backup"}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestReviewerFallbackAfterHarnessFailure(t *testing.T) {
	c := fallbackConfig(t, 1)
	codex := "reviewer/" + c.Harnesses["codex"].Model
	runner := &scriptedRunner{fail: map[string]error{codex: errors.New("provider unavailable")}}
	dir := t.TempDir()
	r, err := (Engine{c, runner}).Run(context.Background(), dir, Context{}, changedFiles)
	if err != nil {
		t.Fatal(err)
	}
	if r.Harness != "backup" || r.Model != "backup-model" {
		t.Fatalf("fallback attribution: %+v", r)
	}
	if got := runner.called(); !reflect.DeepEqual(got, []string{codex, "reviewer/backup-model"}) {
		t.Fatalf("attempts: %v", got)
	}
	// Both attempts keep their inputs, so the failed provider's context remains.
	for slot, want := range map[string]string{"reviewer-0": "codex", "reviewer-0-fallback-backup": "backup"} {
		b, err := os.ReadFile(filepath.Join(dir, slot, "input", "context.json"))
		if err != nil {
			t.Fatal(err)
		}
		var rc Context
		if err = json.Unmarshal(b, &rc); err != nil {
			t.Fatal(err)
		}
		if rc.Harness != want {
			t.Fatalf("%s context harness: %+v", slot, rc)
		}
	}
}

func TestReviewerFallbackAfterInvalidReport(t *testing.T) {
	c := fallbackConfig(t, 1)
	codex := "reviewer/" + c.Harnesses["codex"].Model
	runner := &scriptedRunner{invalid: map[string]bool{codex: true}}
	r, err := (Engine{c, runner}).Run(context.Background(), t.TempDir(), Context{}, changedFiles)
	if err != nil {
		t.Fatal(err)
	}
	if r.Harness != "backup" || !reflect.DeepEqual(runner.called(), []string{codex, "reviewer/backup-model"}) {
		t.Fatalf("invalid report did not fall back: %+v %v", r, runner.called())
	}
}

func TestFallbackExhaustionFailsSlot(t *testing.T) {
	c := fallbackConfig(t, 2)
	codex := "reviewer/" + c.Harnesses["codex"].Model
	runner := &scriptedRunner{fail: map[string]error{codex: errors.New("provider unavailable"), "reviewer/backup-model": errors.New("backup unavailable")}}
	_, err := (Engine{c, runner}).Run(context.Background(), t.TempDir(), Context{}, changedFiles)
	if err == nil || !strings.Contains(err.Error(), "codex: provider unavailable") || !strings.Contains(err.Error(), "backup: backup unavailable") {
		t.Fatalf("exhausted fallbacks not reported: %v", err)
	}
	// Both slots try codex first; goroutine scheduling makes only the counts
	// deterministic.
	got := runner.called()
	codexAttempts, backupAttempts := 0, 0
	for _, call := range got {
		switch call {
		case codex:
			codexAttempts++
		case "reviewer/backup-model":
			backupAttempts++
		default:
			t.Fatalf("unexpected attempt %q in %v", call, got)
		}
	}
	if len(got) != 4 || codexAttempts != 2 || backupAttempts != 2 {
		t.Fatalf("exhausted attempts: %v", got)
	}
}

func TestValidatorFallback(t *testing.T) {
	c := fallbackConfig(t, 2)
	codex := "validator/" + c.Harnesses["codex"].Model
	runner := &scriptedRunner{fail: map[string]error{codex: errors.New("validator unavailable")}}
	r, err := (Engine{c, runner}).Run(context.Background(), t.TempDir(), Context{}, changedFiles)
	if err != nil {
		t.Fatal(err)
	}
	if r.Harness != "backup" || r.Model != "backup-model" {
		t.Fatalf("validator fallback attribution: %+v", r)
	}
	if got := runner.called(); !reflect.DeepEqual(got, []string{"reviewer/" + c.Harnesses["codex"].Model, "reviewer/" + c.Harnesses["codex"].Model, codex, "validator/backup-model"}) {
		t.Fatalf("validator attempts: %v", got)
	}
}

func TestFallbackStopsWhenDeadlineExhausted(t *testing.T) {
	c := fallbackConfig(t, 1)
	codex := "reviewer/" + c.Harnesses["codex"].Model
	runner := &scriptedRunner{fail: map[string]error{codex: errors.New("provider unavailable")}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Engine{c, runner}).Run(ctx, t.TempDir(), Context{}, changedFiles); err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("deadline error: %v", err)
	}
	if got := runner.called(); !reflect.DeepEqual(got, []string{codex}) {
		t.Fatalf("fallback started after deadline: %v", got)
	}
}

func TestParallelReviewThenValidator(t *testing.T) {
	c := config.Default()
	c.SizeTiers = nil
	c.Parallelism = 3
	runner := &fakeRunner{}
	engine := Engine{c, runner}
	r, err := engine.Run(context.Background(), t.TempDir(), Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runner.max != 3 || runner.reviewers != 3 || runner.validators != 1 || *r.Confidence != 4 {
		t.Fatalf("unexpected fanout: %+v", runner)
	}
	if r.Harness != c.Validator || r.Model != c.Harnesses[c.Validator].Model {
		t.Fatalf("unattributed review: %+v", r)
	}
}
func TestSingleReviewerSkipsValidator(t *testing.T) {
	c := config.Default()
	c.Parallelism = 1
	c.Harnesses["other"] = c.Harnesses[c.Validator]
	c.Validator = "other"
	runner := &fakeRunner{}
	engine := Engine{c, runner}
	r, err := engine.Run(context.Background(), t.TempDir(), Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runner.max != 1 || runner.reviewers != 1 || runner.validators != 0 {
		t.Fatalf("expected a single reviewer pass: %+v", runner)
	}
	if r.Harness != c.Reviewers[0] || r.Model != c.Harnesses[c.Reviewers[0]].Model {
		t.Fatalf("single pass must keep reviewer attribution: %+v", r)
	}
}
func TestWorkerFailureCannotBecomeCleanReview(t *testing.T) {
	c := config.Default()
	c.Parallelism = 2
	runner := &fakeRunner{fail: true}
	_, err := (Engine{c, runner}).Run(context.Background(), t.TempDir(), Context{}, nil)
	if err == nil || runner.validators != 0 {
		t.Fatal("failed reviewers were accepted")
	}
}

func TestSizeTierRoutesHarnesses(t *testing.T) {
	c := config.Default()
	c.Harnesses["fast"] = c.Harnesses["codex"]
	one := 1
	c.SizeTiers = []config.SizeTier{
		{Name: "small", MaxLines: 100, Reviewers: []string{"fast"}, Validator: "fast", Parallelism: &one},
		{Name: "large", Reviewers: []string{"codex"}, Validator: "codex"},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	engine := Engine{c, &fakeRunner{}}
	smallDir := t.TempDir()
	small, err := engine.Run(context.Background(), smallDir, Context{Additions: 80, Deletions: 10}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if small.Harness != "fast" {
		t.Fatalf("small PR validated by %q", small.Harness)
	}
	b, err := os.ReadFile(filepath.Join(smallDir, "reviewer-0", "input", "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rc Context
	if err = json.Unmarshal(b, &rc); err != nil {
		t.Fatal(err)
	}
	if rc.Harness != "fast" || rc.SizeTier != "small" {
		t.Fatalf("small PR reviewer context: %+v", rc)
	}
	large, err := engine.Run(context.Background(), t.TempDir(), Context{Additions: 800, Deletions: 200}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if large.Harness != "codex" {
		t.Fatalf("large PR validated by %q", large.Harness)
	}
}

func TestChangedLFSAndSubmoduleCoverage(t *testing.T) {
	head, base := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(head, ".gitmodules"), []byte("unrelated submodule"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(head, "code.go"), []byte("package code"), 0600); err != nil {
		t.Fatal(err)
	}
	g, err := SnapshotCoverage(head, base, []report.ChangedFile{{Filename: "code.go"}})
	if err != nil || len(g) != 0 {
		t.Fatal(g, err)
	}
	if err := os.WriteFile(filepath.Join(head, "asset"), []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc"), 0600); err != nil {
		t.Fatal(err)
	}
	g, err = SnapshotCoverage(head, base, []report.ChangedFile{{Filename: "asset"}, {Filename: "submodule"}})
	if err != nil || len(g) != 2 {
		t.Fatal(g, err)
	}
}

func TestLongCoverageStillPublishesValidUncertainReport(t *testing.T) {
	r, err := (Engine{config.Default(), &fakeRunner{}}).Run(context.Background(), t.TempDir(), Context{Coverage: []string{strings.Repeat("界", 1000)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Validate(); err != nil {
		t.Fatal(err)
	}
	if r.MergeReady() || *r.Confidence != 3 || !strings.Contains(r.Reasons[0], "coverage limitations") {
		t.Fatal(r)
	}
}
