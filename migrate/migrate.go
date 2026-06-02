package migrate

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/StevenACoffman/defederator/config"
)

// Options configures a migration run.
type Options struct {
	Force  bool // overwrite existing .defederator.yml and client.go
	DryRun bool // print what would be written; write nothing
}

// Run migrates a genqlient-based service directory to defederator.
//
// Files are not overwritten unless opts.Force is true.
// With opts.DryRun, files are printed to stdout and not written.
func Run(_ context.Context, dir string, opts Options) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("migrate: resolve dir %q: %w", dir, err)
	}
	gqPath, err := findGenqlientConfig(abs)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	gqCfg, err := loadGenqlientConfig(gqPath)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	subgraphs, sdl, err := loadSubgraphs(abs, gqCfg.Schema)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	modulePath, err := findModulePath(abs)
	if err != nil {
		modulePath = "github.com/Khan/webapp"
		_, _ = fmt.Fprintf(
			os.Stderr,
			"migrate: warning: could not find go.mod (%v); defaulting module path to %q\n",
			err,
			modulePath,
		)
	}
	if err := generateFiles(abs, modulePath, gqCfg, subgraphs, sdl, opts); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if !opts.DryRun {
		printNextSteps(abs)
	}
	return nil
}

// loadSubgraphs reads the supergraph SDL at the given schema path (relative to abs)
// and returns the parsed subgraph entries and raw SDL bytes.
func loadSubgraphs(abs, schema string) ([]SubgraphEntry, []byte, error) {
	schemaPath := filepath.Join(abs, schema)
	sdl, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read supergraph schema %s: %w", schemaPath, err)
	}
	entries, err := ParseSubgraphs(string(sdl))
	if err != nil {
		return nil, nil, err
	}
	return entries, sdl, nil
}

// generateFiles writes .defederator.yml and cross_service/client.go.
func generateFiles(
	abs, modulePath string,
	gqCfg GenqlientConfig,
	subgraphs []SubgraphEntry,
	sdl []byte,
	opts Options,
) error {
	serviceName := filepath.Base(abs)
	genqlientPkg := modulePath + "/services/" + serviceName + "/generated/genqlient"

	// Find the join__Graph enum name for this service so we can filter INPUT_OBJECTs
	// to only those owned by this subgraph (via @join__type(graph:) directives).
	var serviceEnumName string
	for _, sg := range subgraphs {
		if sg.ServiceName == serviceName {
			serviceEnumName = sg.EnumName
			break
		}
	}
	inputObjects, _ := ParseInputObjectsForService(string(sdl), serviceEnumName)

	in := YAMLInput{
		Genqlient:    gqCfg,
		InputObjects: inputObjects,
		GenqlientPkg: genqlientPkg,
	}
	defedYAML, err := DefederatorYAML(in)
	if err != nil {
		return fmt.Errorf("generate .defederator.yml: %w", err)
	}
	if err := writeFile(filepath.Join(abs, ".defederator.yml"), []byte(defedYAML), opts); err != nil {
		return fmt.Errorf("write .defederator.yml: %w", err)
	}
	data := DataFromDir(abs, modulePath, subgraphs)
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
		"  3. Update cross_service call sites to use the new FederationClient.\n",
	)
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
		return err
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
