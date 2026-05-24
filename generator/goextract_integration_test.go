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

// TestCodegenFromGoFile runs the full Generate pipeline using a .go source file
// that embeds a # @genqlient query. The generated output should be identical in
// structure to the equivalent .graphql variant.
func TestCodegenFromGoFile(t *testing.T) {
	supergraphRel := filepath.Join("..", "..", "gorouter", "federation", "testdata", "golden", "supergraph.graphql")
	supergraphPath, err := filepath.Abs(supergraphRel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(supergraphPath); os.IsNotExist(err) {
		t.Skip("supergraph fixture not found, skipping")
	}

	// Embed the same query as the first golden fixture inside a Go source file.
	goSrc := `package ops

const GetProductIDSkuQuery = ` + "`" + `# @genqlient
query GetProductIdSku {
  product(id: "apollo-federation") {
    id
    sku
  }
}` + "`"

	tmpDir := t.TempDir()
	goFile := filepath.Join(tmpDir, "ops.go")
	if err := os.WriteFile(goFile, []byte(goSrc), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}

	outFile := filepath.Join(tmpDir, "client.go")
	cfg := &defConfig.Config{
		Schema: supergraphPath,
		Query:  []string{goFile},
		Client: defConfig.PackageConfig{
			Filename: outFile,
			Package:  "generated",
		},
		Dir: tmpDir,
	}

	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	out := string(data)
	t.Logf("generated output:\n%s", out)

	// Syntax check.
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, outFile, nil, parser.AllErrors); err != nil {
		t.Errorf("syntax error in generated file: %v", err)
	}

	// Content checks — same as the 01_product_id_sku golden fixture.
	for _, want := range []string{
		"GetProductIDSkuDocument",
		"GetProductIDSkuPlanSpec",
		`func (c *Client) GetProductIDSku(`,
		"ResolveURLSpec",
		"ExecuteAndUnmarshal",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated file missing %q", want)
		}
	}
	for _, notWant := range []string{"clientv2", "federationclient"} {
		if strings.Contains(out, notWant) {
			t.Errorf("generated file contains forbidden string %q", notWant)
		}
	}

	execFile := filepath.Join(tmpDir, "federation_exec.go")
	if _, err := os.Stat(execFile); os.IsNotExist(err) {
		t.Errorf("federation_exec.go not written to output dir")
	}
}
