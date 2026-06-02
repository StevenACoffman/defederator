package generator_test

import (
	"reflect"
	"testing"

	"github.com/StevenACoffman/defederator/generator"
)

func TestDescriptionLines(t *testing.T) {
	cases := map[string]struct {
		in   string
		want []string
	}{
		"empty":            {"", nil},
		"all whitespace":   {"   \n\n  ", nil},
		"single line":      {"Hello, world.", []string{"Hello, world."}},
		"trims each line":  {"  one  \n  two  ", []string{"one", "two"}},
		"preserves blanks": {"para 1\n\npara 2", []string{"para 1", "", "para 2"}},
		"trailing newline": {"line\n", []string{"line", ""}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := generator.DescriptionLines(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DescriptionLines(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestGoConstName(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
	}{
		"single word upper":  {"UNAUTHORIZED", "Unauthorized"},
		"snake_case":         {"UNEXPECTED_ERROR", "UnexpectedError"},
		"multi underscore":   {"FOO_BAR_BAZ", "FooBarBaz"},
		"already pascal":     {"AlreadyPascal", "Alreadypascal"},
		"single letter":      {"A", "A"},
		"empty":              {"", ""},
		"only underscores":   {"___", "___"},
		"leading underscore": {"_FOO", "Foo"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := generator.GoConstName(tc.in)
			if got != tc.want {
				t.Fatalf("GoConstName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
