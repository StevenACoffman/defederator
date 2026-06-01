package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// expandGlobs expands each pattern relative to baseDir using doublestar semantics.
// Exact paths (no glob metacharacters) are included as-is without a filesystem
// check. Glob patterns must match at least one file or an error is returned.
// Duplicate paths across patterns are collapsed to one entry.
func expandGlobs(patterns []string, baseDir string) ([]string, error) {
	seen := make(map[string]struct{})
	var result []string

	for _, pattern := range patterns {
		fmt.Printf("Expanding pattern: %s (baseDir: %s)\n", pattern, baseDir)
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(baseDir, pattern)
		}
		pattern = filepath.ToSlash(pattern)

		// No glob metacharacters — treat as a literal path.
		if !strings.ContainsAny(pattern, "*?[{") {
			abs := filepath.FromSlash(pattern)
			if _, exists := seen[abs]; !exists {
				seen[abs] = struct{}{}
				result = append(result, abs)
			}
			continue
		}

		base, pat := doublestar.SplitPattern(pattern)
		matches, err := doublestar.Glob(os.DirFS(base), pat, doublestar.WithFilesOnly())
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("glob %q did not match any files", filepath.FromSlash(pattern))
		}
		for _, m := range matches {
			abs := filepath.Join(base, m)
			fmt.Printf("Matched: %s\n", abs)
			if _, exists := seen[abs]; !exists {
				seen[abs] = struct{}{}
				result = append(result, abs)
			}
		}
	}
	return result, nil
}
