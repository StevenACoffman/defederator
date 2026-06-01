// Command defederator generates typed Go federation clients from a supergraph SDL.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
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
	// Dispatch on the first argument when it is a word (not a flag).
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		switch os.Args[1] {
		case "migrate":
			return runMigrate(os.Args[2:])
		case "generate":
			return runGenerate(os.Args[2:])
		case "help":
			return runHelp()
		default:
			return fmt.Errorf(
				"unknown subcommand %q; valid subcommands: generate, migrate, help",
				os.Args[1],
			)
		}
	}
	// Backward-compatible: no subcommand → generate.
	return runGenerate(os.Args[1:])
}

func runGenerate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	var configPath, dir string
	fs.StringVar(
		&configPath,
		"config",
		"",
		"path to .defederator.yml (default: search current and parent dirs)",
	)
	fs.StringVar(&configPath, "c", "", "path to .defederator.yml (shorthand)")
	fs.StringVar(&dir, "dir", ".", "working directory for config-relative path resolution")
	if err := fs.Parse(args); err != nil {
		return err
	}

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

	ctx := context.Background()
	if err := generator.Generate(ctx, cfg); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "defederator: wrote %s\n", cfg.ClientFilename())
	return nil
}

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	var force, dryRun bool
	fs.BoolVar(
		&force,
		"force",
		false,
		"overwrite existing .defederator.yml and cross_service/client.go",
	)
	fs.BoolVar(&dryRun, "dry-run", false, "print what would be written without writing files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(
			"migrate requires exactly one argument: <service-dir>\nusage: defederator migrate [--force] [--dry-run] <service-dir>",
		)
	}
	return migrate.Run(context.Background(), fs.Arg(0), migrate.Options{
		Force:  force,
		DryRun: dryRun,
	})
}

func runHelp() error {
	fmt.Fprint(os.Stdout, `defederator — typed Go federation client generator

Usage:
  defederator [generate] [flags]      Generate a federation client (default)
  defederator migrate <dir> [flags]   Migrate a genqlient service to defederator
  defederator help                    Show this help

generate flags:
  -config, -c  path to .defederator.yml (default: search current and parent dirs)
  -dir         working directory for config-relative paths (default: .)

migrate flags:
  --force      overwrite existing .defederator.yml and cross_service/client.go
  --dry-run    print what would be written without writing files
`)
	return nil
}
