// Package timing records diagnostic wall-clock spans. Nested and concurrent
// spans intentionally overlap; their durations must not be summed.
package timing

import (
	"context"
	"sync"
	"time"
)

type Span struct {
	Name         string  `json:"name"`
	StartSeconds float64 `json:"start_seconds"`
	Seconds      float64 `json:"seconds"`
}
type Recorder struct {
	started time.Time
	mu      sync.Mutex
	spans   []Span
}
type key struct{}

func New(ctx context.Context) (context.Context, *Recorder) {
	r := &Recorder{started: time.Now()}
	return context.WithValue(ctx, key{}, r), r
}
func Start(ctx context.Context, name string) func() {
	r, _ := ctx.Value(key{}).(*Recorder)
	if r == nil {
		return func() {}
	}
	started := time.Now()
	return func() { r.Add(name, started, time.Now()) }
}
func (r *Recorder) Add(name string, start, end time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spans = append(r.spans, Span{Name: name, StartSeconds: start.Sub(r.started).Seconds(), Seconds: end.Sub(start).Seconds()})
}
func (r *Recorder) Spans() []Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Span(nil), r.spans...)
}
