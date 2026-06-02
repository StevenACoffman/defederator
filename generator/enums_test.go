package generator_test

import (
	"reflect"
	"testing"

	gqlgenConfig "github.com/99designs/gqlgen/codegen/config"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/StevenACoffman/defederator/generator"
)

func mustLoadSchema(t *testing.T, sdl string) *ast.Schema {
	t.Helper()
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "test", Input: sdl})
	if err != nil {
		t.Fatalf("load schema: %v", err)
	}
	return schema
}

func TestCollectEnums(t *testing.T) {
	cases := map[string]struct {
		sdl  string
		want []*generator.EnumDef
	}{
		"no enums": {
			sdl:  `type Query { hello: String }`,
			want: nil,
		},
		"single enum": {
			sdl: `
				enum Color { RED GREEN BLUE }
				type Query { color: Color }
			`,
			want: []*generator.EnumDef{{
				GoName:      "Color",
				GraphQLName: "Color",
				Values: []generator.EnumValueDef{
					{GoName: "ColorBlue", GraphQLName: "BLUE"},
					{GoName: "ColorGreen", GraphQLName: "GREEN"},
					{GoName: "ColorRed", GraphQLName: "RED"},
				},
			}},
		},
		"sorted across enums and values": {
			sdl: `
				enum Zebra { Z A }
				enum Apple { B A }
				type Query { z: Zebra, a: Apple }
			`,
			want: []*generator.EnumDef{
				{
					GoName: "Apple", GraphQLName: "Apple",
					Values: []generator.EnumValueDef{
						{GoName: "AppleA", GraphQLName: "A"},
						{GoName: "AppleB", GraphQLName: "B"},
					},
				},
				{
					GoName: "Zebra", GraphQLName: "Zebra",
					Values: []generator.EnumValueDef{
						{GoName: "ZebraA", GraphQLName: "A"},
						{GoName: "ZebraZ", GraphQLName: "Z"},
					},
				},
			},
		},
		"snake_case values": {
			sdl: `
				enum Code { UNAUTHORIZED UNEXPECTED_ERROR }
				type Query { c: Code }
			`,
			want: []*generator.EnumDef{{
				GoName: "Code", GraphQLName: "Code",
				Values: []generator.EnumValueDef{
					{GoName: "CodeUnauthorized", GraphQLName: "UNAUTHORIZED"},
					{GoName: "CodeUnexpectedError", GraphQLName: "UNEXPECTED_ERROR"},
				},
			}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := generator.CollectEnums(mustLoadSchema(t, tc.sdl))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("CollectEnums mismatch\ngot:  %#v\nwant: %#v", got, tc.want)
			}
		})
	}
}

func TestCollectEnumsSkipsIntrospection(t *testing.T) {
	schema := mustLoadSchema(t, `enum Color { RED } type Query { c: Color }`)
	got := generator.CollectEnums(schema)
	for _, e := range got {
		if len(e.GraphQLName) >= 2 && e.GraphQLName[:2] == "__" {
			t.Fatalf("introspection enum leaked: %s", e.GraphQLName)
		}
	}
	if len(got) != 1 || got[0].GraphQLName != "Color" {
		t.Fatalf("expected just Color, got %v", got)
	}
}

func TestCollectEnumsNilSchema(t *testing.T) {
	if got := generator.CollectEnums(nil); got != nil {
		t.Fatalf("CollectEnums(nil) = %v, want nil", got)
	}
}

func TestBasicTypeModels(t *testing.T) {
	sdl := `
		enum Color { RED }
		input Filter { name: String }
		type Query { c: Color, f(input: Filter!): String }
	`
	schema := mustLoadSchema(t, sdl)
	got := generator.BasicTypeModels(schema, "example.com/pkg/genclient")

	want := gqlgenConfig.TypeMap{
		"Color": {
			Model: gqlgenConfig.StringList{"example.com/pkg/genclient.Color"},
		},
		"Filter": {
			Model: gqlgenConfig.StringList{"github.com/99designs/gqlgen/graphql.String"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BasicTypeModels mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestBasicTypeModelsNilSchema(t *testing.T) {
	got := generator.BasicTypeModels(nil, "example.com/x")
	if len(got) != 0 {
		t.Fatalf("BasicTypeModels(nil) returned %d entries, want 0", len(got))
	}
}
