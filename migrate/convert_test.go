package migrate

import (
	"strings"
	"testing"

	"github.com/StevenACoffman/defederator/config"
)

func TestDefederatorYAML_Basic(t *testing.T) {
	gq := GenqlientConfig{
		Schema:     "../../gengraphql/composed_schema.graphql",
		Operations: []string{"cross_service/*.go", "tasks/*.go"},
		Generated:  "generated/genqlient/queries.go",
		Bindings:   nil,
	}
	got, err := DefederatorYAML(gq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	checks := []string{
		"schema: ../../gengraphql/composed_schema.graphql",
		"query:",
		"  - cross_service/*.go",
		"  - tasks/*.go",
		"client:",
		"filename: generated/defederator/client.go",
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
	gq := GenqlientConfig{
		Schema:    "../../schema.graphql",
		Generated: "generated/genqlient/queries.go",
		Bindings: map[string]config.TypeBinding{
			"DateTime": {Type: "time.Time"},
			"Date":     {Type: "cloud.google.com/go/civil.Date"},
		},
	}
	got, err := DefederatorYAML(gq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "bindings:") {
		t.Error("output missing bindings section")
	}
	if !strings.Contains(got, "DateTime:") {
		t.Error("output missing DateTime binding")
	}
	if !strings.Contains(got, "Date:") {
		t.Error("output missing Date binding")
	}
	// Verify comment about graphql.String alternative is present.
	if !strings.Contains(got, "graphql.String") {
		t.Error("output missing graphql.String comment")
	}
}

func TestDefederatorYAML_NoSchema(t *testing.T) {
	_, err := DefederatorYAML(GenqlientConfig{})
	if err == nil {
		t.Fatal("expected error for missing schema")
	}
}

func TestDefederatorClientFilename(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"generated/genqlient/queries.go", "generated/defederator/client.go"},
		{"", "./generated/defederator/client.go"},
		{"gen/genqlient/out.go", "gen/defederator/client.go"},
	}
	for _, tc := range cases {
		got := defederatorClientFilename(tc.input)
		if got != tc.want {
			t.Errorf("defederatorClientFilename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
