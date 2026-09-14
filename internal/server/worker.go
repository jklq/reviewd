package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"reviewd/internal/config"
	"reviewd/internal/github"
	"reviewd/internal/report"
	"reviewd/internal/review"
	"reviewd/internal/store"
	"reviewd/internal/timing"
)

type Worker struct {
	Config config.Config
	Store  *store.Store
	App    *github.App
	Engine review.Engine
}

var errStale = errors.New("PR closed, draft, or head changed; review superseded")

const (
	watchingMessage   = "👀 Reviewing this pull request."
	supersededMessage = "Review superseded: the PR changed or is no longer ready for review."
)

func failedMessage(j *store.Job) string {
	return "Review could not complete. No merge-readiness assessment was produced. The operator can inspect job " + j.ID + "."
}

func (w Worker) Loop(ctx context.Context) {
	for ctx.Err() == nil {
		j, ok, err := w.Store.Claim()
		if err != nil {
			slog.Error("claim job", "error", err)
		}
		if err != nil || !ok {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
				continue
			}
		}
		w.process(ctx, &j)
	}
}
func (w Worker) process(ctx context.Context, j *store.Job) {
	timeout, _ := time.ParseDuration(w.Config.Timeout)
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	err := w.Run(jobCtx, j)
	cancel()
	switch {
	case err == nil:
		j.Status = "done"
		j.Error = ""
	case errors.Is(err, errStale):
		j.Status = "superseded"
		j.Error = err.Error()
	default:
		j.Error = err.Error()
		j.Status = "failed"
		if ctx.Err() != nil || j.Attempts < 3 {
			j.Status = "queued"
			j.Next = time.Now().Add(time.Duration(j.Attempts*j.Attempts) * 30 * time.Second)
		}
		slog.Error("review failed", "job", j.ID, "attempt", j.Attempts, "error", err)
	}
	if err != nil && j.Status != "queued" {
		notifyCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		c := w.App.Installation(j.Installation)
		w.terminalStatus(notifyCtx, c, j)
		w.reactions(notifyCtx, c, j, "", true)
	}
	_ = w.saveCompletion(ctx, j)
}

// Retain ownership until the final state is durable. A transient storage error
// must not abandon a running job and block its PR for the daemon's lifetime.
func (w Worker) saveCompletion(ctx context.Context, j *store.Job) error {
	for {
		if err := w.Store.Save(j); err == nil {
			return nil
		} else {
			slog.Error("save job; retaining worker until storage recovers", "job", j.ID, "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err() // Startup recovery owns any remaining running record.
		case <-time.After(time.Second):
		}
	}
}
func (w Worker) statusBody(j *store.Job, message string) string {
	return "<!-- reviewd-status:" + j.ID + " -->\n" + message
}
func statusMarker(j *store.Job) string {
	return "<!-- reviewd-status:" + j.ID + " -->"
}

// terminalStatus posts or edits the status comment for a terminal outcome.
// Status comments are never created for a first review; a terminal failure is
// the exception: it is posted once and then edited in place.
func (w Worker) terminalStatus(ctx context.Context, c *github.Client, j *store.Job) {
	if j.Status == "superseded" {
		if j.StatusComment != 0 {
			if err := c.EditComment(ctx, j.Repo, j.StatusComment, w.statusBody(j, supersededMessage)); err != nil {
				slog.Warn("update superseded comment", "error", err)
			}
		}
		return
	}
	if j.StatusComment == 0 {
		id, err := c.Comment(ctx, j.Repo, j.Number, w.statusBody(j, failedMessage(j)))
		if err != nil {
			slog.Warn("post failed review comment", "error", err)
			return
		}
		j.StatusComment = id
		return
	}
	if err := c.EditComment(ctx, j.Repo, j.StatusComment, w.statusBody(j, failedMessage(j))); err != nil {
		slog.Warn("update failed review comment", "error", err)
	}
}
func (w Worker) reactions(ctx context.Context, c *github.Client, j *store.Job, content string, clear bool) {
	// The PR itself is always a reaction target so automatic reviews are visible.
	if clear {
		if err := c.ClearIssueEyes(ctx, j.Repo, j.Number); err != nil {
			slog.Warn("clear reviewing reaction", "job", j.ID, "error", err)
		}
	}
	if content != "" {
		if err := c.IssueReaction(ctx, j.Repo, j.Number, content); err != nil {
			slog.Warn("set review reaction", "job", j.ID, "error", err)
		}
	}
	targets := []int64{j.StatusComment}
	if j.TriggerComment != 0 {
		targets = append(targets, j.TriggerComment)
	}
	for _, id := range targets {
		if id == 0 {
			continue
		}
		if clear {
			if err := c.ClearEyes(ctx, j.Repo, id); err != nil {
				slog.Warn("clear reviewing reaction", "job", j.ID, "error", err)
			}
		}
		if content != "" {
			if err := c.Reaction(ctx, j.Repo, id, content); err != nil {
				slog.Warn("set review reaction", "job", j.ID, "error", err)
			}
		}
	}
}

// Run reconciles job state, produces the report, and publishes the review.
func (w Worker) Run(ctx context.Context, j *store.Job) error {
	ctx, timings := timing.New(ctx)
	if j.Status == "running" && !j.Next.IsZero() && !j.Updated.Before(j.Next) {
		timings.Add("queue_ready_wait", j.Next, j.Updated)
	}
	end := timing.Start(ctx, "job_attempt")
	defer func() {
		end()
		dir := w.Store.RunDir(j.ID)
		if err := os.MkdirAll(dir, 0700); err != nil {
			slog.Warn("create timing directory", "error", err)
			return
		}
		if err := store.WriteJSON(filepath.Join(dir, fmt.Sprintf("timings-%d.json", j.Attempts)), timings.Spans()); err != nil {
			slog.Warn("save timings", "error", err)
		}
	}()
	c := w.App.Installation(j.Installation)
	endReconcile := timing.Start(ctx, "github_reconcile")
	p, err := w.reconcile(ctx, c, j)
	endReconcile()
	if err != nil {
		return err
	}
	result, err := w.produce(ctx, c, j, p)
	if err != nil {
		return err
	}
	endPublish := timing.Start(ctx, "github_publish")
	defer endPublish()
	return w.publish(ctx, c, j, result)
}

// reconcile restores job state and shows progress before any expensive work.
// It returns the fetched PR after verifying the job still matches it.
func (w Worker) reconcile(ctx context.Context, c *github.Client, j *store.Job) (github.PR, error) {
	p, err := c.Pull(ctx, j.Repo, j.Number)
	if err != nil {
		return p, err
	}
	if p.State != "open" || p.Draft || (j.Head != "" && j.Head != p.Head.SHA) {
		return p, errStale
	}
	if !w.Config.AllowForks && p.Head.Repo.FullName != j.Repo {
		return p, errors.New("fork PR review disabled by operator")
	}
	if j.Base != "" && j.Base != p.Base.SHA {
		return p, errStale
	}
	if j.Head == "" || j.Base == "" {
		j.Head = p.Head.SHA
		j.Base = p.Base.SHA
		if err = w.Store.Save(j); err != nil {
			return p, err
		}
	}
	// Reconcile before any expensive execution or publication retry.
	published, err := c.HasReview(ctx, j.Repo, j.Number, j.ID)
	if err != nil {
		return p, err
	}
	if published {
		j.Published = true
	}
	if err = w.reconcileStatus(ctx, c, j); err != nil {
		return p, err
	}
	w.reactions(ctx, c, j, "eyes", false)
	return p, nil
}

// reconcileStatus restores or creates the status comment before review work.
// The first review of a PR only reacts; a retriggered review posts its own
// status comment, which the report then replaces in place.
func (w Worker) reconcileStatus(ctx context.Context, c *github.Client, j *store.Job) error {
	if j.StatusComment == 0 {
		id, err := c.FindComment(ctx, j.Repo, j.Number, statusMarker(j))
		if err != nil {
			return err
		}
		if id == 0 {
			prior, err := c.HasPriorReview(ctx, j.Repo, j.Number)
			if err != nil {
				return err
			}
			if prior {
				if id, err = c.Comment(ctx, j.Repo, j.Number, w.statusBody(j, watchingMessage)); err != nil {
					return err
				}
			}
		}
		j.StatusComment = id
		if err = w.Store.Save(j); err != nil {
			return err
		}
	}
	if j.StatusComment != 0 {
		if err := c.EditComment(ctx, j.Repo, j.StatusComment, w.statusBody(j, watchingMessage)); err != nil {
			return err
		}
	}
	return nil
}

// produce snapshots the head and merge-base trees and runs the review engine,
// unless a validated report from a prior attempt is already durable.
func (w Worker) produce(ctx context.Context, c *github.Client, j *store.Job, p github.PR) (report.Report, error) {
	dir := w.Store.RunDir(j.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return report.Report{}, err
	}
	var result report.Report
	if j.ReportReady {
		b, err := os.ReadFile(filepath.Join(dir, "report.json"))
		if err != nil {
			return result, err
		}
		if err = config.Decode(b, &result); err != nil {
			return result, err
		}
		return result, result.Validate()
	}
	endDiff := timing.Start(ctx, "github_diff")
	files, err := c.Files(ctx, j.Repo, j.Number)
	endDiff()
	if err != nil {
		return result, err
	}
	if len(files) != p.ChangedFiles {
		return result, errors.New("changed-file count mismatch; PR moved or GitHub truncated the diff")
	}
	endMergeBase := timing.Start(ctx, "github_merge_base")
	mergeBase, err := c.MergeBase(ctx, j.Repo, p.Base.SHA, p.Head.SHA)
	endMergeBase()
	if err != nil {
		return result, err
	}
	// Freeze API results only if GitHub still points at the same base and head.
	latest, err := c.Pull(ctx, j.Repo, j.Number)
	if err != nil {
		return result, err
	}
	if latest.Head.SHA != p.Head.SHA || latest.Base.SHA != p.Base.SHA {
		return result, errStale
	}
	for name, sha := range map[string]string{"head": p.Head.SHA, "base": mergeBase} {
		target := filepath.Join(dir, name)
		if err = os.RemoveAll(target); err != nil {
			return result, err
		}
		endSnapshot := timing.Start(ctx, "snapshot_"+name)
		err = c.Snapshot(ctx, j.Repo, sha, target)
		endSnapshot()
		if err != nil {
			return result, err
		}
	}
	meta := review.Context{Repo: j.Repo, Number: j.Number, Title: p.Title, Body: p.Body, Head: p.Head.SHA, Base: mergeBase}
	if meta.Coverage, err = review.SnapshotCoverage(filepath.Join(dir, "head"), filepath.Join(dir, "base"), files); err != nil {
		return result, err
	}
	if err = store.WriteJSON(filepath.Join(dir, "context.json"), meta); err != nil {
		return result, err
	}
	if err = store.WriteJSON(filepath.Join(dir, "files.json"), files); err != nil {
		return result, err
	}
	result, err = w.Engine.Run(ctx, dir, meta, files)
	if err != nil {
		return result, err
	}
	if err = store.WriteJSON(filepath.Join(dir, "report.json"), result); err != nil {
		return result, err
	}
	j.ReportReady = true
	return result, w.Store.Save(j)
}

// publish posts the review and final status once the PR is unchanged, then
// records the outcome as reactions.
func (w Worker) publish(ctx context.Context, c *github.Client, j *store.Job, result report.Report) error {
	if !j.Published {
		latest, err := c.Pull(ctx, j.Repo, j.Number)
		if err != nil {
			return err
		}
		if latest.State != "open" || latest.Draft || latest.Head.SHA != j.Head || latest.Base.SHA != j.Base {
			return errStale
		}
		if err = c.Review(ctx, j.Repo, j.Number, j.Head, result.Markdown(j.ID), result.Findings); err != nil {
			return err
		}
		j.Published = true
		if err = w.Store.Save(j); err != nil {
			return err
		}
	}
	emoji, content := "👎", "-1"
	if result.MergeReady() {
		emoji, content = "👍", "+1"
	}
	if j.StatusComment != 0 {
		message := fmt.Sprintf("%s Reviewed commit `%s` — merge confidence **%d/5**. See the review below.", emoji, j.Head[:12], *result.Confidence)
		if err := c.EditComment(ctx, j.Repo, j.StatusComment, w.statusBody(j, message)); err != nil {
			return err
		}
	}
	w.reactions(ctx, c, j, content, true)
	return nil
}
