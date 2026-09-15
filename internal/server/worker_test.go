package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jklq/reviewd/internal/config"
	"github.com/jklq/reviewd/internal/github"
	"github.com/jklq/reviewd/internal/report"
	"github.com/jklq/reviewd/internal/review"
	"github.com/jklq/reviewd/internal/sandbox"
	"github.com/jklq/reviewd/internal/store"
)

type workerRunner struct {
	mu             sync.Mutex
	calls          int
	afterValidator func()
}

func (r *workerRunner) Run(ctx context.Context, req sandbox.Request) (report.Report, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if req.Role == "validator" && r.afterValidator != nil {
		r.afterValidator()
	}
	n := 2
	return report.Report{Summary: "Persist updates", Confidence: &n, Reasons: []string{"Write failures are lost"}, ImportantFiles: []report.File{{Path: "store.go", Description: "Adds persistence"}}, SequenceDiagram: "sequenceDiagram\n C->>S: Save", Findings: []report.Finding{{Path: "store.go", Line: 2, Side: "RIGHT", Priority: 2, Confidence: .96, Title: "Propagate the write error", Body: "A full disk loses accepted updates.", Evidence: "Save result ignored"}}}, nil
}

type githubFixture struct {
	mu                sync.Mutex
	head, base, body  string
	reviews, comments int
	reactions         []string
	published         map[string]any
}

func (f *githubFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/access_tokens"):
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "test-token", "expires_at": time.Now().Add(time.Hour)})
	case p == "/repos/o/r/pulls/1":
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 1, "state": "open", "changed_files": 1, "head": map[string]any{"sha": f.head, "repo": map[string]string{"full_name": "o/r"}}, "base": map[string]any{"sha": f.base, "repo": map[string]string{"full_name": "o/r"}}})
	case strings.HasSuffix(p, "/files"):
		_ = json.NewEncoder(w).Encode([]report.ChangedFile{{Filename: "store.go", Patch: "@@ -1,2 +1,2 @@\n package store\n-func old() {}\n+func save() {}", Additions: 1, Deletions: 1}})
	case strings.Contains(p, "/compare/"):
		_ = json.NewEncoder(w).Encode(map[string]any{"merge_base_commit": map[string]string{"sha": f.base}})
	case strings.Contains(p, "/tarball/"):
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		data := []byte("package store\nfunc save() {}\n")
		_ = tw.WriteHeader(&tar.Header{Name: "repo/store.go", Size: int64(len(data)), Mode: 0644})
		_, _ = tw.Write(data)
		_ = tw.Close()
		_ = gz.Close()
		_, _ = w.Write(b.Bytes())
	case strings.HasSuffix(p, "/reviews"):
		if r.Method == "GET" {
			if f.body == "" {
				fmt.Fprint(w, "[]")
			} else {
				_ = json.NewEncoder(w).Encode([]any{map[string]any{"body": f.body, "user": map[string]string{"login": "test-app[bot]"}}})
			}
		} else {
			f.reviews++
			_ = json.NewDecoder(r.Body).Decode(&f.published)
			f.body, _ = f.published["body"].(string)
			fmt.Fprint(w, `{"id":42}`)
		}
	case strings.HasSuffix(p, "/issues/1/comments"):
		if r.Method == "GET" {
			fmt.Fprint(w, "[]")
		} else {
			f.comments++
			fmt.Fprint(w, `{"id":11}`)
		}
	case strings.HasSuffix(p, "/reactions"):
		var v map[string]string
		_ = json.NewDecoder(r.Body).Decode(&v)
		f.reactions = append(f.reactions, v["content"])
		fmt.Fprint(w, `{"id":12}`)
	case r.Method == "PATCH" || r.Method == "DELETE":
		w.WriteHeader(204)
	default:
		t := fmt.Sprintf("unexpected %s %s", r.Method, p)
		http.Error(w, t, 404)
	}
}
func setupWorker(t *testing.T) (Worker, *store.Job, *githubFixture, *workerRunner) {
	t.Helper()
	fixture := &githubFixture{head: strings.Repeat("a", 40), base: strings.Repeat("b", 40)}
	api := httptest.NewServer(fixture)
	t.Cleanup(api.Close)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	app := &github.App{ID: 1, Slug: "test-app", Key: key, BaseURL: api.URL, HTTP: api.Client()}
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j, _, err := s.Enqueue("delivery", store.Job{Repo: "o/r", Number: 1, Installation: 2, Head: fixture.head, TriggerComment: 9})
	if err != nil {
		t.Fatal(err)
	}
	c := config.Default()
	c.DataDir = s.Dir
	c.SizeTiers = nil
	c.Parallelism = 2
	runner := &workerRunner{}
	return Worker{Config: c, Store: s, App: app, Engine: review.Engine{Config: c, Runner: runner}}, &j, fixture, runner
}
func TestReviewPublicationAndRetry(t *testing.T) {
	w, j, f, runner := setupWorker(t)
	if err := w.Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	// A first review only reacts; it does not create a status comment.
	if f.reviews != 1 || f.comments != 0 || runner.calls != 3 {
		t.Fatalf("reviews=%d comments=%d calls=%d", f.reviews, f.comments, runner.calls)
	}
	if f.published["commit_id"] != j.Head || f.published["event"] != "COMMENT" {
		t.Fatal(f.published)
	}
	comments := f.published["comments"].([]any)
	inline := comments[0].(map[string]any)
	if inline["line"] != float64(2) || inline["side"] != "RIGHT" || !strings.Contains(inline["body"].(string), "**P2") {
		t.Fatal(inline)
	}
	for _, section := range []string{"## Summary", "## Key issues found", "2/5", "Important files changed", "```mermaid"} {
		if !strings.Contains(f.body, section) {
			t.Fatal(section)
		}
	}
	// Simulate the server losing the successful POST response before recording it.
	j.Published = false
	if err := w.Store.Save(j); err != nil {
		t.Fatal(err)
	}
	if err := w.Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if f.reviews != 1 || runner.calls != 3 {
		t.Fatal("retry republished or reran harnesses")
	}
	// A retriggered review creates exactly one status comment, edited in place.
	if f.comments != 1 {
		t.Fatalf("retrigger did not create a status comment: comments=%d", f.comments)
	}
	if _, err := os.Stat(filepath.Join(w.Store.RunDir(j.ID), "report.json")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.reactions, ","), "-1") {
		t.Fatal("missing not-ready reaction")
	}
}
func TestStaleReviewNeverPublished(t *testing.T) {
	w, j, f, runner := setupWorker(t)
	runner.afterValidator = func() { f.mu.Lock(); f.head = strings.Repeat("c", 40); f.mu.Unlock() }
	if err := w.Run(context.Background(), j); err != errStale {
		t.Fatal(err)
	}
	if f.reviews != 0 {
		t.Fatal("stale review published")
	}
}
func TestBaseChangeOnRetryInvalidatesReport(t *testing.T) {
	w, j, f, _ := setupWorker(t)
	if err := w.Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	f.base = strings.Repeat("d", 40)
	if err := w.Run(context.Background(), j); err != errStale {
		t.Fatal(err)
	}
}

func TestCompletionSurvivesTemporaryStorageFailure(t *testing.T) {
	w, _, _, _ := setupWorker(t)
	j, ok, err := w.Store.Claim()
	if err != nil || !ok {
		t.Fatal(err)
	}
	jobs := filepath.Join(w.Store.Dir, "jobs")
	backup := filepath.Join(w.Store.Dir, "jobs-offline")
	if err = os.Rename(jobs, backup); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(jobs, []byte("unavailable"), 0600); err != nil {
		t.Fatal(err)
	}
	j.Status = "done"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- w.saveCompletion(ctx, &j) }()
	select {
	case err = <-finished:
		t.Fatalf("worker abandoned completion: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = os.Remove(jobs); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(backup, jobs); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	persisted, err := w.Store.Get(j.ID)
	if err != nil || persisted.Status != "done" {
		t.Fatalf("completion not persisted: %+v %v", persisted, err)
	}
	if _, _, err = w.Store.Enqueue("next", store.Job{Repo: j.Repo, Number: j.Number, Installation: j.Installation}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = w.Store.Claim(); err != nil || !ok {
		t.Fatalf("PR remains blocked: %v", err)
	}
}

func TestCompletionStorageFailureAllowsShutdown(t *testing.T) {
	w, j, _, _ := setupWorker(t)
	if err := os.Rename(filepath.Join(w.Store.Dir, "jobs"), filepath.Join(w.Store.Dir, "offline")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.saveCompletion(ctx, j); err != context.Canceled {
		t.Fatal(err)
	}
}
