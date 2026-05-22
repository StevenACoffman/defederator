package generator

import (
	"bytes"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
	"github.com/vektah/gqlparser/v2/parser"
)

// federationDirectiveNames is the set of directive names that are federation-only metadata.
var federationDirectiveNames = map[string]bool{
	"join__type":        true,
	"join__field":       true,
	"join__graph":       true,
	"join__implements":  true,
	"join__unionMember": true,
	"join__enumValue":   true,
	"link":              true,
	"tag":               true,
	"inaccessible":      true,
	"override":          true,
	"shareable":         true,
	"external":          true,
	"requires":          true,
	"provides":          true,
	"composeDirective":  true,
	"interfaceObject":   true,
	"authenticated":     true,
	"requiresScopes":    true,
	"policy":            true,
}

// federationTypeNames is the set of type names that exist only in the federation supergraph.
var federationTypeNames = map[string]bool{
	"join__FieldSet": true,
	"link__Import":   true,
	"join__Graph":    true,
	"link__Purpose":  true,
	"_Service":       true,
	"_Entity":        true,
	"_Any":           true,
	"_FieldSet":      true,
}

// StripFederationTypes parses a Federation v2 supergraph SDL and returns a clean SDL
// containing only the user-visible types and fields, without any federation metadata.
func StripFederationTypes(sdl string) (string, error) {
	doc, err := parser.ParseSchema(&ast.Source{Input: sdl, Name: "supergraph"})
	if err != nil {
		return "", err
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
	return federationDirectiveNames[name] ||
		strings.HasPrefix(name, "join__") ||
		strings.HasPrefix(name, "link__")
}

func isFederationDirective(name string) bool {
	return federationDirectiveNames[name] ||
		strings.HasPrefix(name, "join__") ||
		strings.HasPrefix(name, "link__")
}

func shouldDropDefinition(def *ast.Definition) bool {
	if federationTypeNames[def.Name] {
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
