package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testSubgraphs = []SubgraphEntry{
	{EnumName: "CONTENT", ServiceName: "content"},
	{EnumName: "USERS", ServiceName: "users"},
}

var testData = &Data{
	ServiceName:     "example",
	ServiceDir:      "/srv/webapp/services/example",
	PackageName:     "cross_service",
	ImportAlias:     "defed",
	DefedImportPath: "github.com/Khan/webapp/services/example/generated/defederator",
	URLFuncName:     "exampleSubgraphURLs",
	Subgraphs:       testSubgraphs,
	AuthFlavors:     AuthFlavors{User: true, Admin: true},
}

func TestRender_ContainsExpected(t *testing.T) {
	got, err := Render(testData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	checks := []string{
		"package cross_service",
		`defed "github.com/Khan/webapp/services/example/generated/defederator"`,
		`"github.com/Khan/genqlient/graphql"`,
		// B2: process-level service-discovery handle, no ctx cascade.
		"var serviceDiscovery service_discovery.Client",
		"func SetServiceDiscovery(sd service_discovery.Client)",
		// Per-flavor drop-in graphql.Client constructors (compat route).
		"func NewUserGraphQLClient(ctx gqlclient.KAContext) graphql.Client",
		"func NewAdminGraphQLClient(ctx gqlclient.KAContext) graphql.Client",
		// The adapter dispatches by op name through the generated planner.
		"type defederatorCompatClient struct",
		"defed.OperationPlanSpecs[req.OpName]",
		"defed.Resolve(spec, urls)",
		"defed.ExecuteAndUnmarshal(",
		// Test gate + transport extraction.
		"if testing.Testing() {",
		"func extractHTTPClient(",
		// Service-discovery URL resolution.
		"exampleSubgraphURLs",
		`"CONTENT": "content"`,
		`"USERS": "users"`,
		"errors.Wrap(err",
		`"net/url"`,
		`(&url.URL{`,
		`Path:   "/backend-graphql/"`,
		"DO NOT EDIT",
	}
	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q", want)
		}
	}
	// The compat route keeps genqlient types and must not pull in the removed
	// pkg/lib/defederatorcompat helper or the typed FederationClient wiring.
	for _, unwanted := range []string{
		"defederatorcompat",
		"NewClientWithFactories",
		"FederationClient",
		"federationCtx",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf(
				"rendered output should not contain %q (typed-route leftover)",
				unwanted,
			)
		}
	}
}

func TestRender_PerFlavorConstructors(t *testing.T) {
	// Only the detected flavors emit a constructor.
	adminOnly := *testData
	adminOnly.AuthFlavors = AuthFlavors{Admin: true}
	got, err := Render(&adminOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "func NewAdminGraphQLClient(") {
		t.Errorf("admin-only output is missing NewAdminGraphQLClient")
	}
	if strings.Contains(got, "func NewUserGraphQLClient(") {
		t.Errorf("admin-only output should not emit NewUserGraphQLClient")
	}
	if strings.Contains(got, "func NewLocaleUserGraphQLClient(") {
		t.Errorf("admin-only output should not emit NewLocaleUserGraphQLClient")
	}

	locale := *testData
	locale.AuthFlavors = AuthFlavors{LocaleUser: true}
	got, err = Render(&locale)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "func NewLocaleUserGraphQLClient(") {
		t.Errorf("locale-user output is missing NewLocaleUserGraphQLClient")
	}
}

func TestRender_Golden(t *testing.T) {
	got, err := Render(testData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	goldenPath := filepath.Join("testdata", "golden_client.go")

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated golden file %s", goldenPath)
		return
	}

	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if got != string(golden) {
		t.Errorf("rendered output does not match golden file %s\ngot:\n%s", goldenPath, got)
	}
}

func TestToCamelCase(t *testing.T) {
	cases := map[string]string{
		"ai-guide":         "aiGuide",
		"content-editing":  "contentEditing",
		"districts":        "districts",
		"progress-reports": "progressReports",
	}
	for input, want := range cases {
		got := toCamelCase(input)
		if got != want {
			t.Errorf("toCamelCase(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestURLFuncName(t *testing.T) {
	cases := map[string]string{
		"ai-guide":  "aiGuideSubgraphURLs",
		"districts": "districtsSubgraphURLs",
		"donations": "donationsSubgraphURLs",
	}
	for input, want := range cases {
		got := urlFuncName(input)
		if got != want {
			t.Errorf("urlFuncName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDataFromDir(t *testing.T) {
	d := DataFromDir(
		"/srv/webapp/services/ai-guide",
		"github.com/Khan/webapp",
		testSubgraphs,
		AuthFlavors{},
	)
	if d.ServiceName != "ai-guide" {
		t.Errorf("ServiceName = %q, want %q", d.ServiceName, "ai-guide")
	}
	if d.URLFuncName != "aiGuideSubgraphURLs" {
		t.Errorf("URLFuncName = %q, want %q", d.URLFuncName, "aiGuideSubgraphURLs")
	}
	if d.DefedImportPath != "github.com/Khan/webapp/services/ai-guide/generated/defederator" {
		t.Errorf("DefedImportPath = %q", d.DefedImportPath)
	}
}
