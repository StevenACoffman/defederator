package gqlgencfed_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/api"
	gqlgenConfig "github.com/99designs/gqlgen/codegen/config"
	gqlgencConfig "github.com/gqlgo/gqlgenc/config"
	gqlgencGenerator "github.com/gqlgo/gqlgenc/generator"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/StevenACoffman/defederator/generator"
	"github.com/StevenACoffman/defederator/gqlgencfed"
)

func TestPluginViaGenerateGenerate(t *testing.T) {
	supergraphRel := filepath.Join(
		"..",
		"..",
		"gorouter",
		"federation",
		"testdata",
		"golden",
		"supergraph.graphql",
	)
	supergraphPath, err := filepath.Abs(supergraphRel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err2 := os.Stat(supergraphPath); os.IsNotExist(err2) {
		t.Skip("supergraph fixture not found")
	}

	sdlBytes, err := os.ReadFile(supergraphPath)
	if err != nil {
		t.Fatal(err)
	}
	cleanSDL, err := generator.StripFederationTypes(string(sdlBytes))
	if err != nil {
		t.Fatalf("strip: %v", err)
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
`), 0o644); err2 != nil {
		t.Fatal(err2)
	}

	outFile := filepath.Join(tmpDir, "client.go")
	clientPkg := gqlgenConfig.PackageConfig{
		Filename: outFile,
		Package:  "generated",
	}
	generateCfg := &gqlgencConfig.GenerateConfig{}

	// Build the gqlgenc config. Setting SchemaFilename to a non-nil sentinel slice
	// causes LoadSchema to use loadLocalSchema (which reads GQLConfig.Sources) rather
	// than attempting remote introspection.
	gqlCfg := gqlgenConfig.DefaultConfig()
	gqlCfg.Sources = []*ast.Source{{Name: "supergraph", Input: cleanSDL}}

	gqlgencCfg := &gqlgencConfig.Config{
		GQLConfig:      gqlCfg,
		SchemaFilename: gqlgencConfig.StringList{"supergraph"}, // non-nil → loadLocalSchema
		Query:          []string{queryFile},
		Client:         clientPkg,
		Generate:       generateCfg,
	}

	err = gqlgencGenerator.Generate(
		context.Background(),
		gqlgencCfg,
		api.ReplacePlugin(
			gqlgencfed.NewWithFilePaths(
				[]string{queryFile},
				clientPkg,
				generateCfg,
				string(sdlBytes),
			),
		),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	out := string(data)

	for _, notWant := range []string{"clientv2", "federationclient"} {
		if strings.Contains(out, notWant) {
			t.Errorf("output contains forbidden string %q", notWant)
		}
	}
	for _, want := range []string{
		"GetProductDocument",
		"GetProductPlanSpec",
		"ResolveURLSpec",
		"ExecuteAndUnmarshal",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}

	execFile := filepath.Join(tmpDir, "federation_exec.go")
	if _, err := os.Stat(execFile); os.IsNotExist(err) {
		t.Errorf("federation_exec.go not written to output dir")
	}
	t.Logf("plugin-generated output:\n%s", out)
}
