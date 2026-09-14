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
	runner := measuredRunner{inner: inner, strategy: "sharded", total: 3}
	for i := 0; i < 3; i++ {
		dir := t.TempDir()
		if err := store.WriteJSON(filepath.Join(dir, "files.json"), files); err != nil {
			t.Fatal(err)
		}
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
