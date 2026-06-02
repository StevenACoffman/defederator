package migrate

import (
	"strings"
	"testing"

	"github.com/StevenACoffman/defederator/config"
)

func TestDefederatorYAML_Basic(t *testing.T) {
	in := YAMLInput{
		Genqlient: GenqlientConfig{
			Schema:     "../../gengraphql/composed_schema.graphql",
			Operations: []string{"cross_service/*.go", "tasks/*.go"},
			Generated:  "generated/genqlient/queries.go",
		},
	}
	got, err := DefederatorYAML(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	checks := []string{
		"schema: ../../gengraphql/composed_schema.graphql",
		"query:",
		"  - cross_service/*.go",
		"  - tasks/*.go",
		"client:",
		"filename: ./generated/defederator/client.go",
		"package:  defederator",
		"url_mode: enum",
		"clientInterfaceName: FederationClient",
		"optional: pointer",
	}
	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestDefederatorYAML_WithBindings(t *testing.T) {
	in := YAMLInput{
		Genqlient: GenqlientConfig{
			Schema:    "../../schema.graphql",
			Generated: "generated/genqlient/queries.go",
			Bindings: map[string]config.TypeBinding{
				"DateTime": {Type: "time.Time"},
				"Date":     {Type: "cloud.google.com/go/civil.Date"},
				"KALocale": {Type: "string"},
			},
		},
	}
	got, err := DefederatorYAML(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// DateTime kept as time.Time (in keepBindingType).
	if !strings.Contains(got, "DateTime:\n    type: time.Time") {
		t.Errorf("DateTime should remain time.Time\noutput:\n%s", got)
	}
	// Date replaced with graphql.String (civil.Date is outside the module).
	if !strings.Contains(got, "Date:\n    type: github.com/99designs/gqlgen/graphql.String") {
		t.Errorf("Date should become graphql.String\noutput:\n%s", got)
	}
	// KALocale (plain string) replaced with graphql.String.
	if !strings.Contains(got, "KALocale:\n    type: github.com/99designs/gqlgen/graphql.String") {
		t.Errorf("KALocale should become graphql.String\noutput:\n%s", got)
	}
	// ENUM comment always present.
	if !strings.Contains(got, "ENUM types are NOT bound here") {
		t.Error("output missing ENUM comment")
	}
}

func TestDefederatorYAML_WithInputObjects(t *testing.T) {
	in := YAMLInput{
		Genqlient: GenqlientConfig{
			Schema:    "../../schema.graphql",
			Generated: "generated/genqlient/queries.go",
			Bindings: map[string]config.TypeBinding{
				"DateTime": {Type: "time.Time"},
			},
		},
		InputObjects: []string{"CreateFooInput", "UpdateFooInput"},
		GenqlientPkg: "github.com/Khan/webapp/services/foo/generated/genqlient",
	}
	got, err := DefederatorYAML(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"INPUT_OBJECT bindings",
		"CreateFooInput:\n    type: github.com/Khan/webapp/services/foo/generated/genqlient.CreateFooInput",
		"UpdateFooInput:\n    type: github.com/Khan/webapp/services/foo/generated/genqlient.UpdateFooInput",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestDefederatorYAML_NoSchema(t *testing.T) {
	_, err := DefederatorYAML(YAMLInput{})
	if err == nil {
		t.Fatal("expected error for missing schema")
	}
}

func TestDefederatorClientFilename(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"generated/genqlient/queries.go", "./generated/defederator/client.go"},
		{"", "./generated/defederator/client.go"},
		{"gen/genqlient/out.go", "./gen/defederator/client.go"},
	}
	for _, tc := range cases {
		got := defederatorClientFilename(tc.input)
		if got != tc.want {
			t.Errorf("defederatorClientFilename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
