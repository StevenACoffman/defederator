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

	if strings.Contains(out, "clientv2") {
		t.Errorf("generated file references clientv2, should use federationclient")
	}
	for _, want := range []string{
		"federationclient",
		"GetProductDocument",
		"func (c *Client) GetProduct",
		"c.Client.Execute",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated file missing %q", want)
		}
	}

	t.Logf("generated output:\n%s", out)
}
