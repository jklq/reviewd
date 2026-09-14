package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"reviewd/internal/config"
	"reviewd/internal/report"
	"reviewd/internal/store"
)

func readJSON(path string, v any) error { return readJSONLimit(path, v, 1<<20) }
func readJSONLimit(path string, v any, limit int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return err
	}
	if int64(len(b)) > limit {
		return fmt.Errorf("JSON input exceeds %d bytes", limit)
	}
	return config.Decode(b, v)
}
func agent(args []string) error {
	if len(args) == 0 || args[0] == "help" {
		fmt.Print(report.Instructions)
		return nil
	}
	output := os.Getenv("REVIEWD_OUTPUT")
	if output == "" {
		output = ".reviewd-output"
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		return err
	}
	draft := filepath.Join(output, "draft.json")
	fs := flag.NewFlagSet("agent "+args[0], flag.ContinueOnError)
	file := fs.String("file", "", "JSON input file")
	var f report.Finding
	if args[0] == "finding" {
		fs.StringVar(&f.Path, "path", "", "exact changed filename")
		fs.IntVar(&f.Line, "line", 0, "last line (inclusive)")
		fs.IntVar(&f.StartLine, "start-line", 0, "first line (optional)")
		fs.StringVar(&f.Side, "side", "RIGHT", "LEFT or RIGHT")
		fs.IntVar(&f.Priority, "priority", 0, "1 critical, 2 high, 3 moderate, 4 low")
		fs.Float64Var(&f.Confidence, "confidence", 0, "probability that this defect is real (0..1)")
		fs.StringVar(&f.Title, "title", "", "concise imperative title")
		fs.StringVar(&f.Body, "body", "", "trigger, consequence and remedy")
		fs.StringVar(&f.Evidence, "evidence", "", "code/test evidence")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	var r report.Report
	if args[0] == "export" {
		if err := readJSON(filepath.Join(output, "report.json"), &r); err != nil {
			return err
		}
		if err := r.Validate(); err != nil {
			return err
		}
		return printJSON(r)
	}
	if args[0] == "init" {
		_ = os.Remove(filepath.Join(output, "report.json"))
		return store.WriteJSON(draft, r)
	}
	if args[0] == "submit" && *file != "" {
		if err := readJSON(*file, &r); err != nil {
			return err
		}
	} else {
		if err := readJSON(draft, &r); err != nil {
			return fmt.Errorf("read draft (run agent init first): %w", err)
		}
	}
	switch args[0] {
	case "finding":
		if err := f.Validate(); err != nil {
			return err
		}
		r.Findings = append(r.Findings, f)
		return store.WriteJSON(draft, r)
	case "overview":
		if *file == "" {
			return errors.New("overview requires --file")
		}
		var overview report.Report
		if err := readJSON(*file, &overview); err != nil {
			return err
		}
		if len(overview.Findings) > 0 {
			return errors.New("overview may not contain findings; use submit --file for a whole report")
		}
		if err := report.ValidateSequenceDiagram(overview.SequenceDiagram); err != nil {
			return err
		}
		overview.Findings = r.Findings
		return store.WriteJSON(draft, overview)
	case "show":
		return printJSON(r)
	case "submit":
		if err := r.Validate(); err != nil {
			return err
		}
		if input := os.Getenv("REVIEWD_INPUT"); input != "" {
			var files []report.ChangedFile
			if err := readJSONLimit(filepath.Join(input, "files.json"), &files, 32<<20); err != nil {
				return err
			}
			d, err := report.ParseDiff(files)
			if err != nil {
				return err
			}
			if err = d.Validate(r); err != nil {
				return err
			}
		}
		if err := store.WriteJSON(filepath.Join(output, "report.json"), r); err != nil {
			return err
		}
		fmt.Println("Report submitted.")
		return nil
	default:
		return fmt.Errorf("unknown agent command %q; use reviewd agent help", args[0])
	}
}
