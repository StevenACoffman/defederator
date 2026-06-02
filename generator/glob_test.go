package generator

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestExpandGlobs_ExactPath(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "query.graphql")
	if err := os.WriteFile(f, []byte("query {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := expandGlobs([]string{f}, tmp, io.Discard)
	if err != nil {
		t.Fatalf("expandGlobs: %v", err)
	}
	if len(got) != 1 || got[0] != f {
		t.Errorf("want [%s], got %v", f, got)
	}
}

func TestExpandGlobs_SingleStar(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"a.graphql", "b.graphql", "c.go"} {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := expandGlobs([]string{filepath.Join(tmp, "*.graphql")}, tmp, io.Discard)
	if err != nil {
		t.Fatalf("expandGlobs: %v", err)
	}
	sort.Strings(got)
	want := []string{
		filepath.Join(tmp, "a.graphql"),
		filepath.Join(tmp, "b.graphql"),
	}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] want %s, got %s", i, want[i], got[i])
		}
	}
}

func TestExpandGlobs_DoubleStar(t *testing.T) {
	tmp := t.TempDir()
	sub := filepath.Join(tmp, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join(tmp, "a.graphql"), filepath.Join(sub, "b.graphql")} {
		if err := os.WriteFile(name, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := expandGlobs([]string{filepath.Join(tmp, "**/*.graphql")}, tmp, io.Discard)
	if err != nil {
		t.Fatalf("expandGlobs: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 files, got %d: %v", len(got), got)
	}
}

func TestExpandGlobs_EmptyGlobErrors(t *testing.T) {
	tmp := t.TempDir()
	_, err := expandGlobs([]string{filepath.Join(tmp, "*.graphql")}, tmp, io.Discard)
	if err == nil {
		t.Fatal("want error for glob matching no files, got nil")
	}
}

func TestExpandGlobs_Dedup(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "q.graphql")
	if err := os.WriteFile(f, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := expandGlobs([]string{f, f}, tmp, io.Discard)
	if err != nil {
		t.Fatalf("expandGlobs: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("want 1 (deduped), got %d: %v", len(got), got)
	}
}
