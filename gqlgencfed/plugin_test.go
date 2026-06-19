package gqlgencfed_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gqlgenConfig "github.com/99designs/gqlgen/codegen/config"
	gqlgencConfig "github.com/gqlgo/gqlgenc/config"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/StevenACoffman/defederator/generator"
	"github.com/StevenACoffman/defederator/gqlgencfed"
)

type pluginFixture struct {
	rawSDL   string
	cleanSDL string
}

// TestPlugin_MutateConfig exercises the gqlgencfed plugin's ConfigMutator
// interface directly. The plugin used to be wired into gqlgenc's pipeline via
// `gqlgenc.Generate(ctx, cfg, api.ReplacePlugin(plugin))`, but gqlgenc v0.37.0
// removed the variadic plugin-option parameter from Generate. The plugin still
// implements plugin.ConfigMutator, so callers can invoke MutateConfig on a
// gqlgen Config they construct themselves.
func TestPlugin_MutateConfig(t *testing.T) {
	t.Parallel()

	fixture := loadPluginFixture(t)
	tmpDir := t.TempDir()
	queryFile := writeQueryFile(t, tmpDir)
	outFile := filepath.Join(tmpDir, "client.go")

	clientPkg := gqlgenConfig.PackageConfig{Filename: outFile, Package: "generated"}
	gqlCfg := buildGqlgenConfig(t, fixture.cleanSDL)
	plugin := gqlgencfed.NewWithFilePaths(
		[]string{queryFile},
		clientPkg,
		&gqlgencConfig.GenerateConfig{},
		fixture.rawSDL,
	)
	if err := plugin.MutateConfig(gqlCfg); err != nil {
		t.Fatalf("MutateConfig: %v", err)
	}

	out := readFile(t, outFile)
	assertContains(t, out,
		"GetProductDocument", "GetProductPlanSpec",
		"ResolveURLSpec", "ExecuteAndUnmarshal",
	)
	assertExcludes(t, out, "clientv2", "federationclient")

	execFile := filepath.Join(tmpDir, "federation_exec.go")
	if _, statErr := os.Stat(execFile); os.IsNotExist(statErr) {
		t.Errorf("federation_exec.go not written to output dir")
	}
}

func loadPluginFixture(t *testing.T) pluginFixture {
	t.Helper()
	path, err := filepath.Abs(filepath.Join(
		"..", "..", "gorouter", "federation", "testdata", "golden", "supergraph.graphql",
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		t.Skip("supergraph fixture not found")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := generator.StripFederationTypes(string(raw))
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	return pluginFixture{rawSDL: string(raw), cleanSDL: clean}
}

func writeQueryFile(t *testing.T, dir string) string {
	t.Helper()
	const query = `
query GetProduct($id: ID!) {
  product(id: $id) {
    id
    sku
  }
}
`
	path := filepath.Join(dir, "query.graphql")
	if err := os.WriteFile(path, []byte(query), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func buildGqlgenConfig(t *testing.T, cleanSDL string) *gqlgenConfig.Config {
	t.Helper()
	cfg := gqlgenConfig.DefaultConfig()
	cfg.Sources = []*ast.Source{{Name: "supergraph", Input: cleanSDL}}
	schema, err := gqlparser.LoadSchema(cfg.Sources...)
	if err != nil {
		t.Fatalf("load schema: %v", err)
	}
	cfg.Schema = schema
	if err := cfg.Init(); err != nil {
		t.Fatalf("init gqlgen config: %v", err)
	}
	return cfg
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertContains(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func assertExcludes(t *testing.T, body string, forbidden ...string) {
	t.Helper()
	for _, bad := range forbidden {
		if strings.Contains(body, bad) {
			t.Errorf("output contains forbidden string %q", bad)
		}
	}
}
