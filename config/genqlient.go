package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

// genqlientConfig matches the subset of genqlient.yaml fields that defederator needs.
type genqlientConfig struct {
	Schema     stringOrList     `yaml:"schema"`
	Operations []string         `yaml:"operations"`
	Generated  string           `yaml:"generated"`
	Package    string           `yaml:"package"`
	Bindings   map[string]TypeBinding `yaml:"bindings,omitempty"`
}

// stringOrList accepts either a single string or a list of strings in YAML.
type stringOrList []string

func (s *stringOrList) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var single string
	if err := unmarshal(&single); err == nil {
		*s = []string{single}
		return nil
	}
	var list []string
	if err := unmarshal(&list); err != nil {
		return err
	}
	*s = list
	return nil
}

// LoadGenqlientConfig reads a genqlient.yaml file and converts it into a
// *Config suitable for defederator's generator. The SubgraphURLs field is
// left empty; callers must supply it via a sidecar .defederator.yml or a
// --subgraph-urls CLI flag.
func LoadGenqlientConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var gq genqlientConfig
	if err := yaml.Unmarshal(data, &gq); err != nil {
		return nil, fmt.Errorf("config: parse genqlient config %s: %w", path, err)
	}

	dir := filepath.Dir(path)

	cfg := &Config{
		Dir:      dir,
		Bindings: gq.Bindings,
	}

	if len(gq.Schema) > 0 {
		cfg.Schema = gq.Schema[0]
	}

	cfg.Query = gq.Operations

	cfg.Client = PackageConfig{
		Filename: gq.Generated,
		Package:  gq.Package,
	}

	return cfg, nil
}
