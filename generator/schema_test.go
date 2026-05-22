package generator

import (
	"strings"
	"testing"
)

const testSupergraph = `
schema
  @link(url: "https://specs.apollo.dev/link/v1.0")
  @link(url: "https://specs.apollo.dev/join/v0.3", for: EXECUTION)
{
  query: Query
}

directive @join__field(graph: join__Graph, requires: join__FieldSet, external: Boolean) repeatable on FIELD_DEFINITION
directive @join__graph(name: String!, url: String!) on ENUM_VALUE
directive @join__type(graph: join__Graph!, key: join__FieldSet) repeatable on OBJECT
directive @link(url: String) repeatable on SCHEMA
directive @custom on OBJECT

scalar join__FieldSet

enum join__Graph {
  PRODUCTS @join__graph(name: "products", url: "http://localhost:4002")
}

type Query {
  product(id: ID!): Product @join__field(graph: PRODUCTS)
}

type Product
  @join__type(graph: PRODUCTS, key: "id")
  @custom
{
  id: ID! @join__field(graph: PRODUCTS)
  name: String @join__field(graph: PRODUCTS)
}
`

func TestStripFederationTypes(t *testing.T) {
	clean, err := StripFederationTypes(testSupergraph)
	if err != nil {
		t.Fatalf("StripFederationTypes: %v", err)
	}

	// Federation-specific names must not appear.
	for _, forbidden := range []string{"join__", "link__", "@link", "@join__"} {
		if strings.Contains(clean, forbidden) {
			t.Errorf("output still contains %q:\n%s", forbidden, clean)
		}
	}

	// User-visible types must be present.
	for _, want := range []string{"type Product", "type Query", "directive @custom"} {
		if !strings.Contains(clean, want) {
			t.Errorf("output missing %q:\n%s", want, clean)
		}
	}

	t.Logf("clean SDL:\n%s", clean)
}
