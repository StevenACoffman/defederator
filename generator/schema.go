package generator

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
	"github.com/vektah/gqlparser/v2/parser"
)

// isFederationDirectiveName reports whether name is a federation-only directive.
func isFederationDirectiveName(name string) bool {
	switch name {
	case "join__type", "join__field", "join__graph", "join__implements",
		"join__unionMember", "join__enumValue", "link", "tag", "inaccessible",
		"override", "shareable", "external", "requires", "provides",
		"composeDirective", "interfaceObject", "authenticated", "requiresScopes",
		"policy":
		return true
	}
	return false
}

// isFederationTypeName reports whether name is a type that exists only in the
// federation supergraph.
func isFederationTypeName(name string) bool {
	switch name {
	case "join__FieldSet", "link__Import", "join__Graph", "link__Purpose",
		"_Service", "_Entity", "_Any", "_FieldSet":
		return true
	}
	return false
}

// StripFederationTypes parses a Federation v2 supergraph SDL and returns a clean SDL
// containing only the user-visible types and fields, without any federation metadata.
func StripFederationTypes(sdl string) (string, error) {
	doc, err := parser.ParseSchema(&ast.Source{Input: sdl, Name: "supergraph"})
	if err != nil {
		return "", fmt.Errorf("generator: parse supergraph SDL: %w", err)
	}

	// Filter directive definitions (stored in doc.Directives, not doc.Definitions).
	filteredDirs := make(ast.DirectiveDefinitionList, 0, len(doc.Directives))
	for _, dd := range doc.Directives {
		if !isFederationDirectiveDef(dd.Name) {
			filteredDirs = append(filteredDirs, dd)
		}
	}
	doc.Directives = filteredDirs

	// Filter type definitions.
	filteredDefs := make(ast.DefinitionList, 0, len(doc.Definitions))
	for _, def := range doc.Definitions {
		if shouldDropDefinition(def) {
			continue
		}
		stripFederationDirectivesFromDef(def)
		filteredDefs = append(filteredDefs, def)
	}
	doc.Definitions = filteredDefs

	// Strip @link directives from the schema block, keep root operation assignments.
	for i, sd := range doc.Schema {
		clean := make(ast.DirectiveList, 0, len(sd.Directives))
		for _, d := range sd.Directives {
			if !isFederationDirective(d.Name) {
				clean = append(clean, d)
			}
		}
		doc.Schema[i].Directives = clean
	}

	var buf bytes.Buffer
	formatter.NewFormatter(&buf).FormatSchemaDocument(doc)
	return buf.String(), nil
}

func isFederationDirectiveDef(name string) bool {
	return isFederationDirectiveName(name) ||
		strings.HasPrefix(name, "join__") ||
		strings.HasPrefix(name, "link__")
}

func isFederationDirective(name string) bool {
	return isFederationDirectiveName(name) ||
		strings.HasPrefix(name, "join__") ||
		strings.HasPrefix(name, "link__")
}

func shouldDropDefinition(def *ast.Definition) bool {
	if isFederationTypeName(def.Name) {
		return true
	}
	return strings.HasPrefix(def.Name, "join__") || strings.HasPrefix(def.Name, "link__")
}

func stripFederationDirectivesFromDef(def *ast.Definition) {
	def.Directives = filterDirectives(def.Directives)
	for _, f := range def.Fields {
		f.Directives = filterDirectives(f.Directives)
	}
	for _, ev := range def.EnumValues {
		ev.Directives = filterDirectives(ev.Directives)
	}
}

func filterDirectives(dirs ast.DirectiveList) ast.DirectiveList {
	if len(dirs) == 0 {
		return dirs
	}
	out := make(ast.DirectiveList, 0, len(dirs))
	for _, d := range dirs {
		if !isFederationDirective(d.Name) {
			out = append(out, d)
		}
	}
	return out
}
