package generator

import (
	"sort"
	"strings"

	gqlgenConfig "github.com/99designs/gqlgen/codegen/config"
	"github.com/vektah/gqlparser/v2/ast"
)

// CollectEnums walks schema and returns one EnumDef per user-defined enum.
//
// Introspection enums (names beginning `__`) are skipped — gqlgen registers
// them itself during Init.
//
// Results are sorted: enums by GoName, and within each enum the Values are
// sorted by GraphQLName. Stable ordering makes the generated client.go diff
// cleanly across runs.
//
// CollectEnums does not mutate schema and has no side effects; callers decide
// how to use the returned slice.
func CollectEnums(schema *ast.Schema) []*EnumDef {
	if schema == nil {
		return nil
	}
	var enums []*EnumDef
	for name, def := range schema.Types {
		if def.Kind != ast.Enum || strings.HasPrefix(name, "__") {
			continue
		}
		enum := &EnumDef{
			GoName:      name,
			GraphQLName: name,
			Description: def.Description,
		}
		for _, v := range def.EnumValues {
			enum.Values = append(enum.Values, EnumValueDef{
				GoName:      name + GoConstName(v.Name),
				GraphQLName: v.Name,
				Description: v.Description,
			})
		}
		sort.Slice(enum.Values, func(i, j int) bool {
			return enum.Values[i].GraphQLName < enum.Values[j].GraphQLName
		})
		enums = append(enums, enum)
	}
	sort.Slice(enums, func(i, j int) bool { return enums[i].GoName < enums[j].GoName })
	return enums
}

// UsedEnumsInOperations returns the set of enum type names referenced by at
// least one operation in doc, either as the type of a selected response field
// or as the type of an operation variable. The check follows non-null and list
// wrappers down to the base named type and matches against
// schema.Types[name].Kind == ast.Enum.
//
// This drives lazy enum emission: an enum that no operation touches is dead
// code, and crucially, registering it as a model collides with a user fragment
// of the same name (gqlgenc's Fragments() rejects "X is duplicated" if a model
// X already exists when a fragment X is registered). Lazy emission matches
// genqlient's behaviour for the same input.
//
// Variable types must be included because gqlgenc's OperationArguments resolves
// each variable's base type through SourceGenerator.Type(name) at codegen time;
// omitting an enum used in a variable position causes the same
// "no model configured for GraphQL type X" panic that omitting a response-field
// enum does.
func UsedEnumsInOperations(schema *ast.Schema, doc *ast.QueryDocument) map[string]bool {
	used := map[string]bool{}
	if schema == nil || doc == nil {
		return used
	}
	for _, op := range doc.Operations {
		collectSelectionEnums(schema, op.SelectionSet, used)
		for _, vd := range op.VariableDefinitions {
			recordEnumIfBaseType(schema, vd.Type, used)
		}
	}
	// Fragments live at the document level — walk each one's selection set
	// directly rather than recursing through spreads (which would double-count).
	for _, f := range doc.Fragments {
		collectSelectionEnums(schema, f.SelectionSet, used)
	}
	return used
}

// recordEnumIfBaseType marks t's base named type as used if that type is an
// enum in schema. Walks through non-null and list wrappers via Type.Elem to
// reach the underlying NamedType.
func recordEnumIfBaseType(schema *ast.Schema, t *ast.Type, used map[string]bool) {
	if t == nil {
		return
	}
	base := t
	for base.Elem != nil {
		base = base.Elem
	}
	if def := schema.Types[base.NamedType]; def != nil && def.Kind == ast.Enum {
		used[def.Name] = true
	}
}

// collectSelectionEnums walks sels and records any field whose return type is
// an enum into used. Inline fragments recurse; fragment spreads don't (their
// definitions are walked by UsedEnumsInOperations at the top level).
func collectSelectionEnums(schema *ast.Schema, sels ast.SelectionSet, used map[string]bool) {
	for _, sel := range sels {
		switch s := sel.(type) {
		case *ast.Field:
			recordEnumIfFieldType(schema, s, used)
			collectSelectionEnums(schema, s.SelectionSet, used)
		case *ast.InlineFragment:
			collectSelectionEnums(schema, s.SelectionSet, used)
		}
	}
}

// recordEnumIfFieldType marks the field's base return type as used if that
// type is an enum in schema. Split out so collectSelectionEnums stays a flat
// switch over selection kinds.
func recordEnumIfFieldType(schema *ast.Schema, f *ast.Field, used map[string]bool) {
	if f.Definition == nil {
		return
	}
	recordEnumIfBaseType(schema, f.Definition.Type, used)
}

// FilterEnumsByUsed returns the subset of enums whose GoName matches a true
// entry in used. Order is preserved. Pass the result of UsedEnumsInOperations
// for the set.
func FilterEnumsByUsed(enums []*EnumDef, used map[string]bool) []*EnumDef {
	out := make([]*EnumDef, 0, len(enums))
	for _, e := range enums {
		if used[e.GoName] {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// BasicTypeModels returns the gqlgen model bindings that gqlgenc needs in
// order to resolve user-defined INPUT_OBJECT and ENUM names during response
// generation. gqlgenc calls SourceGenerator.Type(name) for any field whose
// type satisfies IsBasicType(), which is true for both kinds; without an
// entry in cfg.Models, Type() panics.
//
// Input objects are bound to graphql.String — they only appear as operation
// arguments and are serialized via JSON, so the Go-side representation can be
// opaque.
//
// Enums are bound to <clientImportPath>.<EnumName> so SourceGenerator.Type
// returns a named Go type living in the client package itself. The binder's
// syntheticNamedType fallback handles the fact that the package being
// generated does not yet exist on disk.
//
// Callers should merge this map into their existing cfg.Models with their own
// precedence rule — typically "user-specified bindings win". BasicTypeModels
// does not mutate its inputs.
func BasicTypeModels(schema *ast.Schema, clientImportPath string) gqlgenConfig.TypeMap {
	out := make(gqlgenConfig.TypeMap)
	if schema == nil {
		return out
	}
	for name, def := range schema.Types {
		if strings.HasPrefix(name, "__") {
			continue
		}
		switch def.Kind {
		case ast.InputObject:
			out[name] = gqlgenConfig.TypeMapEntry{
				Model: gqlgenConfig.StringList{"github.com/99designs/gqlgen/graphql.String"},
			}
		case ast.Enum:
			out[name] = gqlgenConfig.TypeMapEntry{
				Model: gqlgenConfig.StringList{clientImportPath + "." + name},
			}
		case ast.Scalar, ast.Object, ast.Interface, ast.Union:
			// Skipped intentionally: scalars are configured via user bindings
			// (Date, DateTime, etc.); object/interface/union types are resolved
			// through their nested selections, not through SourceGenerator.Type.
		}
	}
	return out
}
