// Package generator orchestrates the defederator code-generation pipeline.
package generator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	gqlgenConfig "github.com/99designs/gqlgen/codegen/config"
	"github.com/gqlgo/gqlgenc/clientgenv2"
	gqlgencConfig "github.com/gqlgo/gqlgenc/config"
	"github.com/gqlgo/gqlgenc/parsequery"
	"github.com/gqlgo/gqlgenc/querydocument"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	defConfig "github.com/StevenACoffman/defederator/config"
	"github.com/StevenACoffman/gorouter/federation"
)

// sourceOutput bundles the gqlgenc-generated artifacts consumed by emitOutputs.
type sourceOutput struct {
	fragments          []*clientgenv2.Fragment
	operationResponses []*clientgenv2.OperationResponse
	operations         []*clientgenv2.Operation
	responseSubTypes   []*clientgenv2.StructSource
	generateCfg        *gqlgencConfig.GenerateConfig
}

// Generate runs the full defederator code-generation pipeline for cfg.
//
// When cfg.Verbose is true, per-pattern, per-file, per-literal, per-match,
// and per-operation progress lines are written to stderr. Otherwise the only
// user-facing output is the final "defederator: wrote …" line emitted by the
// CLI (see cmd/defederator/main.go).
func Generate(_ context.Context, cfg *defConfig.Config) error {
	log := io.Discard
	if cfg.Verbose {
		log = os.Stderr
	}
	_, _ = fmt.Fprintf(log, "Generating with config: %+v\n", cfg)

	sdl, sg, err := loadSupergraph(cfg)
	if err != nil {
		return err
	}
	gqlgencCfg, clientPkg, err := initGqlgenc(cfg, sdl)
	if err != nil {
		return err
	}
	queryDocument, err := loadQueryDocuments(cfg, gqlgencCfg, log)
	if err != nil {
		return err
	}
	enums := registerModelBindings(gqlgencCfg, queryDocument, clientPkg)
	srcOut, err := runClientGen(gqlgencCfg, queryDocument, clientPkg, cfg, log)
	if err != nil {
		return err
	}
	return emitOutputs(cfg, sg, gqlgencCfg, clientPkg, queryDocument, &srcOut, enums)
}

// loadSupergraph reads cfg's supergraph SDL and parses it for federation planning.
func loadSupergraph(cfg *defConfig.Config) (string, *federation.Supergraph, error) {
	sdlPath := cfg.SchemaPath()
	sdlBytes, err := os.ReadFile(sdlPath)
	if err != nil {
		return "", nil, fmt.Errorf("generate: read supergraph %s: %w", sdlPath, err)
	}
	sdl := string(sdlBytes)
	sg, err := federation.ParseSchema(sdl)
	if err != nil {
		return "", nil, fmt.Errorf("generate: parse supergraph: %w", err)
	}
	return sdl, sg, nil
}

// initGqlgenc strips federation types, builds the gqlgenc config and loads the
// schema directly from the cleaned SDL (bypassing remote-introspection).
func initGqlgenc(
	cfg *defConfig.Config,
	sdl string,
) (*gqlgencConfig.Config, gqlgenConfig.PackageConfig, error) {
	cleanSDL, err := StripFederationTypes(sdl)
	if err != nil {
		return nil, gqlgenConfig.PackageConfig{}, fmt.Errorf(
			"generate: strip federation types: %w", err,
		)
	}
	gqlgencCfg := buildGqlgencConfig(cfg, cleanSDL)
	schema, err := gqlparser.LoadSchema(gqlgencCfg.GQLConfig.Sources...)
	if err != nil {
		return nil, gqlgenConfig.PackageConfig{}, fmt.Errorf("generate: load schema: %w", err)
	}
	gqlgencCfg.GQLConfig.Schema = schema
	if err := gqlgencCfg.GQLConfig.Init(); err != nil {
		return nil, gqlgenConfig.PackageConfig{}, fmt.Errorf(
			"generate: init gqlgen config: %w", err,
		)
	}
	clientPkg := gqlgenConfig.PackageConfig{
		Filename: cfg.ClientFilename(),
		Package:  cfg.Client.Package,
	}
	for _, v := range gqlgencCfg.GQLConfig.Schema.Implements {
		sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
	}
	return gqlgencCfg, clientPkg, nil
}

// loadQueryDocuments expands cfg.Query, splits .graphql vs. .go inputs,
// extracts embedded queries from Go files, and returns the parsed query
// document.
func loadQueryDocuments(
	cfg *defConfig.Config,
	gqlgencCfg *gqlgencConfig.Config,
	log io.Writer,
) (*ast.QueryDocument, error) {
	expandedPaths, err := expandGlobs(cfg.Query, cfg.Dir, log)
	if err != nil {
		return nil, fmt.Errorf("generate: expand query globs: %w", err)
	}
	graphqlPaths, extraSources, err := classifyQueryPaths(expandedPaths, log)
	if err != nil {
		return nil, err
	}
	querySources, err := parsequery.LoadQuerySources(graphqlPaths)
	if err != nil {
		return nil, fmt.Errorf("generate: load query sources: %w", err)
	}
	querySources = append(querySources, extraSources...)
	queryDocument, err := parsequery.ParseQueryDocuments(gqlgencCfg.GQLConfig.Schema, querySources)
	if err != nil {
		return nil, fmt.Errorf("generate: parse query documents: %w", err)
	}
	return queryDocument, nil
}

// classifyQueryPaths splits expanded query paths into .graphql files and
// extra ast.Sources extracted from Go file `# @genqlient` literals.
func classifyQueryPaths(
	expandedPaths []string,
	log io.Writer,
) ([]string, []*ast.Source, error) {
	var graphqlPaths []string
	var extraSources []*ast.Source
	for _, p := range expandedPaths {
		switch {
		case hasGraphQLExt(p):
			graphqlPaths = append(graphqlPaths, p)
		case hasGoExt(p):
			embedded, err := extractQueriesFromGoFile(p, log)
			if err != nil {
				return nil, nil, fmt.Errorf("generate: extract queries from %s: %w", p, err)
			}
			for _, eq := range embedded {
				extraSources = append(extraSources, &ast.Source{
					Name:  eq.source,
					Input: eq.text,
				})
			}
		}
	}
	return graphqlPaths, extraSources, nil
}

// registerModelBindings registers user-defined INPUT_OBJECT and ENUM bindings
// on gqlgencCfg so SourceGenerator.Type() can resolve them. Enums are lazily
// registered — only those an operation selects, to avoid colliding with user
// fragments that share a schema enum's name. Returns the set of declared enums.
func registerModelBindings(
	gqlgencCfg *gqlgencConfig.Config,
	queryDocument *ast.QueryDocument,
	clientPkg gqlgenConfig.PackageConfig,
) []*EnumDef {
	usedEnums := UsedEnumsInOperations(gqlgencCfg.GQLConfig.Schema, queryDocument)
	enums := FilterEnumsByUsed(CollectEnums(gqlgencCfg.GQLConfig.Schema), usedEnums)
	basicModels := BasicTypeModels(gqlgencCfg.GQLConfig.Schema, clientPkg.ImportPath())
	if gqlgencCfg.GQLConfig.Models == nil {
		gqlgencCfg.GQLConfig.Models = make(gqlgenConfig.TypeMap, len(basicModels))
	}
	for name, entry := range basicModels {
		def := gqlgencCfg.GQLConfig.Schema.Types[name]
		if def != nil && def.Kind == ast.Enum && !usedEnums[name] {
			continue
		}
		if _, ok := gqlgencCfg.GQLConfig.Models[name]; !ok {
			gqlgencCfg.GQLConfig.Models[name] = entry
		}
	}
	return enums
}

// runClientGen invokes gqlgenc's Source to produce fragments, operation
// responses, and operations from the parsed query document.
func runClientGen(
	gqlgencCfg *gqlgencConfig.Config,
	queryDocument *ast.QueryDocument,
	clientPkg gqlgenConfig.PackageConfig,
	cfg *defConfig.Config,
	log io.Writer,
) (sourceOutput, error) {
	operationQueryDocuments, err := querydocument.QueryDocumentsByOperations(
		gqlgencCfg.GQLConfig.Schema,
		queryDocument.Operations,
	)
	if err != nil {
		return sourceOutput{}, fmt.Errorf("generate: build per-operation documents: %w", err)
	}
	generateCfg := buildGenerateConfig(cfg)
	sourceGenerator := clientgenv2.NewSourceGenerator(gqlgencCfg.GQLConfig, clientPkg, generateCfg)
	source := clientgenv2.NewSource(
		gqlgencCfg.GQLConfig.Schema, queryDocument, sourceGenerator, generateCfg,
	)
	fragments, err := source.Fragments()
	if err != nil {
		return sourceOutput{}, fmt.Errorf("generate: generate fragments: %w", err)
	}
	operationResponses, err := source.OperationResponses()
	if err != nil {
		return sourceOutput{}, fmt.Errorf("generate: generate operation responses: %w", err)
	}
	operations, err := source.Operations(operationQueryDocuments)
	if err != nil {
		return sourceOutput{}, fmt.Errorf("generate: generate operations: %w", err)
	}
	_, _ = fmt.Fprintf(log, "Generated %d operations\n", len(operations))
	for _, op := range operations {
		_, _ = fmt.Fprintf(log, "Operation: %s\n", op.Name)
	}
	out := sourceOutput{
		fragments:          fragments,
		operationResponses: operationResponses,
		operations:         operations,
		responseSubTypes:   source.ResponseSubTypes(),
		generateCfg:        generateCfg,
	}
	if cfg.Generate != nil && cfg.Generate.Optional == "value" {
		applyValueOptional(&out)
	}
	return out, nil
}

// emitOutputs writes the rendered client, the embedded executor source, and
// any optional operations manifest.
func emitOutputs(
	cfg *defConfig.Config,
	sg *federation.Supergraph,
	gqlgencCfg *gqlgencConfig.Config,
	clientPkg gqlgenConfig.PackageConfig,
	queryDocument *ast.QueryDocument,
	srcOut *sourceOutput,
	enums []*EnumDef,
) error {
	introspectionByName, err := PlanIntrospectionOps(gqlgencCfg.GQLConfig.Schema, queryDocument)
	if err != nil {
		return fmt.Errorf("generate: plan introspection: %w", err)
	}
	urlMode := cfg.URLMode
	if urlMode == "" {
		urlMode = "baked"
	}
	planSpecs, err := buildPlanSpecs(sg, srcOut.operations, introspectionByName, urlMode)
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}
	if err := RenderFederationTemplate(
		gqlgencCfg.GQLConfig, srcOut.fragments, srcOut.operations,
		srcOut.operationResponses, srcOut.responseSubTypes, srcOut.generateCfg,
		clientPkg, planSpecs, urlMode, enums, introspectionByName,
	); err != nil {
		return fmt.Errorf("generate: render template: %w", err)
	}
	if err := WriteExecFile(filepath.Dir(cfg.ClientFilename()), cfg.Client.Package); err != nil {
		return fmt.Errorf("generate: write exec file: %w", err)
	}
	return maybeExportOperations(cfg, srcOut.operations)
}

// maybeExportOperations writes the operations manifest if configured.
func maybeExportOperations(cfg *defConfig.Config, operations []*clientgenv2.Operation) error {
	if cfg.Generate == nil || cfg.Generate.ExportOperations == "" {
		return nil
	}
	exportPath := cfg.Generate.ExportOperations
	if !filepath.IsAbs(exportPath) {
		exportPath = filepath.Join(cfg.Dir, exportPath)
	}
	if err := exportOperations(exportPath, operations); err != nil {
		return fmt.Errorf("generate: export operations to %s: %w", exportPath, err)
	}
	return nil
}

// buildPlanSpecs returns one URL/enum plan spec per non-introspection
// operation. Introspection-only operations (entries present in
// introspectionByName) are skipped because they don't make subgraph calls.
//
// urlMode controls the plan-spec format: "enum" produces a placeholder spec
// resolved per-call via service discovery; anything else (including "") bakes
// the subgraph URLs into the spec.
func buildPlanSpecs(
	sg *federation.Supergraph,
	operations []*clientgenv2.Operation,
	introspectionByName map[string]IntrospectionInfo,
	urlMode string,
) (map[string]string, error) {
	marshalSpec := MarshalURLPlanSpec
	if urlMode == "enum" {
		marshalSpec = MarshalEnumPlanSpec
	}
	out := make(map[string]string, len(operations))
	for _, op := range operations {
		if _, isIntrospection := introspectionByName[op.Name]; isIntrospection {
			continue
		}
		plan, err := federation.BuildPlan(sg, op.Operation, op.Name)
		if err != nil {
			return nil, fmt.Errorf("plan %q: %w", op.Name, err)
		}
		specJSON, err := marshalSpec(plan)
		if err != nil {
			return nil, fmt.Errorf("marshal plan spec %q: %w", op.Name, err)
		}
		out[op.Name] = specJSON
	}
	return out, nil
}

func buildGqlgencConfig(cfg *defConfig.Config, cleanSDL string) *gqlgencConfig.Config {
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

	return &gqlgencConfig.Config{
		GQLConfig: gqlCfg,
		Query:     cfg.Query,
	}
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
	gc := &gqlgencConfig.GenerateConfig{}
	if cfg.Generate.ClientInterfaceName != nil {
		gc.ClientInterfaceName = cfg.Generate.ClientInterfaceName
	}
	// NOTE: cfg.Generate.Optional ("value" / "pointer") has no upstream
	// equivalent in gqlgenc v0.37.0. Nullable scalar pointer-wrapping is
	// hard-coded in gqlgen's binder.CopyModifiersFromAst. Honoring "value"
	// requires post-processing the generated AST in defederator itself —
	// not implemented here. The field is accepted but currently ignored.
	return gc
}
