// Package check detects orphaned genqlient call sites in a service directory.
//
// An orphan is a `genqlient.<Op>(...)` call whose operation has no matching
// `# @genqlient` annotation block declaring it in any of the .go files
// genqlient.yaml's `operations:` field globs. The genqlient generator picks up
// operations only from those annotation blocks, so an orphan compiles only
// until the next `make genqlient` run drops the unbacked operation from the
// generated client. This pattern arose in the ai-guide and donations services
// after a migration removed annotations but left call sites; this package
// exists to surface that class of regression at lint time, not at the next
// regen.
package check

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/StevenACoffman/defederator/config"
)

// annotationOpNameRE matches the operation name on a
// `<query|mutation|subscription> <Op>` line inside a `# @genqlient` block.
// Leading whitespace is tolerated so the regex works regardless of how the
// block is indented inside the Go source.
var annotationOpNameRE = regexp.MustCompile(
	`(?m)^\s*(?:query|mutation|subscription)\s+([A-Za-z_][A-Za-z0-9_]*)`,
)

// Orphan describes a `genqlient.<Op>(...)` call site whose Op is not declared
// by any `# @genqlient` annotation in the scanned files.
type Orphan struct {
	Operation string // e.g. "AiGuide_AllSetsOfStandards"
	File      string // path relative to the service directory
	Line      int    // 1-based source line of the call site
}

// call is the internal record for one genqlient call site. It carries the
// absolute file path; Run converts it to a service-relative path for the
// public Orphan struct.
type call struct {
	Operation string
	File      string
	Line      int
}

// Run scans the service directory at dir for orphaned genqlient calls and
// returns them in source order (by file, then by line). dir must contain a
// `genqlient.yaml` whose `operations:` field globs the .go files to scan.
//
// Returns an empty (non-nil) slice when dir is clean. Returns a non-nil error
// only for IO/parse failures — a service with orphans returns (orphans, nil)
// and the caller decides whether to treat that as a failure or a report.
func Run(dir string) ([]Orphan, error) {
	yamlPath := filepath.Join(dir, "genqlient.yaml")
	cfg, err := config.LoadGenqlientConfig(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("check: load %s: %w", yamlPath, err)
	}

	files, err := globOperations(cfg.Query, dir)
	if err != nil {
		return nil, fmt.Errorf("check: glob operations: %w", err)
	}

	var calls []call
	declared := make(map[string]struct{})
	for _, file := range files {
		fileCalls, fileDeclared, err := scanFile(file)
		if err != nil {
			return nil, fmt.Errorf("check: %w", err)
		}
		calls = append(calls, fileCalls...)
		for op := range fileDeclared {
			declared[op] = struct{}{}
		}
	}

	orphans := findOrphans(calls, declared)
	out := make([]Orphan, len(orphans))
	for i, c := range orphans {
		rel, err := filepath.Rel(dir, c.File)
		if err != nil {
			rel = c.File
		}
		out[i] = Orphan{Operation: c.Operation, File: rel, Line: c.Line}
	}
	return out, nil
}

// findOrphans returns calls whose Operation is not in declared, sorted by
// (file, line). Pure — no I/O.
func findOrphans(calls []call, declared map[string]struct{}) []call {
	out := make([]call, 0)
	for _, c := range calls {
		if _, ok := declared[c.Operation]; ok {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		// Operation tiebreaker: two genqlient calls on the same line is
		// unusual but happens (one-liner chains). A deterministic tiebreaker
		// keeps the output reproducible for tests and diff-friendly for CI.
		return out[i].Operation < out[j].Operation
	})
	return out
}

// scanFile parses one .go file and returns its genqlient call sites plus the
// set of operations declared via `# @genqlient` annotation blocks in that
// file.
func scanFile(path string) ([]call, map[string]struct{}, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var calls []call
	declared := make(map[string]struct{})
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if c, ok := extractCall(fset, path, x); ok {
				calls = append(calls, c)
			}
		case *ast.BasicLit:
			for _, name := range extractDeclared(x) {
				declared[name] = struct{}{}
			}
		}
		return true
	})
	return calls, declared, nil
}

// extractCall returns the call record for a `genqlient.<Op>(...)` expression,
// or ok=false for any other call shape.
//
// Go AST cannot distinguish a function call `genqlient.AiGuide_Foo(ctx, ...)`
// from a type conversion `genqlient.DistrictUserTypeFilter(value)` — both are
// `*ast.CallExpr` with the same shape. We rely on the genqlient generator's
// naming convention: operation functions are always `<ServicePrefix>_<Op>`
// (underscore-separated), while the package's exported types are plain
// CamelCase. A `genqlient.X(...)` invocation where X contains no underscore
// is therefore a type conversion (or const), not an operation call, and is
// skipped to avoid false positives in the orphan report.
func extractCall(fset *token.FileSet, path string, c *ast.CallExpr) (call, bool) {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return call{}, false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || ident.Name != "genqlient" {
		return call{}, false
	}
	op := sel.Sel.Name
	if !strings.Contains(op, "_") {
		return call{}, false
	}
	pos := fset.Position(c.Pos())
	return call{
		Operation: op,
		File:      path,
		Line:      pos.Line,
	}, true
}

// extractDeclared returns the operation names a `# @genqlient` annotation
// block declares. Returns nil for any string literal that is not such a
// block (the common case for arbitrary code literals).
//
// A non-@genqlient literal is also possible — for example, query files in
// .graphql syntax embedded as string constants — but only @genqlient-prefixed
// blocks declare operations the genqlient generator consumes.
func extractDeclared(lit *ast.BasicLit) []string {
	if lit.Kind != token.STRING {
		return nil
	}
	text, err := strconv.Unquote(lit.Value)
	if err != nil {
		// The file already parsed, so an unquote failure is structurally
		// impossible. Skip defensively rather than failing the whole scan.
		return nil
	}
	if !strings.HasPrefix(strings.TrimSpace(text), "# @genqlient") {
		return nil
	}
	matches := annotationOpNameRE.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// globOperations expands the operations glob patterns against baseDir and
// returns the matching .go files. Patterns are interpreted relative to
// baseDir; non-Go matches are skipped (a genqlient.yaml may legitimately glob
// .graphql files for the migrate path, but only .go files carry annotations
// and call sites).
func globOperations(patterns []string, baseDir string) ([]string, error) {
	var out []string
	for _, pat := range patterns {
		full := pat
		if !filepath.IsAbs(full) {
			full = filepath.Join(baseDir, full)
		}
		base, p := doublestar.SplitPattern(filepath.ToSlash(full))
		matches, err := doublestar.Glob(os.DirFS(base), p, doublestar.WithFilesOnly())
		if err != nil {
			return nil, fmt.Errorf("glob %q: %w", pat, err)
		}
		for _, m := range matches {
			abs := filepath.Join(base, m)
			if filepath.Ext(abs) != ".go" {
				continue
			}
			out = append(out, abs)
		}
	}
	return out, nil
}
