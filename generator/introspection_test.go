package generator_test

import (
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/StevenACoffman/defederator/generator"
)

// parseOp parses a GraphQL query string and returns its single operation. The
// helper fails the test if the document doesn't have exactly one operation,
// because every test case here is built to exercise exactly one operation.
func parseOp(t *testing.T, query string) *ast.OperationDefinition {
	t.Helper()
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil {
		t.Fatalf("parse %q: %v", query, err)
	}
	if len(doc.Operations) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(doc.Operations))
	}
	return doc.Operations[0]
}

func TestIsIntrospectionOnly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		query string
		want  bool
	}{
		{
			name:  "schema_only",
			query: `query Q { __schema { types { name } } }`,
			want:  true,
		},
		{
			name:  "type_only",
			query: `query Q { __type(name: "Foo") { kind } }`,
			want:  true,
		},
		{
			name:  "typename_only",
			query: `query Q { __typename }`,
			want:  true,
		},
		{
			name:  "schema_and_type",
			query: `query Q { __schema { types { name } } __type(name: "Foo") { kind } }`,
			want:  true,
		},
		{
			name:  "mixed_business_and_introspection",
			query: `query Q { __schema { types { name } } user { id } }`,
			want:  false,
		},
		{
			name:  "business_only",
			query: `query Q { user { id } }`,
			want:  false,
		},
		{
			name:  "subscription_with_introspection",
			query: `subscription Q { __schema { types { name } } }`,
			want:  false,
		},
		{
			name:  "private_underscored_non_introspection",
			query: `query Q { __notReal { x } }`,
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			op := parseOp(t, tc.query)
			got := generator.IsIntrospectionOnly(op)
			if got != tc.want {
				t.Errorf("IsIntrospectionOnly(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

func TestPlanIntrospection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		query        string
		wantBakeable bool
		wantVars     []string
	}{
		{
			name:         "no_args",
			query:        `query Q { __schema { types { name } } }`,
			wantBakeable: true,
			wantVars:     []string{},
		},
		{
			name:         "literal_arg",
			query:        `query Q { __type(name: "Foo") { kind } }`,
			wantBakeable: true,
			wantVars:     []string{},
		},
		{
			name:         "variable_arg",
			query:        `query Q($n: String!) { __type(name: $n) { kind } }`,
			wantBakeable: false,
			wantVars:     []string{"n"},
		},
		{
			name:         "variable_in_nested_field_arg",
			query:        `query Q($d: Boolean!) { __schema { types { fields(includeDeprecated: $d) { name } } } }`,
			wantBakeable: false,
			wantVars:     []string{"d"},
		},
		{
			name:         "same_variable_used_twice",
			query:        `query Q($n: String!) { a: __type(name: $n) { kind } b: __type(name: $n) { name } }`,
			wantBakeable: false,
			wantVars:     []string{"n"},
		},
		{
			name:         "two_distinct_variables_first_encounter_order",
			query:        `query Q($a: String!, $b: Boolean!) { __type(name: $a) { fields(includeDeprecated: $b) { name } } }`,
			wantBakeable: false,
			wantVars:     []string{"a", "b"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			op := parseOp(t, tc.query)
			plan := generator.PlanIntrospection(op)
			if plan.Bakeable != tc.wantBakeable {
				t.Errorf("Bakeable = %v, want %v", plan.Bakeable, tc.wantBakeable)
			}
			if !stringSlicesEqual(plan.Variables, tc.wantVars) {
				t.Errorf("Variables = %v, want %v", plan.Variables, tc.wantVars)
			}
		})
	}
}

// stringSlicesEqual returns true when a and b have identical elements in the
// same order. nil and empty are treated as equal because PlanIntrospection
// initialises an empty slice; callers shouldn't have to distinguish.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
