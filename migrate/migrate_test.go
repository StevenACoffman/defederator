package migrate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureGenqlientYAML = `
schema: ../../schema/composed_schema.graphql
operations:
- cross_service/*.go
generated: generated/genqlient/queries.go
bindings:
  DateTime:
    type: time.Time
  Date:
    type: string
`

const fixtureSupergraphSDL = `
directive @join__graph(identifier: String!, url: String!) on ENUM_VALUE

enum join__Graph {
  CONTENT @join__graph(identifier: "content", url: "unused")
  USERS @join__graph(identifier: "users", url: "unused")
}

type Query {
  _placeholder: String
}
`

const fixtureGoMod = `module github.com/Khan/webapp

go 1.22
`

// setupFixtureDir creates a temporary service directory with:
//   - genqlient.yaml
//   - go.mod two levels up (simulating webapp root)
//   - supergraph SDL at the schema path
func setupFixtureDir(t *testing.T) string {
	t.Helper()
	// Simulate: /tmp/xxx/webapp/services/example/
	root := t.TempDir()
	serviceDir := filepath.Join(root, "services", "example")
	schemaDir := filepath.Join(root, "schema")

	for _, d := range []string{serviceDir, schemaDir, filepath.Join(serviceDir, "cross_service")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	// go.mod at webapp root.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(fixtureGoMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	// Supergraph SDL.
	if err := os.WriteFile(
		filepath.Join(schemaDir, "composed_schema.graphql"),
		[]byte(fixtureSupergraphSDL),
		0o644,
	); err != nil {
		t.Fatalf("write SDL: %v", err)
	}
	// genqlient.yaml (schema path is relative to service dir).
	gqYAML := strings.ReplaceAll(fixtureGenqlientYAML, "../../schema/", "../../schema/")
	if err := os.WriteFile(
		filepath.Join(serviceDir, "genqlient.yaml"),
		[]byte(gqYAML),
		0o644,
	); err != nil {
		t.Fatalf("write genqlient.yaml: %v", err)
	}
	return serviceDir
}

func TestRun_WritesFiles(t *testing.T) {
	dir := setupFixtureDir(t)
	err := Run(context.Background(), dir, Options{Force: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	defedYML := filepath.Join(dir, ".defederator.yml")
	if _, err := os.Stat(defedYML); err != nil {
		t.Errorf(".defederator.yml not written: %v", err)
	}
	clientGo := filepath.Join(dir, "cross_service", "client.go")
	if _, err := os.Stat(clientGo); err != nil {
		t.Errorf("cross_service/client.go not written: %v", err)
	}
}

func TestRun_DefederatorYMLContent(t *testing.T) {
	dir := setupFixtureDir(t)
	if err := Run(context.Background(), dir, Options{Force: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".defederator.yml"))
	if err != nil {
		t.Fatalf("read .defederator.yml: %v", err)
	}
	content := string(data)
	checks := []string{
		"url_mode: enum",
		"clientInterfaceName: FederationClient",
		"optional: pointer",
		"defederator",
		"DateTime:",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf(".defederator.yml missing %q\ncontent:\n%s", want, content)
		}
	}
}

func TestRun_ClientGoContent(t *testing.T) {
	dir := setupFixtureDir(t)
	if err := Run(context.Background(), dir, Options{Force: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "cross_service", "client.go"))
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	content := string(data)
	// The fixture has no operation files in cross_service/, so the pruned
	// _subgraphServices map is empty. We assert structural correctness only;
	// the per-service smoke tests cover the populated case end-to-end.
	checks := []string{
		"package cross_service",
		"newFederationClient",
		"exampleSubgraphURLs",
		"_subgraphServices",
		"errors.Wrap",
		"DO NOT EDIT",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("client.go missing %q\ncontent:\n%s", want, content)
		}
	}
}

func TestRun_DryRun_WritesNothing(t *testing.T) {
	dir := setupFixtureDir(t)
	if err := Run(context.Background(), dir, Options{DryRun: true}); err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".defederator.yml")); err == nil {
		t.Error("dry-run should not write .defederator.yml")
	}
}

func TestRun_NoForce_SkipsExisting(t *testing.T) {
	dir := setupFixtureDir(t)
	// Pre-write a sentinel file.
	sentinel := []byte("# sentinel\n")
	if err := os.WriteFile(filepath.Join(dir, ".defederator.yml"), sentinel, 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := Run(context.Background(), dir, Options{Force: false}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".defederator.yml"))
	if !bytes.Equal(data, sentinel) {
		t.Error("existing .defederator.yml was overwritten without --force")
	}
}

func TestRun_NoGenqlientYAML_Errors(t *testing.T) {
	dir := t.TempDir()
	err := Run(context.Background(), dir, Options{})
	if err == nil {
		t.Fatal("expected error for missing genqlient.yaml")
	}
}

func TestParseModulePath(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"module github.com/Khan/webapp\n\ngo 1.22\n", "github.com/Khan/webapp"},
		{"  module   github.com/foo/bar  \n", "github.com/foo/bar"},
	}
	for _, tc := range cases {
		got, err := parseModulePath(tc.input)
		if err != nil {
			t.Errorf("parseModulePath(%q): unexpected error %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseModulePath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
