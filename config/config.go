// Package config loads and validates .defederator.yml configuration files.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

// PackageConfig specifies the output file and Go package name for generated code.
type PackageConfig struct {
	Filename string `yaml:"filename"`
	Package  string `yaml:"package"`
}

// IsDefined returns true if both Filename and Package are set.
func (p PackageConfig) IsDefined() bool {
	return p.Filename != "" && p.Package != ""
}

// GenerateConfig controls optional code-generation behaviours.
type GenerateConfig struct {
	ClientInterfaceName *string `yaml:"clientInterfaceName,omitempty"`
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

	// Generate controls optional generation behaviours.
	Generate *GenerateConfig `yaml:"generate,omitempty"`

	// Dir is the base directory for resolving relative paths (not serialised).
	Dir string `yaml:"-"`
}

var defaultFilenames = []string{".defederator.yml", "defederator.yml", "defederator.yaml"}

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
	return &cfg, nil
}

// LoadConfigFromDir searches dir and its parents for a config file.
func LoadConfigFromDir(dir string) (*Config, error) {
	path, err := findConfig(dir)
	if err != nil {
		return nil, fmt.Errorf("config: no config file found in %s or its parents: %w", dir, err)
	}
	return LoadConfig(path)
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

// resolvePath resolves p relative to the config file's directory.
func (c *Config) resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Dir, p)
}
