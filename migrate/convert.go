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

// YAMLInput collects everything DefederatorYAML needs to produce a .defederator.yml.
// It is the pure-function boundary: the caller resolves I/O; DefederatorYAML is a
// pure transformation.
type YAMLInput struct {
	Genqlient    GenqlientConfig
	InputObjects []string // INPUT_OBJECT names from the supergraph SDL, sorted
	GenqlientPkg string   // e.g. "github.com/Khan/webapp/services/foo/generated/genqlient"
}

// DefederatorYAML converts a migration input into a .defederator.yml YAML string.
// Pure function — no I/O.
//
// Conversion rules:
//   - schema:     → schema: (verbatim)
//   - operations: → query: (same list)
//   - generated:  → client.filename: (path with genqlient replaced by defederator, ./ prefix)
//   - bindings:   → kept verbatim from genqlient.yaml (defederator generate
//     runs inside the webapp module, so package paths like
//     github.com/Khan/webapp/pkg/content.Author resolve correctly)
//   - url_mode: enum is always added (webapp supergraph uses placeholder URLs)
//   - generate.clientInterfaceName: FederationClient is always added
//   - generate.optional: pointer is always added
//   - INPUT_OBJECT types from the supergraph SDL are added as genqlient-package bindings
func DefederatorYAML(in YAMLInput) (string, error) {
	gq := in.Genqlient
	if gq.Schema == "" {
		return "", errors.New("migrate: genqlient config has no schema field")
	}

	var b strings.Builder

	b.WriteString("schema: ")
	b.WriteString(gq.Schema)
	b.WriteString("\n\n")

	b.WriteString("query:\n")
	for _, op := range gq.Operations {
		// Always single-quote operation patterns: glob entries like `*.go` or
		// `**/*.graphql` start with characters YAML interprets as anchors,
		// aliases, or merge keys (`*`, `&`, `<<`). Quoting makes the value a
		// plain string regardless of leading metacharacter.
		b.WriteString("  - ")
		b.WriteString(yamlSingleQuote(op))
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

	b.WriteString(bindingsSection(gq.Bindings, in.InputObjects, in.GenqlientPkg))

	return b.String(), nil
}

// defederatorClientFilename converts a genqlient output path to a defederator one.
// generated/genqlient/queries.go -> ./generated/defederator/client.go
func defederatorClientFilename(genqlientGenerated string) string {
	if genqlientGenerated == "" {
		return "./generated/defederator/client.go"
	}
	dir := filepath.Dir(genqlientGenerated)
	parentDir := filepath.Dir(dir)
	var result string
	if strings.Contains(dir, "genqlient") {
		result = filepath.Join(parentDir, "defederator", "client.go")
	} else {
		result = filepath.Join(dir, "defederator", "client.go")
	}
	return "./" + result
}

// bindingsSection renders the full bindings: block, including scalar bindings,
// ENUM comment, and optionally INPUT_OBJECT bindings.
// Returns an empty string when there are no bindings of any kind.
func bindingsSection(
	bindings map[string]config.TypeBinding,
	inputObjects []string,
	genqlientPkg string,
) string {
	hasScalars := len(bindings) > 0
	hasInputObjects := len(inputObjects) > 0 && genqlientPkg != ""
	if !hasScalars && !hasInputObjects {
		return ""
	}

	var b strings.Builder
	b.WriteString("bindings:\n")

	if hasScalars {
		b.WriteString(
			"  # Scalars — copied verbatim from genqlient.yaml. Defederator generate runs\n",
		)
		b.WriteString(
			"  # inside the webapp module so any github.com/Khan/... or stdlib type path\n",
		)
		b.WriteString("  # resolves correctly.\n")

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
	}

	// ENUM comment — always emitted when there are any bindings. Reflects the
	// generator's behaviour: enum types are auto-emitted as named Go strings
	// (genqlient-style) in the client package and do not require a binding.
	b.WriteString("  # Enums are auto-emitted as typed Go strings so no need to bind here unless\n")
	b.WriteString("  # you need to override (e.g. to point at an existing Go type elsewhere).\n")

	if hasInputObjects {
		b.WriteString(
			"  # INPUT_OBJECT bindings — keep genqlient types so callers don't need to change.\n",
		)
		b.WriteString(
			"  # INPUT_OBJECTs only appear as operation inputs (never in response fields),\n",
		)
		b.WriteString("  # so gqlgenc won't try to resolve them as response types.\n")
		for _, name := range inputObjects {
			b.WriteString("  ")
			b.WriteString(name)
			b.WriteString(":\n")
			b.WriteString("    type: ")
			b.WriteString(genqlientPkg)
			b.WriteString(".")
			b.WriteString(name)
			b.WriteString("\n")
		}
	}

	return b.String()
}

// yamlSingleQuote wraps s in YAML single quotes, doubling any embedded single
// quote per the YAML 1.2 spec for escaping an apostrophe inside a single-quoted
// scalar. Used for operation patterns like `*.go` where the leading `*` would
// otherwise be parsed as a YAML alias reference.
func yamlSingleQuote(s string) string {
	const sq = "'"
	return sq + strings.ReplaceAll(s, sq, sq+sq) + sq
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
