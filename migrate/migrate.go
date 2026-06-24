package migrate

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/StevenACoffman/defederator/config"
	"github.com/StevenACoffman/gorouter/federation"
)

// Options configures a migration run.
type Options struct {
	Force  bool // overwrite existing .defederator.yml and client.go
	DryRun bool // print what would be written; write nothing
	// SkipNextSteps suppresses the post-migration instruction block. Set when
	// the caller is going to chain into another step (e.g. generate) that
	// makes the printed advice misleading.
	SkipNextSteps bool
}

// migrationInputs is everything generateFiles needs, assembled by analyse().
// Bundling lets Run remain a thin orchestration shell: analyse does the
// I/O-and-detection legwork, generateFiles does the write.
type migrationInputs struct {
	abs              string
	modulePath       string
	gqCfg            GenqlientConfig
	subgraphs        []SubgraphEntry
	sdl              []byte
	schema           *ast.Schema
	operationSources []*ast.Source
	authFlavors      AuthFlavors
}

// Run migrates a genqlient-based service directory to defederator.
//
// Files are not overwritten unless opts.Force is true.
// With opts.DryRun, files are printed to stdout and not written.
func Run(_ context.Context, dir string, opts Options) error {
	in, err := analyse(dir)
	if err != nil {
		return err
	}
	if err := generateFiles(
		in.abs, in.modulePath, in.gqCfg, in.subgraphs, in.sdl,
		in.schema, in.operationSources, in.authFlavors, opts,
	); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if !opts.DryRun && !opts.SkipNextSteps {
		printNextSteps(in.abs)
	}
	return nil
}

// analyse loads everything migrate needs to decide what to write — config,
// supergraph SDL, operation sources, detected auth flavors, the pruned
// subgraph list — and returns it as a single value. Returns a wrapped error
// for any I/O or parse failure; the missing-go.mod case is non-fatal and
// surfaces only as a stderr warning.
func analyse(dir string) (*migrationInputs, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: resolve dir %q: %w", dir, err)
	}
	gqPath, err := findGenqlientConfig(abs)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	gqCfg, err := loadGenqlientConfig(gqPath)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	subgraphs, sdl, schema, err := loadSubgraphs(abs, gqCfg.Schema)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	sg, err := federation.ParseSchema(string(sdl))
	if err != nil {
		return nil, fmt.Errorf("migrate: parse supergraph for planning: %w", err)
	}
	operationSources, err := LoadOperationSources(gqCfg.Operations, abs)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	subgraphs = FilterSubgraphs(
		subgraphs, UsedSubgraphs(sg, operationSources), serviceNameFromDir(abs),
	)
	authFlavors, err := AuthFlavorsFromOperationDir(filepath.Join(abs, "cross_service"))
	if err != nil {
		return nil, fmt.Errorf("migrate: detect auth flavors: %w", err)
	}
	return &migrationInputs{
		abs:              abs,
		modulePath:       resolveModulePath(abs),
		gqCfg:            gqCfg,
		subgraphs:        subgraphs,
		sdl:              sdl,
		schema:           schema,
		operationSources: operationSources,
		authFlavors:      authFlavors,
	}, nil
}

// resolveModulePath returns the go.mod module path for the project containing
// abs, or the conventional webapp default with a stderr warning when no go.mod
// is found. Missing go.mod is non-fatal because migrate is sometimes run in
// isolated fixture trees during development.
func resolveModulePath(abs string) string {
	modulePath, err := findModulePath(abs)
	if err == nil {
		return modulePath
	}
	const fallback = "github.com/Khan/webapp"
	_, _ = fmt.Fprintf(
		os.Stderr,
		"migrate: warning: could not find go.mod (%v); defaulting module path to %q\n",
		err, fallback,
	)
	return fallback
}

// loadSubgraphs reads the supergraph SDL at the given schema path (relative to abs)
// and returns the parsed subgraph entries, raw SDL bytes, and a parsed *ast.Schema
// for downstream type lookups.
func loadSubgraphs(abs, schemaRel string) ([]SubgraphEntry, []byte, *ast.Schema, error) {
	schemaPath := filepath.Join(abs, schemaRel)
	sdl, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read supergraph schema %s: %w", schemaPath, err)
	}
	entries, err := ParseSubgraphs(string(sdl))
	if err != nil {
		return nil, nil, nil, err
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: schemaPath, Input: string(sdl)})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load schema %s: %w", schemaPath, err)
	}
	return entries, sdl, schema, nil
}

// generateFiles writes .defederator.yml and cross_service/client.go.
func generateFiles(
	abs, modulePath string,
	gqCfg GenqlientConfig,
	subgraphs []SubgraphEntry,
	_ []byte, // sdl: reserved; SDL is already parsed into schema by analyse()
	schema *ast.Schema,
	operationSources []*ast.Source,
	authFlavors AuthFlavors,
	opts Options,
) error {
	serviceName := filepath.Base(abs)
	genqlientPkg := modulePath + "/services/" + serviceName + "/generated/genqlient"

	// Bind every INPUT_OBJECT and ENUM that appears in at least one operation
	// to the corresponding genqlient-generated Go type. genqlient emits a Go
	// type for any input/enum referenced by its operations, and defederator
	// reads the same operation files via the inherited query: glob, so the
	// genqlient package is guaranteed to have these types. Pointing
	// defederator at them aligns the two clients' Go types, removing the
	// per-call-site `defederator.Foo(x)` casts that user wrappers would
	// otherwise need.
	//
	// We do NOT restrict to subgraph-owned types: cross-service code routinely
	// passes input objects owned by other subgraphs (the local service is
	// calling into them), so the ownership filter is wrong for this purpose.
	usedInputObjects, err := OperationVariableInputObjects(schema, operationSources)
	if err != nil {
		return fmt.Errorf("collect operation input objects: %w", err)
	}
	usedEnums, err := OperationUsedEnums(schema, operationSources)
	if err != nil {
		return fmt.Errorf("collect operation enums: %w", err)
	}

	in := &YAMLInput{
		Genqlient:    gqCfg,
		InputObjects: usedInputObjects,
		Enums:        usedEnums,
		GenqlientPkg: genqlientPkg,
	}
	defedYAML, err := DefederatorYAML(in)
	if err != nil {
		return fmt.Errorf("generate .defederator.yml: %w", err)
	}
	if err := writeFile(
		filepath.Join(abs, ".defederator.yml"),
		[]byte(defedYAML),
		opts,
	); err != nil {
		return fmt.Errorf("write .defederator.yml: %w", err)
	}
	data := DataFromDir(abs, modulePath, subgraphs, authFlavors)
	clientSrc, err := Render(data)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(abs, "cross_service", "client.go"), []byte(clientSrc), opts)
}

// printNextSteps prints post-migration instructions to stdout.
func printNextSteps(abs string) {
	defedYMLPath := filepath.Join(abs, ".defederator.yml")
	_, _ = fmt.Fprintf(os.Stdout, "migrate: done. Next steps:\n")
	_, _ = fmt.Fprintf(os.Stdout, "  1. Review %s and adjust bindings if needed.\n", defedYMLPath)
	_, _ = fmt.Fprintf(os.Stdout, "  2. Run: defederator --dir %s\n", abs)
	_, _ = fmt.Fprintf(
		os.Stdout,
		"  3. Swap cross_service call sites: genqlient.X(ctx, ctx.GraphQL().AsXxx(), …)\n"+
			"     → genqlient.X(ctx, New{Admin,User}GraphQLClient(ctx), …). Keep the\n"+
			"     genqlient functions and response types; only the client arg changes.\n",
	)
	_, _ = fmt.Fprintf(
		os.Stdout,
		"  4. Wire service discovery once in cmd/serve: cross_service.SetServiceDiscovery(sd).\n",
	)
	_, _ = fmt.Fprintf(os.Stdout, "  5. %s\n", LintFixHint())
}

// LintFixHint returns the single-line suggestion printed after a successful
// migrate run. With the process-level service-discovery handle (SetServiceDiscovery),
// the call-site swap adds no new ctx requirement, so the ADR-429 cascade does not
// occur; this hint is a fallback for any pre-existing ka-context-interface debt a
// recompile surfaces. The analyzer emits SuggestedFix records that golangci-lint
// applies in place with --fix, resolving the batch in one command.
func LintFixHint() string {
	return "If a recompile surfaces ka-context-interface debt: tools/runlint.sh --fix <changed-files-or-packages>"
}

// writeFile writes data to path, respecting DryRun and Force options.
func writeFile(path string, data []byte, opts Options) error {
	if opts.DryRun {
		_, _ = fmt.Fprintf(os.Stdout, "\n=== %s ===\n%s\n", path, data)
		return nil
	}
	if _, err := os.Stat(path); err == nil && !opts.Force {
		_, _ = fmt.Fprintf(
			os.Stdout,
			"migrate: skipping %s (already exists; use --force to overwrite)\n",
			path,
		)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	_, _ = fmt.Fprintf(os.Stdout, "migrate: wrote %s\n", path)
	return nil
}

// findGenqlientConfig looks for genqlient.yaml / genqlient.yml in dir.
func findGenqlientConfig(dir string) (string, error) {
	candidates := []string{"genqlient.yaml", "genqlient.yml", ".genqlient.yaml", ".genqlient.yml"}
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no genqlient.yaml found in %s", dir)
}

// loadGenqlientConfig reads a genqlient.yaml and returns a GenqlientConfig.
func loadGenqlientConfig(path string) (GenqlientConfig, error) {
	cfg, err := config.LoadGenqlientConfig(path)
	if err != nil {
		return GenqlientConfig{}, fmt.Errorf("load genqlient config: %w", err)
	}
	return GenqlientConfig{
		Schema:     cfg.Schema,
		Operations: cfg.Query,
		Generated:  cfg.Client.Filename,
		Bindings:   cfg.Bindings,
	}, nil
}

// findModulePath walks up from dir to find go.mod and returns the module path.
func findModulePath(dir string) (string, error) {
	for {
		candidate := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(candidate); err == nil {
			return parseModulePath(string(data))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("go.mod not found")
}

// parseModulePath extracts the module path from go.mod content.
func parseModulePath(content string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", errors.New("module directive not found in go.mod")
}
