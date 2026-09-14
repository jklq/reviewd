package report

import (
	"strings"
	"testing"
)

func validReport() Report {
	n := 2
	return Report{Summary: "Persist updates", Confidence: &n, Reasons: []string{"Save error ignored"}, ImportantFiles: []File{{"store.go", "Persists updates"}}, SequenceDiagram: "sequenceDiagram\n C->>S: Save", Findings: []Finding{{Path: "store.go", Line: 2, Side: "RIGHT", Priority: 2, Confidence: .95, Title: "Handle the write error", Body: "A disk error loses the accepted update.", Evidence: "Save error is ignored."}}}
}
func TestDiffAnchors(t *testing.T) {
	files := []ChangedFile{{Filename: "store.go", Patch: "@@ -1,3 +1,3 @@\n context\n-old\n+new\n end\n@@ -10 +10 @@\n-a\n+b"}}
	d, err := ParseDiff(files)
	if err != nil {
		t.Fatal(err)
	}
	r := validReport()
	if err = d.Validate(r); err != nil {
		t.Fatal(err)
	}
	for _, f := range []Finding{{Path: "store.go", Line: 7, Side: "RIGHT"}, {Path: "store.go", StartLine: 3, Line: 10, Side: "RIGHT"}, {Path: "other.go", Line: 2, Side: "RIGHT"}} {
		r.Findings = []Finding{f}
		if d.Validate(r) == nil {
			t.Fatalf("accepted invalid anchor %+v", f)
		}
	}
	for _, patch := range []string{"@@ -1,2 +1,2 @@\n-a\n+b", "@@ -1 +1 @@\n-a\n+b\n+c"} {
		if _, err = ParseDiff([]ChangedFile{{Filename: "x", Patch: patch}}); err == nil {
			t.Fatal("truncated/malformed patch accepted")
		}
	}
}
func TestPrioritiesAndMergeConfidence(t *testing.T) {
	r := validReport()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	n := 5
	r.Confidence = &n
	if r.Validate() == nil || r.MergeReady() {
		t.Fatal("blocker accepted as safe")
	}
	r.Findings = nil
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if !r.MergeReady() {
		t.Fatal("safe result rejected")
	}
	r.Confidence = nil
	if r.Validate() == nil {
		t.Fatal("omitted score accepted")
	}
}
func TestNormalizeAndRendering(t *testing.T) {
	r := validReport()
	r.Findings = append(r.Findings, r.Findings[0])
	low := r.Findings[0]
	low.Title = "Uncertain"
	low.Confidence = .6
	r.Findings = append(r.Findings, low)
	Normalize(&r, .85, 20)
	if len(r.Findings) != 1 {
		t.Fatal(r.Findings)
	}
	r.Summary = "@everyone <script>"
	s := r.Markdown("abc")
	for _, expected := range []string{"## Summary", "## Key issues found", "2/5", "<details>", "```mermaid", "@\u200beveryone"} {
		if !strings.Contains(s, expected) {
			t.Fatal(expected)
		}
	}
	if strings.Contains(s, "<script>") {
		t.Fatal("unsafe HTML")
	}
}
func TestDeletedAndRenamedAnchors(t *testing.T) {
	d, err := ParseDiff([]ChangedFile{{Filename: "new.go", PreviousFilename: "old.go", Patch: "@@ -4,2 +4 @@\n-gone\n keep"}})
	if err != nil {
		t.Fatal(err)
	}
	r := validReport()
	r.ImportantFiles = []File{{"new.go", "Renames the implementation"}}
	r.Findings[0].Path = "new.go"
	r.Findings[0].Side = "LEFT"
	r.Findings[0].Line = 4
	if err = d.Validate(r); err != nil {
		t.Fatal(err)
	}
	r.Findings[0].Path = "old.go"
	if d.Validate(r) == nil {
		t.Fatal("old filename accepted")
	}
}

func TestModelAndHarnessAttribution(t *testing.T) {
	r := validReport()
	r.Harness = "codex"
	r.Model = "gpt-5.6-luna"
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if s := r.Markdown("abc"); !strings.Contains(s, "Reviewed by reviewd using codex (gpt-5.6-luna).") {
		t.Fatal(s)
	}
	r.Model = ""
	if s := r.Markdown("abc"); !strings.Contains(s, "Reviewed by reviewd using codex.") {
		t.Fatal(s)
	}
	r.Harness, r.Model = "", "gpt-5.6-luna"
	if s := r.Markdown("abc"); !strings.Contains(s, "Reviewed by reviewd using model gpt-5.6-luna.") {
		t.Fatal(s)
	}
	r.Model = "two\nlines"
	if r.Validate() == nil {
		t.Fatal("multiline model accepted")
	}
}

func TestServerReasonsAndCommentCapRespectReportLimits(t *testing.T) {
	r := validReport()
	r.Reasons = []string{"one", "two", "three", "four"}
	r.AddReason(strings.Repeat("界", 200))
	r.Findings = append(r.Findings, r.Findings[0])
	r.Findings[1].Title = "Another issue"
	Normalize(&r, .85, 1)
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(r.Reasons) != 4 || !strings.Contains(r.Reasons[0], "comment limit") || !strings.Contains(r.Reasons[1], "界") {
		t.Fatal(r.Reasons)
	}
}
