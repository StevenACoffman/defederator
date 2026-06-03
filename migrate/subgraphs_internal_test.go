package migrate

import (
	"testing"
)

const minimalSupergraph = `
directive @join__graph(identifier: String!, url: String!) on ENUM_VALUE
directive @join__type(graph: join__Graph!) repeatable on OBJECT | INTERFACE | UNION | ENUM | INPUT_OBJECT | SCALAR

enum join__Graph {
  ADMIN @join__graph(identifier: "admin", url: "unused")
  AI_GUIDE @join__graph(identifier: "ai-guide", url: "unused")
  CONTENT_EDITING @join__graph(identifier: "content-editing", url: "unused")
  USERS @join__graph(identifier: "users", url: "unused")
}

input AdminOnlyInput @join__type(graph: ADMIN) {
  id: String!
}

input SharedInput @join__type(graph: ADMIN) @join__type(graph: USERS) {
  name: String!
}

input UsersInput @join__type(graph: USERS) {
  email: String!
}

type Query {
  _placeholder: String
}
`

const noIdentifierSupergraph = `
directive @join__graph(identifier: String!, url: String!) on ENUM_VALUE

enum join__Graph {
  AI_GUIDE @join__graph(url: "unused")
}

type Query {
  _placeholder: String
}
`

func TestParseSubgraphs(t *testing.T) {
	entries, err := ParseSubgraphs(minimalSupergraph)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []SubgraphEntry{
		{EnumName: "ADMIN", ServiceName: "admin"},
		{EnumName: "AI_GUIDE", ServiceName: "ai-guide"},
		{EnumName: "CONTENT_EDITING", ServiceName: "content-editing"},
		{EnumName: "USERS", ServiceName: "users"},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(entries), len(want))
	}
	for i, w := range want {
		if entries[i] != w {
			t.Errorf("entry[%d]: got %+v, want %+v", i, entries[i], w)
		}
	}
}

func TestParseSubgraphs_FallbackName(t *testing.T) {
	entries, err := ParseSubgraphs(noIdentifierSupergraph)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].ServiceName != "ai-guide" {
		t.Errorf("fallback service name: got %q, want %q", entries[0].ServiceName, "ai-guide")
	}
}

func TestParseSubgraphs_MissingEnum(t *testing.T) {
	_, err := ParseSubgraphs("type Query { _placeholder: String }")
	if err == nil {
		t.Fatal("expected error for missing join__Graph enum")
	}
}

func TestEnumToServiceName(t *testing.T) {
	cases := map[string]string{
		"ADMIN":            "admin",
		"AI_GUIDE":         "ai-guide",
		"CONTENT_EDITING":  "content-editing",
		"PROGRESS_REPORTS": "progress-reports",
	}
	for input, want := range cases {
		got := enumToServiceName(input)
		if got != want {
			t.Errorf("enumToServiceName(%q) = %q, want %q", input, got, want)
		}
	}
}
