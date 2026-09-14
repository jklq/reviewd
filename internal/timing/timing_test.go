package timing

import (
	"context"
	"sync"
	"testing"
)

func TestConcurrentSpansAndSnapshotIsolation(t *testing.T) {
	ctx, r := New(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); end := Start(ctx, "worker"); end() }()
	}
	wg.Wait()
	spans := r.Spans()
	if len(spans) != 20 {
		t.Fatalf("spans=%d", len(spans))
	}
	for _, s := range spans {
		if s.Seconds < 0 || s.StartSeconds < 0 {
			t.Fatal(s)
		}
	}
	spans[0].Name = "mutated"
	if r.Spans()[0].Name != "worker" {
		t.Fatal("caller mutated recorder")
	}
	Start(context.Background(), "disabled")()
}
