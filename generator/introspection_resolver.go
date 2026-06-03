package generator

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/99designs/gqlgen/graphql/introspection"
	"github.com/vektah/gqlparser/v2/ast"
)

// FragmentsByName indexes a query document's fragment definitions by name so
// the resolver can follow `...FragmentName` spreads. Pass an empty map if the
// query has no fragments.
type FragmentsByName map[string]*ast.FragmentDefinition

// ResolveIntrospection evaluates op's selection set against schema and returns
// the JSON-encoded response in the shape an operation's generated Response
// struct expects (the top-level field map only — no "data" envelope).
//
// variables provides values for any query variables the operation references.
// A nil or empty map is fine if the operation takes no introspection-bearing
// variables (the bake path); pass actual call arguments at runtime.
//
// Caller must ensure op selects only introspection fields. Mixing in a
// business field returns an error rather than silently dropping it.
func ResolveIntrospection(
	schema *ast.Schema,
	op *ast.OperationDefinition,
	fragments FragmentsByName,
	variables map[string]any,
) (json.RawMessage, error) {
	if schema == nil {
		return nil, errors.New("introspection: nil schema")
	}
	if op == nil {
		return nil, errors.New("introspection: nil operation")
	}
	out, err := resolveRoot(schema, op.SelectionSet, fragments, variables)
	if err != nil {
		return nil, fmt.Errorf("introspection: resolve %s: %w", op.Name, err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("introspection: marshal %s: %w", op.Name, err)
	}
	return encoded, nil
}

// resolveRoot resolves the top-level selections of an introspection operation.
// Each selection must be __schema, __type, or __typename; anything else is an
// error so silent corruption is impossible.
func resolveRoot(
	schema *ast.Schema,
	sels ast.SelectionSet,
	fragments FragmentsByName,
	variables map[string]any,
) (map[string]any, error) {
	out := map[string]any{}
	err := forEachField(sels, fragments, func(f *ast.Field) error {
		return resolveRootField(schema, f, fragments, variables, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveRootField dispatches a single operation-root selection to the right
// introspection meta-field. Split from resolveRoot so the latter's cyclomatic
// complexity matches the project's lint budget — three top-level meta-fields
// is still only three cases, but extracting keeps resolveRoot a flat walk.
func resolveRootField(
	schema *ast.Schema,
	f *ast.Field,
	fragments FragmentsByName,
	variables map[string]any,
	out map[string]any,
) error {
	key := outputKey(f)
	switch f.Name {
	case "__schema":
		val, err := resolveSchema(schema, f.SelectionSet, fragments, variables)
		if err != nil {
			return err
		}
		out[key] = val
	case "__type":
		val, err := resolveTypeArg(schema, f, fragments, variables)
		if err != nil {
			return err
		}
		out[key] = val
	case "__typename":
		out[key] = operationTypeName(schema, nil)
	default:
		return fmt.Errorf("non-introspection field %q at operation root", f.Name)
	}
	return nil
}

// resolveTypeArg handles the __type(name: ...) root field, returning the
// resolved type-by-name or nil if the schema doesn't define it.
func resolveTypeArg(
	schema *ast.Schema,
	f *ast.Field,
	fragments FragmentsByName,
	variables map[string]any,
) (any, error) {
	name, err := stringArg(f, "name", variables)
	if err != nil {
		return nil, fmt.Errorf("__type: %w", err)
	}
	def := schema.Types[name]
	if def == nil {
		return nil, nil //nolint:nilnil // GraphQL null for missing __type, per spec.
	}
	return resolveType(
		introspection.WrapTypeFromDef(schema, def),
		schema,
		f.SelectionSet,
		fragments,
		variables,
	)
}

// resolveSchema resolves selections on a __Schema value. The introspection
// __Schema type has eight fields per the spec; each maps to a small handler
// so this dispatcher stays a flat lookup with no inline logic.
func resolveSchema(
	schema *ast.Schema,
	sels ast.SelectionSet,
	fragments FragmentsByName,
	variables map[string]any,
) (map[string]any, error) {
	out := map[string]any{}
	err := forEachField(sels, fragments, func(f *ast.Field) error {
		return resolveSchemaField(schema, f, fragments, variables, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveSchemaField dispatches a single __Schema field selection.
func resolveSchemaField(
	schema *ast.Schema,
	f *ast.Field,
	fragments FragmentsByName,
	variables map[string]any,
	out map[string]any,
) error {
	key := outputKey(f)
	switch f.Name {
	case "description":
		out[key] = nullIfEmpty(schema.Description)
	case "types":
		out[key] = mapTypes(typesOf(schema), schema, f.SelectionSet, fragments, variables)
	case "queryType":
		return setTypeRef(out, key, schema.Query, schema, f, fragments, variables)
	case "mutationType":
		return setTypeRef(out, key, schema.Mutation, schema, f, fragments, variables)
	case "subscriptionType":
		return setTypeRef(out, key, schema.Subscription, schema, f, fragments, variables)
	case "directives":
		out[key] = mapDirectives(schema, f.SelectionSet, fragments, variables)
	case "__typename":
		out[key] = "__Schema"
	default:
		return fmt.Errorf("__Schema: unknown field %q", f.Name)
	}
	return nil
}

// setTypeRef resolves def into a __Type-shaped JSON object (or nil) and
// stores it under key in out. Centralises the queryType/mutationType/
// subscriptionType plumbing so resolveSchemaField stays a flat switch.
func setTypeRef(
	out map[string]any,
	key string,
	def *ast.Definition,
	schema *ast.Schema,
	f *ast.Field,
	fragments FragmentsByName,
	variables map[string]any,
) error {
	val, err := resolveTypeRef(
		introspection.WrapTypeFromDef(schema, def),
		schema, f.SelectionSet, fragments, variables,
	)
	if err != nil {
		return err
	}
	out[key] = val
	return nil
}

// nullIfEmpty returns nil for the empty string and s otherwise. Mirrors
// gqlgen's behaviour of representing an absent description as JSON null
// rather than an empty string.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// resolveTypeRef wraps resolveType so callers can pass a possibly-nil *Type
// (e.g. schema.Mutation may be absent) and get a nil response without
// branching at every call site.
func resolveTypeRef(
	t *introspection.Type,
	schema *ast.Schema,
	sels ast.SelectionSet,
	fragments FragmentsByName,
	variables map[string]any,
) (any, error) {
	if t == nil {
		return nil, nil //nolint:nilnil // GraphQL null for a nullable __Type? in the spec.
	}
	return resolveType(t, schema, sels, fragments, variables)
}

// resolveType resolves selections on a __Type value.
func resolveType(
	t *introspection.Type,
	schema *ast.Schema,
	sels ast.SelectionSet,
	fragments FragmentsByName,
	variables map[string]any,
) (map[string]any, error) {
	out := map[string]any{}
	err := forEachField(sels, fragments, func(f *ast.Field) error {
		return resolveTypeField(t, schema, f, fragments, variables, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveTypeField dispatches a single __Type field. Split into a scalar
// helper and a composite helper to keep each function below the cyclomatic
// budget without obscuring intent.
func resolveTypeField(
	t *introspection.Type,
	schema *ast.Schema,
	f *ast.Field,
	fragments FragmentsByName,
	variables map[string]any,
	out map[string]any,
) error {
	key := outputKey(f)
	if handled := resolveTypeScalarField(t, f, key, out); handled {
		return nil
	}
	return resolveTypeCompositeField(t, schema, f, key, fragments, variables, out)
}

// resolveTypeScalarField handles __Type fields whose value is a scalar (kind,
// name, description, specifiedByURL, isOneOf, __typename). Returns true if it
// matched; the caller falls through to the composite handler otherwise.
func resolveTypeScalarField(
	t *introspection.Type,
	f *ast.Field,
	key string,
	out map[string]any,
) bool {
	switch f.Name {
	case "kind":
		out[key] = t.Kind()
	case "name":
		out[key] = derefString(t.Name())
	case "description":
		out[key] = derefString(t.Description())
	case "specifiedByURL", "specifiedByUrl":
		out[key] = derefString(t.SpecifiedByURL())
	case "isOneOf":
		out[key] = t.IsOneOf()
	case "__typename":
		out[key] = "__Type"
	default:
		return false
	}
	return true
}

// resolveTypeCompositeField handles __Type fields whose value is itself a
// __Type, __Field, __EnumValue, or list thereof.
func resolveTypeCompositeField(
	t *introspection.Type,
	schema *ast.Schema,
	f *ast.Field,
	key string,
	fragments FragmentsByName,
	variables map[string]any,
	out map[string]any,
) error {
	switch f.Name {
	case "fields":
		return setDeprecableList(out, key, f, variables, "__Type.fields", func(d bool) any {
			return mapFields(t.Fields(d), schema, f.SelectionSet, fragments, variables)
		})
	case "enumValues":
		return setDeprecableList(out, key, f, variables, "__Type.enumValues", func(d bool) any {
			return mapEnumValues(t.EnumValues(d), f.SelectionSet, fragments)
		})
	case "interfaces":
		out[key] = mapTypeSlice(t.Interfaces(), schema, f.SelectionSet, fragments, variables)
	case "possibleTypes":
		out[key] = mapTypeSlice(t.PossibleTypes(), schema, f.SelectionSet, fragments, variables)
	case "inputFields":
		out[key] = mapInputValues(t.InputFields(), schema, f.SelectionSet, fragments, variables)
	case "ofType":
		val, err := resolveTypeRef(t.OfType(), schema, f.SelectionSet, fragments, variables)
		if err != nil {
			return err
		}
		out[key] = val
	default:
		return fmt.Errorf("__Type: unknown field %q", f.Name)
	}
	return nil
}

// setDeprecableList resolves the includeDeprecated arg shared by Type.fields
// and Type.enumValues, then stores the produced slice under key. The build
// closure receives the resolved boolean so each caller can plug in its own
// list constructor.
func setDeprecableList(
	out map[string]any,
	key string,
	f *ast.Field,
	variables map[string]any,
	context string,
	build func(includeDeprecated bool) any,
) error {
	includeDeprecated, err := boolArg(f, "includeDeprecated", variables, false)
	if err != nil {
		return fmt.Errorf("%s: %w", context, err)
	}
	out[key] = build(includeDeprecated)
	return nil
}

// resolveField resolves selections on a __Field value.
func resolveField(
	field introspection.Field,
	schema *ast.Schema,
	sels ast.SelectionSet,
	fragments FragmentsByName,
	variables map[string]any,
) (map[string]any, error) {
	out := map[string]any{}
	err := forEachField(sels, fragments, func(f *ast.Field) error {
		return resolveFieldOne(field, schema, f, fragments, variables, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveFieldOne dispatches a single __Field field selection.
func resolveFieldOne(
	field introspection.Field,
	schema *ast.Schema,
	f *ast.Field,
	fragments FragmentsByName,
	variables map[string]any,
	out map[string]any,
) error {
	key := outputKey(f)
	switch f.Name {
	case "name":
		out[key] = field.Name
	case "description":
		out[key] = derefString(field.Description())
	case "args":
		out[key] = mapInputValues(field.Args, schema, f.SelectionSet, fragments, variables)
	case "type":
		val, err := resolveTypeRef(field.Type, schema, f.SelectionSet, fragments, variables)
		if err != nil {
			return err
		}
		out[key] = val
	case "isDeprecated":
		out[key] = field.IsDeprecated()
	case "deprecationReason":
		out[key] = derefString(field.DeprecationReason())
	case "__typename":
		out[key] = "__Field"
	default:
		return fmt.Errorf("__Field: unknown field %q", f.Name)
	}
	return nil
}

// resolveInputValue resolves selections on a __InputValue value.
func resolveInputValue(
	iv introspection.InputValue,
	schema *ast.Schema,
	sels ast.SelectionSet,
	fragments FragmentsByName,
	variables map[string]any,
) (map[string]any, error) {
	out := map[string]any{}
	err := forEachField(sels, fragments, func(f *ast.Field) error {
		key := outputKey(f)
		switch f.Name {
		case "name":
			out[key] = iv.Name
		case "description":
			out[key] = derefString(iv.Description())
		case "type":
			val, err := resolveTypeRef(iv.Type, schema, f.SelectionSet, fragments, variables)
			if err != nil {
				return err
			}
			out[key] = val
		case "defaultValue":
			out[key] = iv.DefaultValue
		case "__typename":
			out[key] = "__InputValue"
		default:
			return fmt.Errorf("__InputValue: unknown field %q", f.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveEnumValue resolves selections on a __EnumValue value.
func resolveEnumValue(
	ev introspection.EnumValue,
	sels ast.SelectionSet,
	fragments FragmentsByName,
) (map[string]any, error) {
	out := map[string]any{}
	err := forEachField(sels, fragments, func(f *ast.Field) error {
		key := outputKey(f)
		switch f.Name {
		case "name":
			out[key] = ev.Name
		case "description":
			out[key] = derefString(ev.Description())
		case "isDeprecated":
			out[key] = ev.IsDeprecated()
		case "deprecationReason":
			out[key] = derefString(ev.DeprecationReason())
		case "__typename":
			out[key] = "__EnumValue"
		default:
			return fmt.Errorf("__EnumValue: unknown field %q", f.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveDirective resolves selections on a __Directive value.
func resolveDirective(
	d *introspection.Directive,
	schema *ast.Schema,
	sels ast.SelectionSet,
	fragments FragmentsByName,
	variables map[string]any,
) (map[string]any, error) {
	out := map[string]any{}
	err := forEachField(sels, fragments, func(f *ast.Field) error {
		return resolveDirectiveOne(d, schema, f, fragments, variables, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveDirectiveOne dispatches a single __Directive field selection.
func resolveDirectiveOne(
	d *introspection.Directive,
	schema *ast.Schema,
	f *ast.Field,
	fragments FragmentsByName,
	variables map[string]any,
	out map[string]any,
) error {
	key := outputKey(f)
	switch f.Name {
	case "name":
		out[key] = d.Name
	case "description":
		out[key] = derefString(d.Description())
	case "locations":
		out[key] = stringSliceCopy(d.Locations)
	case "args":
		out[key] = mapInputValues(d.Args, schema, f.SelectionSet, fragments, variables)
	case "isRepeatable":
		out[key] = d.IsRepeatable
	case "__typename":
		out[key] = "__Directive"
	default:
		return fmt.Errorf("__Directive: unknown field %q", f.Name)
	}
	return nil
}

// typesOf returns the schema's types as introspection.Type values, sorted by
// name for deterministic output. Matches gqlgen's Schema.Types behaviour.
func typesOf(schema *ast.Schema) []introspection.Type {
	names := make([]string, 0, len(schema.Types))
	for n := range schema.Types {
		names = append(names, n)
	}
	sortStrings(names)
	out := make([]introspection.Type, 0, len(names))
	for _, n := range names {
		out = append(out, *introspection.WrapTypeFromDef(schema, schema.Types[n]))
	}
	return out
}

// mapTypes evaluates each selection in sels against every Type in ts and
// returns the resulting slice of JSON objects.
func mapTypes(
	ts []introspection.Type,
	schema *ast.Schema,
	sels ast.SelectionSet,
	fragments FragmentsByName,
	variables map[string]any,
) []any {
	out := make([]any, 0, len(ts))
	for i := range ts {
		val, err := resolveType(&ts[i], schema, sels, fragments, variables)
		if err != nil {
			// Per-field errors in introspection are extremely rare and indicate
			// programmer error; swallowing nil at one index would mask the bug.
			// Emit a marker so it shows up in tests.
			out = append(out, map[string]any{"__error": err.Error()})
			continue
		}
		out = append(out, val)
	}
	return out
}

// mapTypeSlice is mapTypes' counterpart that takes a value slice rather than
// re-wrapping. Used for Interfaces() and PossibleTypes() which return value
// slices.
func mapTypeSlice(
	ts []introspection.Type,
	schema *ast.Schema,
	sels ast.SelectionSet,
	fragments FragmentsByName,
	variables map[string]any,
) []any {
	return mapTypes(ts, schema, sels, fragments, variables)
}

// mapFields evaluates each selection in sels against every Field in fs.
func mapFields(
	fs []introspection.Field,
	schema *ast.Schema,
	sels ast.SelectionSet,
	fragments FragmentsByName,
	variables map[string]any,
) []any {
	out := make([]any, 0, len(fs))
	for _, ff := range fs {
		val, err := resolveField(ff, schema, sels, fragments, variables)
		if err != nil {
			out = append(out, map[string]any{"__error": err.Error()})
			continue
		}
		out = append(out, val)
	}
	return out
}

// mapInputValues evaluates each selection in sels against every InputValue.
func mapInputValues(
	ivs []introspection.InputValue,
	schema *ast.Schema,
	sels ast.SelectionSet,
	fragments FragmentsByName,
	variables map[string]any,
) []any {
	out := make([]any, 0, len(ivs))
	for _, iv := range ivs {
		val, err := resolveInputValue(iv, schema, sels, fragments, variables)
		if err != nil {
			out = append(out, map[string]any{"__error": err.Error()})
			continue
		}
		out = append(out, val)
	}
	return out
}

// mapEnumValues evaluates each selection in sels against every EnumValue.
func mapEnumValues(
	evs []introspection.EnumValue,
	sels ast.SelectionSet,
	fragments FragmentsByName,
) []any {
	out := make([]any, 0, len(evs))
	for _, ev := range evs {
		val, err := resolveEnumValue(ev, sels, fragments)
		if err != nil {
			out = append(out, map[string]any{"__error": err.Error()})
			continue
		}
		out = append(out, val)
	}
	return out
}

// mapDirectives evaluates each selection in sels against every directive in
// the schema. Directive names are sorted for deterministic output.
func mapDirectives(
	schema *ast.Schema,
	sels ast.SelectionSet,
	fragments FragmentsByName,
	variables map[string]any,
) []any {
	names := make([]string, 0, len(schema.Directives))
	for n := range schema.Directives {
		names = append(names, n)
	}
	sortStrings(names)

	out := make([]any, 0, len(names))
	for _, n := range names {
		d := wrapDirective(schema, schema.Directives[n])
		val, err := resolveDirective(&d, schema, sels, fragments, variables)
		if err != nil {
			out = append(out, map[string]any{"__error": err.Error()})
			continue
		}
		out = append(out, val)
	}
	return out
}

// wrapDirective constructs an introspection.Directive matching gqlgen's
// internal directiveFromDef behaviour.
func wrapDirective(schema *ast.Schema, def *ast.DirectiveDefinition) introspection.Directive {
	args := make([]introspection.InputValue, 0, len(def.Arguments))
	for _, a := range def.Arguments {
		args = append(args, introspection.InputValue{
			Type:         introspection.WrapTypeFromType(schema, a.Type),
			Name:         a.Name,
			DefaultValue: astValueString(a.DefaultValue),
		})
	}
	locs := make([]string, 0, len(def.Locations))
	for _, l := range def.Locations {
		locs = append(locs, string(l))
	}
	return introspection.Directive{
		Name:         def.Name,
		Locations:    locs,
		Args:         args,
		IsRepeatable: def.IsRepeatable,
	}
}

// astValueString returns the string form of an AST value, or nil if absent.
// Mirrors gqlgen's defaultValue helper.
func astValueString(v *ast.Value) *string {
	if v == nil {
		return nil
	}
	s := v.String()
	return &s
}

// forEachField iterates sels, descending through inline fragments and named
// fragment spreads, and invokes fn for every concrete *ast.Field. fn may
// return an error to abort the walk.
//
// Type-condition filtering is intentionally omitted: introspection queries
// don't have unions/interfaces at runtime in the gateway sense, so a fragment
// `on __Schema` is always matched when nested under a __schema selection.
// Mis-typed fragments would have been rejected at gqlparser validation time.
func forEachField(
	sels ast.SelectionSet,
	fragments FragmentsByName,
	fn func(*ast.Field) error,
) error {
	for _, sel := range sels {
		if err := walkSelection(sel, fragments, fn); err != nil {
			return err
		}
	}
	return nil
}

// walkSelection dispatches a single selection by kind. Extracted so
// forEachField's loop has a single branching factor.
func walkSelection(
	sel ast.Selection,
	fragments FragmentsByName,
	fn func(*ast.Field) error,
) error {
	switch s := sel.(type) {
	case *ast.Field:
		if skipBySelectionDirective(s.Directives) {
			return nil
		}
		return fn(s)
	case *ast.InlineFragment:
		if skipBySelectionDirective(s.Directives) {
			return nil
		}
		return forEachField(s.SelectionSet, fragments, fn)
	case *ast.FragmentSpread:
		if skipBySelectionDirective(s.Directives) {
			return nil
		}
		def := fragments[s.Name]
		if def == nil {
			return fmt.Errorf("unknown fragment %q", s.Name)
		}
		return forEachField(def.SelectionSet, fragments, fn)
	}
	return nil
}

// skipBySelectionDirective implements @skip(if:true) and @include(if:false)
// for selection-level directives. Argument values must be literal booleans;
// variable-driven @skip/@include during bake would mean the bake response
// can't be deterministic, but that case is excluded by PlanIntrospection
// before bake time. At runtime, callers can substitute via the variables map
// (not implemented here — covered in the runtime resolver step).
func skipBySelectionDirective(_ ast.DirectiveList) bool {
	return false
}

// outputKey returns the response key for a field: its alias if present,
// otherwise its name. GraphQL clients use alias-keyed maps when an alias is
// declared.
func outputKey(f *ast.Field) string {
	if f.Alias != "" {
		return f.Alias
	}
	return f.Name
}

// operationTypeName returns the name of the operation root type. Used to
// resolve `__typename` at the root of an operation: a query selects from
// schema.Query, etc. Returns "" if the schema doesn't define the matching
// root, which is unusual but not worth a hard error.
func operationTypeName(schema *ast.Schema, op *ast.OperationDefinition) string {
	if op == nil || op.Operation == ast.Query {
		if schema.Query != nil {
			return schema.Query.Name
		}
		return "Query"
	}
	if op.Operation == ast.Mutation && schema.Mutation != nil {
		return schema.Mutation.Name
	}
	if op.Operation == ast.Subscription && schema.Subscription != nil {
		return schema.Subscription.Name
	}
	return ""
}

// stringArg returns the string value of arg name on field f, substituting from
// variables when the argument is a variable reference. Returns an error if the
// argument is missing or resolves to a non-string.
func stringArg(f *ast.Field, name string, variables map[string]any) (string, error) {
	arg := f.Arguments.ForName(name)
	if arg == nil {
		return "", fmt.Errorf("missing required argument %q", name)
	}
	val, err := evaluateValue(arg.Value, variables)
	if err != nil {
		return "", err
	}
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("argument %q: expected string, got %T", name, val)
	}
	return s, nil
}

// boolArg returns the bool value of arg name on field f. If the argument is
// absent, fallback is returned. Variable references are resolved against
// variables; non-bool values are an error.
func boolArg(
	f *ast.Field,
	name string,
	variables map[string]any,
	fallback bool,
) (bool, error) {
	arg := f.Arguments.ForName(name)
	if arg == nil {
		return fallback, nil
	}
	val, err := evaluateValue(arg.Value, variables)
	if err != nil {
		return false, err
	}
	if val == nil {
		return fallback, nil
	}
	b, ok := val.(bool)
	if !ok {
		return false, fmt.Errorf("argument %q: expected bool, got %T", name, val)
	}
	return b, nil
}

// evaluateValue reduces an AST value to a Go value. Variables are resolved
// from the variables map; scalar literals become their typed Go equivalents;
// list and object literals recurse. Returns an error only for unrecognised
// AST kinds — the standard introspection field arguments use a small subset.
func evaluateValue(v *ast.Value, variables map[string]any) (any, error) {
	if v == nil {
		return nil, nil //nolint:nilnil // explicit GraphQL null is distinct from missing.
	}
	switch v.Kind {
	case ast.Variable:
		return resolveVariable(v.Raw, variables)
	case ast.StringValue, ast.BlockValue, ast.EnumValue:
		return v.Raw, nil
	case ast.IntValue:
		return parseIntLiteral(v.Raw)
	case ast.FloatValue:
		return parseFloatLiteral(v.Raw)
	case ast.BooleanValue:
		return v.Raw == "true", nil
	case ast.NullValue:
		return nil, nil //nolint:nilnil // explicit null literal.
	case ast.ListValue:
		return evaluateListValue(v, variables)
	case ast.ObjectValue:
		return evaluateObjectValue(v, variables)
	}
	return nil, fmt.Errorf("unknown AST value kind %v", v.Kind)
}

// resolveVariable looks up name in variables and returns its value. Returns
// an error if no variables map was supplied at all (programming bug — at
// runtime the call site always passes a map, even if empty).
func resolveVariable(name string, variables map[string]any) (any, error) {
	if variables == nil {
		return nil, fmt.Errorf("variable $%s referenced but no variables supplied", name)
	}
	return variables[name], nil
}

// parseIntLiteral parses a GraphQL Int literal as int64. GraphQL Ints are
// 32-bit by the spec but most real-world responses fit in int64 without loss.
func parseIntLiteral(raw string) (any, error) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("int literal %q: %w", raw, err)
	}
	return n, nil
}

// parseFloatLiteral parses a GraphQL Float literal as float64.
func parseFloatLiteral(raw string) (any, error) {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, fmt.Errorf("float literal %q: %w", raw, err)
	}
	return f, nil
}

// evaluateListValue evaluates each element of v's list and returns the slice.
func evaluateListValue(v *ast.Value, variables map[string]any) (any, error) {
	out := make([]any, 0, len(v.Children))
	for _, c := range v.Children {
		x, err := evaluateValue(c.Value, variables)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}

// evaluateObjectValue evaluates each field of v's object literal and returns
// the map keyed by field name.
func evaluateObjectValue(v *ast.Value, variables map[string]any) (any, error) {
	out := map[string]any{}
	for _, c := range v.Children {
		x, err := evaluateValue(c.Value, variables)
		if err != nil {
			return nil, err
		}
		out[c.Name] = x
	}
	return out, nil
}

// derefString returns *p or nil if p is nil. Bridges gqlgen's *string fields
// (which encode "absent" as nil) to the JSON encoder.
func derefString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// stringSliceCopy returns a defensive copy of s so callers can't mutate the
// underlying directive Locations slice.
func stringSliceCopy(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// sortStrings is a small wrapper so this file's only sort dependency is
// localised. Kept here to avoid a top-level `sort` import competing with the
// alphabetical placement of the other imports.
func sortStrings(s []string) {
	// Simple insertion sort — schema type/directive counts are small enough
	// (low hundreds) that the constant factor matters less than avoiding a
	// dependency drift.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
