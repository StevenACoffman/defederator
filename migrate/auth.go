package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// detectionPatterns hold the regex shapes used by DetectAuthFlavors. Compiled
// once at package init so call sites pay no per-file regex cost.
//
// The patterns are deliberately narrow — they match the exact call chains
// webapp services use today. Adding a new pattern (e.g. `AsServiceJob()`)
// requires extending both this var and AuthFlavors.
var (
	authLocaleUserRe = regexp.MustCompile(`WithKALocale\([^)]*\)\.AsUser\(\)`)
	authUserRe       = regexp.MustCompile(`\.AsUser\(\)`)
	authAdminRe      = regexp.MustCompile(`\.AsServiceAdmin\(\)`)
)

// AuthFlavors records which gqlclient auth modes a service's cross-service code
// invokes. Each field is true when at least one call site in the operation Go
// files uses the corresponding pattern:
//
//   - User:       ctx.GraphQL().AsUser()
//   - Admin:      ctx.GraphQL().AsServiceAdmin()
//   - LocaleUser: ctx.GraphQL().WithKALocale(...).AsUser()
//
// The set drives which federation-client constructors migrate emits in
// cross_service/client.go.
type AuthFlavors struct {
	User       bool
	Admin      bool
	LocaleUser bool
}

// Any reports whether any flavor was detected. Used by the template to decide
// between the single-constructor default and the multi-factory shape.
func (a AuthFlavors) Any() bool { return a.User || a.Admin || a.LocaleUser }

// Multi reports whether more than one flavor is present, which is the signal
// that the multi-factory pattern is required (separate User/Admin/LocaleUser
// constructors plus a shared newJobFederationClient).
func (a AuthFlavors) Multi() bool {
	n := 0
	for _, b := range []bool{a.User, a.Admin, a.LocaleUser} {
		if b {
			n++
		}
	}
	return n > 1
}

// DetectAuthFlavors scans the contents of the given Go source files for
// gqlclient auth-mode call chains and returns the union of flavors found.
//
// Missing files are skipped silently. Read errors return immediately with the
// error; caller decides whether to proceed with a partial result.
//
// Detection is regex-based on raw source text rather than AST: every flavor we
// care about has a unique, unambiguous lexical form (an `.AsXxx()` method call
// at the end of a chain). False positives in comments are unlikely in practice
// because webapp cross-service files don't quote these call chains.
func DetectAuthFlavors(goFiles []string) (AuthFlavors, error) {
	var a AuthFlavors
	for _, p := range goFiles {
		src, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return a, fmt.Errorf("read %s: %w", p, err)
		}
		// LocaleUser must be checked first because its match also contains
		// .AsUser(); without ordering, every locale call would also set User.
		// We strip the locale matches before checking for the bare user form.
		stripped := authLocaleUserRe.ReplaceAllString(string(src), "")
		if authLocaleUserRe.MatchString(string(src)) {
			a.LocaleUser = true
		}
		if authUserRe.MatchString(stripped) {
			a.User = true
		}
		if authAdminRe.MatchString(stripped) {
			a.Admin = true
		}
	}
	return a, nil
}

// AuthFlavorsFromOperationDir scans every .go file in the given directory
// (non-recursive) and returns the union of auth flavors detected. This is the
// convenience entrypoint migrate.Run uses: the migrate command knows the
// cross_service dir, hands it here, and gets back the flavor set.
func AuthFlavorsFromOperationDir(dir string) (AuthFlavors, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return AuthFlavors{}, nil
		}
		return AuthFlavors{}, fmt.Errorf("read dir %s: %w", dir, err)
	}
	var goFiles []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
			continue
		}
		goFiles = append(goFiles, filepath.Join(dir, e.Name()))
	}
	return DetectAuthFlavors(goFiles)
}
