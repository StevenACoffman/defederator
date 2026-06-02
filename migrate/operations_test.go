package migrate_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/StevenACoffman/defederator/migrate"
)

func loadTestSchema(t *testing.T, sdl string) *ast.Schema {
	t.Helper()
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "test", Input: sdl})
	if err != nil {
		t.Fatalf("load schema: %v", err)
	}
	return schema
}

func TestOperationVariableInputObjects(t *testing.T) {
	const sdl = `
		input Filter { name: String }
		input UnusedInput { name: String }
		input Wrapper { f: Filter }
		type Query {
			search(f: Filter!): String
			searchMany(fs: [Filter!]!): String
			wrap(w: Wrapper!): String
			scalar(s: String!): String
		}
	`
	schema := loadTestSchema(t, sdl)

	cases := map[string]struct {
		query string
		want  []string
	}{
		"single input object": {
			query: `query Q($f: Filter!) { search(f: $f) }`,
			want:  []string{"Filter"},
		},
		"list of input object": {
			query: `query Q($fs: [Filter!]!) { searchMany(fs: $fs) }`,
			want:  []string{"Filter"},
		},
		"two input objects deduped across operations": {
			query: `
				query A($f: Filter!) { search(f: $f) }
				query B($w: Wrapper!) { wrap(w: $w) }
				query C($f: Filter!) { search(f: $f) }
			`,
			want: []string{"Filter", "Wrapper"},
		},
		"only scalars used": {
			query: `query Q($s: String!) { scalar(s: $s) }`,
			want:  nil,
		},
		"unused input object not reported": {
			// UnusedInput exists in schema but no operation references it.
			query: `query Q($f: Filter!) { search(f: $f) }`,
			want:  []string{"Filter"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sources := []*ast.Source{{Name: name, Input: tc.query}}
			got, err := migrate.OperationVariableInputObjects(schema, sources)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFilterSubgraphs(t *testing.T) {
	all := []migrate.SubgraphEntry{
		{EnumName: "CONTENT", ServiceName: "content"},
		{EnumName: "USERS", ServiceName: "users"},
		{EnumName: "SEARCH", ServiceName: "search"},
		{EnumName: "ADMIN", ServiceName: "admin"},
	}
	cases := map[string]struct {
		used     []string
		ownName  string
		wantSvcs []string // in declaration order
	}{
		"keeps only used": {
			used:     []string{"CONTENT", "USERS"},
			ownName:  "",
			wantSvcs: []string{"content", "users"},
		},
		"keeps own service even if unused": {
			used:     []string{"USERS"},
			ownName:  "admin",
			wantSvcs: []string{"users", "admin"},
		},
		"empty used returns empty": {
			used:     nil,
			ownName:  "admin",
			wantSvcs: nil,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := migrate.FilterSubgraphs(all, tc.used, tc.ownName)
			var svcs []string
			for _, e := range got {
				svcs = append(svcs, e.ServiceName)
			}
			if !reflect.DeepEqual(svcs, tc.wantSvcs) {
				t.Fatalf("got %v, want %v", svcs, tc.wantSvcs)
			}
		})
	}
}

func TestLoadOperationSources(t *testing.T) {
	dir := t.TempDir()
	// .graphql file with one query
	gqlPath := filepath.Join(dir, "q.graphql")
	if err := os.WriteFile(gqlPath, []byte(`query G { hello }`), 0o644); err != nil {
		t.Fatal(err)
	}
	// .go file with embedded @genqlient
	goPath := filepath.Join(dir, "ops.go")
	goSrc := "package x\n\nfunc f() {\n\t_ = `# @genqlient\n\tquery G2 { hi }\n\t`\n}\n"
	if err := os.WriteFile(goPath, []byte(goSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	sources, err := migrate.LoadOperationSources([]string{"*.graphql", "*.go"}, dir)
	if err != nil {
		t.Fatalf("LoadOperationSources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}
}
