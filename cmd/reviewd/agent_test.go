package main

import (
	"os"
	"path/filepath"
	"testing"

	"reviewd/internal/report"
	"reviewd/internal/store"
)

func TestAgentSubmissionValidatesMetadataAndDiff(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REVIEWD_OUTPUT", dir)
	input := t.TempDir()
	t.Setenv("REVIEWD_INPUT", input)
	if err := store.WriteJSON(filepath.Join(input, "files.json"), []report.ChangedFile{{Filename: "a.go", Patch: "@@ -1 +1 @@\n-old\n+new"}}); err != nil {
		t.Fatal(err)
	}
	if err := agent([]string{"init"}); err != nil {
		t.Fatal(err)
	}
	if err := agent([]string{"submit"}); err == nil {
		t.Fatal("empty report submitted")
	}
	n := 4
	r := report.Report{Summary: "Change behavior", Confidence: &n, Reasons: []string{"Integration tests unavailable"}, ImportantFiles: []report.File{{Path: "a.go", Description: "Changes behavior"}}, SequenceDiagram: "sequenceDiagram\n A->>B: Call"}
	file := filepath.Join(dir, "full.json")
	if err := store.WriteJSON(file, r); err != nil {
		t.Fatal(err)
	}
	if err := agent([]string{"submit", "--file", file}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "report.json")); err != nil {
		t.Fatal(err)
	}
	r.Findings = []report.Finding{{Path: "a.go", Line: 88, Side: "RIGHT", Priority: 3, Confidence: .9, Title: "Fix it", Body: "Actual behavior", Evidence: "Concrete trigger"}}
	if err := store.WriteJSON(file, r); err != nil {
		t.Fatal(err)
	}
	if err := agent([]string{"submit", "--file", file}); err == nil {
		t.Fatal("out-of-diff finding accepted")
	}
}
