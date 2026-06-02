package migrate

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// SubgraphEntry is a single join__Graph enum value parsed from the supergraph SDL.
type SubgraphEntry struct {
	EnumName    string // e.g. "AI_GUIDE"
	ServiceName string // e.g. "ai-guide" (from @join__graph identifier directive arg)
}

// ParseSubgraphs reads a Federation v2 supergraph SDL and returns all join__Graph
// enum values in declaration order. Pure function — no I/O.
//
// For each enum value, the service name is taken from the @join__graph(identifier:)
// directive argument. If the argument is absent the service name is derived by
// lowercasing the enum name and replacing underscores with hyphens.
func ParseSubgraphs(sdl string) ([]SubgraphEntry, error) {
	doc, err := parser.ParseSchema(&ast.Source{Input: sdl, Name: "supergraph"})
	if err != nil {
		return nil, fmt.Errorf("migrate: parse supergraph SDL: %w", err)
	}

	for _, def := range doc.Definitions {
		if def.Kind != ast.Enum || def.Name != "join__Graph" {
			continue
		}
		entries := make([]SubgraphEntry, 0, len(def.EnumValues))
		for _, ev := range def.EnumValues {
			entries = append(entries, SubgraphEntry{
				EnumName:    ev.Name,
				ServiceName: joinGraphIdentifier(ev),
			})
		}
		return entries, nil
	}
	return nil, errors.New("migrate: join__Graph enum not found in supergraph SDL")
}

// ParseInputObjectsForService returns all INPUT_OBJECT type names from the supergraph
// SDL that are owned by the given join__Graph enum value (e.g. "DISTRICTS"), sorted
// alphabetically. Ownership is determined by the @join__type(graph:) directive.
//
// Returns nil if graphEnumName is empty. Pure function — no I/O.
func ParseInputObjectsForService(sdl string, graphEnumName string) ([]string, error) {
	if graphEnumName == "" {
		return nil, nil
	}
	doc, err := parser.ParseSchema(&ast.Source{Input: sdl, Name: "supergraph"})
	if err != nil {
		return nil, fmt.Errorf("migrate: parse supergraph SDL for input objects: %w", err)
	}
	var names []string
	for _, def := range doc.Definitions {
		if def.Kind != ast.InputObject {
			continue
		}
		for _, d := range def.Directives {
			if d.Name != "join__type" {
				continue
			}
			for _, arg := range d.Arguments {
				if arg.Name == "graph" && arg.Value.Raw == graphEnumName {
					names = append(names, def.Name)
					break
				}
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// joinGraphIdentifier extracts the identifier string from a @join__graph directive
// on an enum value, falling back to a name-derived service name.
func joinGraphIdentifier(ev *ast.EnumValueDefinition) string {
	for _, d := range ev.Directives {
		if d.Name != "join__graph" {
			continue
		}
		for _, arg := range d.Arguments {
			if arg.Name == "identifier" {
				// Value is a quoted string; .Raw strips the quotes.
				return arg.Value.Raw
			}
		}
	}
	return enumToServiceName(ev.Name)
}

// enumToServiceName derives a service-discovery name from a join__Graph enum name.
// CONTENT_EDITING -> content-editing
func enumToServiceName(enumName string) string {
	return strings.ReplaceAll(strings.ToLower(enumName), "_", "-")
}
