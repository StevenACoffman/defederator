package migrate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/gqlgo/gqlgenc/parsequery"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/StevenACoffman/defederator/generator"
	"github.com/StevenACoffman/gorouter/federation"
)

// LoadOperationSources reads every operation file listed in genqlient.yaml's
// `operations:` (or `query:`) section and returns one gqlparser ast.Source per
// operation string. Glob patterns are expanded relative to baseDir.
//
// Both .graphql and .go files are supported: .go files are scanned for
// `# @genqlient`-tagged string literals (via generator.QuerySourcesFromGoFile).
//
// Used by migrate to identify which subgraphs and INPUT_OBJECT types the
// service's operations actually reference, so the generated `.defederator.yml`
// and `cross_service/client.go` can be pruned accordingly.
func LoadOperationSources(patterns []string, baseDir string) ([]*ast.Source, error) {
	var graphqlFiles []string
	var sources []*ast.Source

	for _, pat := range patterns {
		matches, err := expandPattern(pat, baseDir)
		if err != nil {
			return nil, fmt.Errorf("migrate: expand %q: %w", pat, err)
		}
		for _, m := range matches {
			switch filepath.Ext(m) {
			case ".graphql", ".graphqls", ".gql":
				graphqlFiles = append(graphqlFiles, m)
			case ".go":
				// io.Discard: migrate reads operation files for analysis only and
				// the per-literal trace is irrelevant to the user. `defederator
				// generate` later prints the same files via the generator's
				// verbose path if the user asked for it.
				sub, err := generator.QuerySourcesFromGoFile(m, io.Discard)
				if err != nil {
					return nil, fmt.Errorf("migrate: extract queries from %s: %w", m, err)
				}
				sources = append(sources, sub...)
			}
		}
	}

	if len(graphqlFiles) > 0 {
		gqls, err := parsequery.LoadQuerySources(graphqlFiles)
		if err != nil {
			return nil, fmt.Errorf("migrate: load graphql files: %w", err)
		}
		sources = append(sources, gqls...)
	}
	return sources, nil
}

// expandPattern resolves a glob pattern (or literal path) against baseDir and
// returns matching files. Patterns without glob metacharacters are returned
// verbatim regardless of whether the file exists, mirroring genqlient's
// behaviour.
func expandPattern(pattern, baseDir string) ([]string, error) {
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(baseDir, pattern)
	}
	base, pat := doublestar.SplitPattern(filepath.ToSlash(pattern))
	matches, err := doublestar.Glob(os.DirFS(base), pat, doublestar.WithFilesOnly())
	if err != nil {
		return nil, fmt.Errorf("glob %q: %w", pattern, err)
	}
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = filepath.Join(base, m)
	}
	return out, nil
}

// OperationVariableInputObjects returns the sorted, deduplicated set of
// INPUT_OBJECT type names that appear as operation variable types across the
// given query sources, restricted to types declared in schema.
//
// Used to prune the `.defederator.yml` bindings: only INPUT_OBJECTs actually
// passed as cross-service operation arguments need a binding.
func OperationVariableInputObjects(
	schema *ast.Schema,
	sources []*ast.Source,
) ([]string, error) {
	if schema == nil {
		return nil, nil
	}
	doc, err := parsequery.ParseQueryDocuments(schema, sources)
	if err != nil {
		return nil, fmt.Errorf("migrate: parse operations: %w", err)
	}
	seen := map[string]struct{}{}
	for _, op := range doc.Operations {
		for _, vd := range op.VariableDefinitions {
			collectInputObjectNames(schema, vd.Type, seen)
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// collectInputObjectNames walks a variable type expression (which may be
// non-null and/or list-wrapped) and adds the named base type to seen if the
// schema marks it as an INPUT_OBJECT.
func collectInputObjectNames(schema *ast.Schema, t *ast.Type, seen map[string]struct{}) {
	if t == nil {
		return
	}
	if t.Elem != nil { // list type — unwrap
		collectInputObjectNames(schema, t.Elem, seen)
		return
	}
	def := schema.Types[t.NamedType]
	if def != nil && def.Kind == ast.InputObject {
		seen[t.NamedType] = struct{}{}
	}
}

// UsedSubgraphs returns the sorted, deduplicated set of join__Graph enum names
// that the given operation sources actually touch, as determined by planning
// each operation with the federation query planner.
//
// Used to prune `_subgraphServices` in the generated cross_service/client.go:
// only subgraphs whose fields appear in at least one operation need a
// service-discovery lookup at runtime.
//
// An operation that fails to plan (e.g. references a type the supergraph
// doesn't have, which would be a programmer error) is skipped silently so that
// migrate is no stricter than `defederator generate` itself.
func UsedSubgraphs(sg *federation.Supergraph, sources []*ast.Source) []string {
	seen := map[string]struct{}{}
	for _, src := range sources {
		plan, err := federation.BuildPlan(sg, src.Input, "")
		if err != nil {
			continue
		}
		for _, f := range plan.Fetches {
			seen[f.Subgraph.EnumName] = struct{}{}
		}
		for _, ef := range plan.EntityFetches {
			seen[ef.Subgraph.EnumName] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// FilterSubgraphs keeps only the SubgraphEntries whose EnumName appears in
// usedEnumNames, preserving original declaration order. The own-service entry
// (matching ownServiceName) is always kept regardless of usage so that the
// generated map looks complete for the local service.
//
// usedEnumNames must be sorted; passing the result of UsedSubgraphs satisfies
// that. ownServiceName may be "" to skip the always-keep behavior.
func FilterSubgraphs(
	all []SubgraphEntry,
	usedEnumNames []string,
	ownServiceName string,
) []SubgraphEntry {
	if len(usedEnumNames) == 0 {
		return nil
	}
	used := make(map[string]struct{}, len(usedEnumNames))
	for _, n := range usedEnumNames {
		used[n] = struct{}{}
	}
	out := make([]SubgraphEntry, 0, len(usedEnumNames))
	for _, e := range all {
		_, ok := used[e.EnumName]
		if !ok && e.ServiceName != ownServiceName {
			continue
		}
		out = append(out, e)
	}
	return out
}

// serviceNameFromDir returns the basename of an absolute service directory.
// Centralized so the rule for deriving a service name from its path lives in
// one place.
func serviceNameFromDir(absDir string) string { return filepath.Base(absDir) }

// intersectSorted returns the sorted intersection of two sorted string slices.
// Inputs must already be sorted (the helpers in this package emit sorted
// results); duplicates within one input are preserved at most once in the
// output.
func intersectSorted(a, b []string) []string {
	out := make([]string, 0, min(len(a), len(b)))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			if len(out) == 0 || out[len(out)-1] != a[i] {
				out = append(out, a[i])
			}
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return out
}
