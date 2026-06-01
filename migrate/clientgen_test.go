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

var testData = Data{
	ServiceName:     "example",
	ServiceDir:      "/srv/webapp/services/example",
	PackageName:     "cross_service",
	ImportAlias:     "defed",
	DefedImportPath: "github.com/Khan/webapp/services/example/generated/defederator",
	URLFuncName:     "exampleSubgraphURLs",
	Subgraphs:       testSubgraphs,
}

func TestRender_ContainsExpected(t *testing.T) {
	got, err := Render(testData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	checks := []string{
		"package cross_service",
		`defed "github.com/Khan/webapp/services/example/generated/defederator"`,
		"_federationCtx",
		"func newFederationClient(",
		"defed.NewClientWithFactories(",
		"defed.Resolve(specJSON, urls)",
		"exampleSubgraphURLs",
		`"CONTENT": "content"`,
		`"USERS": "users"`,
		"errors.Wrap(err",
		`u.Path = "/backend-graphql/"`,
		"DO NOT EDIT",
	}
	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q", want)
		}
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
	d := DataFromDir("/srv/webapp/services/ai-guide", "github.com/Khan/webapp", testSubgraphs)
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
