// Package report is the contract between untrusted harnesses and the publisher.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

type Finding struct {
	Path       string  `json:"path"`
	Line       int     `json:"line"`
	StartLine  int     `json:"start_line,omitempty"`
	Side       string  `json:"side"`
	Priority   int     `json:"priority"`
	Confidence float64 `json:"confidence"`
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	Evidence   string  `json:"evidence"`
}
type File struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}
type Report struct {
	Summary         string    `json:"summary"`
	Confidence      *int      `json:"confidence"`
	Reasons         []string  `json:"reasons"`
	ImportantFiles  []File    `json:"important_files"`
	SequenceDiagram string    `json:"sequence_diagram"`
	Findings        []Finding `json:"findings"`
}

func SafePath(p string) bool {
	return p != "" && p != "." && path.Clean(p) == p && !strings.HasPrefix(p, "/") && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\x00\r\n\\")
}
func (f Finding) Validate() error {
	if !SafePath(f.Path) || f.Line < 1 || f.StartLine < 0 || f.StartLine > f.Line || (f.StartLine > 0 && f.Line-f.StartLine > 10) {
		return errors.New("finding requires a relative path and a line range of at most 11 lines")
	}
	if f.Side != "LEFT" && f.Side != "RIGHT" {
		return errors.New("side must be LEFT or RIGHT")
	}
	if f.Priority < 1 || f.Priority > 4 || f.Confidence < 0 || f.Confidence > 1 {
		return errors.New("priority must be 1..4; confidence must be 0..1")
	}
	if strings.TrimSpace(f.Title) == "" || len(f.Title) > 140 || strings.ContainsAny(f.Title, "\r\n") || strings.TrimSpace(f.Body) == "" || len(f.Body) > 2000 || strings.TrimSpace(f.Evidence) == "" || len(f.Evidence) > 800 {
		return errors.New("title (<=140), body (<=2000) and concrete evidence (<=800) are required")
	}
	return nil
}
func (r Report) Validate() error {
	if strings.TrimSpace(r.Summary) == "" || len(r.Summary) > 2000 {
		return errors.New("summary is required (<=2000 bytes)")
	}
	if r.Confidence == nil || *r.Confidence < 0 || *r.Confidence > 5 {
		return errors.New("merge confidence is required (integer 0..5)")
	}
	if *r.Confidence < 5 && len(r.Reasons) == 0 {
		return errors.New("confidence below 5 requires reasons")
	}
	if len(r.Reasons) > 4 || len(r.ImportantFiles) > 30 || len(r.Findings) > 100 {
		return errors.New("report exceeds item limits (at most 4 reasons)")
	}
	for _, s := range r.Reasons {
		if strings.TrimSpace(s) == "" || len(s) > 320 {
			return errors.New("reasons must be nonempty and at most 320 bytes; keep each to one sentence")
		}
	}
	for _, f := range r.ImportantFiles {
		if !SafePath(f.Path) || strings.TrimSpace(f.Description) == "" || len(f.Description) > 240 {
			return errors.New("invalid important file (description <=240 bytes)")
		}
	}
	if len(r.SequenceDiagram) > 12000 || !strings.HasPrefix(strings.TrimSpace(r.SequenceDiagram), "sequenceDiagram\n") || strings.Contains(r.SequenceDiagram, "```") || strings.Contains(r.SequenceDiagram, "%%{") {
		return errors.New("provide a sequenceDiagram without fences or Mermaid directives")
	}
	for _, f := range r.Findings {
		if err := f.Validate(); err != nil {
			return err
		}
		if f.Priority <= 2 && *r.Confidence >= 4 {
			return errors.New("P1/P2 findings are incompatible with merge confidence >=4")
		}
	}
	if len(r.Markdown(strings.Repeat("0", 32))) > 60000 {
		return errors.New("rendered overview exceeds 60000 bytes; condense the report")
	}
	return nil
}
func (f Finding) ID() string {
	b := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%s", f.Path, f.Side, f.Line, strings.ToLower(strings.TrimSpace(f.Title)))))
	return hex.EncodeToString(b[:])[:16]
}

// AddReason reserves space for server explanations without invalidating an
// otherwise valid report. New explanations precede the model's existing reasons.
func (r *Report) AddReason(reason string) {
	if len(reason) > 320 {
		reason = reason[:317]
		for !utf8.ValidString(reason) {
			reason = reason[:len(reason)-1]
		}
		reason += "…"
	}
	r.Reasons = append([]string{reason}, r.Reasons[:min(len(r.Reasons), 3)]...)
}
func Normalize(r *Report, min float64, max int) {
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	seen := map[string]bool{}
	out := []Finding{}
	for _, f := range r.Findings {
		if f.Confidence < min || seen[f.ID()] {
			continue
		}
		seen[f.ID()] = true
		out = append(out, f)
	}
	if len(out) > max {
		out = out[:max]
		r.AddReason("Additional validated issues were omitted by the operator's comment limit; inspect the stored report.")
		if *r.Confidence > 3 {
			n := 3
			r.Confidence = &n
		}
	}
	r.Findings = out
}
func (r Report) MergeReady() bool {
	if r.Confidence == nil || *r.Confidence < 4 {
		return false
	}
	for _, f := range r.Findings {
		if f.Priority <= 2 {
			return false
		}
	}
	return true
}

// Neutralize accidental mentions and HTML while preserving useful Markdown.
func prose(s string) string {
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return strings.ReplaceAll(s, "@", "@\u200b")
}
func (f Finding) Markdown() string {
	return fmt.Sprintf("**P%d · %s**\n\n%s\n\n**Evidence:** %s\n\n<!-- reviewd-finding:%s -->", f.Priority, prose(f.Title), prose(f.Body), prose(f.Evidence), f.ID())
}
func (r Report) Markdown(marker string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- reviewd:%s -->\n## Summary\n\n%s\n\n## Key issues found\n\n", marker, prose(r.Summary))
	if len(r.Findings) == 0 {
		b.WriteString("No high-confidence actionable issues found.\n")
	}
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "- **P%d** %s (%s:%d, %s)\n", f.Priority, prose(f.Title), prose(f.Path), f.Line, f.Side)
	}
	fmt.Fprintf(&b, "\n## Confidence score: %d/5\n\n", *r.Confidence)
	if r.MergeReady() {
		b.WriteString("👍 Appears safe to merge.\n")
	} else {
		b.WriteString("👎 Not merge ready.\n")
	}
	for _, s := range r.Reasons {
		fmt.Fprintf(&b, "- %s\n", prose(s))
	}
	b.WriteString("\n<details>\n<summary>Important files changed</summary>\n\n")
	for _, f := range r.ImportantFiles {
		fmt.Fprintf(&b, "- **%s** — %s\n", prose(f.Path), prose(f.Description))
	}
	b.WriteString("\n</details>\n\n<details>\n<summary>Relevant sequence diagram</summary>\n\n```mermaid\n")
	b.WriteString(r.SequenceDiagram)
	b.WriteString("\n```\n\n</details>\n\n*Reviewed by reviewd. Confidence reflects review evidence, not a guarantee.*\n")
	return b.String()
}
