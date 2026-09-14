package review

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"reviewd/internal/config"
	"reviewd/internal/report"
	"reviewd/internal/sandbox"
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
func TestParallelReviewThenValidator(t *testing.T) {
	c := config.Default()
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
