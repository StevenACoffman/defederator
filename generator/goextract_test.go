package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractQueriesFromGoFile(t *testing.T) {
	src := `package testpkg

const queryA = ` + "`" + `# @genqlient
query GetUser($id: ID!) {
  user(id: $id) {
    name
  }
}` + "`" + `

const queryB = ` + "`" + `# @genqlient
query ListProducts {
  products {
    id
  }
}` + "`" + `

const notAQuery = "select * from users"

var alsoNotAQuery = ` + "`" + `query without annotation {}` + "`" + `
`

	tmp := t.TempDir()
	f := filepath.Join(tmp, "ops.go")
	if err := os.WriteFile(f, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	queries, err := extractQueriesFromGoFile(f)
	if err != nil {
		t.Fatalf("extractQueriesFromGoFile: %v", err)
	}

	if len(queries) != 2 {
		t.Fatalf("want 2 queries, got %d: %v", len(queries), queries)
	}

	for _, want := range []string{"GetUser", "ListProducts"} {
		found := false
		for _, q := range queries {
			if strings.Contains(q.text, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("operation %q not found in any extracted query", want)
		}
	}
}

func TestExtractQueriesFromGoFile_SourcePositions(t *testing.T) {
	// Line 3 of this source is the start of queryA's string literal.
	src := "package testpkg\n\nconst queryA = `# @genqlient\nquery GetUser { id }`\n"

	tmp := t.TempDir()
	f := filepath.Join(tmp, "ops.go")
	if err := os.WriteFile(f, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	queries, err := extractQueriesFromGoFile(f)
	if err != nil {
		t.Fatalf("extractQueriesFromGoFile: %v", err)
	}
	if len(queries) != 1 {
		t.Fatalf("want 1 query, got %d", len(queries))
	}
	if !strings.Contains(queries[0].source, "ops.go") {
		t.Errorf("source %q should contain filename", queries[0].source)
	}
	if !strings.Contains(queries[0].source, ":3") {
		t.Errorf("source %q should contain line number 3", queries[0].source)
	}
}

func TestExtractQueriesFromGoFile_NoAnnotations(t *testing.T) {
	src := `package testpkg
const x = "query NoAnnotation { field }"
`
	tmp := t.TempDir()
	f := filepath.Join(tmp, "ops.go")
	if err := os.WriteFile(f, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	queries, err := extractQueriesFromGoFile(f)
	if err != nil {
		t.Fatalf("extractQueriesFromGoFile: %v", err)
	}
	if len(queries) != 0 {
		t.Errorf("want 0 queries, got %d", len(queries))
	}
}

