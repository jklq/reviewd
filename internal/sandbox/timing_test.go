package sandbox

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"reviewd/internal/timing"
)

func TestPhaseLogFragmentedMarkersAndBoundedLines(t *testing.T) {
	ctx, r := timing.New(context.Background())
	var sink bytes.Buffer
	l := &phaseLog{sink: &sink, marker: "private:", ctx: ctx, name: "reviewer/", end: timing.Start(ctx, "reviewer/container_start")}
	input := "private:harness\n" + strings.Repeat("x", 10000) + "\nprivate:workspace_copy\nnormal output\nprivate:harness\nprivate:harness\nprivate:report_export\n"
	for i := 0; i < len(input); i += 3 {
		part := input[i:min(i+3, len(input))]
		if n, err := l.Write([]byte(part)); err != nil || n != len(part) {
			t.Fatalf("write %d %v", n, err)
		}
		if len(l.prefix) > len(l.marker)+65 {
			t.Fatal("unbounded line retention")
		}
	}
	l.close()
	if sink.String() != input {
		t.Fatal("output changed")
	}
	spans := r.Spans()
	want := []string{"reviewer/container_start", "reviewer/workspace_copy", "reviewer/harness", "reviewer/report_export"}
	if len(spans) != len(want) {
		t.Fatal(spans)
	}
	for i, s := range spans {
		if s.Name != want[i] {
			t.Fatal(spans)
		}
	}
}

func TestPhaseLogClosesFailedHarnessWithoutInventingExport(t *testing.T) {
	ctx, r := timing.New(context.Background())
	var sink bytes.Buffer
	l := &phaseLog{sink: &sink, marker: "p:", ctx: ctx, end: timing.Start(ctx, "container_start")}
	_, _ = l.Write([]byte("p:workspace_copy\np:harness\nfailed"))
	l.close()
	spans := r.Spans()
	if len(spans) != 3 || spans[2].Name != "harness" {
		t.Fatal(spans)
	}
}
