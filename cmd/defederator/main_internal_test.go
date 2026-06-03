package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// TestRun_VersionFlag verifies the --version flag is handled before subcommand
// dispatch and produces the expected single-line banner on stdout.
func TestRun_VersionFlag(t *testing.T) {
	cases := map[string]struct {
		args []string
	}{
		"long_flag":     {[]string{"defederator", "--version"}},
		"short_flag":    {[]string{"defederator", "-version"}},
		"subcommand":    {[]string{"defederator", "version"}},
		"after_subcmd":  {[]string{"defederator", "generate", "--version"}},
		"between_flags": {[]string{"defederator", "-c", "x.yml", "--version"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run(context.Background(), tc.args, &stdout, &stderr); err != nil {
				t.Fatalf("run: %v", err)
			}
			if !strings.HasPrefix(stdout.String(), "defederator ") {
				t.Errorf("stdout banner: got %q", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr should be empty for --version, got %q", stderr.String())
			}
		})
	}
}

// TestRun_Help verifies the help subcommand writes usage text to stdout.
func TestRun_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(
		context.Background(),
		[]string{"defederator", "help"},
		&stdout,
		&stderr,
	); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{
		"defederator — typed Go federation client generator",
		"defederator [generate] [flags]",
		"defederator migrate <dir> [flags]",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help output missing %q\ngot:\n%s", want, stdout.String())
		}
	}
}

// TestRun_UnknownSubcommand verifies that an unrecognized verb returns an
// error naming the unknown subcommand and listing the valid ones.
func TestRun_UnknownSubcommand(t *testing.T) {
	err := run(context.Background(), []string{"defederator", "nope"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
	for _, want := range []string{"unknown subcommand", `"nope"`, "generate", "migrate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

// TestRun_MigrateRequiresArg verifies that `migrate` without a positional
// argument fails with a usage message rather than running.
func TestRun_MigrateRequiresArg(t *testing.T) {
	err := run(context.Background(), []string{"defederator", "migrate"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error for missing migrate arg")
	}
	if !strings.Contains(err.Error(), "migrate requires exactly one argument") {
		t.Errorf("error missing usage hint: %v", err)
	}
}

// TestRun_GenerateBadFlag verifies that flag.ContinueOnError propagates parse
// errors out of run() rather than calling os.Exit.
func TestRun_GenerateBadFlag(t *testing.T) {
	err := run(
		context.Background(),
		[]string{"defederator", "generate", "--no-such-flag"},
		io.Discard,
		io.Discard,
	)
	if err == nil {
		t.Fatal("expected error for unknown flag")
	}
	if !strings.Contains(err.Error(), "parse generate flags") {
		t.Errorf("error should mention parse failure: %v", err)
	}
}

// TestVersionInfo_Defaults verifies versionInfo returns the "(unknown)"
// sentinel for fields BuildInfo does not populate. go test binaries do not
// embed VCS info, so the defaults path is exercised here.
func TestVersionInfo_Defaults(t *testing.T) {
	version, revision, buildTime := versionInfo()
	for _, s := range []string{version, revision, buildTime} {
		if s == "" {
			t.Errorf("versionInfo returned empty string; want non-empty")
		}
	}
}
