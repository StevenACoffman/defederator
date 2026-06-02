// Package generator orchestrates the defederator code-generation pipeline.
package generator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

	// Map user-defined types that lack an explicit binding so source_generator
	// can resolve them.
	//
	// - INPUT_OBJECT types are mapped to graphql.String. The generated client
	//   serializes input objects via the JSON marshaler in each operation method,
	//   so the Go-side representation can be opaque.
	// - ENUM types are registered as named string types in the client package
	//   itself. The template (see template.gotpl) emits a Go `type T string` plus
	//   typed `const ( … )` constants, matching genqlient's enum output. The
	//   binder's package-load fallback (source_generator.go's syntheticNamedType)
	//   handles the fact that the package being generated isn't on disk yet.
	//
	// gqlgenc calls Type(name) for any field where IsBasicType() returns true,
	// which covers both enums (no sub-selections) and input types (used as args).
	// gqlgen's Init() registers introspection enums but leaves user-defined enums
	// and input objects unmapped when no server model file is generated.
	clientPkg := gqlgenConfig.PackageConfig{
		Filename: cfg.ClientFilename(),
		Package:  cfg.Client.Package,
	}
	clientImportPath := clientPkg.ImportPath()

	if gqlgencCfg.GQLConfig.Models == nil {
		gqlgencCfg.GQLConfig.Models = make(gqlgenConfig.TypeMap)
	}
	var enums []*EnumDef
	for name, def := range gqlgencCfg.GQLConfig.Schema.Types {
		switch def.Kind {
		case ast.InputObject:
			if _, ok := gqlgencCfg.GQLConfig.Models[name]; !ok {
				gqlgencCfg.GQLConfig.Models[name] = gqlgenConfig.TypeMapEntry{
					Model: gqlgenConfig.StringList{"github.com/99designs/gqlgen/graphql.String"},
				}
			}
		case ast.Enum:
			if strings.HasPrefix(name, "__") {
				// Skip introspection enums; gqlgen already registers them.
				continue
			}
			enum := &EnumDef{
				GoName:      name,
				GraphQLName: name,
				Description: def.Description,
			}
			for _, v := range def.EnumValues {
				enum.Values = append(enum.Values, EnumValueDef{
					GoName:      name + GoConstName(v.Name),
					GraphQLName: v.Name,
					Description: v.Description,
				})
			}
			sort.Slice(enum.Values, func(i, j int) bool {
				return enum.Values[i].GraphQLName < enum.Values[j].GraphQLName
			})
			enums = append(enums, enum)

			if _, ok := gqlgencCfg.GQLConfig.Models[name]; !ok {
				gqlgencCfg.GQLConfig.Models[name] = gqlgenConfig.TypeMapEntry{
					Model: gqlgenConfig.StringList{clientImportPath + "." + name},
				}
			}
		}
	}
	sort.Slice(enums, func(i, j int) bool { return enums[i].GoName < enums[j].GoName })

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

	operationQueryDocuments, err := querydocument.QueryDocumentsByOperations(
		gqlgencCfg.GQLConfig.Schema,
		queryDocument.Operations,
	)
	if err != nil {
		return fmt.Errorf("generate: build per-operation documents: %w", err)
	}

	generateCfg := buildGenerateConfig(cfg)

	sourceGenerator := clientgenv2.NewSourceGenerator(gqlgencCfg.GQLConfig, clientPkg, generateCfg)
	source := clientgenv2.NewSource(
		gqlgencCfg.GQLConfig.Schema,
		queryDocument,
		sourceGenerator,
		generateCfg,
	)

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
		enums,
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
