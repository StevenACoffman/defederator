package generator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	defConfig "github.com/StevenACoffman/defederator/config"
)

func TestGenerate(t *testing.T) {
	supergraphRel := filepath.Join("..", "..", "gorouter", "federation", "testdata", "golden", "supergraph.graphql")
	supergraphPath, err := filepath.Abs(supergraphRel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err2 := os.Stat(supergraphPath); os.IsNotExist(err2) {
		t.Skip("supergraph fixture not found, skipping")
	}

	tmpDir := t.TempDir()
	queryFile := filepath.Join(tmpDir, "query.graphql")
	if err2 := os.WriteFile(queryFile, []byte(`
query GetProduct($id: ID!) {
  product(id: $id) {
    id
    sku
  }
}
`), 0644); err2 != nil {
		t.Fatal(err2)
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

	if err2 := Generate(context.Background(), cfg); err2 != nil {
		t.Fatalf("Generate: %v", err2)
	}

	data, err2 := os.ReadFile(outFile)
	if err2 != nil {
		t.Fatalf("read output: %v", err2)
	}
	out := string(data)

	for _, notWant := range []string{"clientv2", "federationclient"} {
		if strings.Contains(out, notWant) {
			t.Errorf("generated file contains forbidden string %q", notWant)
		}
	}
	for _, want := range []string{
		"GetProductDocument",
		"func (c *Client) GetProduct",
		"ResolveURLSpec",
		"ExecuteAndUnmarshal",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated file missing %q", want)
		}
	}

	execFile := filepath.Join(tmpDir, "federation_exec.go")
	if _, err := os.Stat(execFile); os.IsNotExist(err) {
		t.Errorf("federation_exec.go not written to output dir")
	}

	t.Logf("generated output:\n%s", out)
}
