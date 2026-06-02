package migrate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/StevenACoffman/defederator/migrate"
)

func TestDetectAuthFlavors(t *testing.T) {
	cases := map[string]struct {
		source string
		want   migrate.AuthFlavors
	}{
		"user only": {
			source: `package x
func F(ctx C) { genqlient.Foo(ctx, ctx.GraphQL().AsUser()) }`,
			want: migrate.AuthFlavors{User: true},
		},
		"admin only": {
			source: `package x
func F(ctx C) { genqlient.Foo(ctx, ctx.GraphQL().AsServiceAdmin()) }`,
			want: migrate.AuthFlavors{Admin: true},
		},
		"locale user only": {
			// Bare .AsUser() must NOT be flagged when it's part of a
			// WithKALocale(...).AsUser() chain.
			source: `package x
func F(ctx C, l string) { genqlient.Foo(ctx, ctx.GraphQL().WithKALocale(l).AsUser()) }`,
			want: migrate.AuthFlavors{LocaleUser: true},
		},
		"user and admin": {
			source: `package x
func F(ctx C) {
    genqlient.A(ctx, ctx.GraphQL().AsUser())
    genqlient.B(ctx, ctx.GraphQL().AsServiceAdmin())
}`,
			want: migrate.AuthFlavors{User: true, Admin: true},
		},
		"all three": {
			source: `package x
func F(ctx C, l string) {
    genqlient.A(ctx, ctx.GraphQL().AsUser())
    genqlient.B(ctx, ctx.GraphQL().AsServiceAdmin())
    genqlient.C(ctx, ctx.GraphQL().WithKALocale(l).AsUser())
}`,
			want: migrate.AuthFlavors{User: true, Admin: true, LocaleUser: true},
		},
		"none": {
			source: `package x
func F() { _ = 1 }`,
			want: migrate.AuthFlavors{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "f.go")
			if err := os.WriteFile(p, []byte(tc.source), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := migrate.DetectAuthFlavors([]string{p})
			if err != nil {
				t.Fatalf("DetectAuthFlavors: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAuthFlavors_AnyMulti(t *testing.T) {
	cases := map[string]struct {
		flavors  migrate.AuthFlavors
		wantAny  bool
		wantMult bool
	}{
		"none":   {migrate.AuthFlavors{}, false, false},
		"user":   {migrate.AuthFlavors{User: true}, true, false},
		"two":    {migrate.AuthFlavors{User: true, Admin: true}, true, true},
		"three":  {migrate.AuthFlavors{User: true, Admin: true, LocaleUser: true}, true, true},
		"locale": {migrate.AuthFlavors{LocaleUser: true}, true, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.flavors.Any(); got != tc.wantAny {
				t.Errorf("Any() = %v, want %v", got, tc.wantAny)
			}
			if got := tc.flavors.Multi(); got != tc.wantMult {
				t.Errorf("Multi() = %v, want %v", got, tc.wantMult)
			}
		})
	}
}

func TestAuthFlavorsFromOperationDir_NoDir(t *testing.T) {
	// Missing dir should not error.
	got, err := migrate.AuthFlavorsFromOperationDir(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Any() {
		t.Fatalf("expected empty flavors, got %+v", got)
	}
}
