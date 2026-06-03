package generator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	defConfig "github.com/StevenACoffman/defederator/config"
	"github.com/StevenACoffman/defederator/generator"
)

// minimalSupergraphWithMutations is a Federation v2 supergraph with a Query
// and a Mutation root. The mutation fields are named like webapp's
// delete_user_data_for_service_* convention so the test data shape mirrors
// the real Users_ListMutations use case.
const minimalSupergraphWithMutations = `
schema
  @link(url: "https://specs.apollo.dev/link/v1.0")
  @link(url: "https://specs.apollo.dev/join/v0.3", for: EXECUTION)
{
  query: Query
  mutation: Mutation
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
  USERS @join__graph(name: "users", url: "https://users.example/graphql")
}

type Query
  @join__type(graph: USERS)
{
  hello: String @join__field(graph: USERS)
}

type Mutation
  @join__type(graph: USERS)
{
  delete_user_data_for_service_users: Boolean! @join__field(graph: USERS)
  delete_user_data_for_service_progress: Boolean! @join__field(graph: USERS)
  rename_user: Boolean! @join__field(graph: USERS)
}
`

// TestGenerate_IntrospectionBake covers the end-to-end bake path: a query
// that selects only __schema causes Generate to emit a const with the baked
// response and a method that unmarshals it. No PlanSpec for that operation,
// no Document const, no federation call.
//
// This is the regression test for the original "field __schema has no
// subgraph owner in routing table" error on Users_ListMutations.
func TestGenerate_IntrospectionBake(t *testing.T) {
	tmp := t.TempDir()
	schemaPath := filepath.Join(tmp, "supergraph.graphql")
	if err := os.WriteFile(schemaPath, []byte(minimalSupergraphWithMutations), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	queryPath := filepath.Join(tmp, "query.graphql")
	const query = `
query Users_ListMutations {
  __schema {
    mutationType {
      fields { name }
    }
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
			Package:  "usersgen",
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
	out := string(got)

	// The bake const must appear and contain all the mutation names.
	// gqlgenc strips underscores from Go identifiers, so the const name is
	// UsersListMutationsIntrospectionResponse, not Users_ListMutations...
	const constName = "UsersListMutationsIntrospectionResponse"
	if !strings.Contains(out, "const "+constName) {
		t.Errorf("missing bake const %q\n%s", constName, out)
	}
	for _, want := range []string{
		"delete_user_data_for_service_users",
		"delete_user_data_for_service_progress",
		"rename_user",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("bake response missing %q\n%s", want, out)
		}
	}

	// No PlanSpec, no Document const for the introspection op.
	if strings.Contains(out, "UsersListMutationsPlanSpec") {
		t.Errorf("introspection op should not have a PlanSpec const\n%s", out)
	}
	if strings.Contains(out, "UsersListMutationsDocument") {
		t.Errorf("introspection op should not have a Document const\n%s", out)
	}

	// The method body must unmarshal the const into a typed response.
	if !strings.Contains(out, "json.Unmarshal([]byte("+constName+")") {
		t.Errorf("method body should unmarshal the bake const\n%s", out)
	}

	// And the federation call path must not appear for this op.
	if strings.Contains(out, "Users_ListMutations: resolve plan") {
		t.Errorf("introspection op should not use the federation resolve path\n%s", out)
	}

	// The baked JSON should be valid JSON that round-trips into a map with the
	// expected shape — guards against the template embedding a malformed const.
	resp := extractBakeResponse(t, out, constName)
	verifyMutationFieldNames(t, resp, []string{
		"delete_user_data_for_service_users",
		"delete_user_data_for_service_progress",
		"rename_user",
	})
}

// extractBakeResponse pulls the backtick-quoted JSON payload out of
// `const Name = ` + "`" + `...` + "`" + ` and returns it parsed as a map.
func extractBakeResponse(t *testing.T, src, constName string) map[string]any {
	t.Helper()
	needle := "const " + constName + " = `"
	i := strings.Index(src, needle)
	if i < 0 {
		t.Fatalf("const %q not found in source", constName)
	}
	rest := src[i+len(needle):]
	j := strings.Index(rest, "`")
	if j < 0 {
		t.Fatalf("unterminated backtick string after %q", constName)
	}
	payload := rest[:j]
	out := map[string]any{}
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("bake response is not valid JSON: %v\npayload=%s", err, payload)
	}
	return out
}

// verifyMutationFieldNames asserts the bake response has the expected
// __schema.mutationType.fields[].name list.
func verifyMutationFieldNames(t *testing.T, resp map[string]any, wantNames []string) {
	t.Helper()
	schema, ok := resp["__schema"].(map[string]any)
	if !ok {
		t.Fatalf("__schema missing")
	}
	mutationType, ok := schema["mutationType"].(map[string]any)
	if !ok {
		t.Fatalf("mutationType missing")
	}
	fields, ok := mutationType["fields"].([]any)
	if !ok {
		t.Fatalf("fields missing")
	}
	gotNames := make(map[string]bool, len(fields))
	for _, f := range fields {
		m, ok := f.(map[string]any)
		if !ok {
			continue
		}
		if n, ok := m["name"].(string); ok {
			gotNames[n] = true
		}
	}
	for _, w := range wantNames {
		if !gotNames[w] {
			t.Errorf("baked response missing mutation field %q (got %v)", w, gotNames)
		}
	}
}
