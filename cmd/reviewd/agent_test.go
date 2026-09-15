package main

import (
	"path/filepath"
	"testing"

	"github.com/jklq/reviewd/internal/report"
	"github.com/jklq/reviewd/internal/store"
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
	r := report.Report{Summary: "Change behavior", Confidence: &n, Reasons: []string{"Integration tests unavailable"}, ImportantFiles: []report.File{{Path: "a.go", Description: "Changes behavior"}}, SequenceDiagram: "sequenceDiagram\n A->>B: Call", Harness: "codex", Model: "gpt-5.6-luna"}
	file := filepath.Join(dir, "full.json")
	if err := store.WriteJSON(file, r); err != nil {
		t.Fatal(err)
	}
	if err := agent([]string{"submit", "--file", file}); err != nil {
		t.Fatal(err)
	}
	var persisted report.Report
	if err := readJSON(filepath.Join(dir, "report.json"), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Harness != "codex" || persisted.Model != "gpt-5.6-luna" {
		t.Fatalf("model and harness not persisted: %+v", persisted)
	}
	r.Findings = []report.Finding{{Path: "a.go", Line: 88, Side: "RIGHT", Priority: 3, Confidence: .9, Title: "Fix it", Body: "Actual behavior", Evidence: "Concrete trigger"}}
	if err := store.WriteJSON(file, r); err != nil {
		t.Fatal(err)
	}
	if err := agent([]string{"submit", "--file", file}); err == nil {
		t.Fatal("out-of-diff finding accepted")
	}
}

func TestAgentOverviewRejectsBrokenMermaid(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REVIEWD_OUTPUT", dir)
	t.Setenv("REVIEWD_INPUT", "")
	if err := agent([]string{"init"}); err != nil {
		t.Fatal(err)
	}
	n := 4
	overview := filepath.Join(dir, "overview.json")
	r := report.Report{Summary: "Change behavior", Confidence: &n, Reasons: []string{"All good"}, ImportantFiles: []report.File{{Path: "a.go", Description: "Changes behavior"}}, SequenceDiagram: "sequenceDiagram\n A->>B Missing colon"}
	if err := store.WriteJSON(overview, r); err != nil {
		t.Fatal(err)
	}
	if err := agent([]string{"overview", "--file", overview}); err == nil {
		t.Fatal("overview with broken mermaid accepted")
	}
	r.SequenceDiagram = "sequenceDiagram\n A->>B: Call"
	if err := store.WriteJSON(overview, r); err != nil {
		t.Fatal(err)
	}
	if err := agent([]string{"overview", "--file", overview}); err != nil {
		t.Fatal(err)
	}
}
