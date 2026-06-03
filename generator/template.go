package generator

import (
	"bytes"
	_ "embed"
	"fmt"
	"go/types"
	"strings"
	"unicode"

	"github.com/99designs/gqlgen/codegen/config"
	"github.com/99designs/gqlgen/codegen/templates"
	"github.com/gqlgo/gqlgenc/clientgenv2"
	gqlgencConfig "github.com/gqlgo/gqlgenc/config"
)

// Recursion kinds for writeStructLiteral. Untyped ints rather than a named
// type so the file's top-of-file ordering (const → var → type → func) stays
// compatible with the decorder lint rule.
const (
	// recurseNone means assign the field's value verbatim.
	recurseNone = iota
	// recurseStruct means emit `&T{…}` (or `T{…}` for a value field) and walk
	// the inner struct.
	recurseStruct
	// recurseSlice means emit a slice-construction expression that builds a
	// new slice element-by-element from the source.
	recurseSlice
)

//go:embed template.gotpl
var federationTemplate string

// EnumDef describes a single GraphQL enum to emit as a Go named-string type.
// The template renders one `type T string` declaration plus a `const ( … )` block
// and a `var AllT = []T{ … }` slice, matching genqlient's output.
type EnumDef struct {
	GoName      string
	GraphQLName string
	Description string
	Values      []EnumValueDef
}

// EnumValueDef is a single enum value: GraphQL wire name and Go constant name.
type EnumValueDef struct {
	GoName      string
	GraphQLName string
	Description string
}

// genGettersGenerator is a local copy of clientgenv2.GenGettersGenerator.
// clientgenv2 does not export it, so we duplicate the ~80 lines here.
type genGettersGenerator struct {
	clientPackageName string
}

// inlineFragField pairs a Go struct field name with the concrete GraphQL type it
// represents via a graphql:"... on Type" tag.
type inlineFragField struct {
	goName   string // Go field name, e.g. "District"
	typeName string // concrete GraphQL type, e.g. "District"
}

// DescriptionLines splits a GraphQL description into the sequence of payload
// strings that a generated Go `// …` comment block should contain — one element
// per line, with each line's leading/trailing whitespace trimmed.
//
// The template emits `// {{ . }}` for each returned element, so each line is
// individually prefixed with `// ` (matching genqlient's writeDescription).
// Blank lines inside multi-paragraph descriptions are preserved as empty
// strings, which render as bare `//` lines and keep paragraph breaks visible
// in `go doc` output.
//
// Returns nil for an all-whitespace description so callers can `if len(...) > 0`
// to suppress an empty comment.
func DescriptionLines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	raw := strings.Split(s, "\n")
	out := make([]string, len(raw))
	for i, line := range raw {
		out[i] = strings.TrimSpace(line)
	}
	return out
}

// GoConstName converts a GraphQL enum value name (typically SCREAMING_SNAKE_CASE)
// into a Go constant suffix.
//
// The rules: each underscore is dropped, the character after each underscore
// (and the first character) is upper-cased, and all other characters are
// lower-cased. So "UNEXPECTED_ERROR" → "UnexpectedError".
//
// This is a behavioural port of genqlient's unexported `goConstName`:
//
//	source: github.com/Khan/genqlient v0.8.1, file generate/util.go (line 52)
//
// Defederator generates enums in genqlient's exact format so callers migrating
// from genqlient need no source changes. If you bump the genqlient version in
// go.mod, re-check that file: if its goConstName diverges, update this port
// and the version tag above in lockstep, with a test case demonstrating the
// new behaviour.
func GoConstName(s string) string {
	if strings.TrimLeft(s, "_") == "" {
		return s
	}
	var prev rune
	return strings.Map(func(r rune) rune {
		var ret rune
		switch {
		case r == '_':
			ret = -1
		case prev == '_' || prev == 0:
			ret = unicode.ToUpper(r)
		default:
			ret = unicode.ToLower(r)
		}
		prev = r
		return ret
	}, s)
}

// introspectionLookup returns a template function that resolves an operation
// name to a *IntrospectionInfo or nil. Returning a pointer (rather than the
// zero struct) lets the template use `{{ with introspection $name }}` to
// branch — `with` skips the block when the value is nil.
func introspectionLookup(
	byName map[string]IntrospectionInfo,
) func(string) *IntrospectionInfo {
	return func(name string) *IntrospectionInfo {
		if info, ok := byName[name]; ok {
			return &info
		}
		return nil
	}
}

// RenderFederationTemplate renders the federation client template.
// It mirrors clientgenv2.RenderTemplate but uses the federation-specific template.
// urlMode is "baked" (default) or "enum"; it controls which NewClient signature
// is generated. See config.Config.URLMode for the full description.
func RenderFederationTemplate(
	cfg *config.Config,
	fragments []*clientgenv2.Fragment,
	operations []*clientgenv2.Operation,
	operationResponses []*clientgenv2.OperationResponse,
	structSources []*clientgenv2.StructSource,
	generateCfg *gqlgencConfig.GenerateConfig,
	client config.PackageConfig,
	planSpecs map[string]string,
	urlMode string,
	enums []*EnumDef,
	introspectionByName map[string]IntrospectionInfo,
) error {
	if urlMode == "" {
		urlMode = "baked"
	}
	g := &genGettersGenerator{clientPackageName: client.Package}

	respTypeByName := buildResponseTypeMap(operationResponses)
	needsJSON := needsJSONImport(
		g,
		operations,
		operationResponses,
		planSpecs,
		respTypeByName,
		introspectionByName,
	)

	err := templates.Render(templates.Options{
		PackageName: client.Package,
		Filename:    client.Filename,
		Template:    federationTemplate,
		Data: map[string]any{
			"Fragment":            fragments,
			"Operation":           operations,
			"OperationResponse":   operationResponses,
			"GenerateClient":      generateCfg.ShouldGenerateClient(),
			"StructSources":       structSources,
			"ClientInterfaceName": generateCfg.GetClientInterfaceName(),
			"PlanSpecs":           planSpecs,
			"URLModeEnum":         urlMode == "enum",
			"HasTypedEntityFetch": needsJSON,
			"Enum":                enums,
			"Introspection":       introspectionByName,
		},
		Packages:   cfg.Packages,
		PackageDoc: "// Code generated by github.com/StevenACoffman/defederator, DO NOT EDIT.\n",
		Funcs: map[string]any{
			"genGetters":           g.genFunc(),
			"genConversionGetters": g.conversionGettersFunc(fragments),
			"genEntityFetch":       g.genEntityFetchFunc(operationResponses, planSpecs),
			"genUnmarshalJSON":     genUnmarshalJSONMethod,
			"descriptionLines":     DescriptionLines,
			"introspection":        introspectionLookup(introspectionByName),
		},
	})
	if err != nil {
		return fmt.Errorf("%s generating failed: %w", client.Filename, err)
	}
	return nil
}

// buildResponseTypeMap indexes operationResponses by name for fast lookup
// during entity-fetch code generation.
func buildResponseTypeMap(
	operationResponses []*clientgenv2.OperationResponse,
) map[string]types.Type {
	m := make(map[string]types.Type, len(operationResponses))
	for _, or := range operationResponses {
		m[or.Name] = or.Type
	}
	return m
}

// needsJSONImport reports whether the rendered template needs encoding/json.
// Set when any operation produces typed entity-merge code, any response type
// requires a custom UnmarshalJSON, or any introspection op is present (the
// baked bytes for introspection are unmarshalled into the response struct).
func needsJSONImport(
	g *genGettersGenerator,
	operations []*clientgenv2.Operation,
	operationResponses []*clientgenv2.OperationResponse,
	planSpecs map[string]string,
	respTypeByName map[string]types.Type,
	introspectionByName map[string]IntrospectionInfo,
) bool {
	if len(introspectionByName) > 0 {
		return true
	}
	if hasTypedEntityFetch(g, operations, planSpecs, respTypeByName) {
		return true
	}
	for _, or := range operationResponses {
		if genUnmarshalJSONMethod(or.Name, or.Type) != "" {
			return true
		}
	}
	return false
}

// hasTypedEntityFetch reports whether any operation generates typed entity
// merge code (which references encoding/json).
func hasTypedEntityFetch(
	g *genGettersGenerator,
	operations []*clientgenv2.Operation,
	planSpecs map[string]string,
	respTypeByName map[string]types.Type,
) bool {
	for _, op := range operations {
		specJSON := planSpecs[op.Name]
		if specJSON == "" {
			continue
		}
		respType := respTypeByName[op.ResponseStructName]
		if respType == nil {
			continue
		}
		if g.genEntityFetch(
			op.Name,
			respType,
			templates.ToGo(op.ResponseStructName),
			specJSON,
		) != "" {
			return true
		}
	}
	return false
}

func (g *genGettersGenerator) genFunc() func(name string, p types.Type) string {
	return func(name string, p types.Type) string {
		st, ok := p.(*types.Struct)
		if !ok {
			return ""
		}
		var buf bytes.Buffer
		for i := range st.NumFields() {
			field := st.Field(i)
			ret := g.returnTypeName(field.Type(), false)
			buf.WriteString("func (t *" + name + ") Get" + field.Name() + "() " + ret + "{\n")
			buf.WriteString("if t == nil {\n t = &" + name + "{}\n}\n")
			ptr := ""
			if _, ok := field.Type().(*types.Named); ok {
				ptr = "&"
			}
			buf.WriteString("return " + ptr + "t." + field.Name() + "\n}\n")
		}
		return buf.String()
	}
}

// resolveNamedTypeName returns the Go type name for a *types.Named, applying
// package qualification when the type lives outside clientPackageName.
func (g *genGettersGenerator) resolveNamedTypeName(it *types.Named, nested bool) string {
	s := strings.Split(it.String(), ".")
	name := s[len(s)-1]
	if it.Obj().Parent() != nil && it.Obj().Pkg().Name() != g.clientPackageName {
		name = fmt.Sprintf("%s.%s", it.Obj().Pkg().Name(), it.Obj().Name())
	}
	if nested {
		return name
	}
	return "*" + name
}

func (g *genGettersGenerator) returnTypeName(t types.Type, nested bool) string {
	switch it := t.(type) {
	case *types.Basic:
		return it.String()
	case *types.Pointer:
		return "*" + g.returnTypeName(it.Elem(), true)
	case *types.Slice:
		return "[]" + g.returnTypeName(it.Elem(), true)
	case *types.Named:
		return g.resolveNamedTypeName(it, nested)
	case *types.Interface:
		return "any"
	case *types.Map:
		return "map[" + g.returnTypeName(it.Key(), true) + "]" + g.returnTypeName(it.Elem(), true)
	case *types.Alias:
		return g.returnTypeName(it.Underlying(), nested)
	default:
		return fmt.Sprintf("%T----", it)
	}
}

func (g *genGettersGenerator) conversionGettersFunc(
	allFragments []*clientgenv2.Fragment,
) func(ownerName string, spreads []*clientgenv2.SpreadFragmentInfo) string {
	fragmentMap := make(map[string]*clientgenv2.Fragment, len(allFragments))
	fragmentNames := make(map[string]bool, len(allFragments))
	for _, f := range allFragments {
		fragmentMap[f.Name] = f
		fragmentNames[f.Name] = true
	}
	return func(ownerName string, spreads []*clientgenv2.SpreadFragmentInfo) string {
		if len(spreads) == 0 {
			return ""
		}
		var buf bytes.Buffer
		for _, spread := range spreads {
			frag, ok := fragmentMap[spread.Name]
			if !ok {
				continue
			}
			targetStruct, ok := frag.Type.(*types.Struct)
			if !ok {
				continue
			}
			targetName := g.typeName(spread.Type)
			buf.WriteString(
				"func (t *" + ownerName + ") Get" + targetName + "() *" + targetName + " {\n",
			)
			buf.WriteString("if t == nil {\n t = &" + ownerName + "{}\n}\n")
			buf.WriteString("return &" + targetName + "{\n")
			g.writeStructLiteral(&buf, targetStruct, "t", fragmentNames)
			buf.WriteString("}\n}\n")
		}
		return buf.String()
	}
}

// writeStructLiteral writes the body of a struct literal that maps fields from
// src (a Go expression for the owner value) onto the fields of st (gqlgenc's
// internal types.Struct for the target fragment).
//
// fragmentNames is the set of named-fragment type names; fields whose type
// resolves to one of those names are assigned directly rather than recursed
// into. See recursionKindFor for the full rule.
func (g *genGettersGenerator) writeStructLiteral(
	buf *bytes.Buffer,
	st *types.Struct,
	src string,
	fragmentNames map[string]bool,
) {
	for i := range st.NumFields() {
		f := st.Field(i)
		nested, kind := g.recursionKindFor(f.Type(), fragmentNames)
		switch kind {
		case recurseNone:
			buf.WriteString(f.Name() + ": " + src + "." + f.Name() + ",\n")
		case recurseStruct:
			g.writeStructFieldLiteral(buf, f, nested, src, fragmentNames)
		case recurseSlice:
			g.writeSliceFieldLiteral(buf, f, nested, src, fragmentNames)
		}
	}
}

// writeStructFieldLiteral emits a struct field whose value is a nested
// per-call-site struct: `Field: &T{…}` (or `T{…}` if the field isn't a
// pointer), walking st's fields with src.Field as the new source expression.
func (g *genGettersGenerator) writeStructFieldLiteral(
	buf *bytes.Buffer,
	f *types.Var,
	nested *types.Struct,
	src string,
	fragmentNames map[string]bool,
) {
	prefix := ""
	if _, ok := f.Type().(*types.Pointer); ok {
		prefix = "&"
	}
	buf.WriteString(f.Name() + ": " + prefix + g.typeName(f.Type()) + "{\n")
	g.writeStructLiteral(buf, nested, src+"."+f.Name(), fragmentNames)
	buf.WriteString("},\n")
}

// writeSliceFieldLiteral emits a slice field whose element type is a
// per-call-site struct that differs between source and target Go types. The
// emitted expression is an immediately-invoked closure that allocates a new
// slice and copies element-by-element with a fresh struct literal per
// element. nil/empty source yields a nil result.
func (g *genGettersGenerator) writeSliceFieldLiteral(
	buf *bytes.Buffer,
	f *types.Var,
	elemStruct *types.Struct,
	src string,
	fragmentNames map[string]bool,
) {
	sliceType := g.returnTypeName(f.Type(), false)
	// Element name without pointer prefix or package qualification. gqlgenc
	// emits []*X for response slices, so the element type expression in a
	// struct literal is &X{…}; we need the X.
	elemName := sliceElementName(f.Type())
	srcExpr := src + "." + f.Name()
	buf.WriteString(f.Name() + ": func() " + sliceType + " {\n")
	buf.WriteString("if len(" + srcExpr + ") == 0 { return nil }\n")
	buf.WriteString("_out := make(" + sliceType + ", len(" + srcExpr + "))\n")
	buf.WriteString("for _i := range " + srcExpr + " {\n")
	buf.WriteString("_e := " + srcExpr + "[_i]\n")
	buf.WriteString("_ = _e\n")
	buf.WriteString("_out[_i] = &" + elemName + "{\n")
	g.writeStructLiteral(buf, elemStruct, "_e", fragmentNames)
	buf.WriteString("}\n")
	buf.WriteString("}\n")
	buf.WriteString("return _out\n")
	buf.WriteString("}(),\n")
}

// sliceElementName returns the bare struct name of the element type of a
// slice-of-pointer-to-named-struct field. Panics on shapes
// writeSliceFieldLiteral wouldn't have been called for; the caller must
// ensure recursionKindFor returned recurseSlice.
func sliceElementName(t types.Type) string {
	slice, ok := t.(*types.Slice)
	if !ok {
		return ""
	}
	elem := slice.Elem()
	if ptr, ok := elem.(*types.Pointer); ok {
		elem = ptr.Elem()
	}
	named, ok := elem.(*types.Named)
	if !ok {
		return ""
	}
	// types.Named.String() returns "<full-import-path>.<TypeName>"; we want
	// the last dot-separated segment.
	s := named.String()
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

// recursionKindFor decides how writeStructLiteral should handle a field of
// type t.
//
// Recursion is required only for gqlgenc's per-call-site shape types: structs
// whose Go type name embeds the parent path (e.g.
// CourseProgressFields_CurationNodeProgressFields_CurrentMasteryV2) so that
// the owner's field and the target's field have the same logical shape but
// distinct Go types. For those, direct assignment doesn't compile.
//
// Cases handled:
//
//   - *PerCallSiteStruct / PerCallSiteStruct → recurseStruct, returning the
//     element's underlying struct so the caller can walk its fields.
//   - []*PerCallSiteStruct → recurseSlice; element-by-element rebuild.
//
// Cases that fall through to recurseNone (direct assignment):
//
//   - Foreign-package types (time.Time, civil.Date, …): copying unexported
//     fields doesn't compile.
//   - Named-fragment types (CurationNodeWrapper, …): the owner and target
//     reference the same Go type, so direct assignment is correct. The
//     internal types.Struct that gqlgenc holds for a named fragment is the
//     pre-flattening shape (with spread-named fields), which does not match
//     the emitted Go struct.
//   - Slices of named-fragment types: same element type both sides.
//   - Slices of primitives or foreign types: same element type both sides.
func (g *genGettersGenerator) recursionKindFor(
	t types.Type,
	fragmentNames map[string]bool,
) (*types.Struct, int) {
	if slice, ok := t.(*types.Slice); ok {
		st := g.perCallSiteStruct(slice.Elem(), fragmentNames)
		if st == nil {
			return nil, recurseNone
		}
		return st, recurseSlice
	}
	st := g.perCallSiteStruct(t, fragmentNames)
	if st == nil {
		return nil, recurseNone
	}
	return st, recurseStruct
}

// perCallSiteStruct returns the underlying *types.Struct of t when t is a
// pointer-or-direct named type defined in the client package and not a named
// fragment. Returns nil for everything else: foreign-package types, named
// fragments, primitives, basic Go types, etc.
//
// This is the single decision point used by both the pointer/struct recursion
// and the slice-element recursion.
func (g *genGettersGenerator) perCallSiteStruct(
	t types.Type,
	fragmentNames map[string]bool,
) *types.Struct {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	pkg := named.Obj().Pkg()
	if pkg == nil || pkg.Name() != g.clientPackageName {
		return nil
	}
	if fragmentNames[named.Obj().Name()] {
		return nil
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	return st
}

func (g *genGettersGenerator) typeName(t types.Type) string {
	switch it := t.(type) {
	case *types.Named:
		s := strings.Split(it.String(), ".")
		return s[len(s)-1]
	case *types.Pointer:
		return g.typeName(it.Elem())
	default:
		return t.String()
	}
}

// graphqlInlineFragType returns the concrete type name from a `graphql:"... on Type"`
// struct tag, or "" if the tag is absent or not in inline-fragment form.
func graphqlInlineFragType(tag string) string {
	const prefix = `graphql:"... on `
	i := strings.Index(tag, prefix)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(prefix):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// genUnmarshalJSONMethod returns a custom UnmarshalJSON method for structName when t
// is a struct with at least one field carrying graphql:"... on Type" but no json: tag.
// Returns "" when no such field exists — the common case, so no method is emitted.
func genUnmarshalJSONMethod(structName string, t types.Type) string {
	st := unwrapToStruct(t)
	if st == nil {
		return ""
	}
	var frags []inlineFragField
	var typenameGoName string
	for i := range st.NumFields() {
		tag := st.Tag(i)
		goName := st.Field(i).Name()
		if parseJSONKey(tag) == "__typename" {
			typenameGoName = goName
			continue
		}
		// Inline fragment field: graphql:"... on Type" tag with no json: tag.
		if parseJSONKey(tag) == "" {
			if tn := graphqlInlineFragType(tag); tn != "" {
				frags = append(frags, inlineFragField{goName: goName, typeName: tn})
			}
		}
	}
	if len(frags) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("func (t *" + structName + ") UnmarshalJSON(data []byte) error {\n")
	b.WriteString("var _disc struct{ Typename *string `json:\"__typename\"` }\n")
	b.WriteString("if _err := json.Unmarshal(data, &_disc); _err != nil { return _err }\n")
	if typenameGoName != "" {
		b.WriteString("t." + typenameGoName + " = _disc.Typename\n")
	}
	b.WriteString("if _disc.Typename != nil {\n")
	b.WriteString("switch *_disc.Typename {\n")
	for _, f := range frags {
		b.WriteString("case \"" + f.typeName + "\":\n")
		b.WriteString(
			"if _err := json.Unmarshal(data, &t." + f.goName + "); _err != nil { return _err }\n",
		)
	}
	b.WriteString("}\n}\nreturn nil\n}\n")
	return b.String()
}
