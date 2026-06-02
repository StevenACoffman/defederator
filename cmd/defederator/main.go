// Command defederator generates typed Go federation clients from a supergraph SDL.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/StevenACoffman/defederator/config"
	"github.com/StevenACoffman/defederator/generator"
	"github.com/StevenACoffman/defederator/migrate"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "defederator: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// --version is intentionally handled before subcommand dispatch so it
	// works as both `defederator --version` and `defederator -version`.
	for _, a := range os.Args[1:] {
		if a == "--version" || a == "-version" {
			printVersion(os.Stdout)
			return nil
		}
	}
	// Dispatch on the first argument when it is a word (not a flag).
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		switch os.Args[1] {
		case "migrate":
			return runMigrate(os.Args[2:])
		case "generate":
			return runGenerate(os.Args[2:])
		case "version":
			printVersion(os.Stdout)
			return nil
		case "help":
			return runHelp()
		default:
			return fmt.Errorf(
				"unknown subcommand %q; valid subcommands: generate, migrate, version, help",
				os.Args[1],
			)
		}
	}
	// Backward-compatible: no subcommand → generate.
	return runGenerate(os.Args[1:])
}

// printVersion writes a one-line version banner derived from the Go module
// build info embedded in the binary. The line includes the module's semantic
// version (or "(devel)" for unreleased builds), the git revision SHA, and the
// build time when present.
//
// Output format is deliberately simple so it parses cleanly in shell scripts:
//
//	defederator <version> <revision> <build-time>
//
// Any field absent from BuildInfo is rendered as "(unknown)".
func printVersion(w *os.File) {
	version, revision, buildTime := versionInfo()
	_, _ = fmt.Fprintf(w, "defederator %s %s %s\n", version, revision, buildTime)
}

// versionInfo extracts the three fields printVersion needs from the embedded
// BuildInfo, defaulting each missing field to "(unknown)". Separated from
// printVersion so the formatting and the BuildInfo decoding stay independent.
func versionInfo() (version, revision, buildTime string) {
	version, revision, buildTime = "(unknown)", "(unknown)", "(unknown)"
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version, revision, buildTime
	}
	if info.Main.Version != "" {
		version = info.Main.Version
	}
	for _, s := range info.Settings {
		switch {
		case s.Key == "vcs.revision" && s.Value != "":
			revision = s.Value
		case s.Key == "vcs.time" && s.Value != "":
			buildTime = s.Value
		}
	}
	return version, revision, buildTime
}

func runGenerate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	var configPath, dir string
	var verbose bool
	fs.StringVar(
		&configPath,
		"config",
		"",
		"path to .defederator.yml (default: search current and parent dirs)",
	)
	fs.StringVar(&configPath, "c", "", "path to .defederator.yml (shorthand)")
	fs.StringVar(&dir, "dir", ".", "working directory for config-relative path resolution")
	fs.BoolVar(&verbose, "verbose", false, "print per-file and per-operation progress on stderr")
	fs.BoolVar(&verbose, "v", false, "shorthand for --verbose")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return generateAt(context.Background(), configPath, dir, verbose)
}

// generateAt loads a defederator config and runs code generation against it.
// Exactly one of configPath or dir is meaningful per call: if configPath is
// non-empty it is loaded directly; otherwise .defederator.yml is searched
// starting from dir. The "defederator: wrote <path>" success line is printed
// on stdout so both the generate subcommand and the migrate-chains-generate
// path produce the same user-facing output. verbose enables per-file progress
// diagnostics on stderr.
func generateAt(ctx context.Context, configPath, dir string, verbose bool) error {
	var cfg *config.Config
	var err error
	if configPath != "" {
		cfg, err = config.LoadConfig(configPath)
	} else {
		cfg, err = config.LoadConfigFromDir(dir)
	}
	if err != nil {
		return err
	}
	cfg.Verbose = verbose
	if err := generator.Generate(ctx, cfg); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "defederator: wrote %s\n", cfg.ClientFilename())
	return nil
}

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	var force, dryRun, noGenerate, verbose bool
	fs.BoolVar(
		&force,
		"force",
		false,
		"overwrite existing .defederator.yml and cross_service/client.go",
	)
	fs.BoolVar(&dryRun, "dry-run", false, "print what would be written without writing files")
	fs.BoolVar(
		&noGenerate,
		"no-generate",
		false,
		"only write .defederator.yml and cross_service/client.go; skip the generate step",
	)
	fs.BoolVar(&verbose, "verbose", false, "print per-file and per-operation progress on stderr")
	fs.BoolVar(&verbose, "v", false, "shorthand for --verbose")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(
			"migrate requires exactly one argument: <service-dir>\nusage: defederator migrate [--force] [--dry-run] [--no-generate] [-v] <service-dir>",
		)
	}
	dir := fs.Arg(0)
	ctx := context.Background()
	chainGenerate := !dryRun && !noGenerate
	if err := migrate.Run(ctx, dir, migrate.Options{
		Force:         force,
		DryRun:        dryRun,
		SkipNextSteps: chainGenerate, // suppress "Run: defederator --dir" message
	}); err != nil {
		return err
	}
	if !chainGenerate {
		return nil
	}
	// Chain into generate using the just-written .defederator.yml. dir may be
	// relative; generateAt resolves it via LoadConfigFromDir's ancestor walk.
	return generateAt(ctx, "", dir, verbose)
}

func runHelp() error {
	fmt.Fprint(os.Stdout, `defederator — typed Go federation client generator

Usage:
  defederator [generate] [flags]      Generate a federation client (default)
  defederator migrate <dir> [flags]   Migrate a genqlient service to defederator
  defederator version                 Print binary version + git SHA
  defederator help                    Show this help

generate flags:
  -config, -c    path to .defederator.yml (default: search current and parent dirs)
  -dir           working directory for config-relative paths (default: .)
  -verbose, -v   print per-file and per-operation progress on stderr

migrate flags:
  --force        overwrite existing .defederator.yml and cross_service/client.go
  --dry-run      print what would be written without writing files
  --no-generate  skip the chained 'defederator generate' step
  --verbose, -v  print per-file and per-operation progress on stderr
`)
	return nil
}
