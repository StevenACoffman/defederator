// Package generator orchestrates the defederator code-generation pipeline.
package generator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	gqlgenConfig "github.com/99designs/gqlgen/codegen/config"

	defConfig "github.com/StevenACoffman/defederator/config"
	"github.com/StevenACoffman/gorouter/federation"

	"github.com/gqlgo/gqlgenc/clientgenv2"
	gqlgencConfig "github.com/gqlgo/gqlgenc/config"
	"github.com/gqlgo/gqlgenc/parsequery"
	"github.com/gqlgo/gqlgenc/querydocument"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// Generate runs the full defederator code-generation pipeline for cfg.
func Generate(ctx context.Context, cfg *defConfig.Config) error {
	fmt.Printf("Generating with config: %+v\n", cfg)
	sdlPath := cfg.SchemaPath()
	sdlBytes, err := os.ReadFile(sdlPath)
	if err != nil {
		return fmt.Errorf("generate: read supergraph %s: %w", sdlPath, err)
	}
	sdl := string(sdlBytes)

	sg, err := federation.ParseSchema(sdl)
	if err != nil {
		return fmt.Errorf("generate: parse supergraph: %w", err)
	}

	cleanSDL, err := StripFederationTypes(sdl)
	if err != nil {
		return fmt.Errorf("generate: strip federation types: %w", err)
	}

	gqlgencCfg, err := buildGqlgencConfig(cfg, cleanSDL)
	if err != nil {
		return fmt.Errorf("generate: build gqlgenc config: %w", err)
	}

	// Parse the schema directly from the clean SDL string.
	// We bypass gqlgencCfg.LoadSchema to avoid its remote-introspection path.
	schema, err := gqlparser.LoadSchema(gqlgencCfg.GQLConfig.Sources...)
	if err != nil {
		return fmt.Errorf("generate: load schema: %w", err)
	}
	gqlgencCfg.GQLConfig.Schema = schema

	if err := gqlgencCfg.GQLConfig.Init(); err != nil {
		return fmt.Errorf("generate: init gqlgen config: %w", err)
	}

	// Map user-defined types that lack an explicit binding to graphql.String.
	// gqlgenc calls Type(name) for any field where IsBasicType() returns true —
	// this includes enums (no sub-selections) and input types (used as arguments).
	// gqlgen's Init() adds introspection enums but leaves user-defined enums and
	// input objects unmapped when no server model file is generated.
	// Mapping them to string lets the generated client compile; the actual wire
	// representation is handled by the JSON marshaler in the operation methods.
	if gqlgencCfg.GQLConfig.Models == nil {
		gqlgencCfg.GQLConfig.Models = make(gqlgenConfig.TypeMap)
	}
	for name, def := range gqlgencCfg.GQLConfig.Schema.Types {
		switch def.Kind {
		case "INPUT_OBJECT": // We still map input objects to string for now?
		default:
			continue
		}
		if _, ok := gqlgencCfg.GQLConfig.Models[name]; !ok {
			gqlgencCfg.GQLConfig.Models[name] = gqlgenConfig.TypeMapEntry{
				Model: gqlgenConfig.StringList{"github.com/99designs/gqlgen/graphql.String"},
			}
		}
	}

	// Schema.Implements iteration order is non-deterministic; sort for stable output.
	for _, v := range gqlgencCfg.GQLConfig.Schema.Implements {
		sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
	}

	expandedPaths, err := expandGlobs(cfg.Query, cfg.Dir)
	if err != nil {
		return fmt.Errorf("generate: expand query globs: %w", err)
	}

	var graphqlPaths []string
	var extraSources []*ast.Source
	for _, p := range expandedPaths {
		switch {
		case hasGraphQLExt(p):
			graphqlPaths = append(graphqlPaths, p)
		case hasGoExt(p):
			embedded, err := extractQueriesFromGoFile(p)
			if err != nil {
				return fmt.Errorf("generate: extract queries from %s: %w", p, err)
			}
			for _, eq := range embedded {
				extraSources = append(extraSources, &ast.Source{
					Name:  eq.source, // "filename:line" so gqlparser errors point back to Go source
					Input: eq.text,
				})
			}
		}
	}

	querySources, err := parsequery.LoadQuerySources(graphqlPaths)
	if err != nil {
		return fmt.Errorf("generate: load query sources: %w", err)
	}
	querySources = append(querySources, extraSources...)

	queryDocument, err := parsequery.ParseQueryDocuments(gqlgencCfg.GQLConfig.Schema, querySources)
	if err != nil {
		return fmt.Errorf("generate: parse query documents: %w", err)
	}

	operationQueryDocuments, err := querydocument.QueryDocumentsByOperations(gqlgencCfg.GQLConfig.Schema, queryDocument.Operations)
	if err != nil {
		return fmt.Errorf("generate: build per-operation documents: %w", err)
	}

	clientPkg := gqlgenConfig.PackageConfig{
		Filename: cfg.ClientFilename(),
		Package:  cfg.Client.Package,
	}

	generateCfg := buildGenerateConfig(cfg)

	sourceGenerator := clientgenv2.NewSourceGenerator(gqlgencCfg.GQLConfig, clientPkg, generateCfg)
	source := clientgenv2.NewSource(gqlgencCfg.GQLConfig.Schema, queryDocument, sourceGenerator, generateCfg)

	fragments, err := source.Fragments()
	if err != nil {
		return fmt.Errorf("generate: generate fragments: %w", err)
	}

	operationResponses, err := source.OperationResponses()
	if err != nil {
		return fmt.Errorf("generate: generate operation responses: %w", err)
	}

	operations, err := source.Operations(operationQueryDocuments)
	if err != nil {
		return fmt.Errorf("generate: generate operations: %w", err)
	}
	fmt.Printf("Generated %d operations\n", len(operations))
	for _, op := range operations {
		fmt.Printf("Operation: %s\n", op.Name)
	}

	urlMode := cfg.URLMode
	if urlMode == "" {
		urlMode = "baked"
	}

	marshalSpec := MarshalURLPlanSpec
	if urlMode == "enum" {
		marshalSpec = MarshalEnumPlanSpec
	}

	planSpecs := make(map[string]string, len(operations))
	for _, op := range operations {
		plan, err := federation.BuildPlan(sg, op.Operation, op.Name)
		if err != nil {
			return fmt.Errorf("generate: plan %q: %w", op.Name, err)
		}
		specJSON, err := marshalSpec(plan)
		if err != nil {
			return fmt.Errorf("generate: marshal plan spec %q: %w", op.Name, err)
		}
		planSpecs[op.Name] = specJSON
	}

	if err := RenderFederationTemplate(
		gqlgencCfg.GQLConfig,
		fragments,
		operations,
		operationResponses,
		source.ResponseSubTypes(),
		generateCfg,
		clientPkg,
		planSpecs,
		urlMode,
	); err != nil {
		return fmt.Errorf("generate: render template: %w", err)
	}

	if err := WriteExecFile(filepath.Dir(cfg.ClientFilename()), cfg.Client.Package); err != nil {
		return fmt.Errorf("generate: write exec file: %w", err)
	}

	if cfg.Generate != nil && cfg.Generate.ExportOperations != "" {
		exportPath := cfg.Generate.ExportOperations
		if !filepath.IsAbs(exportPath) {
			exportPath = filepath.Join(cfg.Dir, exportPath)
		}
		if err := exportOperations(exportPath, operations); err != nil {
			return fmt.Errorf("generate: export operations to %s: %w", exportPath, err)
		}
	}

	return nil
}

func buildGqlgencConfig(cfg *defConfig.Config, cleanSDL string) (*gqlgencConfig.Config, error) {
	gqlCfg := gqlgenConfig.DefaultConfig()
	gqlCfg.Sources = []*ast.Source{
		{Name: "supergraph", Input: cleanSDL},
	}

	// Convert defederator bindings: to gqlgen Models TypeMap entries.
	// Each binding maps a GraphQL scalar name to a Go type path, which is what
	// gqlgen's type binder needs to resolve response field types.
	if len(cfg.Bindings) > 0 {
		if gqlCfg.Models == nil {
			gqlCfg.Models = make(gqlgenConfig.TypeMap, len(cfg.Bindings))
		}
		for graphqlName, binding := range cfg.Bindings {
			gqlCfg.Models[graphqlName] = gqlgenConfig.TypeMapEntry{
				Model: gqlgenConfig.StringList{binding.Type},
			}
		}
	}

	gqlgencCfg := &gqlgencConfig.Config{
		GQLConfig: gqlCfg,
		Query:     cfg.Query,
	}
	return gqlgencCfg, nil
}

func hasGraphQLExt(p string) bool {
	switch filepath.Ext(p) {
	case ".graphql", ".graphqls", ".gql":
		return true
	}
	return false
}

func hasGoExt(p string) bool { return filepath.Ext(p) == ".go" }

func buildGenerateConfig(cfg *defConfig.Config) *gqlgencConfig.GenerateConfig {
	if cfg.Generate == nil {
		return &gqlgencConfig.GenerateConfig{}
	}
	gc := &gqlgencConfig.GenerateConfig{
		Optional: cfg.Generate.Optional,
	}
	if cfg.Generate.ClientInterfaceName != nil {
		gc.ClientInterfaceName = cfg.Generate.ClientInterfaceName
	}
	return gc
}
