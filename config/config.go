// Package config loads and validates .defederator.yml configuration files.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

var genqlientFilenames = []string{
	"genqlient.yaml",
	"genqlient.yml",
	".genqlient.yaml",
	".genqlient.yml",
}

// defaultFilenames is searched in order; defederator files take precedence.
var defaultFilenames = []string{
	".defederator.yml", "defederator.yml", "defederator.yaml",
	"genqlient.yaml", "genqlient.yml", ".genqlient.yaml", ".genqlient.yml",
}

// PackageConfig specifies the output file and Go package name for generated code.
type PackageConfig struct {
	Filename string `yaml:"filename"`
	Package  string `yaml:"package"`
}

// TypeBinding maps a GraphQL scalar name to a Go type with optional custom
// marshal/unmarshal functions, matching genqlient's bindings: format.
type TypeBinding struct {
	Type        string `yaml:"type"`
	Marshaler   string `yaml:"marshaler,omitempty"`
	Unmarshaler string `yaml:"unmarshaler,omitempty"`
}

// GenerateConfig controls optional code-generation behaviours.
type GenerateConfig struct {
	ClientInterfaceName *string `yaml:"clientInterfaceName,omitempty"`
	// ExportOperations is a path to write a JSON manifest of all generated operations.
	// Empty means no manifest is written.
	ExportOperations string `yaml:"export_operations,omitempty"`
	// Optional controls how nullable GraphQL fields are represented in Go.
	// "pointer" (default): nullable T → *T; "value": nullable T → T (zero = absent).
	Optional string `yaml:"optional,omitempty"`
}

// Config is the top-level .defederator.yml structure.
type Config struct {
	// Schema is the path to the Federation v2 supergraph SDL.
	// It is used both as the routing table and as the GraphQL schema for type generation.
	Schema string `yaml:"schema"`

	// Query lists glob patterns or explicit paths to .graphql operation files.
	Query []string `yaml:"query"`

	// Client configures the generated client Go file.
	Client PackageConfig `yaml:"client"`

	// Model optionally generates model types into a separate file.
	Model PackageConfig `yaml:"model,omitempty"`

	// SubgraphURLs overrides subgraph URLs at runtime.
	// Keys are join__Graph enum values (e.g. "PRODUCTS").
	SubgraphURLs map[string]string `yaml:"subgraph_urls,omitempty"`

	// Bindings maps GraphQL scalar names to Go types. Equivalent to genqlient's
	// bindings: section.
	Bindings map[string]TypeBinding `yaml:"bindings,omitempty"`

	// Generate controls optional generation behaviours.
	Generate *GenerateConfig `yaml:"generate,omitempty"`

	// URLMode controls how subgraph URLs appear in generated plan specs.
	// "baked" (default): URLs from the supergraph SDL are embedded in the
	// plan spec constants at generation time. NewClient takes only an *http.Client.
	// "enum": plan specs use subgraph enum names; URLs are provided at runtime.
	// NewClient takes an additional subgraphURLs map[string]string parameter.
	// Use "enum" when the supergraph SDL contains placeholder URLs (e.g. "unused").
	URLMode string `yaml:"url_mode,omitempty"`

	// Dir is the base directory for resolving relative paths (not serialised).
	Dir string `yaml:"-"`

	// Verbose enables per-file / per-operation progress diagnostics on stderr
	// during generation. Set by the CLI's --verbose flag; not serialised.
	Verbose bool `yaml:"-"`
}

// IsDefined returns true if both Filename and Package are set.
func (p PackageConfig) IsDefined() bool {
	return p.Filename != "" && p.Package != ""
}

// LoadConfig reads and parses a .defederator.yml file.
// All relative paths inside the config are resolved relative to the file's directory.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.Dir = filepath.Dir(path)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return &cfg, nil
}

// Validate reports problems that would otherwise surface as obscure failures
// further down the pipeline. The check set intentionally errs on the side of
// "fail loudly at load time" — every condition here has previously caused a
// confusing downstream symptom.
func (c *Config) Validate() error {
	if c.Schema == "" {
		return errMissingField("schema")
	}
	if c.Client.Filename == "" {
		return errMissingField("client.filename")
	}
	// An empty Client.Package makes the generator emit `package` with no name,
	// which fails gofmt with hundreds of follow-on errors that hide the real
	// cause. Catch it here while the user still has the config in their head.
	if c.Client.Package == "" {
		return errMissingField("client.package")
	}
	return nil
}

// errMissingField returns a consistent error message for a required config
// field, including the YAML path so the user can find it.
func errMissingField(yamlPath string) error {
	return fmt.Errorf("missing required field %q", yamlPath)
}

// LoadConfigFromDir searches dir and its parents for a config file.
// Defederator files take precedence over genqlient files. When a genqlient
// config is found, LoadGenqlientConfig is used so the field mapping is correct.
//
// In both cases the returned Config is validated — callers can rely on
// Schema, Client.Filename, and Client.Package all being non-empty.
func LoadConfigFromDir(dir string) (*Config, error) {
	path, err := findConfig(dir)
	if err != nil {
		return nil, fmt.Errorf("config: no config file found in %s or its parents: %w", dir, err)
	}
	if isGenqlientFilename(filepath.Base(path)) {
		cfg, err := LoadGenqlientConfig(path)
		if err != nil {
			return nil, err
		}
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("config: %s (genqlient fallback): %w", path, err)
		}
		return cfg, nil
	}
	return LoadConfig(path)
}

func isGenqlientFilename(name string) bool {
	for _, n := range genqlientFilenames {
		if name == n {
			return true
		}
	}
	return false
}

func findConfig(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		for _, name := range defaultFilenames {
			candidate := filepath.Join(abs, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			break
		}
		abs = parent
	}
	return "", os.ErrNotExist
}

// SchemaPath returns the absolute path to the supergraph SDL.
func (c *Config) SchemaPath() string {
	return c.resolvePath(c.Schema)
}

// ClientFilename returns the absolute path for the generated client file.
func (c *Config) ClientFilename() string {
	return c.resolvePath(c.Client.Filename)
}

// resolvePath resolves p relative to the config file's directory.
func (c *Config) resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Dir, p)
}
