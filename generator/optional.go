package generator

import "go/types"

// applyValueOptional rewrites every response-side Type in out to drop
// pointers from nullable scalar (and other non-nilable) fields, implementing
// Config.Generate.Optional == "value".
//
// Touched: fragments, operation responses, response sub-types.
// Untouched: operation arguments — input nullability must remain distinguishable
// from zero values, so input pointer-wrapping is preserved.
func applyValueOptional(out *sourceOutput) {
	for _, f := range out.fragments {
		f.Type = stripResponsePointers(f.Type)
	}
	for _, or := range out.operationResponses {
		or.Type = stripResponsePointers(or.Type)
	}
	for _, ss := range out.responseSubTypes {
		ss.Type = stripResponsePointers(ss.Type)
	}
}

// stripResponsePointers returns a Type equivalent to t with every pointer to a
// non-nilable type replaced by the pointee. Recurses into anonymous structs
// and slice elements. Named types are returned as-is — their bodies are owned
// by separate generated structs (visited via responseSubTypes) or by external
// bindings (e.g., time.Time) that this transform must not rewrite.
func stripResponsePointers(t types.Type) types.Type {
	switch x := t.(type) {
	case *types.Pointer:
		return stripPointerType(x)
	case *types.Struct:
		return rebuildStruct(x)
	case *types.Slice:
		return types.NewSlice(stripResponsePointers(x.Elem()))
	default:
		return t
	}
}

// stripPointerType returns the pointee when it is non-nilable, otherwise a
// new pointer wrapping the (possibly transformed) pointee. The pointee is
// always processed first so nested pointer-of-pointer cases collapse.
func stripPointerType(p *types.Pointer) types.Type {
	elem := stripResponsePointers(p.Elem())
	if isNilable(elem) {
		return types.NewPointer(elem)
	}
	return elem
}

// rebuildStruct returns a new struct with each field's type processed by
// stripResponsePointers. Identity is preserved when no field changed, so the
// caller's referential equality checks (and lint-noticeable allocations) are
// avoided in the common no-op case.
func rebuildStruct(s *types.Struct) types.Type {
	n := s.NumFields()
	if n == 0 {
		return s
	}
	fields := make([]*types.Var, n)
	tags := make([]string, n)
	changed := false
	for i := range n {
		v := s.Field(i)
		newType := stripResponsePointers(v.Type())
		if newType != v.Type() {
			changed = true
		}
		fields[i] = types.NewVar(v.Pos(), v.Pkg(), v.Name(), newType)
		tags[i] = s.Tag(i)
	}
	if !changed {
		return s
	}
	return types.NewStruct(fields, tags)
}

// isNilable reports whether t can hold the value nil. Matches gqlgen's
// binder.IsNilable: pointer, slice, map, and interface qualify; named types
// inherit from their underlying type.
func isNilable(t types.Type) bool {
	t = types.Unalias(t)
	if named, ok := t.(*types.Named); ok {
		return isNilable(named.Underlying())
	}
	switch t.(type) {
	case *types.Pointer, *types.Slice, *types.Map, *types.Interface:
		return true
	}
	return false
}
