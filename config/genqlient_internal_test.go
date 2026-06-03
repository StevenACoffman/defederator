package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadGenqlientConfig(t *testing.T) {
	content := `
schema: supergraph.graphql
operations:
  - queries/**/*.graphql
  - pkg/app/*.go
generated: ./generated/client.go
package: generated
bindings:
  DateTime:
    type: time.Time
`
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "genqlient.yaml")
	if err := os.WriteFile(cfgFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadGenqlientConfig(cfgFile)
	if err != nil {
		t.Fatalf("LoadGenqlientConfig: %v", err)
	}

	if cfg.Schema != "supergraph.graphql" {
		t.Errorf("Schema: want %q, got %q", "supergraph.graphql", cfg.Schema)
	}
	if len(cfg.Query) != 2 {
		t.Errorf("Query: want 2 entries, got %d: %v", len(cfg.Query), cfg.Query)
	}
	if cfg.Client.Filename != "./generated/client.go" {
		t.Errorf("Client.Filename: want %q, got %q", "./generated/client.go", cfg.Client.Filename)
	}
	if cfg.Client.Package != "generated" {
		t.Errorf("Client.Package: want %q, got %q", "generated", cfg.Client.Package)
	}
	if cfg.Dir != tmp {
		t.Errorf("Dir: want %q, got %q", tmp, cfg.Dir)
	}
	if b, ok := cfg.Bindings["DateTime"]; !ok {
		t.Error("Bindings: DateTime not found")
	} else if b.Type != "time.Time" {
		t.Errorf("Bindings[DateTime].Type: want %q, got %q", "time.Time", b.Type)
	}
}

func TestLoadConfigFromDir_GenqlientFile(t *testing.T) {
	content := `schema: sg.graphql
operations: [ops.graphql]
generated: client.go
package: mypkg
`
	tmp := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(tmp, "genqlient.yaml"),
		[]byte(content),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfigFromDir(tmp)
	if err != nil {
		t.Fatalf("LoadConfigFromDir: %v", err)
	}
	// Verify it was routed through LoadGenqlientConfig (operations → Query).
	if len(cfg.Query) != 1 || cfg.Query[0] != "ops.graphql" {
		t.Errorf("Query: want [ops.graphql], got %v", cfg.Query)
	}
}

func TestLoadConfigFromDir_PreferDefederator(t *testing.T) {
	tmp := t.TempDir()
	// Both files present: defederator file must win.
	defContent := `schema: def.graphql
query: [def_query.graphql]
client:
  filename: def_client.go
  package: defpkg
`
	gqContent := `schema: gq.graphql
operations: [gq_ops.graphql]
generated: gq_client.go
package: gqpkg
`
	if err := os.WriteFile(
		filepath.Join(tmp, ".defederator.yml"),
		[]byte(defContent),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(tmp, "genqlient.yaml"),
		[]byte(gqContent),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfigFromDir(tmp)
	if err != nil {
		t.Fatalf("LoadConfigFromDir: %v", err)
	}
	if cfg.Schema != "def.graphql" {
		t.Errorf("expected defederator config to win, got schema=%q", cfg.Schema)
	}
}

func TestValidate_RejectsMissingFields(t *testing.T) {
	cases := map[string]struct {
		cfg     Config
		wantSub string // substring expected in error
	}{
		"missing schema": {
			cfg: Config{
				Client: PackageConfig{Filename: "c.go", Package: "x"},
			},
			wantSub: `"schema"`,
		},
		"missing client.filename": {
			cfg: Config{
				Schema: "sg.graphql",
				Client: PackageConfig{Package: "x"},
			},
			wantSub: `"client.filename"`,
		},
		"missing client.package": {
			cfg: Config{
				Schema: "sg.graphql",
				Client: PackageConfig{Filename: "c.go"},
			},
			wantSub: `"client.package"`,
		},
		"all set": {
			cfg: Config{
				Schema: "sg.graphql",
				Client: PackageConfig{Filename: "c.go", Package: "x"},
			},
			wantSub: "",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantSub == "" {
				if err != nil {
					t.Errorf("expected nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestLoadGenqlientConfig_SingleSchemaString(t *testing.T) {
	content := `schema: schema.graphql
operations: [ops.go]
generated: client.go
package: mypkg
`
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "genqlient.yaml")
	if err := os.WriteFile(cfgFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadGenqlientConfig(cfgFile)
	if err != nil {
		t.Fatalf("LoadGenqlientConfig: %v", err)
	}

	if cfg.Schema != "schema.graphql" {
		t.Errorf("Schema: want %q, got %q", "schema.graphql", cfg.Schema)
	}
}
