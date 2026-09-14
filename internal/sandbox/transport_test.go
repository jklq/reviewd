package sandbox

import (
	"io"
	"strings"
	"testing"
)

func TestReportTransportIsBoundedDuringCopy(t *testing.T) {
	b := &reportBuffer{}
	n, err := io.Copy(b, io.LimitReader(strings.NewReader(strings.Repeat("x", 2<<20)), 2<<20))
	if err != nil || n != 2<<20 || !b.overflow || len(b.Bytes()) != 1<<20 {
		t.Fatalf("unbounded transport: n=%d buffered=%d overflow=%v err=%v", n, len(b.Bytes()), b.overflow, err)
	}
}
