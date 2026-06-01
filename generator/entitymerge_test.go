package generator

import (
	"go/token"
	"go/types"
	"strings"
	"testing"
)

var (
	strType = types.Typ[types.String]
	intType = types.Typ[types.Int]
)

// makeField builds a types.Var with the given name, type, and json struct tag.
func makeField(name string, typ types.Type, jsonKey string) (*types.Var, string) {
	v := types.NewField(token.NoPos, nil, name, typ, false)
	return v, `json:"` + jsonKey + `"`
}

// makeStruct builds a *types.Struct from (name, json-key, type) triples.
func makeStruct(fields []struct {
	name    string
	jsonKey string
	typ     types.Type
},
) *types.Struct {
	vars := make([]*types.Var, len(fields))
	tags := make([]string, len(fields))
	for i, f := range fields {
		vars[i], tags[i] = makeField(f.name, f.typ, f.jsonKey)
	}
	return types.NewStruct(vars, tags)
}

// namedType wraps a *types.Struct in a *types.Named with the given name.
func namedType(name string, underlying *types.Struct) *types.Named {
	obj := types.NewTypeName(token.NoPos, nil, name, nil)
	return types.NewNamed(obj, underlying, nil)
}

// ── parseJSONKey ─────────────────────────────────────────────────────────────

func TestParseJSONKey(t *testing.T) {
	cases := map[string]struct {
		tag  string
		want string
	}{
		"simple":          {`json:"name"`, "name"},
		"omitempty":       {`json:"name,omitempty"`, "name"},
		"multi_tag":       {`json:"name,omitempty" graphql:"name"`, "name"},
		"no_json_tag":     {`graphql:"name"`, ""},
		"empty":           {"", ""},
		"dash":            {`json:"-"`, "-"},
		"underscore_name": {`json:"my_field"`, "my_field"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseJSONKey(tc.tag)
			if got != tc.want {
				t.Errorf("parseJSONKey(%q): want %q, got %q", tc.tag, tc.want, got)
			}
		})
	}
}

// ── fieldByJSONKey ────────────────────────────────────────────────────────────

func TestFieldByJSONKey(t *testing.T) {
	st := makeStruct([]struct {
		name    string
		jsonKey string
		typ     types.Type
	}{
		{"Name", "name", strType},
		{"Age", "age", intType},
		{"Email", "email,omitempty", strType},
	})

	cases := map[string]struct {
		key      string
		wantName string
		wantOK   bool
	}{
		"found_name":    {"name", "Name", true},
		"found_age":     {"age", "Age", true},
		"found_email":   {"email", "Email", true}, // tag has omitempty; parseJSONKey strips it
		"not_found":     {"missing", "", false},
		"partial_match": {"na", "", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotName, _, gotOK := fieldByJSONKey(st, tc.key)
			if gotOK != tc.wantOK {
				t.Errorf("fieldByJSONKey ok: want %v, got %v", tc.wantOK, gotOK)
			}
			if gotOK && gotName != tc.wantName {
				t.Errorf("fieldByJSONKey name: want %q, got %q", tc.wantName, gotName)
			}
		})
	}
}

// ── walkTypeForChain ──────────────────────────────────────────────────────────

func TestWalkTypeForChain(t *testing.T) {
	// Build a small type tree:
	//   Root struct { Product *ProductType `json:"product"` }
	//   ProductType struct { Delivery DeliveryType `json:"delivery"` }
	//   DeliveryType struct { EstimatedDelivery string `json:"estimatedDelivery"` }
	//   WithList struct { Items []ItemType `json:"items"` }
	//   ItemType struct { ID string `json:"id"` }

	deliverySt := makeStruct([]struct {
		name    string
		jsonKey string
		typ     types.Type
	}{
		{"EstimatedDelivery", "estimatedDelivery", strType},
	})
	deliveryNamed := namedType("DeliveryType", deliverySt)

	productSt := makeStruct([]struct {
		name    string
		jsonKey string
		typ     types.Type
	}{
		{"Delivery", "delivery", deliveryNamed},
	})
	productNamed := namedType("ProductType", productSt)

	rootSt := makeStruct([]struct {
		name    string
		jsonKey string
		typ     types.Type
	}{
		{"Product", "product", types.NewPointer(productNamed)},
	})

	itemSt := makeStruct([]struct {
		name    string
		jsonKey string
		typ     types.Type
	}{
		{"ID", "id", strType},
	})
	itemNamed := namedType("ItemType", itemSt)
	listRootSt := makeStruct([]struct {
		name    string
		jsonKey string
		typ     types.Type
	}{
		{"Items", "items", types.NewSlice(itemNamed)},
	})

	cases := map[string]struct {
		root      types.Type
		path      []string
		wantSteps []chainStep
		wantErr   bool
	}{
		"single_pointer_step": {
			root: rootSt,
			path: []string{"product"},
			wantSteps: []chainStep{
				{GoName: "Product", IsPtr: true},
			},
		},
		"two_level_named_types": {
			root: rootSt,
			path: []string{"product", "delivery"},
			wantSteps: []chainStep{
				{GoName: "Product", IsPtr: true},
				{GoName: "Delivery"},
			},
		},
		"slice_field": {
			root: listRootSt,
			path: []string{"items"},
			wantSteps: []chainStep{
				{GoName: "Items", IsSlice: true},
			},
		},
		"named_root": {
			root: namedType("Root", rootSt),
			path: []string{"product"},
			wantSteps: []chainStep{
				{GoName: "Product", IsPtr: true},
			},
		},
		"unknown_field": {
			root:    rootSt,
			path:    []string{"missing"},
			wantErr: true,
		},
		"non_struct_mid_path": {
			root:    rootSt,
			path:    []string{"product", "delivery", "estimatedDelivery", "deeper"},
			wantErr: true,
		},
		"empty_path": {
			root:      rootSt,
			path:      []string{},
			wantSteps: []chainStep{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := walkTypeForChain(tc.root, tc.path)
			if tc.wantErr {
				if err == nil {
					t.Error("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.wantSteps) {
				t.Fatalf(
					"steps len: want %d, got %d\nwant: %v\n got: %v",
					len(tc.wantSteps),
					len(got),
					tc.wantSteps,
					got,
				)
			}
			for i, ws := range tc.wantSteps {
				gs := got[i]
				if gs.GoName != ws.GoName || gs.IsPtr != ws.IsPtr || gs.IsSlice != ws.IsSlice ||
					gs.SliceElemPtr != ws.SliceElemPtr {
					t.Errorf("step[%d]: want %+v, got %+v", i, ws, gs)
				}
			}
		})
	}
}

// ── genNilGuard ───────────────────────────────────────────────────────────────

func TestGenNilGuard(t *testing.T) {
	cases := map[string]struct {
		steps []chainStep
		want  string
	}{
		"empty":        {nil, ""},
		"non_ptr_step": {[]chainStep{{GoName: "A"}}, ""},
		"single_ptr":   {[]chainStep{{GoName: "A", IsPtr: true}}, "res.A != nil"},
		"two_ptrs": {
			[]chainStep{{GoName: "A", IsPtr: true}, {GoName: "B", IsPtr: true}},
			"res.A != nil && res.A.B != nil",
		},
		"ptr_then_slice": {
			[]chainStep{{GoName: "A", IsPtr: true}, {GoName: "B", IsSlice: true}},
			"res.A != nil",
		},
		"slice_first": {[]chainStep{{GoName: "A", IsSlice: true}}, ""},
		"ptr_non_ptr_ptr": {
			[]chainStep{{GoName: "A", IsPtr: true}, {GoName: "B"}, {GoName: "C", IsPtr: true}},
			"res.A != nil && res.A.B.C != nil",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := genNilGuard(tc.steps)
			if got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// ── genAccessChain ────────────────────────────────────────────────────────────

func TestGenAccessChain(t *testing.T) {
	cases := map[string]struct {
		steps []chainStep
		want  string
	}{
		"empty":     {nil, "res"},
		"one_step":  {[]chainStep{{GoName: "A"}}, "res.A"},
		"two_steps": {[]chainStep{{GoName: "A"}, {GoName: "B"}}, "res.A.B"},
		"three_steps": {
			[]chainStep{{GoName: "A", IsPtr: true}, {GoName: "B"}, {GoName: "C", IsSlice: true}},
			"res.A.B.C",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := genAccessChain(tc.steps)
			if got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// ── leafKeyAccessors ──────────────────────────────────────────────────────────

func TestLeafKeyAccessors(t *testing.T) {
	// Type tree:
	//   Root struct {
	//       Product *ProductType `json:"product"`
	//   }
	//   ProductType struct {
	//       ID   *string `json:"id"`    — pointer (nullable)
	//       Name string  `json:"name"`  — non-pointer (non-null)
	//   }
	//   WithList struct {
	//       Items []ItemType `json:"items"`
	//   }
	//   ItemType struct { Key string `json:"key"` }
	//   WithPtrElems struct {
	//       Items []*ItemType `json:"items"`
	//   }

	productSt := makeStruct([]struct {
		name    string
		jsonKey string
		typ     types.Type
	}{
		{"ID", "id", types.NewPointer(strType)},
		{"Name", "name", strType},
	})
	productNamed := namedType("ProductType", productSt)

	rootSt := makeStruct([]struct {
		name    string
		jsonKey string
		typ     types.Type
	}{
		{"Product", "product", types.NewPointer(productNamed)},
	})

	itemSt := makeStruct([]struct {
		name    string
		jsonKey string
		typ     types.Type
	}{
		{"Key", "key", strType},
	})
	itemNamed := namedType("ItemType", itemSt)
	withListSt := makeStruct([]struct {
		name    string
		jsonKey string
		typ     types.Type
	}{
		{"Items", "items", types.NewSlice(itemNamed)},
	})
	withPtrElemsSt := makeStruct([]struct {
		name    string
		jsonKey string
		typ     types.Type
	}{
		{"Items", "items", types.NewSlice(types.NewPointer(itemNamed))},
	})

	cases := map[string]struct {
		root    types.Type
		path    []string
		fields  []string
		want    []keyFieldInfo
		wantErr bool
	}{
		"pointer_field": {
			root:   rootSt,
			path:   []string{"product"},
			fields: []string{"id"},
			want:   []keyFieldInfo{{"ID", true}},
		},
		"non_pointer_field": {
			root:   rootSt,
			path:   []string{"product"},
			fields: []string{"name"},
			want:   []keyFieldInfo{{"Name", false}},
		},
		"multiple_fields": {
			root:   rootSt,
			path:   []string{"product"},
			fields: []string{"id", "name"},
			want:   []keyFieldInfo{{"ID", true}, {"Name", false}},
		},
		"list_path_segment": {
			root:   withListSt,
			path:   []string{"items"},
			fields: []string{"key"},
			want:   []keyFieldInfo{{"Key", false}},
		},
		"ptr_elem_list_path_segment": {
			root:   withPtrElemsSt,
			path:   []string{"items"},
			fields: []string{"key"},
			want:   []keyFieldInfo{{"Key", false}},
		},
		"field_absent_in_leaf": {
			root:    rootSt,
			path:    []string{"product"},
			fields:  []string{"missing"},
			wantErr: true,
		},
		"path_segment_absent": {
			root:    rootSt,
			path:    []string{"missing"},
			fields:  []string{"id"},
			wantErr: true,
		},
		"leaf_not_struct": {
			root:    strType,
			path:    []string{},
			fields:  []string{"id"},
			wantErr: true,
		},
		"named_root_traversed": {
			root:   namedType("Root", rootSt),
			path:   []string{"product"},
			fields: []string{"name"},
			want:   []keyFieldInfo{{"Name", false}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := leafKeyAccessors(tc.root, tc.path, tc.fields)
			if tc.wantErr {
				if err == nil {
					t.Error("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len: want %d, got %d", len(tc.want), len(got))
			}
			for i, w := range tc.want {
				if got[i] != w {
					t.Errorf("[%d]: want %+v, got %+v", i, w, got[i])
				}
			}
		})
	}
}

// ── isFieldFromPriorEntityFetch ───────────────────────────────────────────────

func TestIsFieldFromPriorEntityFetch(t *testing.T) {
	prior := []urlSpecEntityFetch{
		{Selection: "id\nname\nemail"},
		{Selection: "{\ntotalProductsCreated\n}"},
	}

	cases := map[string]struct {
		fieldName string
		prior     []urlSpecEntityFetch
		want      bool
	}{
		"found_in_first":       {"id", prior, true},
		"found_name":           {"name", prior, true},
		"found_in_second_body": {"totalProductsCreated", prior, true},
		"not_found":            {"missing", prior, false},
		"empty_prior":          {"id", nil, false},
		"brace_line_skipped":   {"{", prior, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := isFieldFromPriorEntityFetch(tc.fieldName, tc.prior)
			if got != tc.want {
				t.Errorf("want %v, got %v", tc.want, got)
			}
		})
	}
}

// ── title ─────────────────────────────────────────────────────────────────────

func TestTitle(t *testing.T) {
	cases := map[string]struct {
		input string
		want  string
	}{
		"empty":       {"", ""},
		"lower":       {"foo", "Foo"},
		"already_cap": {"Foo", "Foo"},
		"camel":       {"fooBar", "FooBar"},
		"single":      {"a", "A"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := title(tc.input)
			if got != tc.want {
				t.Errorf("title(%q): want %q, got %q", tc.input, tc.want, got)
			}
		})
	}
}

// ── graphqlInlineFragType ─────────────────────────────────────────────────────

func TestGraphqlInlineFragType(t *testing.T) {
	cases := map[string]struct {
		tag  string
		want string
	}{
		"match":           {`graphql:"... on District"`, "District"},
		"match_with_json": {`json:"foo" graphql:"... on MetaDistrict"`, "MetaDistrict"},
		"no_tag":          {`json:"foo"`, ""},
		"regular_graphql": {`graphql:"fieldName"`, ""},
		"empty":           {"", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := graphqlInlineFragType(tc.tag)
			if got != tc.want {
				t.Errorf("graphqlInlineFragType(%q): want %q, got %q", tc.tag, tc.want, got)
			}
		})
	}
}

// ── genUnmarshalJSONMethod ────────────────────────────────────────────────────

// makeInlineFragStruct builds a *types.Struct with an inline-fragment field
// (only graphql:"... on Type" tag, no json: tag) and a __typename field.
func makeInlineFragStruct(fragFieldName, fragTypeName string) *types.Struct {
	vars := []*types.Var{
		types.NewField(0, nil, fragFieldName, strType, false),
		types.NewField(0, nil, "Typename", types.NewPointer(strType), false),
	}
	tags := []string{
		`graphql:"... on ` + fragTypeName + `"`,
		`json:"__typename" graphql:"__typename"`,
	}
	return types.NewStruct(vars, tags)
}

func TestGenUnmarshalJSONMethod_NoInlineFragFields(t *testing.T) {
	st := makeStruct([]struct {
		name    string
		jsonKey string
		typ     types.Type
	}{
		{"ID", "id", strType},
		{"Name", "name", strType},
	})
	got := genUnmarshalJSONMethod("MyType", namedType("MyType", st))
	if got != "" {
		t.Errorf("expected empty string for struct with no inline-frag fields, got:\n%s", got)
	}
}

func TestGenUnmarshalJSONMethod_WithInlineFragField(t *testing.T) {
	st := makeInlineFragStruct("District", "District")
	got := genUnmarshalJSONMethod("Descendants", namedType("Descendants", st))
	if got == "" {
		t.Fatal("expected non-empty UnmarshalJSON for struct with inline-frag field")
	}
	for _, want := range []string{
		"func (t *Descendants) UnmarshalJSON",
		`json:"__typename"`,
		`t.Typename = _disc.Typename`,
		`case "District"`,
		`json.Unmarshal(data, &t.District)`,
		"return nil",
	} {
		if !containsStr(got, want) {
			t.Errorf("generated code missing %q:\n%s", want, got)
		}
	}
}

func TestGenUnmarshalJSONMethod_MultipleConcreteTypes(t *testing.T) {
	vars := []*types.Var{
		types.NewField(0, nil, "District", strType, false),
		types.NewField(0, nil, "MetaDistrict", strType, false),
		types.NewField(0, nil, "Typename", types.NewPointer(strType), false),
	}
	tags := []string{
		`graphql:"... on District"`,
		`graphql:"... on MetaDistrict"`,
		`json:"__typename" graphql:"__typename"`,
	}
	st := types.NewStruct(vars, tags)
	got := genUnmarshalJSONMethod("AdminAggregate", namedType("AdminAggregate", st))
	if !containsStr(got, `case "District"`) || !containsStr(got, `case "MetaDistrict"`) {
		t.Errorf("expected both District and MetaDistrict cases:\n%s", got)
	}
}

func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}
