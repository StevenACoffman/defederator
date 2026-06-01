package migrate

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/StevenACoffman/defederator/config"
)

// GenqlientConfig is the parsed subset of a genqlient.yaml relevant to migration.
type GenqlientConfig struct {
	Schema     string
	Operations []string
	Generated  string
	Bindings   map[string]config.TypeBinding
}

// DefederatorYAML converts a parsed genqlient config into a .defederator.yml
// YAML string. Pure function — no I/O.
//
// Conversion rules:
//   - schema:     → schema: (verbatim)
//   - operations: → query: (same list)
//   - generated:  → client.filename: (path with genqlient segment replaced by defederator)
//   - bindings:   → bindings: (preserved; a comment explains the graphql.String alternative)
//   - url_mode: enum is always added (webapp supergraph uses placeholder URLs)
//   - generate.clientInterfaceName: FederationClient is always added
//   - generate.optional: pointer is always added
func DefederatorYAML(gq GenqlientConfig) (string, error) {
	if gq.Schema == "" {
		return "", errors.New("migrate: genqlient config has no schema field")
	}

	var b strings.Builder

	b.WriteString("schema: ")
	b.WriteString(gq.Schema)
	b.WriteString("\n\n")

	b.WriteString("query:\n")
	for _, op := range gq.Operations {
		b.WriteString("  - ")
		b.WriteString(op)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	clientFile := defederatorClientFilename(gq.Generated)
	b.WriteString("client:\n")
	b.WriteString("  filename: ")
	b.WriteString(clientFile)
	b.WriteString("\n")
	b.WriteString("  package:  defederator\n\n")

	b.WriteString("url_mode: enum\n\n")

	b.WriteString("generate:\n")
	b.WriteString("  clientInterfaceName: FederationClient\n")
	b.WriteString("  optional: pointer\n\n")

	b.WriteString(bindingsYAML(gq.Bindings))

	return b.String(), nil
}

// defederatorClientFilename converts a genqlient output path to a defederator one.
// generated/genqlient/queries.go → generated/defederator/client.go
// If the path does not contain "genqlient", it returns generated/defederator/client.go
// relative to the same directory.
func defederatorClientFilename(genqlientGenerated string) string {
	if genqlientGenerated == "" {
		return "./generated/defederator/client.go"
	}
	// Replace the genqlient directory segment and filename.
	dir := filepath.Dir(genqlientGenerated)
	parentDir := filepath.Dir(dir)
	if strings.Contains(dir, "genqlient") {
		return filepath.Join(parentDir, "defederator", "client.go")
	}
	return filepath.Join(dir, "defederator", "client.go")
}

// bindingsYAML renders the bindings section with an explanatory comment.
func bindingsYAML(bindings map[string]config.TypeBinding) string {
	if len(bindings) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("bindings:\n")
	b.WriteString("  # Bindings preserved from genqlient.yaml.\n")
	b.WriteString("  # If defederator is run outside the webapp module and cannot resolve\n")
	b.WriteString("  # these types, replace them with:\n")
	b.WriteString("  #   type: github.com/99designs/gqlgen/graphql.String\n")

	// Sort for deterministic output.
	keys := sortedKeys(bindings)
	for _, k := range keys {
		v := bindings[k]
		b.WriteString("  ")
		b.WriteString(k)
		b.WriteString(":\n")
		b.WriteString("    type: ")
		b.WriteString(v.Type)
		b.WriteString("\n")
		if v.Marshaler != "" {
			b.WriteString("    marshaler: ")
			b.WriteString(v.Marshaler)
			b.WriteString("\n")
		}
		if v.Unmarshaler != "" {
			b.WriteString("    unmarshaler: ")
			b.WriteString(v.Unmarshaler)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func sortedKeys(m map[string]config.TypeBinding) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort — binding maps are small.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
