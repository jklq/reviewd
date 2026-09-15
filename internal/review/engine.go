// Package review orchestrates independent review passes and a final validator,
// except a lone reviewer is already the final pass.
package review

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/jklq/reviewd/internal/config"
	"github.com/jklq/reviewd/internal/report"
	"github.com/jklq/reviewd/internal/sandbox"
	"github.com/jklq/reviewd/internal/store"
)

type Context struct {
	Repo          string   `json:"repo"`
	Number        int      `json:"number"`
	Title         string   `json:"title"`
	Body          string   `json:"body"`
	Head          string   `json:"head"`
	Base          string   `json:"merge_base"`
	Additions     int      `json:"additions"`
	Deletions     int      `json:"deletions"`
	ChangedFiles  int      `json:"changed_files"`
	SizeTier      string   `json:"size_tier,omitempty"`
	Role          string   `json:"role"`
	Index         int      `json:"index"`
	Total         int      `json:"total"`
	Harness       string   `json:"harness,omitempty"`
	Model         string   `json:"model,omitempty"`
	MinConfidence float64  `json:"min_finding_confidence"`
	Coverage      []string `json:"coverage_limitations,omitempty"`
}
type Engine struct {
	Config config.Config
	Runner sandbox.Runner
}

func (eng Engine) Run(ctx context.Context, dir string, meta Context, files []report.ChangedFile) (report.Report, error) {
	diff, err := report.ParseDiff(files)
	if err != nil {
		return report.Report{}, err
	}
	meta.MinConfidence = eng.Config.MinFindingConfidence
	// Size tiers route on the PR's changed lines and file count. The file
	// list corroborates the PR counts when the API omits them.
	lines := meta.Additions + meta.Deletions
	if lines <= 0 {
		for _, f := range files {
			lines += f.Additions + f.Deletions
		}
	}
	changed := len(files)
	if changed == 0 {
		changed = meta.ChangedFiles
	}
	sel := eng.Config.Select(lines, changed)
	meta.SizeTier = sel.Tier
	meta.Total = sel.Parallelism
	omitted, err := OmittedPatches(filepath.Join(dir, "head"), filepath.Join(dir, "base"), files, diff.Incomplete)
	if err != nil {
		return report.Report{}, err
	}
	meta.Coverage = append(meta.Coverage, omitted...)
	candidates := make([]report.Report, sel.Parallelism)
	errs := make([]error, sel.Parallelism)
	var wg sync.WaitGroup
	reviewCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := range candidates {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := sel.Reviewers[i%len(sel.Reviewers)]
			candidates[i], errs[i] = eng.runSlot(reviewCtx, diff, dir, meta, files, "reviewer", i, name, nil)
			if errs[i] != nil {
				cancel()
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return report.Report{}, fmt.Errorf("reviewer %d: %w", i, err)
		}
	}
	if err = store.WriteJSON(filepath.Join(dir, "candidates.json"), candidates); err != nil {
		return report.Report{}, err
	}
	var result report.Report
	if sel.Parallelism == 1 {
		result = candidates[0]
	} else {
		var err error
		result, err = eng.runSlot(ctx, diff, dir, meta, files, "validator", 0, sel.Validator, candidates)
		if err != nil {
			return result, fmt.Errorf("validator: %w", err)
		}
	}
	if err = diff.Validate(result); err != nil {
		return result, err
	}
	if len(meta.Coverage) > 0 {
		result.AddReason(fmt.Sprintf("These changes could not be fully reviewed (%d coverage limitations); see the stored reviewer context.json for details.", len(meta.Coverage)))
		if *result.Confidence > 3 {
			n := 3
			result.Confidence = &n
		}
	}
	if err = store.WriteJSON(filepath.Join(dir, "validated.json"), result); err != nil {
		return result, err
	}
	report.Normalize(&result, eng.Config.MinFindingConfidence, eng.Config.MaxFindings)
	if err = result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

// runSlot runs a reviewer or validator slot on its selected harness, then on
// the configured fallbacks in order, until one returns a report valid for this
// diff. Attempts share the job deadline; each keeps its own work directory so a
// failed provider's inputs, log and output remain inspectable.
func (eng Engine) runSlot(ctx context.Context, diff report.Diff, dir string, meta Context, files []report.ChangedFile, role string, index int, name string, candidates []report.Report) (report.Report, error) {
	chain := eng.Config.Chain(name)
	failures := make([]error, 0, len(chain))
	for attempt, harness := range chain {
		work := filepath.Join(dir, slotDir(role, index, harness, attempt))
		result, err := eng.runOne(ctx, dir, work, meta, files, role, index, harness, candidates)
		if err == nil {
			err = diff.Validate(result)
		}
		if err == nil {
			return result, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", harness, err))
		if attempt+1 == len(chain) || ctx.Err() != nil {
			break
		}
		slog.Warn("harness failed; trying fallback", "role", role, "index", index, "harness", harness, "fallback", chain[attempt+1], "error", err)
	}
	return report.Report{}, errors.Join(failures...)
}

// slotDir names one attempt's work directory. The primary attempt keeps the
// historical <role>-<index> name; fallback attempts add their harness.
func slotDir(role string, index int, harness string, attempt int) string {
	if attempt == 0 {
		return fmt.Sprintf("%s-%d", role, index)
	}
	return fmt.Sprintf("%s-%d-fallback-%s", role, index, harness)
}

func (eng Engine) runOne(ctx context.Context, dir, work string, meta Context, files []report.ChangedFile, role string, index int, harness string, candidates []report.Report) (report.Report, error) {
	if err := os.RemoveAll(work); err != nil {
		return report.Report{}, err
	}
	input := filepath.Join(work, "input")
	output := filepath.Join(work, "output")
	if err := os.MkdirAll(filepath.Join(input, "base"), 0700); err != nil {
		return report.Report{}, err
	}
	meta.Role = role
	meta.Index = index
	meta.Harness = harness
	meta.Model = eng.Config.Harnesses[harness].Model
	for name, v := range map[string]any{"context.json": meta, "files.json": files, "candidates.json": candidates} {
		if err := store.WriteJSON(filepath.Join(input, name), v); err != nil {
			return report.Report{}, err
		}
	}
	for name, s := range map[string]string{"AGENTS.md": report.Instructions, "policy.md": eng.Config.Policy} {
		if err := os.WriteFile(filepath.Join(input, name), []byte(s), 0600); err != nil {
			return report.Report{}, err
		}
	}
	prompt := fmt.Sprintf("Read /review/AGENTS.md and follow its full review protocol. You are %s %d of %d. Review /review/files.json against /workspace and /review/base. Read context and trusted policy in /review. Set the overview harness and model fields to the harness and model you are running as, from /review/context.json or your own configuration, and report the actual model if it differs. Work directly: do not spawn subagents or delegate to other agents. Keep the report terse: a one-paragraph summary, at most three one-sentence confidence reasons and short finding bodies. Submit your complete report with reviewd agent submit. Do not stop at a chat response.", role, index+1, meta.Total)
	if role == "validator" {
		prompt += " Independently validate and semantically deduplicate /review/candidates.json; report only substantiated findings and reassess merge confidence."
	}
	result, err := eng.Runner.Run(ctx, sandbox.Request{Harness: eng.Config.Harnesses[harness], Source: filepath.Join(dir, "head"), Base: filepath.Join(dir, "base"), Input: input, Output: output, Role: role, Index: index, Prompt: prompt})
	if err != nil {
		return result, err
	}
	// The server knows the configured harness and declared model even when the
	// agent does not report its own; keep the operator's values as the fallback.
	if result.Harness == "" {
		result.Harness = harness
	}
	if result.Model == "" {
		result.Model = eng.Config.Harnesses[harness].Model
	}
	return result, nil
}
