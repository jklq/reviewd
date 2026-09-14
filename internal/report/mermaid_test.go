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
		"sequenceDiagram\n graphQL->>API: Query",
		"sequenceDiagram\n A->>flowchartX: Hi",
		"sequenceDiagram\n participant graphStore\n graphStore->>B: Save",
		"sequenceDiagram\n graphQL->>API: Query\n Note over graphQL,API: Contract",
		"sequenceDiagram\r\n A->>B: Hi",
		"\n\nsequenceDiagram\n A->>B: Hi",
		"sequenceDiagram\n my-id_2->>other_3: Dashed IDs",
		"sequenceDiagram\n par\n A->>B: One\n end",
		"sequenceDiagram\n critical\n A->>B: One\n end",
		"sequenceDiagram\n break\n A->>B: One\n end",
		"sequenceDiagram\n autonumber off\n A->>B: One",
		"sequenceDiagram\n box Aqua\n participant A\n end\n A->>B: Hi",
		"sequenceDiagram\n A->>B: Hi\n Note over A: Solo note",
		"sequenceDiagram\n A->>T: Work\n destroy T\n T-->>A: Done",
		"sequenceDiagram\n A->>T: Work\n destroy T\n A->>T: Bye",
		"sequenceDiagram\n create participant A\n create participant B\n X->>B: Hi",
		"sequenceDiagram\n create participant T\n Note over A: Pause\n A->>T: Init",
		"sequenceDiagram\n activate B\n activate B\n A->>B: Hi\n deactivate B\n deactivate B",
		"sequenceDiagram\n activate B\n B-->>-A: Done",
		"sequenceDiagram\n A->>+B: Start\n deactivate B",
		"sequenceDiagram\n A->>+A: Self\n A-->>-A: End",
		"sequenceDiagram\n box Aqua Team\n participant A\n participant B\n destroy B\n end\n A->>B: Hi",
		"sequenceDiagram\n create participant B\n A->>+B: Start\n deactivate B",
		"sequenceDiagram\n graph ->>B: text",
		"sequenceDiagram\n flowchart ->>B: text",
		"sequenceDiagram\n gantt ->>B: text",
		"sequenceDiagram\n participant A\n participant A\n A->>B: Hi",
		"sequenceDiagram\n create participant A\n X->>A: Hi\n participant A",
		"sequenceDiagram\n X->>Y: Hi\n activate B\n create participant B\n X->>B: Yo",
		"sequenceDiagram\n X->>Y: Hi\n destroy B\n create participant B\n X->>B: Yo",
		"sequenceDiagram\n titleCase->>B: Hi",
		"sequenceDiagram\rA->>B: Hi",
		"sequenceDiagram\r\nA->>B: Hi\rB-->>A: Yo",
		"sequenceDiagram\n A-B->>C-D: Hi",
		"sequenceDiagram\n box One\n participant A\n participant A\n end\n A->>B: Hi",
		"sequenceDiagram\n box One\n participant A\n end\n participant A\n A->>B: Hi",
		"sequenceDiagram\n participant A\n box One\n participant A\n end\n A->>B: Hi",
		"sequenceDiagram\n endgame->>B: Call",
		"sequenceDiagram\n activate B\n A->>B: Hi\n deactivate B",
		"sequenceDiagram\n title->>B: Call",
		"sequenceDiagram\n as->>B: Call",
		"sequenceDiagram\n accTitle->>B: Call",
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
		"create to another":    "sequenceDiagram\n create participant T\n A->>Other: Hi",
		"create from created":  "sequenceDiagram\n create participant T\n T->>A: Hi",
		"destroy unrelated":    "sequenceDiagram\n A->>T: Work\n destroy T\n A->>B: Hi",
		"deactivate alone":     "sequenceDiagram\n A->>B: Hi\n deactivate B",
		"deactivate twice":     "sequenceDiagram\n activate B\n A->>B: Hi\n deactivate B\n deactivate B",
		"minus w/o activation": "sequenceDiagram\n A->>B: Hi\n B-->>-A: Done",
		"minus wrong party":    "sequenceDiagram\n A->>+B: Start\n A-->>-B: Done",
		"bare graph":           "sequenceDiagram\n graph\n A->>B: Hi",
		"bare flowchart":       "sequenceDiagram\n flowchart\n A->>B: Hi",
		"graph tab":            "sequenceDiagram\n graph\tTD\n A->>B: Hi",
		"class diagram":        "sequenceDiagram\n classDiagram\n A->>B: Hi",
		"gantt":                "sequenceDiagram\n gantt\n A->>B: Hi",
		"rect without label":   "sequenceDiagram\n rect\n A->>B: Hi\n end",
		"box without label":    "sequenceDiagram\n box\n A->>B: Hi\n end",
		"alt without label":    "sequenceDiagram\n alt\n A->>B: Hi\n end",
		"label colon":          "sequenceDiagram\n participant A as A:B\n A->>B: Hi",
		"label semicolon":      "sequenceDiagram\n participant A as A;B\n A->>B: Hi",
		"label html":           "sequenceDiagram\n participant A as <A>\n A->>B: Hi",
		"block label colon":    "sequenceDiagram\n loop A:B\n A->>B: Hi\n end",
		"block label html":     "sequenceDiagram\n loop <A>\n A->>B: Hi\n end",
		"note two left":        "sequenceDiagram\n Note left of A,B: Hi\n A->>B: Hi",
		"note bad id":          "sequenceDiagram\n Note over 9A: Hi\n A->>B: Hi",
		"note html":            "sequenceDiagram\n Note over A: <b>Hi</b>\n A->>B: Hi",
		"title empty":          "sequenceDiagram\n title:\n A->>B: Hi",
		"title html":           "sequenceDiagram\n title: <Hi>\n A->>B: Hi",
		"capital participant":  "sequenceDiagram\n Participant A\n A->>B: Hi",
		"create bare":          "sequenceDiagram\n create B\n A->>B: Hi",
		"destroy bare":         "sequenceDiagram\n A->>B: Hi\n destroy",
		"activate bare":        "sequenceDiagram\n A->>B: Hi\n activate",
		"long block line":      "sequenceDiagram\n loop " + strings.Repeat("L", 495) + "\n A->>B: Hi",
		"oversized label":      "sequenceDiagram\n participant A as " + strings.Repeat("z", 65) + "\n A->>B: Hi",
		"else in opt":          "sequenceDiagram\n opt Maybe\n A->>B: x\n else Other\n A->>B: y\n end",
		"note over three":      "sequenceDiagram\n A->>B: Hi\n Note over A,B,C: x",
		"semicolon message":    "sequenceDiagram\n A->>B: a;b",
		"semicolon note":       "sequenceDiagram\n A->>B: Hi\n Note over A: x;y",
		"semicolon title":      "sequenceDiagram\n title: a;b\n A->>B: Hi",
		"semicolon block":      "sequenceDiagram\n loop A;B\n A->>B: Hi\n end",
		"semicolon else":       "sequenceDiagram\n alt A\n A->>B: x\n else O;ther\n A->>B: y\n end",
		"message in box":       "sequenceDiagram\n box Aqua Team\n participant A\n A->>B: Hi\n end",
		"note in box":          "sequenceDiagram\n box Aqua Team\n participant A\n Note over A: x\n end",
		"create in box":        "sequenceDiagram\n box Aqua Team\n create participant B\n end\n A->>B: Hi",
		"activate in box":      "sequenceDiagram\n box Aqua Team\n participant A\n activate A\n end\n A->>B: Hi",
		"loop in box":          "sequenceDiagram\n box Aqua Team\n participant A\n loop X\n A->>B: Hi\n end\n end",
		"title in box":         "sequenceDiagram\n box Aqua Team\n title: T\n end\n A->>B: Hi",
		"autonumber in box":    "sequenceDiagram\n box Aqua Team\n autonumber\n end\n A->>B: Hi",
		"box in box":           "sequenceDiagram\n box Outer\n box Inner\n end\n end\n A->>B: Hi",
		"dup declared create":  "sequenceDiagram\n participant A\n create participant A\n X->>A: Hi",
		"dup message create":   "sequenceDiagram\n Y->>A: Hi\n create participant A\n X->>A: Yo",
		"dup note create":      "sequenceDiagram\n X->>Y: Hi\n Note over A: x\n create participant A\n X->>A: Yo",
		"dup create twice":     "sequenceDiagram\n create participant A\n X->>A: Hi\n create participant A\n Y->>A: Yo",
		"dup after destroy":    "sequenceDiagram\n participant A\n A->>B: Hi\n destroy A\n B->>A: Bye\n create participant A\n X->>A: Yo",
		"hash message":         "sequenceDiagram\n A->>B: issue #123",
		"hash note":            "sequenceDiagram\n A->>B: Hi\n Note over A: #1 priority",
		"hash title":           "sequenceDiagram\n title: Sprint #3\n A->>B: Hi",
		"hash block":           "sequenceDiagram\n loop Sprint #2\n A->>B: Hi\n end",
		"hash label":           "sequenceDiagram\n participant A as C# dev\n A->>B: Hi",
		"title no space":       "sequenceDiagram\n title:Checkout flow\n A->>B: Hi",
		"title space colon":    "sequenceDiagram\n title : Text\n A->>B: Hi",
		// Valid Mermaid, but outside the strict subset: harnesses must use "title: Text".
		"title without colon": "sequenceDiagram\n title Checkout\n A->>B: Hi",
		"lone cr comment":     "sequenceDiagram\n %%\rgraph TD\n A->>B: Hi",
		"lone cr smuggling":   "sequenceDiagram\n A->>B: Hi\rgarbage",
		"extra hyphen":        "sequenceDiagram\n A--->B: hi",
		"extra hyphen 4":      "sequenceDiagram\n A---->B: hi",
		"dash receiver":       "sequenceDiagram\n A-->B-: hi",
		"dash note":           "sequenceDiagram\n X->>Y: Hi\n Note over A-: x",
		// Trailing-dash IDs are Mermaid-valid in some positions, but the
		// strict subset bans them everywhere: the spacing-dependent
		// exceptions are too subtle for generated diagrams to rely on.
		"dash participant":   "sequenceDiagram\n participant A-\n A->>B: Hi",
		"dash activate":      "sequenceDiagram\n X->>Y: Hi\n activate A-",
		"dash destroy":       "sequenceDiagram\n X->>Y: Hi\n destroy A-",
		"dash create":        "sequenceDiagram\n create participant A-\n X->>A: Hi",
		"dash sender spaced": "sequenceDiagram\n A- ->>B: hi",
		"box dup two boxes":  "sequenceDiagram\n box One\n participant A\n end\n box Two\n participant A\n end\n A->>B: Hi",
		"keyword sender":     "sequenceDiagram\n end->>B: Call",
		"keyword receiver":   "sequenceDiagram\n A->>note: Hi",
		"keyword upper":      "sequenceDiagram\n END->>B: Call",
		"keyword declare":    "sequenceDiagram\n participant loop\n A->>B: Hi",
		"keyword create":     "sequenceDiagram\n create participant alt\n A->>B: Hi",
		"keyword note":       "sequenceDiagram\n X->>Y: Hi\n Note over box: x",
		"keyword activate":   "sequenceDiagram\n X->>Y: Hi\n activate off",
		"keyword destroy":    "sequenceDiagram\n X->>Y: Hi\n destroy par",
		"activate unknown":   "sequenceDiagram\n activate B\n A->>C: Call",
		"activate unknown 2": "sequenceDiagram\n A->>C: Call\n activate B\n deactivate B",
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

func TestValidateSequenceDiagramDiagramTypeBoundary(t *testing.T) {
	accept := []string{
		"sequenceDiagram\n graphQL->>API: Query",
		"sequenceDiagram\n A->>graphQL: Query",
		"sequenceDiagram\n flowchart9->>B: Hi",
		"sequenceDiagram\n flowchart->>B: Hi",
		"sequenceDiagram\n participant graph\n graph->>B: Hi",
		"sequenceDiagram\n participant ganttMaster\n ganttMaster->>B: Hi",
	}
	for _, src := range accept {
		if err := ValidateSequenceDiagram(src); err != nil {
			t.Errorf("ID with diagram-type prefix rejected: %v\n%s", err, src)
		}
	}
	reject := map[string]string{
		"graph":            "only sequenceDiagram is allowed",
		"graph TD":         "only sequenceDiagram is allowed",
		"graph\tLR":        "only sequenceDiagram is allowed",
		"flowchart":        "only sequenceDiagram is allowed",
		"flowchart TD":     "only sequenceDiagram is allowed",
		"classDiagram":     "only sequenceDiagram is allowed",
		"classDiagram Foo": "only sequenceDiagram is allowed",
		"gantt":            "only sequenceDiagram is allowed",
		"pie":              "only sequenceDiagram is allowed",
		"graphQL":          "unsupported statement",
		"flowchartX":       "unsupported statement",
	}
	for line, want := range reject {
		err := ValidateSequenceDiagram("sequenceDiagram\n " + line + "\n A->>B: Hi")
		if err == nil {
			t.Errorf("%q accepted", line)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %q lacks %q", line, err.Error(), want)
		}
	}
}

func TestValidateSequenceDiagramLifecyclePairing(t *testing.T) {
	valid := []string{
		"sequenceDiagram\n create participant B\n A-->B: Hello",
		"sequenceDiagram\n create actor B as Bob\n A->>B: Hello",
		"sequenceDiagram\n A->>B: Work\n destroy B\n B-->>A: Done",
		"sequenceDiagram\n A->>B: Work\n destroy B\n A->>B: Bye",
		"sequenceDiagram\n A->>B: Hi\n destroy B",
		"sequenceDiagram\n A->>B: Work\n activate B\n activate B\n deactivate B\n deactivate B",
		"sequenceDiagram\n A->>+B: s\n B->>+C: t\n C-->>-B: u\n B-->>-A: v",
	}
	for _, src := range valid {
		if err := ValidateSequenceDiagram(src); err != nil {
			t.Errorf("paired lifecycle rejected: %v\n%s", err, src)
		}
	}
	invalid := map[string]string{
		"sequenceDiagram\n create participant T\n A->>Other: Hi":                     "needs a message to T next",
		"sequenceDiagram\n create participant T\n T->>A: Hi":                         "needs a message to T next",
		"sequenceDiagram\n create participant T\n Note over A: x\n A->>Other: Hi":    "needs a message to T next",
		"sequenceDiagram\n A->>T: W\n destroy T\n A->>B: Hi":                         "needs a message to or from T next",
		"sequenceDiagram\n create participant A\n destroy B\n X->>A: Hi\n Y->>Z: Yo": "needs a message to or from B next",
		"sequenceDiagram\n create participant A\n destroy B\n X->>B: Hi":             "needs a message to A next",
		"sequenceDiagram\n A->>B: Hi\n deactivate B":                                 "without a matching activation",
		"sequenceDiagram\n A->>B: Hi\n B-->>-A: Done":                                "without a matching activation",
		"sequenceDiagram\n participant A\n create participant A\n X->>A: Hi":         "reuses an ID already used",
	}
	for src, want := range invalid {
		err := ValidateSequenceDiagram(src)
		if err == nil {
			t.Errorf("unpaired lifecycle accepted:\n%s", src)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q\n%s", err.Error(), want, src)
		}
	}
}

func TestValidateSequenceDiagramActivationShorthandTargets(t *testing.T) {
	if err := ValidateSequenceDiagram("sequenceDiagram\n A->>+B: S\n deactivate B"); err != nil {
		t.Errorf("+ should activate the receiver: %v", err)
	}
	if err := ValidateSequenceDiagram("sequenceDiagram\n activate A\n A-->>-B: D"); err != nil {
		t.Errorf("- should deactivate the sender: %v", err)
	}
	if err := ValidateSequenceDiagram("sequenceDiagram\n A->>+B: S\n A-->>-B: D"); err == nil {
		t.Error("- deactivating a never-activated sender accepted")
	}
}

func TestValidateSequenceDiagramSubsetRestrictions(t *testing.T) {
	cases := map[string]string{
		"sequenceDiagram\n opt Maybe\n A->>B: x\n else Other\n A->>B: y\n end":                        `"else" is only valid inside an alt block`,
		"sequenceDiagram\n A->>B: Hi\n Note over A,B,C: x":                                            `"Note over" takes at most two participants`,
		"sequenceDiagram\n A->>B: a;b":                                                                "must not contain semicolons",
		"sequenceDiagram\n loop A;B\n A->>B: Hi\n end":                                                "must not contain semicolons",
		"sequenceDiagram\n box T\n participant A\n A->>B: Hi\n end":                                   "allowed inside a box",
		"sequenceDiagram\n box T\n participant A\n Note over A: x\n end":                              "allowed inside a box",
		"sequenceDiagram\n box One\n participant A\n end\n box Two\n participant A\n end\n A->>B: Hi": "already in another box",
		"sequenceDiagram\n end->>B: Call":                                                             "reserved Mermaid keyword",
		"sequenceDiagram\n activate B\n A->>C: Call":                                                  "needs B to be",
	}
	for src, want := range cases {
		err := ValidateSequenceDiagram(src)
		if err == nil {
			t.Errorf("accepted:\n%s", src)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q\n%s", err.Error(), want, src)
		}
	}
}

func TestValidateSequenceDiagramDoubleDashArrowKeepsSenderID(t *testing.T) {
	if err := ValidateSequenceDiagram("sequenceDiagram\n A->>+B: Start\n B-->>-A: Done"); err != nil {
		t.Errorf("B-->>-A must parse the sender as B: %v", err)
	}
}
