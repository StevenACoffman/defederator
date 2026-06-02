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
