package generator

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// expandGlobs expands each pattern relative to baseDir using doublestar semantics.
// Exact paths (no glob metacharacters) are included as-is without a filesystem
// check. Glob patterns must match at least one file or an error is returned.
// Duplicate paths across patterns are collapsed to one entry.
//
// Per-pattern and per-match progress lines are written to log. Pass io.Discard
// for silence.
func expandGlobs(patterns []string, baseDir string, log io.Writer) ([]string, error) {
	seen := make(map[string]struct{})
	var result []string
	for _, pattern := range patterns {
		_, _ = fmt.Fprintf(log, "Expanding pattern: %s (baseDir: %s)\n", pattern, baseDir)
		paths, err := expandOnePattern(pattern, baseDir, log)
		if err != nil {
			return nil, err
		}
		for _, abs := range paths {
			if _, exists := seen[abs]; exists {
				continue
			}
			seen[abs] = struct{}{}
			result = append(result, abs)
		}
	}
	return result, nil
}

// expandOnePattern resolves a single pattern against baseDir and returns the
// matching paths. Patterns without glob metacharacters are returned as-is
// (without a filesystem check); glob patterns must match at least one file.
func expandOnePattern(pattern, baseDir string, log io.Writer) ([]string, error) {
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(baseDir, pattern)
	}
	pattern = filepath.ToSlash(pattern)
	if !strings.ContainsAny(pattern, "*?[{") {
		return []string{filepath.FromSlash(pattern)}, nil
	}
	base, pat := doublestar.SplitPattern(pattern)
	matches, err := doublestar.Glob(os.DirFS(base), pat, doublestar.WithFilesOnly())
	if err != nil {
		return nil, fmt.Errorf("glob %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("glob %q did not match any files", filepath.FromSlash(pattern))
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		abs := filepath.Join(base, m)
		_, _ = fmt.Fprintf(log, "Matched: %s\n", abs)
		out = append(out, abs)
	}
	return out, nil
}
