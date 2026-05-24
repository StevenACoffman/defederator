package generator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	defConfig "github.com/StevenACoffman/defederator/config"
	"github.com/gqlgo/gqlgenc/clientgenv2"
)

func TestExportOperations_Unit(t *testing.T) {
	ops := []*clientgenv2.Operation{
		{Name: "GetUser", Operation: "query GetUser { id }", ResponseStructName: "GetUser"},
		{Name: "ListProducts", Operation: "query ListProducts { products { id } }", ResponseStructName: "ListProducts"},
	}
	tmp := t.TempDir()
	outFile := filepath.Join(tmp, "ops.json")

	if err := exportOperations(outFile, ops); err != nil {
		t.Fatalf("exportOperations: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	var got []exportedOperation
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 operations, got %d", len(got))
	}
	if got[0].Name != "GetUser" {
		t.Errorf("got[0].Name: want %q, got %q", "GetUser", got[0].Name)
	}
	if got[1].ResponseType != "ListProducts" {
		t.Errorf("got[1].ResponseType: want %q, got %q", "ListProducts", got[1].ResponseType)
	}
}

func TestExportOperations_EmptySlice(t *testing.T) {
	tmp := t.TempDir()
	outFile := filepath.Join(tmp, "ops.json")

	if err := exportOperations(outFile, nil); err != nil {
		t.Fatalf("exportOperations with nil: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	// nil slice marshals as "null" but we want "[]"
	// (we use make([]exportedOperation, 0) so this is always valid JSON array)
	var got []exportedOperation
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}
}

func TestGenerate_ExportOperations(t *testing.T) {
	supergraphRel := filepath.Join("..", "..", "gorouter", "federation", "testdata", "golden", "supergraph.graphql")
	supergraphPath, err := filepath.Abs(supergraphRel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(supergraphPath); os.IsNotExist(err) {
		t.Skip("supergraph fixture not found, skipping")
	}

	query := `query GetProductIdSku {
  product(id: "apollo-federation") { id sku }
}`
	tmp := t.TempDir()
	queryFile := filepath.Join(tmp, "query.graphql")
	if err := os.WriteFile(queryFile, []byte(query), 0644); err != nil {
		t.Fatal(err)
	}

	exportFile := filepath.Join(tmp, "operations.json")
	cfg := &defConfig.Config{
		Schema: supergraphPath,
		Query:  []string{queryFile},
		Client: defConfig.PackageConfig{Filename: filepath.Join(tmp, "client.go"), Package: "generated"},
		Dir:    tmp,
		Generate: &defConfig.GenerateConfig{
			ExportOperations: exportFile,
		},
	}

	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	data, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}

	var ops []exportedOperation
	if err := json.Unmarshal(data, &ops); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("want 1 op, got %d: %v", len(ops), ops)
	}
	if ops[0].Name != "GetProductIdSku" {
		t.Errorf("Name: want %q, got %q", "GetProductIdSku", ops[0].Name)
	}
}
