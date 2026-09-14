package benchmark

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"reviewd/internal/report"
)

// ContextPack is bounded navigation assistance, never a claim of full coverage.
// Go declarations and call sites come from the standard parser. Name matches
// are intentionally not represented as type-resolved edges.
func ContextPack(root string, files []report.ChangedFile, structural bool) string {
	const limit = 64000
	var out strings.Builder
	appendText := func(s string) {
		remaining := limit - out.Len()
		if remaining > 0 {
			out.WriteString(s[:min(len(s), remaining)])
		}
	}
	appendText("\nCHANGED PATCHES\n")
	changed := map[string]bool{}
	for _, f := range files {
		changed[f.Filename] = true
		patch := f.Patch
		if structural {
			room := max(0, 24000-out.Len())
			if len(patch) > min(2000, room) {
				patch = patch[:min(2000, room)] + "\n[PATCH TRUNCATED; read files.json]"
			}
		}
		appendText(fmt.Sprintf("\n%s\n%s\n", f.Filename, patch))
	}
	if !structural {
		appendText("\nCHANGED FILE CONTENTS\n")
		for _, f := range files {
			if !report.SafePath(f.Filename) {
				continue
			}
			path := filepath.Join(root, f.Filename)
			info, e := os.Stat(path)
			if e != nil || info.Size() > 1<<20 {
				appendText("[FILE CONTENT OMITTED: over 1 MiB or unavailable]\n")
				continue
			}
			b, e := os.ReadFile(path)
			if e == nil {
				appendText(fmt.Sprintf("\n%s\n%s\n", f.Filename, b))
			}
		}
	} else {
		appendText("\nGO AST DECLARATIONS AND SAME-NAME CALL SITES (not type resolved)\n")
		type parsed struct {
			path string
			tree *ast.File
			set  *token.FileSet
		}
		parsedFiles := []parsed{}
		names := map[string]bool{}
		scanned := 0
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && path != root || d.Name() == "vendor" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || scanned >= 10000 {
				return nil
			}
			scanned++
			stat, e := d.Info()
			if e != nil || stat.Size() > 1<<20 {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			set := token.NewFileSet()
			tree, e := parser.ParseFile(set, path, nil, 0)
			if e != nil {
				return nil
			}
			parsedFiles = append(parsedFiles, parsed{rel, tree, set})
			if changed[rel] {
				for _, decl := range tree.Decls {
					if f, ok := decl.(*ast.FuncDecl); ok {
						names[f.Name.Name] = true
						appendText(fmt.Sprintf("declaration %s:%d %s\n", rel, set.Position(f.Pos()).Line, f.Name.Name))
					}
				}
			}
			return nil
		})
		for _, p := range parsedFiles {
			ast.Inspect(p.tree, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch f := call.Fun.(type) {
				case *ast.Ident:
					name = f.Name
				case *ast.SelectorExpr:
					name = f.Sel.Name
				}
				if names[name] {
					appendText(fmt.Sprintf("call %s:%d %s\n", p.path, p.set.Position(call.Pos()).Line, name))
				}
				return true
			})
		}
		appendText("\nNEIGHBOR FILES (navigation only; non-Go has no AST support)\n")
		dirs := map[string]bool{}
		for path := range changed {
			dirs[filepath.Dir(path)] = true
		}
		sorted := []string{}
		for dir := range dirs {
			sorted = append(sorted, dir)
		}
		sort.Strings(sorted)
		for _, dir := range sorted {
			if dir != "." && !report.SafePath(dir) {
				continue
			}
			entries, _ := os.ReadDir(filepath.Join(root, dir))
			for _, e := range entries {
				if !e.IsDir() {
					appendText(filepath.Join(dir, e.Name()) + "\n")
				}
			}
		}
	}
	if out.Len() >= limit {
		out.WriteString("\n[CONTEXT TRUNCATED at 64000 bytes; use files.json and source for complete context]\n")
	}
	return out.String()
}
