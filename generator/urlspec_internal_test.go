package generator

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/StevenACoffman/defederator/execengine"
	"github.com/StevenACoffman/gorouter/federation"
)

const urlspecFixtureSDL = "../../gorouter/federation/testdata/golden/supergraph.graphql"

// query01 is fixture 01: single-subgraph, no entity fetches.
const query01 = `query GetProductIdSku {
  product(id: "apollo-federation") { id sku }
}`

// query02 is fixture 02: PRODUCTS initial + INVENTORY entity fetch (@requires).
const query02 = `query GetProductDelivery {
  product(id: "apollo-federation") {
    id
    delivery(zip: "94111") { estimatedDelivery fastestDelivery }
  }
}`

func loadTestSupergraph(t *testing.T) *federation.Supergraph {
	t.Helper()
	sdlPath, err := filepath.Abs(urlspecFixtureSDL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(sdlPath)
	if err != nil {
		t.Skip("supergraph fixture not found; skipping")
	}
	sg, err := federation.ParseSchema(string(b))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	return sg
}

// TestMarshalURLPlanSpec_RoundTrip verifies that MarshalURLPlanSpec produces a
// JSON string that ResolveURLSpec can decode into a Plan with the correct URLs.
func TestMarshalURLPlanSpec_RoundTrip(t *testing.T) {
	sg := loadTestSupergraph(t)

	cases := map[string]struct {
		query            string
		wantFetchURLPart string // substring of URL baked into the spec
		wantEntityFetch  bool
	}{
		"single_subgraph": {
			query:            query01,
			wantFetchURLPart: "PRODUCTS_URL",
		},
		"with_entity_fetch": {
			query:            query02,
			wantFetchURLPart: "PRODUCTS_URL",
			wantEntityFetch:  true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			runURLPlanSpecCase(t, sg, tc.query, tc.wantFetchURLPart, tc.wantEntityFetch)
		})
	}
}

// runURLPlanSpecCase executes one TestMarshalURLPlanSpec_RoundTrip subtest.
func runURLPlanSpecCase(
	t *testing.T,
	sg *federation.Supergraph,
	query, wantFetchURLPart string,
	wantEntityFetch bool,
) {
	t.Helper()
	plan, err := federation.BuildPlan(sg, query, "")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	specJSON, err := MarshalURLPlanSpec(plan)
	if err != nil {
		t.Fatalf("MarshalURLPlanSpec: %v", err)
	}
	if strings.Contains(specJSON, "subgraphEnum") {
		t.Error("specJSON contains 'subgraphEnum'; expected URL-keyed format")
	}
	if !strings.Contains(specJSON, wantFetchURLPart) {
		t.Errorf("specJSON missing expected URL part %q:\n%s", wantFetchURLPart, specJSON)
	}
	resolved, err := execengine.ResolveURLSpec(specJSON)
	if err != nil {
		t.Fatalf("ResolveURLSpec: %v", err)
	}
	if len(resolved.Fetches) == 0 {
		t.Error("resolved plan has no fetches")
	}
	if !strings.Contains(resolved.Fetches[0].URL, wantFetchURLPart) {
		t.Errorf("resolved fetch URL %q does not contain %q",
			resolved.Fetches[0].URL, wantFetchURLPart)
	}
	if wantEntityFetch && len(resolved.EntityFetches) == 0 {
		t.Error("expected entity fetches in resolved plan")
	}
}

// TestMarshalEnumPlanSpec_RoundTrip verifies that MarshalEnumPlanSpec produces a
// JSON string with subgraphEnum keys (not URLs), and that execengine.Resolve can
// decode it into a valid Plan when supplied a URL map.
func TestMarshalEnumPlanSpec_RoundTrip(t *testing.T) {
	sg := loadTestSupergraph(t)

	cases := map[string]struct {
		query           string
		wantEnum        string
		wantEntityFetch bool
	}{
		"single_subgraph": {
			query:    query01,
			wantEnum: "PRODUCTS",
		},
		"with_entity_fetch": {
			query:           query02,
			wantEnum:        "PRODUCTS",
			wantEntityFetch: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			runEnumPlanSpecCase(t, sg, tc.query, tc.wantEnum, tc.wantEntityFetch)
		})
	}
}

// runEnumPlanSpecCase executes one TestMarshalEnumPlanSpec_RoundTrip subtest.
func runEnumPlanSpecCase(
	t *testing.T,
	sg *federation.Supergraph,
	query, wantEnum string,
	wantEntityFetch bool,
) {
	t.Helper()
	plan, err := federation.BuildPlan(sg, query, "")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	specJSON, err := MarshalEnumPlanSpec(plan)
	if err != nil {
		t.Fatalf("MarshalEnumPlanSpec: %v", err)
	}
	if !strings.Contains(specJSON, "subgraphEnum") {
		t.Errorf("specJSON missing 'subgraphEnum'; got:\n%s", specJSON)
	}
	if strings.Contains(specJSON, "PRODUCTS_URL") {
		t.Error("specJSON contains URL placeholder; expected enum-keyed format")
	}
	if !strings.Contains(specJSON, wantEnum) {
		t.Errorf("specJSON missing expected enum %q:\n%s", wantEnum, specJSON)
	}
	urls := map[string]string{
		"PRODUCTS":  "https://products.example.com/graphql",
		"INVENTORY": "https://inventory.example.com/graphql",
	}
	resolved, err := execengine.Resolve(specJSON, urls)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved.Fetches) == 0 {
		t.Fatal("resolved plan has no fetches")
	}
	if resolved.Fetches[0].URL != urls[wantEnum] {
		t.Errorf("resolved fetch URL: got %q, want %q",
			resolved.Fetches[0].URL, urls[wantEnum])
	}
	if wantEntityFetch && len(resolved.EntityFetches) == 0 {
		t.Error("expected entity fetches in resolved plan")
	}
}

// TestWriteExecFile_PackageDecl verifies that federation_exec.go is written with
// the correct package declaration.
func TestWriteExecFile_PackageDecl(t *testing.T) {
	cases := map[string]string{
		"default_package": "generated",
		"custom_package":  "myfedclient",
	}
	for name, pkg := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := WriteExecFile(dir, pkg); err != nil {
				t.Fatalf("WriteExecFile: %v", err)
			}
			b, err := os.ReadFile(filepath.Join(dir, "federation_exec.go"))
			if err != nil {
				t.Fatalf("read federation_exec.go: %v", err)
			}
			content := string(b)
			wantDecl := "package " + pkg
			if !strings.Contains(content, wantDecl) {
				t.Errorf("federation_exec.go missing %q; got package line:\n%s",
					wantDecl, firstNonCommentLine(content))
			}
			if strings.Contains(content, "package execengine") {
				t.Error("federation_exec.go still contains 'package execengine'")
			}
		})
	}
}

// TestWriteExecFile_Parseable verifies that federation_exec.go is syntactically
// valid Go after the package rename.
func TestWriteExecFile_Parseable(t *testing.T) {
	dir := t.TempDir()
	if err := WriteExecFile(dir, "testpkg"); err != nil {
		t.Fatalf("WriteExecFile: %v", err)
	}
	dest := filepath.Join(dir, "federation_exec.go")
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, dest, nil, parser.AllErrors); err != nil {
		t.Errorf("federation_exec.go has syntax errors: %v", err)
	}
}

// TestWriteExecFile_NoDefederatorImport verifies that the written file contains
// no import of any defederator package — the generated client must be self-contained.
func TestWriteExecFile_NoDefederatorImport(t *testing.T) {
	dir := t.TempDir()
	if err := WriteExecFile(dir, "generated"); err != nil {
		t.Fatalf("WriteExecFile: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "federation_exec.go"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(b), "defederator") {
		t.Error("federation_exec.go imports a defederator package")
	}
}

// firstNonCommentLine returns the first non-blank, non-comment line of s.
func firstNonCommentLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		return line
	}
	return ""
}
