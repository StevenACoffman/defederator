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
