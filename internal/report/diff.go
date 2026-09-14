package report

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type ChangedFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename,omitempty"`
	Status           string `json:"status"`
	Patch            string `json:"patch,omitempty"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
}
type Anchor struct {
	Path, Side string
	Line       int
}
type Diff struct {
	Lines      map[Anchor]int
	Files      map[string]bool
	Incomplete []string
}

var hunk = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func ParseDiff(files []ChangedFile) (Diff, error) {
	d := Diff{Lines: map[Anchor]int{}, Files: map[string]bool{}}
	group := 0
	for _, f := range files {
		if !SafePath(f.Filename) {
			return d, fmt.Errorf("invalid diff filename %q", f.Filename)
		}
		d.Files[f.Filename] = true
		if f.Patch == "" {
			if f.Additions+f.Deletions > 0 {
				d.Incomplete = append(d.Incomplete, f.Filename)
			}
			continue
		}
		scanner := bufio.NewScanner(strings.NewReader(f.Patch))
		scanner.Buffer(make([]byte, 4096), 4<<20)
		old, newLine := 0, 0
		oldLeft, newLeft := 0, 0
		for scanner.Scan() {
			s := scanner.Text()
			if m := hunk.FindStringSubmatch(s); m != nil {
				if oldLeft != 0 || newLeft != 0 {
					return d, fmt.Errorf("truncated patch for %s", f.Filename)
				}
				group++
				old, _ = strconv.Atoi(m[1])
				newLine, _ = strconv.Atoi(m[3])
				oldLeft = 1
				newLeft = 1
				if m[2] != "" {
					oldLeft, _ = strconv.Atoi(m[2])
				}
				if m[4] != "" {
					newLeft, _ = strconv.Atoi(m[4])
				}
				continue
			}
			if len(s) == 0 || s[0] == '\\' {
				continue
			}
			if old == 0 && newLine == 0 {
				return d, fmt.Errorf("patch without hunk for %s", f.Filename)
			}
			switch s[0] {
			case ' ':
				d.Lines[Anchor{f.Filename, "LEFT", old}] = group
				d.Lines[Anchor{f.Filename, "RIGHT", newLine}] = group
				old++
				newLine++
				oldLeft--
				newLeft--
			case '-':
				d.Lines[Anchor{f.Filename, "LEFT", old}] = group
				old++
				oldLeft--
			case '+':
				d.Lines[Anchor{f.Filename, "RIGHT", newLine}] = group
				newLine++
				newLeft--
			default:
				return d, fmt.Errorf("malformed patch for %s", f.Filename)
			}
			if oldLeft < 0 || newLeft < 0 {
				return d, fmt.Errorf("invalid hunk lengths for %s", f.Filename)
			}
		}
		if err := scanner.Err(); err != nil {
			return d, err
		}
		if oldLeft != 0 || newLeft != 0 {
			return d, fmt.Errorf("truncated patch for %s", f.Filename)
		}
	}
	return d, nil
}
func (d Diff) Validate(r Report) error {
	if len(d.Files) > 0 && len(r.ImportantFiles) == 0 {
		return fmt.Errorf("include at least one important changed file")
	}
	for _, f := range r.Findings {
		group, ok := d.Lines[Anchor{f.Path, f.Side, f.Line}]
		if !ok {
			return fmt.Errorf("finding %s:%d %s is outside the PR diff", f.Path, f.Line, f.Side)
		}
		start := f.StartLine
		if start == 0 {
			start = f.Line
		}
		for i := start; i <= f.Line; i++ {
			if d.Lines[Anchor{f.Path, f.Side, i}] != group {
				return fmt.Errorf("finding range crosses a diff hunk or side")
			}
		}
	}
	for _, f := range r.ImportantFiles {
		if !d.Files[f.Path] {
			return fmt.Errorf("important file %q is not changed", f.Path)
		}
	}
	return nil
}
