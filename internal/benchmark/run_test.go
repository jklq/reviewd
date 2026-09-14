package benchmark

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reviewd/internal/report"
	"reviewd/internal/sandbox"
	"reviewd/internal/store"
)

type capture struct{ calls []sandbox.Request }

func (c *capture) Run(_ context.Context, r sandbox.Request) (report.Report, error) {
	c.calls = append(c.calls, r)
	return report.Report{}, nil
}
func TestSinglePassDoesNotLaunchValidator(t *testing.T) {
	dir := t.TempDir()
	n := 5
	want := report.Report{Summary: "unchanged", Confidence: &n}
	if err := store.WriteJSON(filepath.Join(dir, "candidates.json"), []report.Report{want}); err != nil {
		t.Fatal(err)
	}
	inner := &capture{}
	runner := measuredRunner{inner: inner, strategy: "single"}
	got, err := runner.Run(context.Background(), sandbox.Request{Input: dir, Role: "validator"})
	if err != nil || got.Summary != want.Summary || len(inner.calls) != 0 {
		t.Fatalf("got %+v, %v, calls=%d", got, err, len(inner.calls))
	}
}
func TestShardsCoverEveryFileExactlyOnce(t *testing.T) {
	files := []report.ChangedFile{{Filename: "a.go"}, {Filename: "b.go"}, {Filename: "c.go"}, {Filename: "d.go"}}
	seen := map[string]int{}
	inner := &capture{}
	runner := measuredRunner{inner: inner, strategy: "sharded-3", total: 3, shardFiles: AssignShards(files, 3), shardPacks: make([]string, 3)}
	for i := 0; i < 3; i++ {
		dir := t.TempDir()
		if _, err := runner.Run(context.Background(), sandbox.Request{Input: dir, Role: "reviewer", Index: i}); err != nil {
			t.Fatal(err)
		}
		var assigned []report.ChangedFile
		if err := readJSON(filepath.Join(dir, "files.json"), &assigned); err != nil {
			t.Fatal(err)
		}
		for _, f := range assigned {
			seen[f.Filename]++
		}
	}
	for _, f := range files {
		if seen[f.Filename] != 1 {
			t.Fatalf("%s covered %d times", f.Filename, seen[f.Filename])
		}
	}
}
func TestParseShardsGrowsWithChangedLines(t *testing.T) {
	cases := []struct {
		strategy string
		lines    int
		files    int
		want     int
	}{
		{"sharded", 51, 45, 3},
		{"sharded", 1000, 2, 2},
		{"sharded-1", 4000, 50, 1},
		{"sharded-3", 10, 2, 2},
		{"sharded-adaptive", 1, 1, 1},
		{"sharded-adaptive", 99, 12, 1},
		{"sharded-adaptive", 127, 13, 2},
		{"sharded-adaptive", 139, 14, 2},
		{"sharded-adaptive", 249, 10, 2},
		{"sharded-adaptive", 1503, 23, 4},
		{"sharded-adaptive", 4432, 51, 6},
		{"sharded-adaptive", 4432, 3, 3},
	}
	for _, c := range cases {
		got, err := parseShards(c.strategy, c.lines, c.files)
		if err != nil || got != c.want {
			t.Errorf("%s lines=%d files=%d: got %d, %v; want %d", c.strategy, c.lines, c.files, got, err, c.want)
		}
	}
	if _, err := parseShards("sharded-9", 10, 10); err == nil {
		t.Fatal("sharded-9 must be rejected")
	}
	if _, err := parseShards("sharded-bogus", 10, 10); err == nil {
		t.Fatal("sharded-bogus must be rejected")
	}
}
func TestAssignShardsBalancesAndPrefersSameDirectory(t *testing.T) {
	files := []report.ChangedFile{
		{Filename: "a/x.go", Additions: 100},
		{Filename: "a/y.go", Additions: 90},
		{Filename: "b/z.go", Additions: 10},
		{Filename: "b/w.go", Additions: 10},
	}
	out := AssignShards(files, 2)
	if len(out) != 2 {
		t.Fatalf("shards=%d", len(out))
	}
	seen := map[string]int{}
	loads := make([]int, 2)
	for i, slice := range out {
		for _, f := range slice {
			seen[f.Filename]++
			loads[i] += f.Additions + f.Deletions
		}
	}
	for _, f := range files {
		if seen[f.Filename] != 1 {
			t.Fatalf("%s covered %d times", f.Filename, seen[f.Filename])
		}
	}
	if loads[0] != 100 || loads[1] != 110 {
		t.Fatalf("unbalanced loads %v", loads)
	}
	if len(out[0]) != 1 || out[0][0].Filename != "a/x.go" {
		t.Fatalf("unexpected first slice %+v", out[0])
	}
	for _, f := range out[1] {
		if f.Filename == "a/x.go" {
			t.Fatal("largest change must anchor the first slice")
		}
	}
}
func TestAssignShardsCapsAtFileCount(t *testing.T) {
	out := AssignShards([]report.ChangedFile{{Filename: "a.go", Additions: 1}}, 6)
	if len(out) != 1 || len(out[0]) != 1 {
		t.Fatalf("unexpected %+v", out)
	}
}
func TestContextIncludesASTCallersAndBoundsOutput(t *testing.T) {
	dir := t.TempDir()
	for name, source := range map[string]string{"a.go": "package example\nfunc Changed() {}\n", "b.go": "package example\nfunc Caller(){ Changed() }\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pack := ContextPack(dir, []report.ChangedFile{{Filename: "a.go", Patch: "@@ -1 +1 @@\n-old\n+new"}}, true)
	if !strings.Contains(pack, "call b.go:2 Changed") || !strings.Contains(pack, "declaration a.go:2 Changed") {
		t.Fatal(pack)
	}
	pack = ContextPack(dir, []report.ChangedFile{{Filename: "a.go", Patch: strings.Repeat("x", 100000)}}, false)
	if len(pack) > 64200 || !strings.Contains(pack, "TRUNCATED") {
		t.Fatalf("length=%d", len(pack))
	}
}
func TestBudgetForSizeTiers(t *testing.T) {
	cases := []struct {
		lines   int
		tier    string
		steps   int
		timeout time.Duration
	}{
		{0, "small", 8, 3 * time.Minute},
		{99, "small", 8, 3 * time.Minute},
		{100, "medium", 12, 6 * time.Minute},
		{499, "medium", 12, 6 * time.Minute},
		{500, "large", 16, 10 * time.Minute},
		{1999, "large", 16, 10 * time.Minute},
		{2000, "very-large", 24, 15 * time.Minute},
		{8000, "very-large", 24, 15 * time.Minute},
	}
	for _, c := range cases {
		tier, steps, timeout := BudgetForSize(c.lines)
		if tier != c.tier || steps != c.steps || timeout != c.timeout {
			t.Errorf("lines=%d: got %s/%d/%v", c.lines, tier, steps, timeout)
		}
	}
}

func TestConditionalValidationPreservesEmptyReportAndChecksFindings(t *testing.T) {
	for _, hasFinding := range []bool{false, true} {
		dir := t.TempDir()
		confidence := 3
		candidate := report.Report{Summary: "Coverage incomplete", Confidence: &confidence, Reasons: []string{"A dependency is absent."}}
		if hasFinding {
			candidate.Findings = []report.Finding{{Title: "Candidate defect"}}
		}
		if err := store.WriteJSON(filepath.Join(dir, "candidates.json"), []report.Report{candidate}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(report.Instructions), 0600); err != nil {
			t.Fatal(err)
		}
		inner := &capture{}
		runner := measuredRunner{inner: inner, strategy: "conditional"}
		got, err := runner.Run(context.Background(), sandbox.Request{Input: dir, Role: "validator"})
		if err != nil {
			t.Fatal(err)
		}
		if !hasFinding {
			if len(inner.calls) != 0 || got.Summary != candidate.Summary || *got.Confidence != 3 || len(got.Reasons) != 1 {
				t.Fatalf("empty report changed: %+v", got)
			}
		} else {
			if len(inner.calls) != 1 || !strings.Contains(inner.calls[0].Prompt, "Validate only candidate") {
				t.Fatal("candidate validation omitted")
			}
			protocol, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(protocol), "review core paths\nmissed by the workers") {
				t.Fatal("full-discovery instruction retained")
			}
		}
	}
}
