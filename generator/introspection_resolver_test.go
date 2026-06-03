package generator_test

import (
	"encoding/json"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/StevenACoffman/defederator/generator"
)

// smallSchema is a self-contained schema with enough variety to exercise the
// introspection resolver: a regular object type, an enum with one deprecated
// value, an input object, and a directive. Federation directives are absent
// because gqlparser refuses to validate operations against them.
const smallSchema = `
"Schema description."
schema { query: Query }

"Top-level entrypoint."
type Query {
  user(id: ID!): User
  echo(in: EchoInput!, kind: Kind = NICE): String
}

"A registered learner."
type User {
  id: ID!
  name: String!
  legacyAlias: String @deprecated(reason: "use name")
}

"Tone with which to echo."
enum Kind {
  NICE
  MEAN @deprecated(reason: "be kind")
}

"Input for the echo field."
input EchoInput {
  msg: String!
  shout: Boolean = false
}

"Tag a field for special handling."
directive @special(level: Int = 1) on FIELD_DEFINITION
`

// parseSchemaAndOp loads smallSchema and parses the supplied query. The
// returned operation is the single operation in the parsed document. Test
// fixtures that need fragments should pass them through queryWithFragments.
func parseSchemaAndOp(
	t *testing.T,
	query string,
) (*ast.Schema, *ast.OperationDefinition, generator.FragmentsByName) {
	t.Helper()
	schema, gqlErr := gqlparser.LoadSchema(&ast.Source{Name: "test", Input: smallSchema})
	if gqlErr != nil {
		t.Fatalf("LoadSchema: %v", gqlErr)
	}
	doc, qErr := parser.ParseQuery(&ast.Source{Name: "q", Input: query})
	if qErr != nil {
		t.Fatalf("ParseQuery: %v", qErr)
	}
	if len(doc.Operations) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(doc.Operations))
	}
	fragments := generator.FragmentsByName{}
	for _, f := range doc.Fragments {
		fragments[f.Name] = f
	}
	return schema, doc.Operations[0], fragments
}

// resolveTo unmarshals the resolver's output into a generic map for ergonomic
// assertions in tests.
func resolveTo(
	t *testing.T,
	schema *ast.Schema,
	op *ast.OperationDefinition,
	fragments generator.FragmentsByName,
	vars map[string]any,
) map[string]any {
	t.Helper()
	raw, err := generator.ResolveIntrospection(schema, op, fragments, vars)
	if err != nil {
		t.Fatalf("ResolveIntrospection: %v", err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return out
}

func TestResolveIntrospection_SchemaTypes(t *testing.T) {
	t.Parallel()
	schema, op, frags := parseSchemaAndOp(t, `
		query Q {
			__schema {
				types { name }
				queryType { name }
				mutationType { name }
				subscriptionType { name }
			}
		}
	`)
	got := resolveTo(t, schema, op, frags, nil)

	sch, ok := got["__schema"].(map[string]any)
	if !ok {
		t.Fatalf("__schema missing or wrong shape: %T", got["__schema"])
	}

	// Query exists, mutation/subscription don't.
	wantNamedRoot(t, sch, "queryType", "Query")
	if sch["mutationType"] != nil {
		t.Errorf("mutationType = %v, want nil", sch["mutationType"])
	}
	if sch["subscriptionType"] != nil {
		t.Errorf("subscriptionType = %v, want nil", sch["subscriptionType"])
	}

	// Types includes our user-defined ones plus the built-ins.
	types, ok := sch["types"].([]any)
	if !ok {
		t.Fatalf("types not a list: %T", sch["types"])
	}
	names := typeNames(types)
	wantTypes := []string{"Query", "User", "Kind", "EchoInput"}
	for _, w := range wantTypes {
		if !contains(names, w) {
			t.Errorf("types missing %q; got %v", w, names)
		}
	}
}

func TestResolveIntrospection_MutationListShape(t *testing.T) {
	t.Parallel()
	// This is the exact selection shape Users_ListMutations uses:
	// __schema { mutationType { fields { name } } }. Our test schema has no
	// mutation type so mutationType is null — but Query mirrors the same
	// shape and we can verify field names land.
	schema, op, frags := parseSchemaAndOp(t, `
		query Q {
			__schema {
				queryType {
					fields { name }
				}
			}
		}
	`)
	got := resolveTo(t, schema, op, frags, nil)
	fields := nestedSlice(t, got, "__schema", "queryType", "fields")
	names := fieldNames(fields)
	for _, w := range []string{"user", "echo"} {
		if !contains(names, w) {
			t.Errorf("Query.fields missing %q; got %v", w, names)
		}
	}
}

func TestResolveIntrospection_TypeByName_Enum(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		query      string
		wantNames  []string
		wantLength int
	}{
		{
			name: "deprecated_excluded_by_default",
			query: `query Q { __type(name: "Kind") {
				kind name
				enumValues { name isDeprecated }
			}}`,
			wantNames:  []string{"NICE"},
			wantLength: 1,
		},
		{
			name: "deprecated_included_when_arg_true",
			query: `query Q { __type(name: "Kind") {
				kind name
				enumValues(includeDeprecated: true) { name isDeprecated deprecationReason }
			}}`,
			wantNames:  []string{"NICE", "MEAN"},
			wantLength: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schema, op, frags := parseSchemaAndOp(t, tc.query)
			got := resolveTo(t, schema, op, frags, nil)
			assertEnumType(t, got, tc.wantNames, tc.wantLength)
		})
	}
}

// assertEnumType verifies that got["__type"] is the ENUM kind and contains
// the expected number of values including the named ones.
func assertEnumType(t *testing.T, got map[string]any, wantNames []string, wantLength int) {
	t.Helper()
	typ, ok := got["__type"].(map[string]any)
	if !ok {
		t.Fatalf("__type missing: %v", got)
	}
	if typ["kind"] != "ENUM" {
		t.Errorf("kind = %v, want ENUM", typ["kind"])
	}
	vals, ok := typ["enumValues"].([]any)
	if !ok {
		t.Fatalf("enumValues not a list: %T", typ["enumValues"])
	}
	if len(vals) != wantLength {
		t.Errorf("len(enumValues) = %d, want %d", len(vals), wantLength)
	}
	names := enumNames(vals)
	for _, w := range wantNames {
		if !contains(names, w) {
			t.Errorf("missing enum value %q; got %v", w, names)
		}
	}
}

func TestResolveIntrospection_TypeByName_InputObject(t *testing.T) {
	t.Parallel()
	schema, op, frags := parseSchemaAndOp(t, `
		query Q { __type(name: "EchoInput") {
			kind name
			inputFields { name type { kind name ofType { kind name } } defaultValue }
		}}
	`)
	got := resolveTo(t, schema, op, frags, nil)
	typ, ok := got["__type"].(map[string]any)
	if !ok {
		t.Fatalf("__type missing")
	}
	if typ["kind"] != "INPUT_OBJECT" {
		t.Errorf("kind = %v, want INPUT_OBJECT", typ["kind"])
	}
	fields, ok := typ["inputFields"].([]any)
	if !ok {
		t.Fatalf("inputFields not a list: %T", typ["inputFields"])
	}
	if len(fields) != 2 {
		t.Errorf("len(inputFields) = %d, want 2", len(fields))
	}
}

func TestResolveIntrospection_VariableArgument(t *testing.T) {
	t.Parallel()
	schema, op, frags := parseSchemaAndOp(t, `
		query Q($n: String!) { __type(name: $n) { kind name } }
	`)
	cases := []struct {
		name     string
		vars     map[string]any
		wantKind any
		wantName any
	}{
		{
			name:     "resolves_existing_type",
			vars:     map[string]any{"n": "User"},
			wantKind: "OBJECT",
			wantName: "User",
		},
		{
			name:     "returns_null_for_unknown_type",
			vars:     map[string]any{"n": "NoSuch"},
			wantKind: nil,
			wantName: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveTo(t, schema, op, frags, tc.vars)
			assertTypeKindAndName(t, got["__type"], tc.wantKind, tc.wantName)
		})
	}
}

// assertTypeKindAndName checks that val is either a JSON null (when wantKind
// is nil) or a __Type-shaped object whose kind/name match expectations.
func assertTypeKindAndName(t *testing.T, val, wantKind, wantName any) {
	t.Helper()
	if wantKind == nil {
		if val != nil {
			t.Errorf("__type = %v, want nil", val)
		}
		return
	}
	m, ok := val.(map[string]any)
	if !ok {
		t.Fatalf("__type not a map: %T (%v)", val, val)
	}
	if m["kind"] != wantKind {
		t.Errorf("kind = %v, want %v", m["kind"], wantKind)
	}
	if m["name"] != wantName {
		t.Errorf("name = %v, want %v", m["name"], wantName)
	}
}

func TestResolveIntrospection_FragmentSpread(t *testing.T) {
	t.Parallel()
	schema, op, frags := parseSchemaAndOp(t, `
		query Q { __schema { ...SchemaShape } }
		fragment SchemaShape on __Schema { queryType { name } }
	`)
	got := resolveTo(t, schema, op, frags, nil)
	wantNamedRoot(t, got["__schema"].(map[string]any), "queryType", "Query")
}

// TestResolveIntrospection_RejectsBusinessField guards against silent drift:
// a non-introspection field at the operation root must fail loudly so the
// generator doesn't silently emit broken client code.
func TestResolveIntrospection_RejectsBusinessField(t *testing.T) {
	t.Parallel()
	schema, op, frags := parseSchemaAndOp(t, `query Q { user(id: "1") { name } }`)
	if _, err := generator.ResolveIntrospection(schema, op, frags, nil); err == nil {
		t.Fatalf("expected error on business field at root, got nil")
	}
}

// TestResolveIntrospection_UsersListMutationsShape is an end-to-end shape
// check against the exact query the recommendations service uses.
func TestResolveIntrospection_UsersListMutationsShape(t *testing.T) {
	t.Parallel()
	// We can't reuse smallSchema because it has no mutation type. Build a
	// dedicated schema with a known mutation root.
	const sdl = `
		schema { query: Q mutation: M }
		type Q { ping: String }
		type M {
			delete_user_data_for_service_users: Boolean!
			delete_user_data_for_service_progress: Boolean!
			unrelated: Boolean!
		}
	`
	schema, gqlErr := gqlparser.LoadSchema(&ast.Source{Name: "t", Input: sdl})
	if gqlErr != nil {
		t.Fatalf("LoadSchema: %v", gqlErr)
	}
	doc, qErr := parser.ParseQuery(&ast.Source{Name: "q", Input: `
		query Users_ListMutations {
			__schema {
				mutationType {
					fields { name }
				}
			}
		}
	`})
	if qErr != nil {
		t.Fatalf("ParseQuery: %v", qErr)
	}

	raw, err := generator.ResolveIntrospection(schema, doc.Operations[0], nil, nil)
	if err != nil {
		t.Fatalf("ResolveIntrospection: %v", err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	fields := nestedSlice(t, out, "__schema", "mutationType", "fields")
	names := fieldNames(fields)
	for _, w := range []string{
		"delete_user_data_for_service_users",
		"delete_user_data_for_service_progress",
		"unrelated",
	} {
		if !contains(names, w) {
			t.Errorf("mutation field %q missing; got %v", w, names)
		}
	}
}

// ----- small assertion helpers --------------------------------------------

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func typeNames(arr []any) []string {
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		if n, ok := m["name"].(string); ok {
			out = append(out, n)
		}
	}
	return out
}

func fieldNames(arr []any) []string {
	return typeNames(arr) // same shape — both have a "name" string
}

func enumNames(arr []any) []string {
	return typeNames(arr) // same shape
}

func nestedSlice(t *testing.T, m map[string]any, path ...string) []any {
	t.Helper()
	cur := any(m)
	for i, p := range path {
		nm, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("nested %v at index %d: not a map (%T)", path, i, cur)
		}
		cur = nm[p]
	}
	out, ok := cur.([]any)
	if !ok {
		t.Fatalf("nested %v: not a slice (%T)", path, cur)
	}
	return out
}

func wantNamedRoot(t *testing.T, sch map[string]any, key, name string) {
	t.Helper()
	m, ok := sch[key].(map[string]any)
	if !ok {
		t.Fatalf("%s missing or wrong shape: %T", key, sch[key])
	}
	if m["name"] != name {
		t.Errorf("%s.name = %v, want %v", key, m["name"], name)
	}
}
