package generator

import (
	"errors"
	"fmt"
	"go/types"
	"strconv"
	"strings"

	"github.com/99designs/gqlgen/codegen/templates"
	"github.com/gqlgo/gqlgenc/clientgenv2"
)

// chainStep describes one Go struct field traversal step when following a parentPath.
type chainStep struct {
	GoName       string
	IsPtr        bool // field type is a pointer — nil check before access
	IsSlice      bool // field type is a slice — iteration for list entity fetches
	SliceElemPtr bool // slice elements are pointers (only meaningful when IsSlice=true)
}

// keyFieldInfo holds the Go identifier and pointer-ness of one key or requires field
// at the leaf element type reached by following parentPath through the response struct.
type keyFieldInfo struct {
	goName string
	isPtr  bool
}

// parseJSONKey extracts the JSON field name from a struct tag string.
// e.g., `json:"fieldName,omitempty" graphql:"fieldName"` → "fieldName"
func parseJSONKey(tag string) string {
	const prefix = `json:"`
	i := strings.Index(tag, prefix)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(prefix):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	name := rest[:end]
	if comma := strings.IndexByte(name, ','); comma >= 0 {
		name = name[:comma]
	}
	return name
}

// fieldByJSONKey finds the struct field whose json tag matches key.
// Returns the Go field name, its type, and whether the field was found.
func fieldByJSONKey(st *types.Struct, key string) (string, types.Type, bool) {
	for i := range st.NumFields() {
		if parseJSONKey(st.Tag(i)) == key {
			f := st.Field(i)
			return f.Name(), f.Type(), true
		}
	}
	return "", nil, false
}

// unwrapToStruct unwraps any pointer and named-type layers to reach the underlying struct.
// Returns nil if t is not ultimately a *types.Struct.
func unwrapToStruct(t types.Type) *types.Struct {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		t = named.Underlying()
	}
	st, _ := t.(*types.Struct)
	return st
}

// walkTypeForChain follows path through t (a response struct type), returning the
// sequence of Go field accessor steps with pointer/slice metadata.
// t is the root response struct type as produced by gqlgenc (a *types.Struct).
func walkTypeForChain(t types.Type, path []string) ([]chainStep, error) {
	steps := make([]chainStep, 0, len(path))
	cur := t
	for segIdx, seg := range path {
		st := unwrapToStruct(cur)
		if st == nil {
			return nil, fmt.Errorf(
				"generator: expected struct at path segment %q (index %d), got %T",
				seg,
				segIdx,
				cur,
			)
		}
		name, fieldType, ok := fieldByJSONKey(st, seg)
		if !ok {
			return nil, fmt.Errorf(
				"generator: no field with json:%q in struct at path index %d",
				seg,
				segIdx,
			)
		}
		step := chainStep{GoName: name}
		switch ft := fieldType.(type) {
		case *types.Pointer:
			step.IsPtr = true
			cur = ft.Elem()
		case *types.Slice:
			step.IsSlice = true
			elem := ft.Elem()
			if ptr, ok2 := elem.(*types.Pointer); ok2 {
				step.SliceElemPtr = true
				cur = ptr.Elem()
			} else {
				cur = elem
			}
		default:
			cur = fieldType
		}
		steps = append(steps, step)
	}
	return steps, nil
}

// isFieldFromPriorEntityFetch reports whether fieldName appears as a top-level field
// in any prior entity fetch's selection. This detects the Phase C.2 case where
// requiresFields for entity fetch N come from entity fetch N-1's selection (not from
// the initial fetch). Generation falls back to ExecuteAndUnmarshal in this case.
func isFieldFromPriorEntityFetch(fieldName string, prior []urlSpecEntityFetch) bool {
	for _, ef := range prior {
		for _, line := range strings.Split(strings.TrimSpace(ef.Selection), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "{") {
				continue
			}
			// strip sub-selection: "field { ... }" → "field"
			fieldPart := strings.SplitN(line, " ", 2)[0]
			fieldPart = strings.SplitN(fieldPart, "{", 2)[0]
			fieldPart = strings.SplitN(fieldPart, "(", 2)[0]
			fieldPart = strings.TrimSpace(fieldPart)
			if fieldPart == fieldName {
				return true
			}
		}
	}
	return false
}

// leafKeyAccessors returns one keyFieldInfo per field name, looked up by JSON tag at
// the element type reached by walking responseType along path. The walk advances through
// pointer and slice layers so the returned infos describe fields on the slice-element
// type (for list paths) or the pointed-to struct (for pointer paths).
// Returns an error if any path segment or field name is absent, or if an intermediate
// type is not a struct.
func leafKeyAccessors(
	responseType types.Type,
	path []string,
	fields []string,
) ([]keyFieldInfo, error) {
	cur := responseType
	for segIdx, seg := range path {
		st := unwrapToStruct(cur)
		if st == nil {
			return nil, fmt.Errorf(
				"generator: leafKeyAccessors: expected struct at path segment %q (index %d)",
				seg,
				segIdx,
			)
		}
		_, fieldType, ok := fieldByJSONKey(st, seg)
		if !ok {
			return nil, fmt.Errorf(
				"generator: leafKeyAccessors: no field with json:%q at path index %d",
				seg,
				segIdx,
			)
		}
		// Advance cur: unwrap pointer, then unwrap slice to element type.
		if ptr, ok2 := fieldType.(*types.Pointer); ok2 {
			fieldType = ptr.Elem()
		}
		if sl, ok2 := fieldType.(*types.Slice); ok2 {
			elem := sl.Elem()
			if ptr, ok3 := elem.(*types.Pointer); ok3 {
				elem = ptr.Elem()
			}
			fieldType = elem
		}
		cur = fieldType
	}
	st := unwrapToStruct(cur)
	if st == nil {
		return nil, errors.New("generator: leafKeyAccessors: leaf type is not a struct")
	}
	result := make([]keyFieldInfo, len(fields))
	for i, field := range fields {
		name, fieldType, ok := fieldByJSONKey(st, field)
		if !ok {
			return nil, fmt.Errorf(
				"generator: leafKeyAccessors: no field with json:%q in leaf struct",
				field,
			)
		}
		_, isPtr := fieldType.(*types.Pointer)
		result[i] = keyFieldInfo{goName: name, isPtr: isPtr}
	}
	return result, nil
}

// title returns seg with its first byte uppercased (ASCII only). Used to turn
// JSON key names like "classroomByDescriptorV2" into Go-safe exported field names
// "ClassroomByDescriptorV2" for generated anonymous structs.
func title(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// genNilGuard generates a Go boolean expression guarding access to a chain of pointer
// fields. For steps=[{A,ptr},{B,ptr}], it returns "res.A != nil && res.A.B != nil".
// Returns "" when no nil checks are needed (no pointer steps in chain).
func genNilGuard(steps []chainStep) string {
	var parts []string
	var acc strings.Builder
	acc.WriteString("res")
	for _, s := range steps {
		acc.WriteString("." + s.GoName)
		if s.IsPtr {
			parts = append(parts, acc.String()+" != nil")
		}
		if s.IsSlice {
			// Don't guard slices with != nil; use len check in loop.
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " && ")
}

// genAccessChain generates the Go expression to access the field chain from res.
// For steps=[{A,ptr},{B,slice}], it returns "res.A.B".
func genAccessChain(steps []chainStep) string {
	var b strings.Builder
	b.WriteString("res")
	for _, s := range steps {
		b.WriteString("." + s.GoName)
	}
	return b.String()
}

// genKeyExtractionStruct generates an anonymous Go struct type for extracting
// key and requires fields from a raw JSON initial-fetch response. path is the
// sequence of JSON segment names (e.g., ["product"]), steps are the corresponding
// chainStep values (used for GoName and IsSlice), and keys are the JSON field
// names to extract at the leaf struct.
//
// Example: path=["product"], steps=[{GoName:"Product",IsPtr:true}], keys=["id","dimensions"]
// → `struct{ Product struct{ ID any \`json:"id"\`; Dimensions any \`json:"dimensions"\` }
// \`json:"product"\` }`
func genKeyExtractionStruct(path []string, steps []chainStep, keys []string) string {
	var b strings.Builder
	for i := range path {
		s := steps[i]
		b.WriteString("struct{ " + s.GoName + " ")
		if s.IsSlice {
			b.WriteString("[]")
		}
	}
	b.WriteString("struct{ ")
	for i, k := range keys {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(title(k) + " any `json:\"" + k + "\"`")
	}
	b.WriteString(" }")
	for i := len(path) - 1; i >= 0; i-- {
		b.WriteString(" `json:\"" + path[i] + "\"` }")
	}
	return b.String()
}

// genEntityFetch generates the typed entity merge code for an operation with entity
// fetches. Returns "" to fall back to ExecuteAndUnmarshal when:
//   - the plan has no entity fetches
//   - any requiresField comes from a prior entity fetch (Phase C.2 case)
//   - any intermediate path step is a slice (nested list in path, Phase C.2 case)
//   - walkTypeForChain fails for any entity fetch path
//
// Two code paths are generated depending on whether requires fields are present:
//
//   - Optimized (Fix 1 + Fix 2): used when all entity fetches have no requires fields
//     AND all key fields exist in the gqlgenc-generated response struct. Generates a
//     single-pass initial decode (no intermediate json.RawMessage) and reads key fields
//     directly from res.*.
//
//   - Legacy typed: used when any entity fetch has requires fields (which the federation
//     planner injects into the initial query but which are not in the gqlgenc struct).
//     Generates two-step initial decode (keeping _raw bytes) and extracts key+requires
//     fields from _raw via an anonymous extraction struct.
//
// respTypeName is the Go-normalized response struct name (e.g. "GetProductDelivery")
// used only in the optimized path. Callers should apply templates.ToGo before passing it.
func (g *genGettersGenerator) genEntityFetch(
	opName string,
	responseType types.Type,
	respTypeName string,
	specJSON string,
) string {
	entityFetches, err := decodePlanEntityFetches(specJSON)
	if err != nil || len(entityFetches) == 0 {
		return ""
	}

	// Detect Phase C.2: requiresField sourced from a prior entity fetch's selection.
	for i, ef := range entityFetches {
		for _, rf := range ef.RequiresFields {
			if isFieldFromPriorEntityFetch(rf, entityFetches[:i]) {
				return ""
			}
		}
	}

	type efMeta struct {
		ef       urlSpecEntityFetch
		steps    []chainStep
		keyInfos []keyFieldInfo // set only in optimized path (keyFields only, no requires)
	}
	metas := make([]efMeta, 0, len(entityFetches))
	for _, ef := range entityFetches {
		steps, err := walkTypeForChain(responseType, ef.ParentPath)
		if err != nil {
			return "" // unknown field in path — fall back to ExecuteAndUnmarshal
		}
		// Detect intermediate slices (Phase C.2 case — nested list in path).
		for _, s := range steps[:max(0, len(steps)-1)] {
			if s.IsSlice {
				return ""
			}
		}
		metas = append(metas, efMeta{ef: ef, steps: steps})
	}

	// Select optimized path only when all entity fetches have no requires fields
	// AND all key fields can be looked up in the gqlgenc-generated response struct.
	// @requires fields are planner-injected into the initial fetch query but are not
	// in the user-selected fields, so the gqlgenc struct does not contain them.
	useOptimized := true
	for i := range metas {
		m := &metas[i]
		if len(m.ef.RequiresFields) > 0 {
			useOptimized = false
			break
		}
		ki, kerr := leafKeyAccessors(responseType, m.ef.ParentPath, m.ef.KeyFields)
		if kerr != nil {
			useOptimized = false
			break
		}
		m.keyInfos = ki
	}

	var b strings.Builder

	if useOptimized {
		// Fix 1: single-pass initial fetch decode. Pre-set _wr.Data = &res so
		// json.Unmarshal populates the typed struct directly without an intermediate
		// json.RawMessage allocation.
		b.WriteString(`{
_f := plan.Fetches[0]
_rb, _err := httpPost(ctx, c.httpFor(ctx), _f.URL, _f.Query, "", filterVars(_opVars, _f.Variables))
if _err != nil {
return nil, fmt.Errorf("` + opName + `: fetch: %w", _err)
}
var _wr struct {
Data   *` + respTypeName + ` ` + "`" + `json:"data"` + "`" + `
Errors []GraphQLError           ` + "`" + `json:"errors,omitempty"` + "`" + `
}
_wr.Data = &res
if _werr := json.Unmarshal(_rb, &_wr); _werr != nil {
return nil, fmt.Errorf("` + opName + `: decode response: %w", _werr)
}
if len(_wr.Errors) > 0 {
return nil, fmt.Errorf("` + opName + `: %v", _wr.Errors)
}
}
`)
		for i, m := range metas {
			g.genSingleEntityFetch(&b, opName, i, m.ef, m.steps, m.keyInfos)
		}
	} else {
		// Legacy typed path: two-step initial decode keeping _raw for key/requires
		// extraction. Used when @requires fields are present or key lookup fails.
		// _raw is declared at function scope so subsequent entity fetch blocks can read it.
		b.WriteString(`var _raw json.RawMessage
{
_f := plan.Fetches[0]
_rb, _err := httpPost(ctx, c.httpFor(ctx), _f.URL, _f.Query, "", filterVars(_opVars, _f.Variables))
if _err != nil {
return nil, fmt.Errorf("` + opName + `: fetch: %w", _err)
}
var _wr struct {
Data   json.RawMessage ` + "`" + `json:"data"` + "`" + `
Errors []GraphQLError  ` + "`" + `json:"errors,omitempty"` + "`" + `
}
if _werr := json.Unmarshal(_rb, &_wr); _werr != nil {
return nil, fmt.Errorf("` + opName + `: decode response: %w", _werr)
}
if len(_wr.Errors) > 0 {
return nil, fmt.Errorf("` + opName + `: %v", _wr.Errors)
}
_raw = _wr.Data
if len(_raw) > 0 {
if _rerr := json.Unmarshal(_raw, &res); _rerr != nil {
return nil, fmt.Errorf("` + opName + `: decode typed response: %w", _rerr)
}
}
}
`)
		for i, m := range metas {
			g.genSingleEntityFetchLegacy(&b, opName, i, m.ef, m.steps)
		}
	}

	return b.String()
}

// genSingleEntityFetch appends the code block for one entity fetch to b.
// keyInfos covers ef.KeyFields ++ ef.RequiresFields in the same order; each entry
// provides the Go field name and pointer-ness in the response struct at the leaf of ParentPath.
func (g *genGettersGenerator) genSingleEntityFetch(
	b *strings.Builder,
	opName string,
	idx int,
	ef urlSpecEntityFetch,
	steps []chainStep,
	keyInfos []keyFieldInfo,
) {
	idxS := strconv.Itoa(idx)
	allKeys := append(append([]string{}, ef.KeyFields...), ef.RequiresFields...)
	p := fmt.Sprintf("_ef%d", idx) // variable prefix for this entity fetch

	chain := genAccessChain(steps)
	nilGuard := genNilGuard(steps)
	lastStep := steps[len(steps)-1]
	keyAccess := chain // direct res.* access — no separate key extraction struct

	b.WriteString("{\n")

	if ef.IsParentList {
		// 2a. List parent: build rep slice from res.* directly.
		// nilGuard protects intermediate pointer steps before the slice field.
		if nilGuard != "" {
			b.WriteString("if " + nilGuard + " {\n")
		}
		b.WriteString(p + "reps := make([]map[string]any, 0, len(" + keyAccess + "))\n")
		b.WriteString("for _i, _u := range " + keyAccess + " {\n")
		if lastStep.SliceElemPtr {
			b.WriteString("if _u == nil {\n")
			b.WriteString(
				"return nil, fmt.Errorf(\"" + opName + ": entity fetch " + idxS + ": nil element at index %d\", _i)\n",
			)
			b.WriteString("}\n")
		}
		for ki, kf := range ef.KeyFields {
			if keyInfos[ki].isPtr {
				b.WriteString("if _u." + keyInfos[ki].goName + " == nil {\n")
				b.WriteString(
					"return nil, fmt.Errorf(\"" + opName + ": entity fetch " + idxS + ": key " + kf + " nil at index %d\", _i)\n",
				)
				b.WriteString("}\n")
			}
		}
		b.WriteString(
			p + "reps = append(" + p + "reps, map[string]any{\"__typename\": \"" + ef.TypeName + "\"",
		)
		for i, kf := range allKeys {
			b.WriteString(", \"" + kf + "\": _u." + keyInfos[i].goName)
		}
		b.WriteString("})\n")
		b.WriteString("}\n") // end for

		// 3. Entity query dispatch (only if there are reps).
		b.WriteString("if len(" + p + "reps) > 0 {\n")
		b.WriteString(p + "bytes, " + p + "err := httpPost(ctx, c.httpFor(ctx),\n")
		b.WriteString("plan.EntityFetches[" + idxS + "].URL,\n")
		b.WriteString("plan.EntityFetches[" + idxS + "].Query,\n")
		b.WriteString(
			"\"\", buildEntityFetchVars(" + p + "reps, _opVars, plan.EntityFetches[" + idxS + "].Variables))\n",
		)
		b.WriteString(
			"if " + p + "err != nil { return nil, fmt.Errorf(\"" + opName + ": entity " + idxS + " " + ef.TypeName + ": %w\", " + p + "err) }\n",
		)
		b.WriteString(
			"var " + p + "w struct{ Data struct{ Entities []json.RawMessage `json:\"_entities\"` } `json:\"data\"`; Errors []GraphQLError `json:\"errors,omitempty\"` }\n",
		)
		b.WriteString(
			"if " + p + "uerr := json.Unmarshal(" + p + "bytes, &" + p + "w); " + p + "uerr != nil {\n",
		)
		b.WriteString(
			"return nil, fmt.Errorf(\"" + opName + ": decode entities " + idxS + ": %w\", " + p + "uerr)\n",
		)
		b.WriteString("}\n")
		b.WriteString(
			"if len(" + p + "w.Errors) > 0 { return nil, fmt.Errorf(\"" + opName + ": entity " + idxS + " " + ef.TypeName + ": %v\", " + p + "w.Errors) }\n",
		)

		// 4a. Merge into list. No inner nilGuard — already inside the outer one.
		b.WriteString("for _i, _eraw := range " + p + "w.Data.Entities {\n")
		b.WriteString("if _i < len(" + chain + ") {\n")
		if lastStep.SliceElemPtr {
			b.WriteString("if " + chain + "[_i] != nil {\n")
			b.WriteString("if _merr := json.Unmarshal(_eraw, " + chain + "[_i]); _merr != nil {\n")
			b.WriteString(
				"return nil, fmt.Errorf(\"" + opName + ": merge " + idxS + " index %d: %w\", _i, _merr)\n",
			)
			b.WriteString("}\n}\n")
		} else {
			b.WriteString("if _merr := json.Unmarshal(_eraw, &" + chain + "[_i]); _merr != nil {\n")
			b.WriteString(
				"return nil, fmt.Errorf(\"" + opName + ": merge " + idxS + " index %d: %w\", _i, _merr)\n",
			)
			b.WriteString("}\n")
		}
		b.WriteString("}\n}\n") // end bounds check && entity loop

		b.WriteString("}\n") // end if len(reps) > 0
		if nilGuard != "" {
			b.WriteString("}\n") // end nilGuard
		}

	} else {
		// 2b. Single parent: nilGuard protects all intermediate pointer steps;
		// pointer key fields additionally guard on the key being non-nil.
		ki0 := keyInfos[0]

		if nilGuard != "" {
			b.WriteString("if " + nilGuard + " {\n")
		}
		if ki0.isPtr {
			b.WriteString("if " + keyAccess + "." + ki0.goName + " != nil {\n")
		}

		b.WriteString(p + "rep := map[string]any{\"__typename\": \"" + ef.TypeName + "\"")
		for i, kf := range allKeys {
			b.WriteString(", \"" + kf + "\": " + keyAccess + "." + keyInfos[i].goName)
		}
		b.WriteString("}\n")

		// 3. Entity query dispatch.
		b.WriteString(p + "bytes, " + p + "err := httpPost(ctx, c.httpFor(ctx),\n")
		b.WriteString("plan.EntityFetches[" + idxS + "].URL,\n")
		b.WriteString("plan.EntityFetches[" + idxS + "].Query,\n")
		b.WriteString(
			"\"\", buildEntityFetchVars([]map[string]any{" + p + "rep}, _opVars, plan.EntityFetches[" + idxS + "].Variables))\n",
		)
		b.WriteString(
			"if " + p + "err != nil { return nil, fmt.Errorf(\"" + opName + ": entity " + idxS + " " + ef.TypeName + ": %w\", " + p + "err) }\n",
		)
		b.WriteString(
			"var " + p + "w struct{ Data struct{ Entities []json.RawMessage `json:\"_entities\"` } `json:\"data\"`; Errors []GraphQLError `json:\"errors,omitempty\"` }\n",
		)
		b.WriteString(
			"if " + p + "uerr := json.Unmarshal(" + p + "bytes, &" + p + "w); " + p + "uerr != nil {\n",
		)
		b.WriteString(
			"return nil, fmt.Errorf(\"" + opName + ": decode entities " + idxS + ": %w\", " + p + "uerr)\n",
		)
		b.WriteString("}\n")
		b.WriteString(
			"if len(" + p + "w.Errors) > 0 { return nil, fmt.Errorf(\"" + opName + ": entity " + idxS + " " + ef.TypeName + ": %v\", " + p + "w.Errors) }\n",
		)

		// 4b. Merge into single parent. No inner nilGuard — already inside the outer one.
		b.WriteString("if len(" + p + "w.Data.Entities) > 0 {\n")
		if lastStep.IsPtr {
			b.WriteString(
				"if _merr := json.Unmarshal(" + p + "w.Data.Entities[0], " + chain + "); _merr != nil {\n",
			)
		} else {
			b.WriteString(
				"if _merr := json.Unmarshal(" + p + "w.Data.Entities[0], &" + chain + "); _merr != nil {\n",
			)
		}
		b.WriteString(
			"return nil, fmt.Errorf(\"" + opName + ": merge entity " + idxS + ": %w\", _merr)\n",
		)
		b.WriteString("}\n")
		b.WriteString("}\n") // end if len(Entities) > 0

		if ki0.isPtr {
			b.WriteString("}\n") // end if firstKey != nil
		}
		if nilGuard != "" {
			b.WriteString("}\n") // end nilGuard
		}
	}

	b.WriteString("}\n") // end outer block
}

// genSingleEntityFetchLegacy appends the code block for one entity fetch to b using the
// legacy (two-step + key extraction struct) approach. It reads key and requires fields
// from _raw (the initial fetch raw JSON bytes) via an anonymous extraction struct rather
// than from res.*. This is used when @requires fields are present, since those fields are
// planner-injected into the initial query but are not in the gqlgenc-generated struct.
// The merge step still uses res.* (genAccessChain), which is always in the gqlgenc struct.
func (g *genGettersGenerator) genSingleEntityFetchLegacy(
	b *strings.Builder,
	opName string,
	idx int,
	ef urlSpecEntityFetch,
	steps []chainStep,
) {
	idxS := strconv.Itoa(idx)
	allKeys := append(append([]string{}, ef.KeyFields...), ef.RequiresFields...)
	p := fmt.Sprintf("_ef%d", idx)

	chain := genAccessChain(steps)
	nilGuard := genNilGuard(steps)
	lastStep := steps[len(steps)-1]
	keysVar := p + "keys"
	keyExtractType := genKeyExtractionStruct(ef.ParentPath, steps, allKeys)

	// Leaf access path within keysVar: _ef0keys.Product (single) or _ef0keys.Products (list).
	leafAccess := keysVar
	var leafAccessSb558 strings.Builder
	for _, s := range steps {
		leafAccessSb558.WriteString("." + s.GoName)
	}
	leafAccess += leafAccessSb558.String()

	b.WriteString("{\n")
	b.WriteString("var " + keysVar + " " + keyExtractType + "\n")
	b.WriteString("if len(_raw) > 0 { _ = json.Unmarshal(_raw, &" + keysVar + ") }\n")

	if ef.IsParentList {
		// 2a. List parent: extract key+requires fields from keysVar, build rep slice.
		b.WriteString(p + "reps := make([]map[string]any, 0, len(" + leafAccess + "))\n")
		b.WriteString("for _i, _u := range " + leafAccess + " {\n")
		if len(ef.KeyFields) > 0 {
			firstKey := ef.KeyFields[0]
			b.WriteString("if _u." + title(firstKey) + " == nil {\n")
			b.WriteString(
				"return nil, fmt.Errorf(\"" + opName + ": entity fetch " + idxS + ": key " + firstKey + " nil at index %d\", _i)\n",
			)
			b.WriteString("}\n")
		}
		b.WriteString(
			p + "reps = append(" + p + "reps, map[string]any{\"__typename\": \"" + ef.TypeName + "\"",
		)
		for _, kf := range allKeys {
			b.WriteString(", \"" + kf + "\": _u." + title(kf))
		}
		b.WriteString("})\n")
		b.WriteString("}\n") // end for

		// 3. Entity query dispatch.
		b.WriteString("if len(" + p + "reps) > 0 {\n")
		b.WriteString(p + "bytes, " + p + "err := httpPost(ctx, c.httpFor(ctx),\n")
		b.WriteString("plan.EntityFetches[" + idxS + "].URL,\n")
		b.WriteString("plan.EntityFetches[" + idxS + "].Query,\n")
		b.WriteString(
			"\"\", buildEntityFetchVars(" + p + "reps, _opVars, plan.EntityFetches[" + idxS + "].Variables))\n",
		)
		b.WriteString(
			"if " + p + "err != nil { return nil, fmt.Errorf(\"" + opName + ": entity " + idxS + " " + ef.TypeName + ": %w\", " + p + "err) }\n",
		)
		b.WriteString(
			"var " + p + "w struct{ Data struct{ Entities []json.RawMessage `json:\"_entities\"` } `json:\"data\"`; Errors []GraphQLError `json:\"errors,omitempty\"` }\n",
		)
		b.WriteString(
			"if " + p + "uerr := json.Unmarshal(" + p + "bytes, &" + p + "w); " + p + "uerr != nil {\n",
		)
		b.WriteString(
			"return nil, fmt.Errorf(\"" + opName + ": decode entities " + idxS + ": %w\", " + p + "uerr)\n",
		)
		b.WriteString("}\n")
		b.WriteString(
			"if len(" + p + "w.Errors) > 0 { return nil, fmt.Errorf(\"" + opName + ": entity " + idxS + " " + ef.TypeName + ": %v\", " + p + "w.Errors) }\n",
		)

		// 4a. Merge into list. Merge target is res.* (from genAccessChain), always present.
		if nilGuard != "" {
			b.WriteString("if " + nilGuard + " {\n")
		}
		b.WriteString("for _i, _eraw := range " + p + "w.Data.Entities {\n")
		b.WriteString("if _i < len(" + chain + ") {\n")
		if lastStep.SliceElemPtr {
			b.WriteString("if " + chain + "[_i] != nil {\n")
			b.WriteString("if _merr := json.Unmarshal(_eraw, " + chain + "[_i]); _merr != nil {\n")
			b.WriteString(
				"return nil, fmt.Errorf(\"" + opName + ": merge " + idxS + " index %d: %w\", _i, _merr)\n",
			)
			b.WriteString("}\n}\n")
		} else {
			b.WriteString("if _merr := json.Unmarshal(_eraw, &" + chain + "[_i]); _merr != nil {\n")
			b.WriteString(
				"return nil, fmt.Errorf(\"" + opName + ": merge " + idxS + " index %d: %w\", _i, _merr)\n",
			)
			b.WriteString("}\n")
		}
		b.WriteString("}\n}\n")
		if nilGuard != "" {
			b.WriteString("}\n")
		}
		b.WriteString("}\n") // end if len(reps) > 0

	} else {
		// 2b. Single parent: nil check on first key field (any type → nil when absent).
		if len(ef.KeyFields) > 0 {
			firstKey := ef.KeyFields[0]
			b.WriteString("if " + leafAccess + "." + title(firstKey) + " != nil {\n")
		}
		b.WriteString(p + "rep := map[string]any{\"__typename\": \"" + ef.TypeName + "\"")
		for _, kf := range allKeys {
			b.WriteString(", \"" + kf + "\": " + leafAccess + "." + title(kf))
		}
		b.WriteString("}\n")

		// 3. Entity query dispatch.
		b.WriteString(p + "bytes, " + p + "err := httpPost(ctx, c.httpFor(ctx),\n")
		b.WriteString("plan.EntityFetches[" + idxS + "].URL,\n")
		b.WriteString("plan.EntityFetches[" + idxS + "].Query,\n")
		b.WriteString(
			"\"\", buildEntityFetchVars([]map[string]any{" + p + "rep}, _opVars, plan.EntityFetches[" + idxS + "].Variables))\n",
		)
		b.WriteString(
			"if " + p + "err != nil { return nil, fmt.Errorf(\"" + opName + ": entity " + idxS + " " + ef.TypeName + ": %w\", " + p + "err) }\n",
		)
		b.WriteString(
			"var " + p + "w struct{ Data struct{ Entities []json.RawMessage `json:\"_entities\"` } `json:\"data\"`; Errors []GraphQLError `json:\"errors,omitempty\"` }\n",
		)
		b.WriteString(
			"if " + p + "uerr := json.Unmarshal(" + p + "bytes, &" + p + "w); " + p + "uerr != nil {\n",
		)
		b.WriteString(
			"return nil, fmt.Errorf(\"" + opName + ": decode entities " + idxS + ": %w\", " + p + "uerr)\n",
		)
		b.WriteString("}\n")
		b.WriteString(
			"if len(" + p + "w.Errors) > 0 { return nil, fmt.Errorf(\"" + opName + ": entity " + idxS + " " + ef.TypeName + ": %v\", " + p + "w.Errors) }\n",
		)

		// 4b. Merge into single parent. nilGuard protects intermediate pointer steps.
		b.WriteString("if len(" + p + "w.Data.Entities) > 0 {\n")
		if nilGuard != "" {
			b.WriteString("if " + nilGuard + " {\n")
		}
		if lastStep.IsPtr {
			b.WriteString(
				"if _merr := json.Unmarshal(" + p + "w.Data.Entities[0], " + chain + "); _merr != nil {\n",
			)
		} else {
			b.WriteString(
				"if _merr := json.Unmarshal(" + p + "w.Data.Entities[0], &" + chain + "); _merr != nil {\n",
			)
		}
		b.WriteString(
			"return nil, fmt.Errorf(\"" + opName + ": merge entity " + idxS + ": %w\", _merr)\n",
		)
		b.WriteString("}\n")
		if nilGuard != "" {
			b.WriteString("}\n")
		}
		b.WriteString("}\n") // end if len(Entities) > 0

		if len(ef.KeyFields) > 0 {
			b.WriteString("}\n") // end if firstKey != nil
		}
	}

	b.WriteString("}\n") // end outer block
}

// genEntityFetchFunc returns a template function that generates typed entity merge code
// for each operation. respTypeByName maps ResponseStructName → types.Type.
func (g *genGettersGenerator) genEntityFetchFunc(
	operationResponses []*clientgenv2.OperationResponse,
	planSpecs map[string]string,
) func(*clientgenv2.Operation) string {
	respTypeByName := make(map[string]types.Type, len(operationResponses))
	for _, or_ := range operationResponses {
		respTypeByName[or_.Name] = or_.Type
	}
	return func(op *clientgenv2.Operation) string {
		specJSON := planSpecs[op.Name]
		respType := respTypeByName[op.ResponseStructName]
		if respType == nil || specJSON == "" {
			return ""
		}
		return g.genEntityFetch(op.Name, respType, templates.ToGo(op.ResponseStructName), specJSON)
	}
}
