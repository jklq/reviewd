package sandbox

import (
	"context"
	"io"
	"strings"

	"reviewd/internal/timing"
)

// phaseLog forwards all harness output while recognizing the fixed wrapper's
// per-run markers. Only a bounded line prefix is retained. These timings are
// diagnostics, never input to review acceptance or other security decisions.
type phaseLog struct {
	sink   io.Writer
	marker string
	prefix string
	end    func()
	ctx    context.Context
	name   string
	next   int
}

func (l *phaseLog) Write(p []byte) (int, error) {
	n, err := l.sink.Write(p)
	for _, b := range p[:n] {
		if b == '\n' {
			phases := []string{"workspace_copy", "harness", "report_export"}
			if l.next < len(phases) && l.prefix == l.marker+phases[l.next] {
				l.end()
				l.end = timing.Start(l.ctx, l.name+phases[l.next])
				l.next++
			}
			l.prefix = ""
		} else if len(l.prefix) <= len(l.marker)+64 {
			l.prefix += string(b)
		}
	}
	return n, err
}
func (l *phaseLog) close() { l.end() }
func phaseMarker(name string) string {
	return "reviewd-timing-" + strings.TrimPrefix(name, "reviewd-") + ":"
}
