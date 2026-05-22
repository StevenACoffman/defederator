// Command defederator generates typed Go federation clients from a supergraph SDL.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/StevenACoffman/defederator/config"
	"github.com/StevenACoffman/defederator/generator"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "defederator: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath string
		dir        string
	)
	flag.StringVar(&configPath, "config", "", "path to .defederator.yml (default: search current and parent dirs)")
	flag.StringVar(&configPath, "c", "", "path to .defederator.yml (shorthand)")
	flag.StringVar(&dir, "dir", ".", "working directory for config-relative path resolution")
	flag.Parse()

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

	fmt.Fprintf(os.Stdout, "defederator: wrote %s\n", cfg.Client.Filename)
	return nil
}
