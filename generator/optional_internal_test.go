package generator

import (
	"go/token"
	"go/types"
	"testing"
)

func TestStripResponsePointers(t *testing.T) {
	t.Parallel()

	str := types.Typ[types.String]
	intT := types.Typ[types.Int]
	boolT := types.Typ[types.Bool]

	// Named struct: type civilDate struct{}. Underlying is a struct, so
	// it counts as non-nilable per gqlgen's IsNilable definition.
	pkg := types.NewPackage("example.com/civil", "civil")
	dateUnderlying := types.NewStruct(nil, nil)
	dateName := types.NewTypeName(token.NoPos, pkg, "Date", nil)
	dateNamed := types.NewNamed(dateName, dateUnderlying, nil)

	// Named type with interface underlying: nilable.
	ifaceUnderlying := types.NewInterfaceType(nil, nil).Complete()
	ifaceName := types.NewTypeName(token.NoPos, pkg, "Any", nil)
	ifaceNamed := types.NewNamed(ifaceName, ifaceUnderlying, nil)

	skuField := types.NewVar(token.NoPos, nil, "Sku", types.NewPointer(str))
	idField := types.NewVar(token.NoPos, nil, "ID", str)
	skuStruct := types.NewStruct([]*types.Var{skuField, idField}, []string{"", ""})
	skuStructWanted := types.NewStruct(
		[]*types.Var{
			types.NewVar(token.NoPos, nil, "Sku", str),
			types.NewVar(token.NoPos, nil, "ID", str),
		},
		[]string{"", ""},
	)

	cases := map[string]struct {
		in   types.Type
		want types.Type
	}{
		"pointer to basic string unwraps": {
			in:   types.NewPointer(str),
			want: str,
		},
		"pointer to basic int unwraps": {
			in:   types.NewPointer(intT),
			want: intT,
		},
		"pointer to named struct unwraps": {
			in:   types.NewPointer(dateNamed),
			want: dateNamed,
		},
		"pointer to nilable named interface preserved": {
			in:   types.NewPointer(ifaceNamed),
			want: types.NewPointer(ifaceNamed),
		},
		"pointer to slice preserved (slice is nilable)": {
			in:   types.NewPointer(types.NewSlice(str)),
			want: types.NewPointer(types.NewSlice(str)),
		},
		"pointer to map preserved (map is nilable)": {
			in:   types.NewPointer(types.NewMap(str, str)),
			want: types.NewPointer(types.NewMap(str, str)),
		},
		"double pointer collapses through to non-nilable": {
			in:   types.NewPointer(types.NewPointer(str)),
			want: str,
		},
		"slice of pointer-basic becomes slice of basic": {
			in:   types.NewSlice(types.NewPointer(str)),
			want: types.NewSlice(str),
		},
		"struct with pointer field rebuilt without pointer": {
			in:   skuStruct,
			want: skuStructWanted,
		},
		"empty struct passes through unchanged": {
			in:   types.NewStruct(nil, nil),
			want: types.NewStruct(nil, nil),
		},
		"plain basic passes through": {
			in:   boolT,
			want: boolT,
		},
		"named type passes through (not descended into)": {
			in:   dateNamed,
			want: dateNamed,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := stripResponsePointers(tc.in)
			if got.String() != tc.want.String() {
				t.Errorf("stripResponsePointers(%s) = %s; want %s",
					tc.in, got, tc.want)
			}
		})
	}
}

func TestIsNilable(t *testing.T) {
	t.Parallel()

	str := types.Typ[types.String]
	pkg := types.NewPackage("example.com/x", "x")

	structNamed := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "S", nil),
		types.NewStruct(nil, nil),
		nil,
	)
	ifaceNamed := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "I", nil),
		types.NewInterfaceType(nil, nil).Complete(),
		nil,
	)

	cases := map[string]struct {
		in   types.Type
		want bool
	}{
		"basic string is not nilable":      {str, false},
		"pointer is nilable":               {types.NewPointer(str), true},
		"slice is nilable":                 {types.NewSlice(str), true},
		"map is nilable":                   {types.NewMap(str, str), true},
		"interface is nilable":             {types.NewInterfaceType(nil, nil).Complete(), true},
		"struct is not nilable":            {types.NewStruct(nil, nil), false},
		"named over struct is not nilable": {structNamed, false},
		"named over interface is nilable":  {ifaceNamed, true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := isNilable(tc.in); got != tc.want {
				t.Errorf("isNilable(%s) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}
