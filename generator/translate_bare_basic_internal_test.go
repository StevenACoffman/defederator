package generator

import "testing"

func TestTranslateBareBasic(t *testing.T) {
	t.Parallel()

	const graphqlPkg = "github.com/99designs/gqlgen/graphql"

	cases := map[string]struct {
		in   string
		want string
	}{
		"string maps to graphql.String": {"string", graphqlPkg + ".String"},
		"bool maps to graphql.Boolean":  {"bool", graphqlPkg + ".Boolean"},
		"int maps to graphql.Int":       {"int", graphqlPkg + ".Int"},
		"int32 maps to graphql.Int":     {"int32", graphqlPkg + ".Int"},
		"int64 maps to graphql.Int64":   {"int64", graphqlPkg + ".Int64"},
		"float32 maps to graphql.Float": {"float32", graphqlPkg + ".Float"},
		"float64 maps to graphql.Float": {"float64", graphqlPkg + ".Float"},
		"qualified path passes through": {"time.Time", "time.Time"},
		"khan path passes through": {
			"github.com/Khan/webapp/pkg/content.Author",
			"github.com/Khan/webapp/pkg/content.Author",
		},
		"interface{} passes through":  {"interface{}", "interface{}"},
		"map literal passes through":  {"map[string]interface{}", "map[string]interface{}"},
		"empty string passes through": {"", ""},
		"unrecognized passes through": {"uint8", "uint8"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := translateBareBasic(tc.in); got != tc.want {
				t.Errorf("translateBareBasic(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
