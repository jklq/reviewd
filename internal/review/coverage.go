package review

import (
	"fmt"
	"io"
	"os"
	"strings"

	"reviewd/internal/report"
)

// SnapshotCoverage only flags unavailable content touched by this PR. Merely
// having submodules elsewhere in the repository does not lower merge confidence.
func SnapshotCoverage(head, base string, files []report.ChangedFile) ([]string, error) {
	roots := map[string]*os.Root{}
	for name, dir := range map[string]string{"head": head, "base": base} {
		r, err := os.OpenRoot(dir)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		roots[name] = r
	}
	var gaps []string
	for _, changed := range files {
		if !report.SafePath(changed.Filename) {
			return nil, fmt.Errorf("unsafe changed filename")
		}
		name, p := "head", changed.Filename
		if changed.Status == "removed" {
			name = "base"
		}
		f, err := roots[name].Open(p)
		if os.IsNotExist(err) {
			gaps = append(gaps, "Changed content is absent from the snapshot (possibly a submodule): "+p)
			continue
		}
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, err
		}
		if !info.Mode().IsRegular() {
			f.Close()
			gaps = append(gaps, "Changed path is not an ordinary file: "+p)
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, 256))
		f.Close()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(string(b), "version https://git-lfs.github.com/spec/v1\n") {
			gaps = append(gaps, "Changed LFS content is a pointer, not hydrated source: "+p)
		}
	}
	return gaps, nil
}
