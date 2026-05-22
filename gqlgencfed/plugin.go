// Package gqlgencfed provides a gqlgenc plugin that generates typed Go clients
// whose methods call the Federation v2 query planner directly instead of an HTTP gateway.
//
// Usage with the modified gqlgenc generator.Generate:
//
//	err = generator.Generate(ctx, cfg,
//	    api.ReplacePlugin(gqlgencfed.New(queryDoc, opDocs, cfg.Client, cfg.Generate)),
//	)
package gqlgencfed

import (
	"fmt"

	gqlgenConfig "github.com/99designs/gqlgen/codegen/config"
	"github.com/99designs/gqlgen/plugin"

	"github.com/gqlgo/gqlgenc/clientgenv2"
	gqlgencConfig "github.com/gqlgo/gqlgenc/config"
	"github.com/gqlgo/gqlgenc/parsequery"
	"github.com/gqlgo/gqlgenc/querydocument"

	"github.com/StevenACoffman/defederator/generator"

	"github.com/vektah/gqlparser/v2/ast"
)

var _ plugin.ConfigMutator = &Plugin{}

// Plugin is the federation code-gen plugin for gqlgenc.
// It replaces clientgenv2.Plugin (shares the name "clientgen") and renders
// with the federation template that uses federationclient.Client.Execute.
type Plugin struct {
	queryFilePaths          []string
	queryDocument           *ast.QueryDocument
	operationQueryDocuments []*ast.QueryDocument
	client                  gqlgenConfig.PackageConfig
	generateConfig          *gqlgencConfig.GenerateConfig
}

// New constructs the plugin with pre-parsed query documents.
// Pass pre-parsed documents when available, or nil to have MutateConfig
// fall back to loading from queryFilePaths.
func New(
	queryDocument *ast.QueryDocument,
	operationQueryDocuments []*ast.QueryDocument,
	client gqlgenConfig.PackageConfig,
	generateConfig *gqlgencConfig.GenerateConfig,
) *Plugin {
	if generateConfig == nil {
		generateConfig = new(gqlgencConfig.GenerateConfig)
	}
	return &Plugin{
		queryDocument:           queryDocument,
		operationQueryDocuments: operationQueryDocuments,
		client:                  client,
		generateConfig:          generateConfig,
	}
}

// NewWithFilePaths constructs the plugin with query file paths.
// MutateConfig loads and parses the queries from these paths.
func NewWithFilePaths(
	queryFilePaths []string,
	client gqlgenConfig.PackageConfig,
	generateConfig *gqlgencConfig.GenerateConfig,
) *Plugin {
	if generateConfig == nil {
		generateConfig = new(gqlgencConfig.GenerateConfig)
	}
	return &Plugin{
		queryFilePaths: queryFilePaths,
		client:         client,
		generateConfig: generateConfig,
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

	if err := generator.RenderFederationTemplate(
		cfg,
		fragments,
		operations,
		operationResponses,
		source.ResponseSubTypes(),
		p.generateConfig,
		p.client,
	); err != nil {
		return fmt.Errorf("gqlgencfed: render template: %w", err)
	}

	return nil
}
