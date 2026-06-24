// Command defederator generates typed Go federation clients from a supergraph SDL.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/StevenACoffman/defederator/check"
	"github.com/StevenACoffman/defederator/config"
	"github.com/StevenACoffman/defederator/generator"
	"github.com/StevenACoffman/defederator/migrate"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt, syscall.SIGQUIT, syscall.SIGTERM,
	)
	err := run(ctx, os.Args, os.Stdout, os.Stderr)
	// Release the signal-handler goroutine before any os.Exit so it doesn't
	// outlive the process. Subsequent calls to a CancelFunc are no-ops.
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "defederator: %v\n", err)
		os.Exit(1)
	}
}

// run is intentionally separated from main so tests can inject args and
// capture stdout/stderr without setting os.Args or replacing real streams.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	// --version is intentionally handled before subcommand dispatch so it
	// works as both `defederator --version` and `defederator -version`.
	for _, a := range args[1:] {
		if a == "--version" || a == "-version" {
			printVersion(stdout)
			return nil
		}
	}
	// Dispatch on the first argument when it is a word (not a flag).
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		switch args[1] {
		case "migrate":
			return runMigrate(ctx, args[2:], stdout, stderr)
		case "generate":
			return runGenerate(ctx, args[2:], stdout, stderr)
		case "check":
			return runCheck(args[2:], stdout, stderr)
		case "version":
			printVersion(stdout)
			return nil
		case "help":
			return runHelp(stdout)
		default:
			return fmt.Errorf(
				"unknown subcommand %q; valid subcommands: generate, migrate, check, version, help",
				args[1],
			)
		}
	}
	// Backward-compatible: no subcommand → generate.
	return runGenerate(ctx, args[1:], stdout, stderr)
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
func printVersion(w io.Writer) {
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

func runGenerate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
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
		return fmt.Errorf("defederator: parse generate flags: %w", err)
	}
	return generateAt(ctx, configPath, dir, verbose, stdout, stderr)
}

// generateAt loads a defederator config and runs code generation against it.
// Exactly one of configPath or dir is meaningful per call: if configPath is
// non-empty it is loaded directly; otherwise .defederator.yml is searched
// starting from dir. The "defederator: wrote <path>" success line is printed
// on stdout so both the generate subcommand and the migrate-chains-generate
// path produce the same user-facing output. verbose enables per-file progress
// diagnostics on stderr; stderr is reserved for the verbose log inside
// generator.Generate (it reads os.Stderr indirectly through io.Discard
// fallback, not through this writer).
func generateAt(
	ctx context.Context,
	configPath, dir string,
	verbose bool,
	stdout, _ io.Writer,
) error {
	var cfg *config.Config
	var err error
	if configPath != "" {
		cfg, err = config.LoadConfig(configPath)
	} else {
		cfg, err = config.LoadConfigFromDir(dir)
	}
	if err != nil {
		return fmt.Errorf("defederator: load config: %w", err)
	}
	cfg.Verbose = verbose
	if err := generator.Generate(ctx, cfg); err != nil {
		return fmt.Errorf("defederator: generate: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "defederator: wrote %s\n", cfg.ClientFilename())
	return nil
}

func runMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var force, dryRun, noGenerate, rewrite, verbose bool
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
	fs.BoolVar(
		&rewrite,
		"rewrite",
		false,
		"also rewrite cross_service/*.go call sites to the New<Flavor>GraphQLClient constructors",
	)
	fs.BoolVar(&verbose, "verbose", false, "print per-file and per-operation progress on stderr")
	fs.BoolVar(&verbose, "v", false, "shorthand for --verbose")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("defederator: parse migrate flags: %w", err)
	}
	if fs.NArg() != 1 {
		return errors.New(
			"migrate requires exactly one argument: <service-dir>\nusage: defederator migrate [--force] [--dry-run] [--no-generate] [--rewrite] [-v] <service-dir>",
		)
	}
	dir := fs.Arg(0)
	chainGenerate := !dryRun && !noGenerate
	if err := migrate.Run(ctx, dir, migrate.Options{
		Force:            force,
		DryRun:           dryRun,
		RewriteCallSites: rewrite,
		SkipNextSteps:    chainGenerate, // suppress "Run: defederator --dir" message
	}); err != nil {
		return fmt.Errorf("defederator: migrate: %w", err)
	}
	if !chainGenerate {
		return nil
	}
	// Chain into generate using the just-written .defederator.yml. dir may be
	// relative; generateAt resolves it via LoadConfigFromDir's ancestor walk.
	if err := generateAt(ctx, "", dir, verbose, stdout, stderr); err != nil {
		return err
	}
	// printNextSteps was suppressed because we chained into generate. Surface
	// just the lint-fix hint, which is the one post-migration step the user
	// still needs to run themselves.
	_, _ = fmt.Fprintf(stdout, "migrate: %s\n", migrate.LintFixHint())
	return nil
}

// runCheck dispatches the `check` subcommand: scans a service directory for
// `genqlient.<Op>(...)` calls lacking a backing `# @genqlient` annotation
// block. Orphans are written to stdout; the subcommand exits non-zero when
// any are found so CI can fail on the regression class. ctx is unused — the
// scan is local, fast, and cancellation-insensitive.
func runCheck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("defederator: parse check flags: %w", err)
	}
	if fs.NArg() != 1 {
		return errors.New(
			"check requires exactly one argument: <service-dir>\nusage: defederator check <service-dir>",
		)
	}
	dir := fs.Arg(0)
	orphans, err := check.Run(dir)
	if err != nil {
		return fmt.Errorf("defederator: check: %w", err)
	}
	for _, o := range orphans {
		_, _ = fmt.Fprintf(
			stdout, "%s:%d: genqlient.%s (no @genqlient annotation declaring it)\n",
			o.File, o.Line, o.Operation,
		)
	}
	if len(orphans) > 0 {
		return fmt.Errorf("defederator: check: %d orphaned genqlient calls", len(orphans))
	}
	return nil
}

func runHelp(stdout io.Writer) error {
	fmt.Fprint(stdout, `defederator — typed Go federation client generator

Usage:
  defederator [generate] [flags]      Generate a federation client (default)
  defederator migrate <dir> [flags]   Migrate a genqlient service to defederator
  defederator check <dir>             Report orphaned genqlient calls in <dir>
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
  --rewrite      rewrite cross_service/*.go call sites to the New<Flavor>GraphQLClient constructors
  --verbose, -v  print per-file and per-operation progress on stderr

check exits non-zero when one or more orphaned genqlient calls are found
(calls to genqlient.<Op>(...) with no backing # @genqlient annotation block).
`)
	return nil
}
