package report

import (
	"strings"
	"testing"
)

func TestValidateSequenceDiagramAcceptsCanonicalSubset(t *testing.T) {
	valid := []string{
		"sequenceDiagram\n A->>B: Call",
		"sequenceDiagram\n C->>S: Save",
		"sequenceDiagram\n    participant C as Caller\n    participant H as Handler\n    participant S as Store\n    C->>H: Update\n    H->>S: Save\n    S-->>H: Write error\n    H-->>C: Success (defect)",
		"sequenceDiagram\n %% a comment\n A->B: solid\n A-->B: dotted\n A->>B: solid arrow\n A-->>B: dotted arrow\n A-xB: sync fail\n A--xB: async fail\n A-)B: open\n A--)B: dotted open",
		"sequenceDiagram\n A->>+B: Start\n B-->>-A: Done",
		"sequenceDiagram\n actor U as User\n participant S as Store\n U->>S: Save",
		"sequenceDiagram\n create participant T as Temp\n A->>T: Init\n destroy T",
		"sequenceDiagram\n A->>B: Work\n activate B\n B-->>A: Done\n deactivate B",
		"sequenceDiagram\n A->>B: Work\n Note left of B: Stores it\n Note right of A: Waits\n Note over A,B: Critical path",
		"sequenceDiagram\n autonumber\n A->>B: One\n B-->>A: Two",
		"sequenceDiagram\n loop Every retry\n A->>B: Try\n end",
		"sequenceDiagram\n alt Happy path\n A->>B: OK\n else Failure\n A->>B: Retry\n end",
		"sequenceDiagram\n par Parallel\n A->>B: One\n and Second\n A->>B: Two\n end",
		"sequenceDiagram\n critical Setup\n A->>B: Connect\n option Fallback\n A->>B: Retry\n end",
		"sequenceDiagram\n rect LightBlue\n A->>B: Grouped\n end",
		"sequenceDiagram\n box Aqua Team\n participant A\n participant B\n end\n A->>B: Hello",
		"sequenceDiagram\n title: Checkout flow\n A->>B: Pay",
		"sequenceDiagram\n opt Optional\n A->>B: Maybe\n end",
		"sequenceDiagram\n break When overloaded\n A->>B: Shed load\n end",
		"sequenceDiagram\n A->>A: Self check",
	}
	for i, src := range valid {
		if err := ValidateSequenceDiagram(src); err != nil {
			t.Fatalf("valid case %d rejected: %v\n%s", i, err, src)
		}
	}
}

func TestValidateSequenceDiagramRejectsBrokenSyntax(t *testing.T) {
	cases := map[string]string{
		"empty":                "",
		"missing header":       "A->>B: Hi",
		"wrong diagram":        "sequenceDiagram\n graph TD\n A-->B",
		"flowchart":            "flowchart TD\n A-->B",
		"header with space":    "sequence diagram\n A->>B: Hi",
		"fenced":               "```mermaid\nsequenceDiagram\n A->>B: Hi\n```",
		"directive":            "sequenceDiagram\n %%{init: {'theme':'dark'}}%%\n A->>B: Hi",
		"missing colon":        "sequenceDiagram\n A->>B Hello",
		"bad arrow":            "sequenceDiagram\n A==>B: Hi",
		"bad sender":           "sequenceDiagram\n 1A->>B: Hi",
		"bad receiver":         "sequenceDiagram\n A->>9: Hi",
		"spaces without as":    "sequenceDiagram\n participant Order Service\n A->>B: Hi",
		"empty message":        "sequenceDiagram\n A->>B:",
		"empty message spaces": "sequenceDiagram\n A->>B:   ",
		"backtick":             "sequenceDiagram\n A->>B: use `code` here",
		"angle brackets":       "sequenceDiagram\n A->>B: a < b",
		"html":                 "sequenceDiagram\n A->>B: hello<br/>world",
		"unknown statement":    "sequenceDiagram\n frobnicate the widget",
		"missing end":          "sequenceDiagram\n loop Forever\n A->>B: Hi",
		"stray end":            "sequenceDiagram\n A->>B: Hi\n end",
		"end with text":        "sequenceDiagram\n loop X\n A->>B: Hi\n end now",
		"else outside alt":     "sequenceDiagram\n A->>B: Hi\n else Otherwise",
		"and outside par":      "sequenceDiagram\n loop X\n A->>B: Hi\n and More\n end",
		"option outside crit":  "sequenceDiagram\n par P\n A->>B: Hi\n option Other\n end",
		"lowercase note":       "sequenceDiagram\n note left of A: Hi\n A->>B: Hi",
		"bad note":             "sequenceDiagram\n Note sideways of A: Hi\n A->>B: Hi",
		"no messages":          "sequenceDiagram\n participant A\n participant B",
		"duplicate header":     "sequenceDiagram\n A->>B: Hi\n sequenceDiagram",
		"loop without label":   "sequenceDiagram\n loop\n A->>B: Hi\n end",
		"nul byte":             "sequenceDiagram\n A->>B: Hi\x00",
	}
	for name, src := range cases {
		if err := ValidateSequenceDiagram(src); err == nil {
			t.Fatalf("%s accepted:\n%s", name, src)
		}
	}
	if err := ValidateSequenceDiagram("sequenceDiagram\n" + strings.Repeat("A->>B: Hi\n", 2000)); err == nil {
		t.Fatal("oversized diagram accepted")
	}
	if err := ValidateSequenceDiagram("sequenceDiagram\n A->>B: " + strings.Repeat("x", 201)); err == nil {
		t.Fatal("oversized message accepted")
	}
}

func TestReportValidateEnforcesSequenceDiagram(t *testing.T) {
	n := 2
	r := Report{Summary: "Persist updates", Confidence: &n, Reasons: []string{"Save error ignored"}, ImportantFiles: []File{{"store.go", "Persists updates"}}, SequenceDiagram: "sequenceDiagram\n C->>S: Save"}
	if err := r.Validate(); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	r.SequenceDiagram = "sequenceDiagram\n graph TD\n A-->B"
	if err := r.Validate(); err == nil {
		t.Fatal("report with flowchart accepted")
	}
	r.SequenceDiagram = "sequenceDiagram\n A->>B Missing colon"
	if err := r.Validate(); err == nil {
		t.Fatal("report with colon-less message accepted")
	}
	r.SequenceDiagram = "sequenceDiagram\n participant A\n participant B"
	if err := r.Validate(); err == nil {
		t.Fatal("report without messages accepted")
	}
}
