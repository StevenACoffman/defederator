package generator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	defConfig "github.com/StevenACoffman/defederator/config"
	"github.com/StevenACoffman/defederator/generator"
)

// minimalSupergraphWithEnum is the smallest Federation v2 supergraph that has
// (a) one regular object field selectable from a Query and (b) a user-defined
// enum returned by that field. It exists only to exercise enum-in-response
// codegen — the exact bug that originally panicked at
// SyncUserHasAnyPermissionsMutationErrorCode and is regressed by this test.
//
// The supergraph uses the standard @link / @join__type / @join__graph
// directives at federation v0.3.
const minimalSupergraphWithEnum = `
schema
  @link(url: "https://specs.apollo.dev/link/v1.0")
  @link(url: "https://specs.apollo.dev/join/v0.3", for: EXECUTION)
{
  query: Query
}

directive @join__enumValue(graph: join__Graph!) repeatable on ENUM_VALUE
directive @join__field(graph: join__Graph, requires: join__FieldSet, provides: join__FieldSet, type: String, external: Boolean, override: String, usedOverridden: Boolean) repeatable on FIELD_DEFINITION | INPUT_FIELD_DEFINITION
directive @join__graph(name: String!, url: String!) on ENUM_VALUE
directive @join__type(graph: join__Graph!, key: join__FieldSet, extension: Boolean! = false, resolvable: Boolean! = true, isInterfaceObject: Boolean! = false) repeatable on OBJECT | INTERFACE | UNION | ENUM | INPUT_OBJECT | SCALAR
directive @link(url: String, as: String, for: link__Purpose, import: [link__Import]) repeatable on SCHEMA

scalar join__FieldSet
scalar link__Import
enum link__Purpose {
  SECURITY
  EXECUTION
}

enum join__Graph {
  PALETTE @join__graph(name: "palette", url: "https://palette.example/graphql")
}

# The user-defined enum the test cares about. Owned by PALETTE; values match
# the SCREAMING_SNAKE → PascalCase mapping that GoConstName performs.
enum Color
  @join__type(graph: PALETTE)
{
  RED
  GREEN_BLUE
  ROYAL_PURPLE
}

type Swatch
  @join__type(graph: PALETTE)
{
  id: ID!
  primary: Color!
  secondary: Color
}

type Query
  @join__type(graph: PALETTE)
{
  swatch(id: ID!): Swatch @join__field(graph: PALETTE)
}
`

// TestGenerate_EnumResponseField is a regression test for the panic that
// originally surfaced as "no model configured for GraphQL type
// SyncUserHasAnyPermissionsMutationErrorCode". The bug was triggered by any
// operation that selected an enum field in its response: gqlgenc's
// SourceGenerator.Type() panicked because user-defined enums weren't
// pre-registered as models. The fix is in generator.BasicTypeModels.
//
// This test exercises the full Generate pipeline against a tiny supergraph
// with an enum-typed response field and asserts that:
//
//   - Generation succeeds (no panic).
//   - The user-defined enum is emitted as a typed Go string with PascalCase
//     constants, matching genqlient's output format.
//   - Response struct fields use the named enum type, not bare string.
func TestGenerate_EnumResponseField(t *testing.T) {
	tmp := t.TempDir()
	schemaPath := filepath.Join(tmp, "supergraph.graphql")
	if err := os.WriteFile(schemaPath, []byte(minimalSupergraphWithEnum), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	queryPath := filepath.Join(tmp, "query.graphql")
	const query = `
query GetSwatch($id: ID!) {
  swatch(id: $id) {
    id
    primary
    secondary
  }
}
`
	if err := os.WriteFile(queryPath, []byte(query), 0o644); err != nil {
		t.Fatalf("write query: %v", err)
	}

	clientPath := filepath.Join(tmp, "client.go")
	cfg := &defConfig.Config{
		Schema: schemaPath,
		Query:  []string{queryPath},
		Client: defConfig.PackageConfig{
			Filename: clientPath,
			Package:  "palettegen",
		},
		Dir: tmp,
	}

	if err := generator.Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	got, err := os.ReadFile(clientPath)
	if err != nil {
		t.Fatalf("read generated client: %v", err)
	}
	// Collapse runs of whitespace so the assertions are insensitive to gofmt's
	// per-line alignment padding (e.g. it aligns `ColorRed` with `ColorRoyalPurple`
	// by inserting variable amounts of space between identifier and type).
	out := strings.Join(strings.Fields(string(got)), " ")

	// The named-string type and PascalCase constants must appear.
	wantSubstrings := []string{
		"type Color string",
		`ColorRed Color = "RED"`,
		`ColorGreenBlue Color = "GREEN_BLUE"`,
		`ColorRoyalPurple Color = "ROYAL_PURPLE"`,
		"var AllColor = []Color{",
		// Response struct field uses the named enum, not bare string.
		`Primary Color "json:\"primary\" graphql:\"primary\""`,
		`Secondary *Color "json:\"secondary\" graphql:\"secondary\""`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf(
				"generated client missing %q\n--- whitespace-collapsed output ---\n%s",
				want, out,
			)
		}
	}

	// Guard against the old broken behaviour: the enum must not be silently
	// mapped to the gqlgen graphql.String shim in any response field.
	forbidden := []string{
		`Primary string "json:`,
		`Secondary *string "json:`,
	}
	for _, bad := range forbidden {
		if strings.Contains(out, bad) {
			t.Errorf("generated client uses untyped %q for an enum field", bad)
		}
	}
}

// TestGenerate_ConversionGetterForeignType regresses against the failure mode
// reported on the recommendations service: a fragment spread's conversion
// getter tried to recursively expand `time.Time` as a struct literal, exposing
// time.Time's unexported fields and producing code that doesn't compile.
//
// The fix in writeStructLiteral: only recurse into struct types defined in the
// client package (gqlgenc's per-call-site shape types); foreign-package
// fields like time.Time are assigned directly.
func TestGenerate_ConversionGetterForeignType(t *testing.T) {
	// Self-contained supergraph: Workspace has a DateTime field bound to
	// time.Time. A fragment spreads another fragment so the conversion
	// getter is generated (and previously recursed into time.Time).
	const sdl = `
schema
  @link(url: "https://specs.apollo.dev/link/v1.0")
  @link(url: "https://specs.apollo.dev/join/v0.3", for: EXECUTION)
{
  query: Query
}

directive @join__enumValue(graph: join__Graph!) repeatable on ENUM_VALUE
directive @join__field(graph: join__Graph, requires: join__FieldSet, provides: join__FieldSet, type: String, external: Boolean, override: String, usedOverridden: Boolean) repeatable on FIELD_DEFINITION | INPUT_FIELD_DEFINITION
directive @join__graph(name: String!, url: String!) on ENUM_VALUE
directive @join__type(graph: join__Graph!, key: join__FieldSet, extension: Boolean! = false, resolvable: Boolean! = true, isInterfaceObject: Boolean! = false) repeatable on OBJECT | INTERFACE | UNION | ENUM | INPUT_OBJECT | SCALAR
directive @link(url: String, as: String, for: link__Purpose, import: [link__Import]) repeatable on SCHEMA

scalar join__FieldSet
scalar link__Import
enum link__Purpose { SECURITY EXECUTION }

scalar DateTime
  @join__type(graph: WS)

enum join__Graph {
  WS @join__graph(name: "ws", url: "https://ws.example/graphql")
}

type Workspace
  @join__type(graph: WS)
{
  id: ID!
  lastModified: DateTime!
}

type Query
  @join__type(graph: WS)
{
  workspace(id: ID!): Workspace @join__field(graph: WS)
}
`
	tmp := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(tmp, "supergraph.graphql"),
		[]byte(sdl),
		0o644,
	); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	// Two fragments where one spreads the other; the parent has a
	// time.Time-mapped DateTime field, the conversion getter must not
	// recurse into time.Time.
	const query = `
query GetWorkspace($id: ID!) {
  workspace(id: $id) {
    ...WorkspaceDetails
  }
}
fragment WorkspaceDetails on Workspace {
  ...WorkspaceID
  lastModified
}
fragment WorkspaceID on Workspace {
  id
}
`
	if err := os.WriteFile(filepath.Join(tmp, "query.graphql"), []byte(query), 0o644); err != nil {
		t.Fatalf("write query: %v", err)
	}

	cfg := &defConfig.Config{
		Schema: filepath.Join(tmp, "supergraph.graphql"),
		Query:  []string{filepath.Join(tmp, "query.graphql")},
		Client: defConfig.PackageConfig{
			Filename: filepath.Join(tmp, "client.go"),
			Package:  "palettegen",
		},
		Dir: tmp,
		Bindings: map[string]defConfig.TypeBinding{
			"DateTime": {Type: "time.Time"},
		},
	}

	if err := generator.Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	got, err := os.ReadFile(cfg.Client.Filename)
	if err != nil {
		t.Fatalf("read generated client: %v", err)
	}
	out := string(got)

	// Must not recurse into time.Time's unexported fields.
	forbidden := []string{".wall", ".ext", "Time{", "Location{", "loc:"}
	for _, bad := range forbidden {
		if strings.Contains(out, bad) {
			t.Errorf(
				"generated client recurses into time.Time (contains %q)\n%s",
				bad, out,
			)
		}
	}
}

// TestGenerate_EnumAsVariableType regresses against the failure mode on the
// users service: a mutation took a variable typed `UserAdminLogKind!` (an
// enum). UsedEnumsInOperations originally only walked response selection
// sets, so the variable's enum type wasn't registered as a model and
// gqlgenc's OperationArguments panicked with
// "no model configured for GraphQL type UserAdminLogKind".
//
// The fix in UsedEnumsInOperations: also walk each operation's
// VariableDefinitions and record enum-typed variables (through any
// non-null/list wrappers).
func TestGenerate_EnumAsVariableType(t *testing.T) {
	const sdl = `
schema
  @link(url: "https://specs.apollo.dev/link/v1.0")
  @link(url: "https://specs.apollo.dev/join/v0.3", for: EXECUTION)
{
  query: Query
}

directive @join__enumValue(graph: join__Graph!) repeatable on ENUM_VALUE
directive @join__field(graph: join__Graph, requires: join__FieldSet, provides: join__FieldSet, type: String, external: Boolean, override: String, usedOverridden: Boolean) repeatable on FIELD_DEFINITION | INPUT_FIELD_DEFINITION
directive @join__graph(name: String!, url: String!) on ENUM_VALUE
directive @join__type(graph: join__Graph!, key: join__FieldSet, extension: Boolean! = false, resolvable: Boolean! = true, isInterfaceObject: Boolean! = false) repeatable on OBJECT | INTERFACE | UNION | ENUM | INPUT_OBJECT | SCALAR
directive @link(url: String, as: String, for: link__Purpose, import: [link__Import]) repeatable on SCHEMA

scalar join__FieldSet
scalar link__Import
enum link__Purpose { SECURITY EXECUTION }

enum join__Graph {
  G @join__graph(name: "g", url: "https://g.example/graphql")
}

enum LogKind
  @join__type(graph: G)
{
  INFO
  WARN
  ERROR
}

type Result
  @join__type(graph: G)
{
  id: ID!
  ok: Boolean!
}

type Query
  @join__type(graph: G)
{
  log(kind: LogKind!): Result @join__field(graph: G)
}
`
	tmp := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(tmp, "supergraph.graphql"),
		[]byte(sdl),
		0o644,
	); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	// The variable $kind: LogKind! is the trigger — the response selects no
	// enum-typed fields, so only the variable position keeps LogKind in use.
	const query = `
query DoLog($kind: LogKind!) {
  log(kind: $kind) {
    id
    ok
  }
}
`
	if err := os.WriteFile(filepath.Join(tmp, "query.graphql"), []byte(query), 0o644); err != nil {
		t.Fatalf("write query: %v", err)
	}

	cfg := &defConfig.Config{
		Schema: filepath.Join(tmp, "supergraph.graphql"),
		Query:  []string{filepath.Join(tmp, "query.graphql")},
		Client: defConfig.PackageConfig{
			Filename: filepath.Join(tmp, "client.go"),
			Package:  "loggen",
		},
		Dir: tmp,
	}

	if err := generator.Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	got, err := os.ReadFile(cfg.Client.Filename)
	if err != nil {
		t.Fatalf("read generated client: %v", err)
	}
	out := strings.Join(strings.Fields(string(got)), " ")
	if !strings.Contains(out, "type LogKind string") {
		t.Errorf("expected `type LogKind string` for variable-position enum, got:\n%s", out)
	}
}

// TestGenerate_ConversionGetterSliceOfPerCallSiteType regresses against the
// failure mode reported on the content-library service: the target struct
// had `[]*X_URLPaths` (the named-fragment's element type) while the owner
// had `[]*Y_X_URLPaths` (a per-call-site element type with the parent path
// embedded). The conversion getter emitted a direct slice assignment which
// the compiler rejected because the element types differ.
//
// The fix in writeStructLiteral/recursionKindFor: detect slice-of-per-call-
// site-struct and emit an element-by-element rebuild loop.
func TestGenerate_ConversionGetterSliceOfPerCallSiteType(t *testing.T) {
	const sdl = `
schema
  @link(url: "https://specs.apollo.dev/link/v1.0")
  @link(url: "https://specs.apollo.dev/join/v0.3", for: EXECUTION)
{
  query: Query
}

directive @join__enumValue(graph: join__Graph!) repeatable on ENUM_VALUE
directive @join__field(graph: join__Graph, requires: join__FieldSet, provides: join__FieldSet, type: String, external: Boolean, override: String, usedOverridden: Boolean) repeatable on FIELD_DEFINITION | INPUT_FIELD_DEFINITION
directive @join__graph(name: String!, url: String!) on ENUM_VALUE
directive @join__type(graph: join__Graph!, key: join__FieldSet, extension: Boolean! = false, resolvable: Boolean! = true, isInterfaceObject: Boolean! = false) repeatable on OBJECT | INTERFACE | UNION | ENUM | INPUT_OBJECT | SCALAR
directive @link(url: String, as: String, for: link__Purpose, import: [link__Import]) repeatable on SCHEMA

scalar join__FieldSet
scalar link__Import
enum link__Purpose { SECURITY EXECUTION }

enum join__Graph {
  G @join__graph(name: "g", url: "https://g.example/graphql")
}

type URLPathEntry
  @join__type(graph: G)
{
  path: String!
  locale: String
}

type Item
  @join__type(graph: G)
{
  id: ID!
  urlPaths: [URLPathEntry!]!
}

type Query
  @join__type(graph: G)
{
  item(id: ID!): Item @join__field(graph: G)
}
`
	tmp := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(tmp, "supergraph.graphql"),
		[]byte(sdl),
		0o644,
	); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	// Outer spreads Common; both selections of urlPaths happen at different
	// call sites so gqlgenc emits Outer_urlPaths and Common_urlPaths as
	// distinct per-call-site Go types. The conversion getter Outer ->
	// Common must therefore rebuild the slice element-by-element rather
	// than direct-assign.
	const query = `
query GetItem($id: ID!) {
  item(id: $id) {
    ...Outer
  }
}
fragment Outer on Item {
  id
  ...Common
}
fragment Common on Item {
  urlPaths { path locale }
}
`
	if err := os.WriteFile(filepath.Join(tmp, "query.graphql"), []byte(query), 0o644); err != nil {
		t.Fatalf("write query: %v", err)
	}

	cfg := &defConfig.Config{
		Schema: filepath.Join(tmp, "supergraph.graphql"),
		Query:  []string{filepath.Join(tmp, "query.graphql")},
		Client: defConfig.PackageConfig{
			Filename: filepath.Join(tmp, "client.go"),
			Package:  "itemgen",
		},
		Dir: tmp,
	}

	if err := generator.Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	got, err := os.ReadFile(cfg.Client.Filename)
	if err != nil {
		t.Fatalf("read generated client: %v", err)
	}
	out := string(got)

	// The conversion getter must rebuild the slice, not direct-assign.
	// The generated code should contain a `make(` for the slice and a
	// `for _i := range` loop that copies each element.
	if !strings.Contains(out, "URLPaths: func()") {
		t.Errorf(
			"expected URLPaths field to use a slice-rebuild closure, got:\n%s",
			out,
		)
	}
	if !strings.Contains(out, "for _i := range") {
		t.Errorf("expected per-element loop in URLPaths rebuild:\n%s", out)
	}
	// The element struct literal must use the bare type name, not the full
	// Go import path. The previous bug emitted `&github.com/.../pkg.Type{…}`
	// because typeName fell through to types.Type.String() for slice fields.
	if strings.Contains(out, "&github.com/") || strings.Contains(out, "&[]*") {
		t.Errorf(
			"slice rebuild emitted a qualified or list-typed element literal:\n%s",
			out,
		)
	}
}

// TestGenerate_ConversionGetterNamedFragmentField regresses against the
// failure mode reported on the recommendations service after the
// foreign-type recursion fix: a fragment's conversion getter recursed into a
// nested *NamedFragment field and tried to reconstruct it field-by-field
// using gqlgenc's pre-flattening internal types.Struct shape. The emitted
// Go struct has those same fields flattened (one per inner spread), so the
// generated literal referenced fields that don't exist on the emitted
// struct ("unknown field CurationNodeFields in struct literal of type
// CurationNodeWrapper").
//
// The fix in localStructIfRecursable: skip recursion when the field's type
// is itself a named fragment — the owner's field and the target's field
// reference the same Go type, so direct assignment is correct.
//
// Test shape: Outer spreads Inner; Inner has an item field of type Wrapper
// (a named fragment that itself contains a spread). The conversion getter
// Outer.GetInner() must build the Wrapper field by direct assignment, not
// by listing Wrapper's pre-flattening fields.
func TestGenerate_ConversionGetterNamedFragmentField(t *testing.T) {
	const sdl = `
schema
  @link(url: "https://specs.apollo.dev/link/v1.0")
  @link(url: "https://specs.apollo.dev/join/v0.3", for: EXECUTION)
{
  query: Query
}

directive @join__enumValue(graph: join__Graph!) repeatable on ENUM_VALUE
directive @join__field(graph: join__Graph, requires: join__FieldSet, provides: join__FieldSet, type: String, external: Boolean, override: String, usedOverridden: Boolean) repeatable on FIELD_DEFINITION | INPUT_FIELD_DEFINITION
directive @join__graph(name: String!, url: String!) on ENUM_VALUE
directive @join__type(graph: join__Graph!, key: join__FieldSet, extension: Boolean! = false, resolvable: Boolean! = true, isInterfaceObject: Boolean! = false) repeatable on OBJECT | INTERFACE | UNION | ENUM | INPUT_OBJECT | SCALAR
directive @link(url: String, as: String, for: link__Purpose, import: [link__Import]) repeatable on SCHEMA

scalar join__FieldSet
scalar link__Import
enum link__Purpose { SECURITY EXECUTION }

enum join__Graph {
  G @join__graph(name: "g", url: "https://g.example/graphql")
}

type Item
  @join__type(graph: G)
{
  id: ID!
  kind: String
}

type Container
  @join__type(graph: G)
{
  id: ID!
  item: Item!
}

type Query
  @join__type(graph: G)
{
  container(id: ID!): Container @join__field(graph: G)
}
`
	tmp := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(tmp, "supergraph.graphql"),
		[]byte(sdl),
		0o644,
	); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	// Outer spreads Inner; Inner.item is a Wrapper (named fragment that
	// itself spreads ItemFields, so its emitted Go struct has fields
	// {ID, Kind} from the flattened spread). The conversion getter
	// Outer.GetInner() must direct-assign Item, not recurse into Wrapper's
	// pre-flattening shape and emit ItemFields: ... as a struct field.
	const query = `
query GetContainer($id: ID!) {
  container(id: $id) {
    ...Outer
  }
}
fragment Outer on Container {
  id
  ...Inner
}
fragment Inner on Container {
  item {
    ...Wrapper
  }
}
fragment Wrapper on Item {
  ...ItemFields
}
fragment ItemFields on Item {
  id
  kind
}
`
	if err := os.WriteFile(filepath.Join(tmp, "query.graphql"), []byte(query), 0o644); err != nil {
		t.Fatalf("write query: %v", err)
	}

	cfg := &defConfig.Config{
		Schema: filepath.Join(tmp, "supergraph.graphql"),
		Query:  []string{filepath.Join(tmp, "query.graphql")},
		Client: defConfig.PackageConfig{
			Filename: filepath.Join(tmp, "client.go"),
			Package:  "containergen",
		},
		Dir: tmp,
	}

	if err := generator.Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	got, err := os.ReadFile(cfg.Client.Filename)
	if err != nil {
		t.Fatalf("read generated client: %v", err)
	}
	out := string(got)

	// The bug: conversion getter would emit `&Wrapper{ItemFields: ...}`
	// using Wrapper's pre-flattening internal shape. With the fix, it
	// direct-assigns: `Item: t.Item`.
	if strings.Contains(out, "ItemFields:") {
		t.Errorf(
			"conversion getter recursed into Wrapper using pre-flattening field name `ItemFields:`\n%s",
			out,
		)
	}
	// The conversion getter Outer -> Inner must exist and use direct
	// assignment for the Wrapper-typed Item field.
	if !strings.Contains(out, "Item: t.Item") {
		t.Errorf("expected `Item: t.Item` direct assignment in conversion getter:\n%s", out)
	}
}

// TestGenerate_EnumNameCollidesWithFragment regresses against the failure mode
// reported on the recommendations service: the supergraph has an enum named
// `Color` and the user's queries declare a fragment also named `Color`.
// Pre-fix, every enum was eagerly registered as a gqlgen model, so when
// gqlgenc tried to register the fragment as a model it bailed with
// "Color is duplicated".
//
// Lazy enum registration (UsedEnumsInOperations) avoids the collision: the
// enum isn't referenced by any operation response field, so no model entry
// is created and the fragment registers cleanly.
func TestGenerate_EnumNameCollidesWithFragment(t *testing.T) {
	tmp := t.TempDir()
	schemaPath := filepath.Join(tmp, "supergraph.graphql")
	if err := os.WriteFile(schemaPath, []byte(minimalSupergraphWithEnum), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	// Query never selects a field of enum Color — only `id`. The fragment
	// happens to share a name with the schema's `enum Color`.
	const query = `
query GetSwatch($id: ID!) {
  swatch(id: $id) {
    ...Color
  }
}
fragment Color on Swatch {
  id
}
`
	queryPath := filepath.Join(tmp, "query.graphql")
	if err := os.WriteFile(queryPath, []byte(query), 0o644); err != nil {
		t.Fatalf("write query: %v", err)
	}

	cfg := &defConfig.Config{
		Schema: schemaPath,
		Query:  []string{queryPath},
		Client: defConfig.PackageConfig{
			Filename: filepath.Join(tmp, "client.go"),
			Package:  "palettegen",
		},
		Dir: tmp,
	}

	if err := generator.Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate (enum/fragment same name): %v", err)
	}

	got, err := os.ReadFile(cfg.Client.Filename)
	if err != nil {
		t.Fatalf("read generated client: %v", err)
	}
	out := strings.Join(strings.Fields(string(got)), " ")

	// The fragment type must be emitted (its name wins because no operation
	// selects a field of type Color).
	if !strings.Contains(out, "type Color struct") {
		t.Errorf("expected `type Color struct` (the fragment), got:\n%s", out)
	}
	// The enum's named-string declaration must NOT appear — it's dead code
	// because no operation selects an enum-typed field.
	if strings.Contains(out, "type Color string") {
		t.Errorf(
			"unexpected `type Color string` — enum should not be emitted when unused",
		)
	}
}
