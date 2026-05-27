// Package gqlgencfed provides a gqlgenc plugin that generates typed Go clients
// whose methods call the Federation v2 query planner directly instead of an HTTP gateway.
//
// Usage with the modified gqlgenc generator.Generate:
//
//	err = generator.Generate(ctx, cfg,
//	    api.ReplacePlugin(gqlgencfed.New(queryDoc, opDocs, cfg.Client, cfg.Generate, supergraphSDL)),
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

	"github.com/StevenACoffman/defederator/generator"
	"github.com/StevenACoffman/gorouter/federation"

	"github.com/vektah/gqlparser/v2/ast"
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
	queryDocument := p.queryDocument
	if queryDocument == nil {
		querySources, err := parsequery.LoadQuerySources(p.queryFilePaths)
		if err != nil {
			return fmt.Errorf("gqlgencfed: load query sources: %w", err)
		}
		queryDocument, err = parsequery.ParseQueryDocuments(cfg.Schema, querySources)
		if err != nil {
			return fmt.Errorf("gqlgencfed: parse query documents: %w", err)
		}
	}

	operationQueryDocuments := p.operationQueryDocuments
	if operationQueryDocuments == nil {
		var err error
		operationQueryDocuments, err = querydocument.QueryDocumentsByOperations(cfg.Schema, queryDocument.Operations)
		if err != nil {
			return fmt.Errorf("gqlgencfed: build per-operation documents: %w", err)
		}
	}

	sourceGenerator := clientgenv2.NewSourceGenerator(cfg, p.client, p.generateConfig)
	source := clientgenv2.NewSource(cfg.Schema, queryDocument, sourceGenerator, p.generateConfig)

	fragments, err := source.Fragments()
	if err != nil {
		return fmt.Errorf("gqlgencfed: generate fragments: %w", err)
	}

	operationResponses, err := source.OperationResponses()
	if err != nil {
		return fmt.Errorf("gqlgencfed: generate operation responses: %w", err)
	}

	operations, err := source.Operations(operationQueryDocuments)
	if err != nil {
		return fmt.Errorf("gqlgencfed: generate operations: %w", err)
	}

	sg, err := federation.ParseSchema(p.supergraphSDL)
	if err != nil {
		return fmt.Errorf("gqlgencfed: parse supergraph: %w", err)
	}

	planSpecs := make(map[string]string, len(operations))
	for _, op := range operations {
		plan, err := federation.BuildPlan(sg, op.Operation, op.Name)
		if err != nil {
			return fmt.Errorf("gqlgencfed: plan %q: %w", op.Name, err)
		}
		specJSON, err := generator.MarshalURLPlanSpec(plan)
		if err != nil {
			return fmt.Errorf("gqlgencfed: marshal plan spec %q: %w", op.Name, err)
		}
		planSpecs[op.Name] = specJSON
	}

	if err := generator.RenderFederationTemplate(
		cfg,
		fragments,
		operations,
		operationResponses,
		source.ResponseSubTypes(),
		p.generateConfig,
		p.client,
		planSpecs,
		"baked",
	); err != nil {
		return fmt.Errorf("gqlgencfed: render template: %w", err)
	}

	if err := generator.WriteExecFile(filepath.Dir(p.client.Filename), p.client.Package); err != nil {
		return fmt.Errorf("gqlgencfed: write exec file: %w", err)
	}

	return nil
}
