// Package generator orchestrates the defederator code-generation pipeline.
package generator

import (
	"context"
	"fmt"
	"os"
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
	sdlPath := cfg.SchemaPath()
	sdlBytes, err := os.ReadFile(sdlPath)
	if err != nil {
		return fmt.Errorf("generate: read supergraph %s: %w", sdlPath, err)
	}
	sdl := string(sdlBytes)

	// Validate the supergraph SDL is parseable as a federation schema.
	if _, err := federation.ParseSchema(sdl); err != nil {
		return fmt.Errorf("generate: parse supergraph: %w", err)
	}

	// Strip federation metadata to produce a clean SDL for gqlgenc's type mapper.
	cleanSDL, err := StripFederationTypes(sdl)
	if err != nil {
		return fmt.Errorf("generate: strip federation types: %w", err)
	}

	// Build the gqlgenc config from our defederator config.
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

	// Ensure deterministic output.
	for _, v := range gqlgencCfg.GQLConfig.Schema.Implements {
		sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
	}

	querySources, err := parsequery.LoadQuerySources(cfg.Query)
	if err != nil {
		return fmt.Errorf("generate: load query sources: %w", err)
	}

	queryDocument, err := parsequery.ParseQueryDocuments(gqlgencCfg.GQLConfig.Schema, querySources)
	if err != nil {
		return fmt.Errorf("generate: parse query documents: %w", err)
	}

	operationQueryDocuments, err := querydocument.QueryDocumentsByOperations(gqlgencCfg.GQLConfig.Schema, queryDocument.Operations)
	if err != nil {
		return fmt.Errorf("generate: build per-operation documents: %w", err)
	}

	clientPkg := gqlgenConfig.PackageConfig{
		Filename: cfg.Client.Filename,
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

	if err := RenderFederationTemplate(
		gqlgencCfg.GQLConfig,
		fragments,
		operations,
		operationResponses,
		source.ResponseSubTypes(),
		generateCfg,
		clientPkg,
	); err != nil {
		return fmt.Errorf("generate: render template: %w", err)
	}

	return nil
}

func buildGqlgencConfig(cfg *defConfig.Config, cleanSDL string) (*gqlgencConfig.Config, error) {
	gqlCfg := gqlgenConfig.DefaultConfig()
	gqlCfg.Sources = []*ast.Source{
		{Name: "supergraph", Input: cleanSDL},
	}

	gqlgencCfg := &gqlgencConfig.Config{
		GQLConfig: gqlCfg,
		Query:     cfg.Query,
	}
	return gqlgencCfg, nil
}

func buildGenerateConfig(cfg *defConfig.Config) *gqlgencConfig.GenerateConfig {
	if cfg.Generate == nil {
		return &gqlgencConfig.GenerateConfig{}
	}
	gc := &gqlgencConfig.GenerateConfig{}
	if cfg.Generate.ClientInterfaceName != nil {
		gc.ClientInterfaceName = cfg.Generate.ClientInterfaceName
	}
	return gc
}
