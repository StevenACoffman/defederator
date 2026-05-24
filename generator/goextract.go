package generator

import (
	"fmt"
	goAst "go/ast"
	goParser "go/parser"
	goToken "go/token"
	"strconv"
	"strings"
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
func extractQueriesFromGoFile(filename string) ([]embeddedQuery, error) {
	fset := goToken.NewFileSet()
	f, err := goParser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		return nil, err
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
		pos := fset.Position(lit.Pos())
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			walkErr = fmt.Errorf("%s:%d: %w", pos.Filename, pos.Line, err)
			return false
		}
		if strings.HasPrefix(strings.TrimSpace(value), "# @genqlient") {
			queries = append(queries, embeddedQuery{
				text:   value,
				source: fmt.Sprintf("%s:%d", pos.Filename, pos.Line),
			})
		}
		return true
	})
	return queries, walkErr
}
