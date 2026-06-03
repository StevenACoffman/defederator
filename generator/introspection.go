package generator

import (
	"fmt"

	"github.com/vektah/gqlparser/v2/ast"
)

// IntrospectionInfo describes one introspection operation's emission strategy.
// The generator builds a map of these keyed by operation name; the template
// uses Response (bakeable case) or Variables (runtime case) to emit the right
// method body.
type IntrospectionInfo struct {
	// Name is the operation name as written in the source query.
	Name string

	// Bakeable is true when the response was fully evaluated at codegen time.
	// Response is non-empty in that case; otherwise the runtime resolver must
	// be called per request with the call site's arguments.
	Bakeable bool

	// Response holds the JSON-encoded introspection response when Bakeable.
	// Empty otherwise.
	Response string

	// Variables lists the GraphQL variable names (no `$`) that flow into
	// introspection field arguments. Used by the runtime emitter to build a
	// `map[string]any` from the generated method's typed parameters.
	Variables []string
}

// IntrospectionPlan summarises how a single introspection-only operation can
// be served by defederator.
//
//   - Bakeable is true when no argument to any introspection field references
//     a query variable. The response can be evaluated at codegen time and
//     embedded as a JSON constant.
//   - Variables, when Bakeable is false, lists the variable names (without the
//     leading `$`) that the operation passes into introspection field
//     arguments. The runtime resolver substitutes their values per call.
//
// IntrospectionPlan does not validate that op selects only introspection
// fields; callers should pair it with IsIntrospectionOnly.
type IntrospectionPlan struct {
	Bakeable  bool
	Variables []string
}

// PlanIntrospectionOps partitions doc's operations into introspection ops and
// federation ops. The returned map is keyed by operation name; ops not present
// in the map should go through the federation path.
//
// For each introspection op, this also evaluates the response if it's bakeable
// (no variable dependencies). Returning eagerly here keeps the
// codegen pipeline straightforward — the caller doesn't need a second pass.
func PlanIntrospectionOps(
	schema *ast.Schema,
	doc *ast.QueryDocument,
) (map[string]IntrospectionInfo, error) {
	if doc == nil {
		return nil, nil //nolint:nilnil // empty input means no introspection ops.
	}
	fragments := fragmentsByName(doc)
	out := map[string]IntrospectionInfo{}
	for _, op := range doc.Operations {
		if !IsIntrospectionOnly(op) {
			continue
		}
		info, err := buildIntrospectionInfo(schema, op, fragments)
		if err != nil {
			return nil, fmt.Errorf("operation %s: %w", op.Name, err)
		}
		out[op.Name] = info
	}
	return out, nil
}

// buildIntrospectionInfo computes one introspection op's metadata, evaluating
// the response at codegen time when there are no variable dependencies.
//
// Operations with variable-bound introspection arguments
// (e.g. `__type(name: $n)`) are not yet supported because the runtime path
// requires either embedding the resolver into each generated package or
// adding a runtime library dependency to caller projects — see TODO.md
// "Runtime introspection fallback" for the design notes. Until that lands,
// return an actionable error rather than producing a generated client that
// fails to compile.
func buildIntrospectionInfo(
	schema *ast.Schema,
	op *ast.OperationDefinition,
	fragments FragmentsByName,
) (IntrospectionInfo, error) {
	plan := PlanIntrospection(op)
	if !plan.Bakeable {
		return IntrospectionInfo{}, fmt.Errorf(
			"introspection operation %q uses variables %v in its arguments — "+
				"runtime introspection is not yet supported; remove the variables "+
				"and use literal arguments, or keep the operation in your genqlient "+
				"client. See TODO.md > Runtime introspection fallback for details",
			op.Name, plan.Variables,
		)
	}
	raw, err := ResolveIntrospection(schema, op, fragments, nil)
	if err != nil {
		return IntrospectionInfo{}, fmt.Errorf("bake response: %w", err)
	}
	return IntrospectionInfo{
		Name:     op.Name,
		Bakeable: true,
		Response: string(raw),
	}, nil
}

// fragmentsByName indexes the document's fragments by name. Returned as the
// FragmentsByName type the resolver expects.
func fragmentsByName(doc *ast.QueryDocument) FragmentsByName {
	out := FragmentsByName{}
	for _, f := range doc.Fragments {
		out[f.Name] = f
	}
	return out
}

// IsIntrospectionOnly reports whether op selects only introspection meta-fields
// at the operation root: `__schema`, `__type`, or `__typename`. Operations that
// mix introspection and business fields return false because defederator can't
// serve them — the business fields need a subgraph call and there's no
// supergraph-aware planner to merge that with the introspection response.
//
// Returns false for subscriptions: introspection over subscription streams is
// outside the GraphQL spec.
func IsIntrospectionOnly(op *ast.OperationDefinition) bool {
	if op == nil || op.Operation == ast.Subscription {
		return false
	}
	if len(op.SelectionSet) == 0 {
		return false
	}
	for _, sel := range op.SelectionSet {
		field, ok := sel.(*ast.Field)
		if !ok {
			return false
		}
		if !isIntrospectionFieldName(field.Name) {
			return false
		}
	}
	return true
}

// PlanIntrospection classifies op as either bakeable (codegen-time evaluation)
// or runtime (depends on at least one query variable). It returns the set of
// variable names that flow into introspection field arguments, deduplicated
// and in first-encounter order so generated code can iterate deterministically.
//
// The walk descends into inline fragments because they're part of the same
// operation; fragment spreads are followed through op.Document.Fragments at
// the caller's discretion (the resolver does this in step 2).
func PlanIntrospection(op *ast.OperationDefinition) IntrospectionPlan {
	if op == nil {
		return IntrospectionPlan{Bakeable: true}
	}
	seen := map[string]bool{}
	vars := []string{}
	collectVariableArgs(op.SelectionSet, seen, &vars)
	return IntrospectionPlan{
		Bakeable:  len(vars) == 0,
		Variables: vars,
	}
}

// collectVariableArgs walks sels and records every variable referenced in a
// field argument. Variables that appear multiple times are recorded once, in
// the order of first appearance.
func collectVariableArgs(sels ast.SelectionSet, seen map[string]bool, out *[]string) {
	for _, sel := range sels {
		switch s := sel.(type) {
		case *ast.Field:
			for _, arg := range s.Arguments {
				recordVariableValue(arg.Value, seen, out)
			}
			collectVariableArgs(s.SelectionSet, seen, out)
		case *ast.InlineFragment:
			collectVariableArgs(s.SelectionSet, seen, out)
		}
	}
}

// recordVariableValue records v's variable name if v is a variable, and
// recurses through list and object literals so an argument like
// `filter: {name: $n}` still discovers $n.
func recordVariableValue(v *ast.Value, seen map[string]bool, out *[]string) {
	if v == nil {
		return
	}
	switch v.Kind {
	case ast.Variable:
		if !seen[v.Raw] {
			seen[v.Raw] = true
			*out = append(*out, v.Raw)
		}
	case ast.ListValue, ast.ObjectValue:
		for _, child := range v.Children {
			recordVariableValue(child.Value, seen, out)
		}
	case ast.IntValue, ast.FloatValue, ast.StringValue, ast.BlockValue,
		ast.BooleanValue, ast.NullValue, ast.EnumValue:
		// Scalar literals — no variable to record.
	}
}

// isIntrospectionFieldName matches the three introspection root field names
// the GraphQL spec defines: __schema, __type, __typename. Any other name
// beginning `__` is non-standard and treated as a business field.
func isIntrospectionFieldName(name string) bool {
	switch name {
	case "__schema", "__type", "__typename":
		return true
	}
	return false
}
