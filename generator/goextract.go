package generator

import (
	"fmt"
	goAst "go/ast"
	goParser "go/parser"
	goToken "go/token"
	"io"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// embeddedQuery is a GraphQL operation string extracted from a Go source file,
// paired with the source position of its containing string literal.
type embeddedQuery struct {
	text string
	// source is "filename:line" — used as the ast.Source.Name so that gqlparser
	// error messages point back to the Go file, not to a synthetic name.
	source string
}

// extractQueriesFromGoFile parses a Go source file and returns all GraphQL
// operation strings embedded in string literals whose first non-blank line is
// "# @genqlient". The annotation acts as an opt-in marker so that arbitrary
// string constants in Go files are not mistakenly treated as GraphQL.
//
// Per-literal and per-match progress lines are written to log. Pass io.Discard
// for silence.
func extractQueriesFromGoFile(filename string, log io.Writer) ([]embeddedQuery, error) {
	fset := goToken.NewFileSet()
	f, err := goParser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("generator: parse %q: %w", filename, err)
	}

	var queries []embeddedQuery
	var walkErr error
	goAst.Inspect(f, func(node goAst.Node) bool {
		if walkErr != nil {
			return false
		}
		lit, ok := node.(*goAst.BasicLit)
		if !ok || lit.Kind != goToken.STRING {
			return true
		}
		_, _ = fmt.Fprintf(log, "Checking literal in %s\n", filename)
		pos := fset.Position(lit.Pos())
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			walkErr = fmt.Errorf("%s:%d: %w", pos.Filename, pos.Line, err)
			return false
		}
		if strings.HasPrefix(strings.TrimSpace(value), "# @genqlient") {
			_, _ = fmt.Fprintf(log, "Found query in %s\n", filename)
			queries = append(queries, embeddedQuery{
				text:   value,
				source: fmt.Sprintf("%s:%d", pos.Filename, pos.Line),
			})
		}
		return true
	})
	return queries, walkErr
}

// QuerySourcesFromGoFile parses a Go source file and returns one
// gqlparser ast.Source per embedded `# @genqlient` query string. Each Source
// has its Name set to "filename:line" so downstream gqlparser errors point at
// the original Go file. Exported for use by callers outside the generator
// package (e.g. migrate's operation-pruning helpers).
//
// Per-literal and per-match progress lines are written to log. Pass io.Discard
// for silence.
func QuerySourcesFromGoFile(filename string, log io.Writer) ([]*ast.Source, error) {
	embedded, err := extractQueriesFromGoFile(filename, log)
	if err != nil {
		return nil, err
	}
	out := make([]*ast.Source, len(embedded))
	for i, eq := range embedded {
		out[i] = &ast.Source{Name: eq.source, Input: eq.text}
	}
	return out, nil
}
