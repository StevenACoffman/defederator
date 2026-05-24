package generator

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	defConfig "github.com/StevenACoffman/defederator/config"
)

// namedQuery maps each golden fixture directory name to the named query
// that should be used for code generation (the golden fixtures use anonymous
// queries which gqlgenc cannot process; we add "query OpN" prefixes).
var namedQuery = map[string]string{
	"01_product_id_sku": `query GetProductIdSku {
  product(id: "apollo-federation") {
    id
    sku
  }
}`,
	"02_product_delivery": `query GetProductDelivery {
  product(id: "apollo-federation") {
    id
    delivery(zip: "94111") {
      estimatedDelivery
      fastestDelivery
    }
  }
}`,
	"03_product_creator_name": `query GetProductCreatorName {
  product(id: "apollo-federation") {
    createdBy {
      email
      name
    }
  }
}`,
	"04_product_creator_requires": `query GetProductCreatorRequires {
  product(id: "apollo-federation") {
    createdBy {
      email
      averageProductsCreatedPerYear
    }
  }
}`,
	"05_product_creator_provides": `query GetProductCreatorProvides {
  product(id: "apollo-federation") {
    createdBy {
      email
      totalProductsCreated
    }
  }
}`,
}

// perCaseChecks lists strings that must appear in the generated output for
// each golden fixture, and strings that must NOT appear.
var perCaseChecks = map[string]struct {
	want    []string
	notWant []string
}{
	// gqlgen's Go-name normaliser capitalises "Id" → "ID", so the generated
	// names are GetProductIDSku / GetProductIDSkuDocument rather than the
	// literal query operation name GetProductIdSku.
	"01_product_id_sku": {
		want: []string{
			"GetProductIDSkuDocument",
			"GetProductIDSkuPlanSpec",
			`func (c *Client) GetProductIDSku(`,
			"ResolveURLSpec",
			"ExecuteAndUnmarshal",
		},
		notWant: []string{"clientv2", "federationclient"},
	},
	"02_product_delivery": {
		want: []string{
			"GetProductDeliveryDocument",
			"GetProductDeliveryPlanSpec",
			`func (c *Client) GetProductDelivery(`,
			"ResolveURLSpec",
			"ExecuteAndUnmarshal",
		},
		notWant: []string{"clientv2", "federationclient"},
	},
	"03_product_creator_name": {
		want: []string{
			"GetProductCreatorNameDocument",
			"GetProductCreatorNamePlanSpec",
			`func (c *Client) GetProductCreatorName(`,
			"ResolveURLSpec",
			"ExecuteAndUnmarshal",
		},
		notWant: []string{"clientv2", "federationclient"},
	},
	"04_product_creator_requires": {
		want: []string{
			"GetProductCreatorRequiresDocument",
			"GetProductCreatorRequiresPlanSpec",
			`func (c *Client) GetProductCreatorRequires(`,
			"ResolveURLSpec",
			"ExecuteAndUnmarshal",
		},
		notWant: []string{"clientv2", "federationclient"},
	},
	"05_product_creator_provides": {
		want: []string{
			"GetProductCreatorProvidesDocument",
			"GetProductCreatorProvidesPlanSpec",
			`func (c *Client) GetProductCreatorProvides(`,
			"ResolveURLSpec",
			"ExecuteAndUnmarshal",
		},
		notWant: []string{"clientv2", "federationclient"},
	},
}

// TestCodegenCompile generates typed federation clients for each of the 5 golden
// fixture queries, verifies the output is syntactically valid Go, and checks that
// each file contains the expected function signatures, type names, and imports.
func TestCodegenCompile(t *testing.T) {
	supergraphRel := filepath.Join("..", "..", "gorouter", "federation", "testdata", "golden", "supergraph.graphql")
	supergraphPath, err := filepath.Abs(supergraphRel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err2 := os.Stat(supergraphPath); os.IsNotExist(err2) {
		t.Skip("supergraph fixture not found, skipping")
	}

	for fixtureName, query := range namedQuery {
		fixtureName, query := fixtureName, query
		t.Run(fixtureName, func(t *testing.T) {
			// Not parallel: gqlgen templates.Render uses a global lock that
			// panics on concurrent invocations.
			runCodegenCompileCase(t, supergraphPath, fixtureName, query)
		})
	}
}

// TestCodegenCompileOptional verifies that optional: value strips pointer types
// for nullable fields and optional: pointer (explicit) preserves them.
func TestCodegenCompileOptional(t *testing.T) {
	supergraphRel := filepath.Join("..", "..", "gorouter", "federation", "testdata", "golden", "supergraph.graphql")
	supergraphPath, err := filepath.Abs(supergraphRel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(supergraphPath); os.IsNotExist(err) {
		t.Skip("supergraph fixture not found, skipping")
	}

	// fixture 01: Sku is nullable in the schema (String), ID is non-null (ID!).
	// With optional: pointer (default): Sku → *string.
	// With optional: value:             Sku → string.
	query := namedQuery["01_product_id_sku"]

	t.Run("optional_value", func(t *testing.T) {
		out := runCodegenWithOptional(t, supergraphPath, query, "value")
		if strings.Contains(out, "Sku *string") {
			t.Error("optional: value should produce Sku string, got Sku *string")
		}
		if !strings.Contains(out, "Sku string") {
			t.Error("optional: value should produce Sku string")
		}
	})

	t.Run("optional_pointer_explicit", func(t *testing.T) {
		out := runCodegenWithOptional(t, supergraphPath, query, "pointer")
		if !strings.Contains(out, "Sku *string") {
			t.Error("optional: pointer should produce Sku *string")
		}
	})

	t.Run("optional_default", func(t *testing.T) {
		out := runCodegenWithOptional(t, supergraphPath, query, "")
		if !strings.Contains(out, "Sku *string") {
			t.Error("empty optional (default) should produce Sku *string")
		}
	})
}

func runCodegenWithOptional(t *testing.T, supergraphPath, query, optional string) string {
	t.Helper()
	tmpDir := t.TempDir()
	queryFile := filepath.Join(tmpDir, "query.graphql")
	if err := os.WriteFile(queryFile, []byte(query), 0644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(tmpDir, "client.go")
	cfg := &defConfig.Config{
		Schema: supergraphPath,
		Query:  []string{queryFile},
		Client: defConfig.PackageConfig{Filename: outFile, Package: "generated"},
		Dir:    tmpDir,
		Generate: &defConfig.GenerateConfig{
			Optional: optional,
		},
	}
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("optional=%q output:\n%s", optional, data)
	return string(data)
}

func runCodegenCompileCase(t *testing.T, supergraphPath, fixtureName, query string) {
	t.Helper()

	tmpDir := t.TempDir()

	// Write the named query to a temp file.
	queryFile := filepath.Join(tmpDir, "query.graphql")
	if err := os.WriteFile(queryFile, []byte(query), 0644); err != nil {
		t.Fatalf("write query file: %v", err)
	}

	outFile := filepath.Join(tmpDir, "client.go")
	cfg := &defConfig.Config{
		Schema: supergraphPath,
		Query:  []string{queryFile},
		Client: defConfig.PackageConfig{
			Filename: outFile,
			Package:  "generated",
		},
		Dir: tmpDir,
	}

	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Read the generated file.
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	out := string(data)
	t.Logf("generated output for %s:\n%s", fixtureName, out)

	// 1. Parse for syntax validity.
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, outFile, nil, parser.AllErrors); err != nil {
		t.Errorf("generated file has syntax errors: %v", err)
	}

	// 1b. Verify federation_exec.go was written alongside client.go.
	execFile := filepath.Join(tmpDir, "federation_exec.go")
	if _, err := os.Stat(execFile); os.IsNotExist(err) {
		t.Errorf("federation_exec.go not written to output dir")
	}

	// 2. Check for expected and forbidden content.
	checks, ok := perCaseChecks[fixtureName]
	if !ok {
		t.Fatalf("no per-case checks defined for %q", fixtureName)
	}

	for _, want := range checks.want {
		if !strings.Contains(out, want) {
			t.Errorf("generated file missing %q", want)
		}
	}
	for _, notWant := range checks.notWant {
		if strings.Contains(out, notWant) {
			t.Errorf("generated file contains forbidden string %q", notWant)
		}
	}
}
