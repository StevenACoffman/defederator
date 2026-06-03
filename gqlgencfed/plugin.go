// Package gqlgencfed provides a gqlgenc plugin that generates typed Go clients
// whose methods call the Federation v2 query planner directly instead of an HTTP gateway.
//
// Usage with the modified gqlgenc generator.Generate:
//
//	err = generator.Generate(ctx, cfg,
//	    api.ReplacePlugin(gqlgencfed.New(queryDoc, opDocs, cfg.Client, cfg.Generate,
//
// supergraphSDL)),
//
//	)
package gqlgencfed

import (
	"fmt"
	"path/filepath"

	gqlgenConfig "github.com/99designs/gqlgen/codegen/config"
	"github.com/99designs/gqlgen/plugin"
	"github.com/gqlgo/gqlgenc/clientgenv2"
	gqlgencConfig "github.com/gqlgo/gqlgenc/config"
	"github.com/gqlgo/gqlgenc/parsequery"
	"github.com/gqlgo/gqlgenc/querydocument"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/StevenACoffman/defederator/generator"
	"github.com/StevenACoffman/gorouter/federation"
)

var _ plugin.ConfigMutator = &Plugin{}

// Plugin is the federation code-gen plugin for gqlgenc.
// It replaces clientgenv2.Plugin (shares the name "clientgen") and renders
// typed clients that call generated federation_exec.go directly.
type Plugin struct {
	queryFilePaths          []string
	queryDocument           *ast.QueryDocument
	operationQueryDocuments []*ast.QueryDocument
	client                  gqlgenConfig.PackageConfig
	generateConfig          *gqlgencConfig.GenerateConfig
	// supergraphSDL is the original Federation v2 SDL (with federation metadata).
	// Required for building plan specs at generation time.
	supergraphSDL string
}

// New constructs the plugin with pre-parsed query documents.
// supergraphSDL is the original Federation v2 supergraph SDL (with federation metadata);
// it is used to build per-operation plan specs at generation time.
func New(
	queryDocument *ast.QueryDocument,
	operationQueryDocuments []*ast.QueryDocument,
	client gqlgenConfig.PackageConfig,
	generateConfig *gqlgencConfig.GenerateConfig,
	supergraphSDL string,
) *Plugin {
	if generateConfig == nil {
		generateConfig = new(gqlgencConfig.GenerateConfig)
	}
	return &Plugin{
		queryDocument:           queryDocument,
		operationQueryDocuments: operationQueryDocuments,
		client:                  client,
		generateConfig:          generateConfig,
		supergraphSDL:           supergraphSDL,
	}
}

// NewWithFilePaths constructs the plugin with query file paths.
// supergraphSDL is the original Federation v2 supergraph SDL (with federation metadata);
// it is used to build per-operation plan specs at generation time.
func NewWithFilePaths(
	queryFilePaths []string,
	client gqlgenConfig.PackageConfig,
	generateConfig *gqlgencConfig.GenerateConfig,
	supergraphSDL string,
) *Plugin {
	if generateConfig == nil {
		generateConfig = new(gqlgencConfig.GenerateConfig)
	}
	return &Plugin{
		queryFilePaths: queryFilePaths,
		client:         client,
		generateConfig: generateConfig,
		supergraphSDL:  supergraphSDL,
	}
}

// Name returns "clientgen" so api.ReplacePlugin swaps this for the default plugin.
func (p *Plugin) Name() string { return "clientgen" }

// MutateConfig mirrors clientgenv2.Plugin.MutateConfig but calls RenderFederationTemplate.
func (p *Plugin) MutateConfig(cfg *gqlgenConfig.Config) error {
	queryDocument, err := p.resolveQueryDocument(cfg)
	if err != nil {
		return err
	}
	enums := registerBasicModels(cfg, p.client)
	operationQueryDocuments, err := p.resolveOperationQueryDocuments(cfg, queryDocument)
	if err != nil {
		return err
	}
	sourceGenerator := clientgenv2.NewSourceGenerator(cfg, p.client, p.generateConfig)
	source := clientgenv2.NewSource(cfg.Schema, queryDocument, sourceGenerator, p.generateConfig)
	fragments, operationResponses, operations, err := loadSourceArtifacts(
		source,
		operationQueryDocuments,
	)
	if err != nil {
		return err
	}
	planSpecs, err := buildPluginPlanSpecs(p.supergraphSDL, operations)
	if err != nil {
		return err
	}
	if err := generator.RenderFederationTemplate(
		cfg, fragments, operations, operationResponses, source.ResponseSubTypes(),
		p.generateConfig, p.client, planSpecs, "baked", enums, nil,
	); err != nil {
		return fmt.Errorf("gqlgencfed: render template: %w", err)
	}
	if err := generator.WriteExecFile(
		filepath.Dir(p.client.Filename),
		p.client.Package,
	); err != nil {
		return fmt.Errorf("gqlgencfed: write exec file: %w", err)
	}
	return nil
}

// resolveQueryDocument returns p.queryDocument if non-nil, otherwise parses
// the configured query file paths against cfg.Schema.
func (p *Plugin) resolveQueryDocument(cfg *gqlgenConfig.Config) (*ast.QueryDocument, error) {
	if p.queryDocument != nil {
		return p.queryDocument, nil
	}
	querySources, err := parsequery.LoadQuerySources(p.queryFilePaths)
	if err != nil {
		return nil, fmt.Errorf("gqlgencfed: load query sources: %w", err)
	}
	queryDocument, err := parsequery.ParseQueryDocuments(cfg.Schema, querySources)
	if err != nil {
		return nil, fmt.Errorf("gqlgencfed: parse query documents: %w", err)
	}
	return queryDocument, nil
}

// resolveOperationQueryDocuments returns p.operationQueryDocuments if set,
// otherwise builds per-operation documents from queryDocument.Operations.
func (p *Plugin) resolveOperationQueryDocuments(
	cfg *gqlgenConfig.Config,
	queryDocument *ast.QueryDocument,
) ([]*ast.QueryDocument, error) {
	if p.operationQueryDocuments != nil {
		return p.operationQueryDocuments, nil
	}
	out, err := querydocument.QueryDocumentsByOperations(cfg.Schema, queryDocument.Operations)
	if err != nil {
		return nil, fmt.Errorf("gqlgencfed: build per-operation documents: %w", err)
	}
	return out, nil
}

// registerBasicModels installs the same INPUT_OBJECT / ENUM bindings the
// generator's Generate function registers. Returns the declared enum set.
func registerBasicModels(
	cfg *gqlgenConfig.Config,
	client gqlgenConfig.PackageConfig,
) []*generator.EnumDef {
	enums := generator.CollectEnums(cfg.Schema)
	basicModels := generator.BasicTypeModels(cfg.Schema, client.ImportPath())
	if cfg.Models == nil {
		cfg.Models = make(gqlgenConfig.TypeMap, len(basicModels))
	}
	for name, entry := range basicModels {
		if _, ok := cfg.Models[name]; !ok {
			cfg.Models[name] = entry
		}
	}
	return enums
}

// loadSourceArtifacts runs the three gqlgenc source generators in order.
func loadSourceArtifacts(
	source *clientgenv2.Source,
	operationQueryDocuments []*ast.QueryDocument,
) ([]*clientgenv2.Fragment, []*clientgenv2.OperationResponse, []*clientgenv2.Operation, error) {
	fragments, err := source.Fragments()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("gqlgencfed: generate fragments: %w", err)
	}
	operationResponses, err := source.OperationResponses()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("gqlgencfed: generate operation responses: %w", err)
	}
	operations, err := source.Operations(operationQueryDocuments)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("gqlgencfed: generate operations: %w", err)
	}
	return fragments, operationResponses, operations, nil
}

// buildPluginPlanSpecs marshals one URL-keyed plan spec per operation against
// the supergraph SDL.
func buildPluginPlanSpecs(
	supergraphSDL string,
	operations []*clientgenv2.Operation,
) (map[string]string, error) {
	sg, err := federation.ParseSchema(supergraphSDL)
	if err != nil {
		return nil, fmt.Errorf("gqlgencfed: parse supergraph: %w", err)
	}
	planSpecs := make(map[string]string, len(operations))
	for _, op := range operations {
		plan, err := federation.BuildPlan(sg, op.Operation, op.Name)
		if err != nil {
			return nil, fmt.Errorf("gqlgencfed: plan %q: %w", op.Name, err)
		}
		specJSON, err := generator.MarshalURLPlanSpec(plan)
		if err != nil {
			return nil, fmt.Errorf("gqlgencfed: marshal plan spec %q: %w", op.Name, err)
		}
		planSpecs[op.Name] = specJSON
	}
	return planSpecs, nil
}
