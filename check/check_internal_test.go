package check

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixtureGenqlientYAML is the minimal genqlient.yaml every fixture writes. The
// schema path is meaningless for the check package — Run only consults the
// `operations:` field — so we point at /dev/null to make that explicit.
const fixtureGenqlientYAML = `
schema: /dev/null
operations:
- cross_service/*.go
generated: generated/genqlient/queries.go
`

// fixtureFile pairs a service-relative path with the Go source written to it.
// Tests materialise the fixture by writing each file under a t.TempDir().
type fixtureFile struct {
	Path     string
	Contents string
}

func TestRun(t *testing.T) {
	cases := map[string]struct {
		Files       []fixtureFile
		WantOrphans []Orphan // empty (nil or zero-length) means clean
	}{
		"clean": {
			Files: []fixtureFile{
				{"cross_service/foo.go", `package cs

func F(ctx ctxT) error {
	_ = ` + "`" + `# @genqlient
		query Svc_Foo($id: String!) {
			foo(id: $id) { id }
		}
	` + "`" + `
	_, err := genqlient.Svc_Foo(ctx, nil, "x")
	return err
}
`},
			},
			WantOrphans: nil,
		},

		"orphaned_single": {
			Files: []fixtureFile{
				{"cross_service/foo.go", `package cs

func F(ctx ctxT) error {
	_, err := genqlient.Svc_Orphaned(ctx, nil, "x")
	return err
}
`},
			},
			WantOrphans: []Orphan{
				{Operation: "Svc_Orphaned", File: "cross_service/foo.go", Line: 4},
			},
		},

		"orphaned_multiple_sorted": {
			Files: []fixtureFile{
				{"cross_service/b.go", `package cs

func B(ctx ctxT) error {
	_, err := genqlient.Svc_B(ctx, nil)
	return err
}
`},
				{"cross_service/a.go", `package cs

func A(ctx ctxT) error {
	_, err := genqlient.Svc_A(ctx, nil)
	if err != nil { return err }
	_, err = genqlient.Svc_C(ctx, nil)
	return err
}
`},
			},
			// Expected order: a.go before b.go; within a.go, line 4 before line 6.
			WantOrphans: []Orphan{
				{Operation: "Svc_A", File: "cross_service/a.go", Line: 4},
				{Operation: "Svc_C", File: "cross_service/a.go", Line: 6},
				{Operation: "Svc_B", File: "cross_service/b.go", Line: 4},
			},
		},

		"cross_file_annotation": {
			// Annotation lives in one file; call site lives in another. genqlient
			// scans the whole operations glob — both files are scanned — so the
			// call is declared.
			Files: []fixtureFile{
				{"cross_service/decl.go", `package cs

var _ = ` + "`" + `# @genqlient
	query Svc_Shared { ok }
` + "`" + `
`},
				{"cross_service/use.go", `package cs

func F(ctx ctxT) error {
	_, err := genqlient.Svc_Shared(ctx, nil)
	return err
}
`},
			},
			WantOrphans: nil,
		},

		"comment_only_call_ignored": {
			// genqlient.X( appears only inside a // comment. AST walk visits
			// *ast.CallExpr nodes, not comment text, so the call is invisible to
			// the scanner — no orphan should be reported.
			Files: []fixtureFile{
				{"cross_service/foo.go", `package cs

// Example: genqlient.Svc_OnlyInComment(ctx, nil) — should not be flagged.
func F() {}
`},
			},
			WantOrphans: nil,
		},

		"type_conversion_not_flagged": {
			// genqlient.SomeTypeName(value) is a type conversion, not a function
			// call — but Go AST renders both as *ast.CallExpr. The underscore
			// heuristic in extractCall keeps these out of the orphan report.
			Files: []fixtureFile{
				{"cross_service/foo.go", `package cs

type DistrictUserTypeFilter string

func F() {
	var u DistrictUserTypeFilter
	_ = genqlient.DistrictUserTypeFilter(u)
}
`},
			},
			WantOrphans: nil,
		},

		"multiple_ops_in_one_annotation_block": {
			// A single @genqlient string literal can declare more than one
			// operation. annotationOpNameRE matches every query/mutation line.
			Files: []fixtureFile{
				{"cross_service/foo.go", `package cs

func F(ctx ctxT) error {
	_ = ` + "`" + `# @genqlient
		query Svc_Read { x }
		mutation Svc_Write { y }
	` + "`" + `
	_, _ = genqlient.Svc_Read(ctx, nil)
	_, _ = genqlient.Svc_Write(ctx, nil)
	return nil
}
`},
			},
			WantOrphans: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, tc.Files)
			got, err := Run(dir)
			if err != nil {
				t.Fatalf("Run(%q) returned unexpected error: %v", dir, err)
			}
			want := tc.WantOrphans
			if want == nil {
				want = []Orphan{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf(
					"Run(%q) orphans mismatch\n got: %s\nwant: %s",
					dir, formatOrphans(got), formatOrphans(want),
				)
			}
		})
	}
}

func TestFindOrphansPure(t *testing.T) {
	// Direct unit test of the pure helper. No file system involved.
	cases := map[string]struct {
		Calls    []call
		Declared map[string]struct{}
		Want     []call
	}{
		"empty": {
			Calls:    nil,
			Declared: map[string]struct{}{},
			Want:     []call{},
		},
		"all_declared": {
			Calls: []call{
				{Operation: "A", File: "a.go", Line: 1},
				{Operation: "B", File: "a.go", Line: 2},
			},
			Declared: map[string]struct{}{"A": {}, "B": {}},
			Want:     []call{},
		},
		"orphans_sorted_by_file_then_line_then_op": {
			Calls: []call{
				{Operation: "Y", File: "z.go", Line: 5},
				{Operation: "X", File: "a.go", Line: 9},
				{Operation: "W", File: "a.go", Line: 2},
				{Operation: "Z", File: "a.go", Line: 9}, // tied with X — Op breaks tie
			},
			Declared: map[string]struct{}{},
			Want: []call{
				{Operation: "W", File: "a.go", Line: 2},
				{Operation: "X", File: "a.go", Line: 9}, // X < Z alphabetically
				{Operation: "Z", File: "a.go", Line: 9},
				{Operation: "Y", File: "z.go", Line: 5},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := findOrphans(tc.Calls, tc.Declared)
			if !reflect.DeepEqual(got, tc.Want) {
				t.Errorf("findOrphans mismatch\n got: %+v\nwant: %+v", got, tc.Want)
			}
		})
	}
}

// writeFixture materialises one test case under base. Subdirectories are
// created as needed. Calls t.Fatalf on any IO error so the test fails fast.
func writeFixture(t *testing.T, base string, files []fixtureFile) {
	t.Helper()
	yamlPath := filepath.Join(base, "genqlient.yaml")
	if err := os.WriteFile(yamlPath, []byte(fixtureGenqlientYAML), 0o644); err != nil {
		t.Fatalf("write %s: %v", yamlPath, err)
	}
	for _, f := range files {
		path := filepath.Join(base, f.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(f.Contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// formatOrphans renders []Orphan as a multi-line block for failure messages.
// "(none)" is used for the empty case so the diff is obviously different from
// a non-empty list rendered with no rows.
func formatOrphans(orphans []Orphan) string {
	if len(orphans) == 0 {
		return "(none)"
	}
	lines := make([]string, len(orphans))
	for i, o := range orphans {
		lines[i] = fmt.Sprintf("  %s:%d %s", o.File, o.Line, o.Operation)
	}
	return "\n" + strings.Join(lines, "\n")
}
