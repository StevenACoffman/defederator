// Package execengine executes a pre-built federation Plan against subgraph HTTP endpoints.
// It has no dependency on the query planner — all routing decisions (which subgraph owns
// which field, key fields for entity resolution, etc.) are encoded in the Plan at build time.
package execengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
)

// Plan is a fully resolved execution plan. URLs are baked in at plan-build time;
// no routing table is needed at execute time.
type Plan struct {
	Fetches       []Fetch
	EntityFetches []EntityFetch
	Projection    []*FieldProjection
}

// Fetch is a query to send to one subgraph URL.
type Fetch struct {
	URL       string
	Query     string
	Variables []string // variable names from the outer operation used by this fetch
}

// EntityFetch resolves cross-subgraph entity fields after the initial fetches complete.
type EntityFetch struct {
	URL            string
	TypeName       string
	KeyFields      []string
	RequiresFields []string
	Selection      string   // GraphQL selection body, e.g. "email\nname\n"
	Query          string   // full entity query with variable declarations; supersedes Selection when non-empty
	Variables      []string // operation variable names to forward beyond "representations"
	ParentPath     []string // JSON path to the parent object in merged data
	IsParentList   bool
}

// FieldProjection is a node in the user-requested selection tree. Used to strip
// planner-added fields (key fields, __typename, @requires pre-fetch fields) from
// the final merged response.
type FieldProjection struct {
	Key      string
	Children []*FieldProjection
}

// rawSpec is the JSON-decodable form of an enum-keyed plan spec.
// SubgraphEnum fields are resolved to URLs by Resolve.
type rawSpec struct {
	Fetches       []rawFetch         `json:"fetches"`
	EntityFetches []rawEntityFetch   `json:"entityFetches,omitempty"`
	Projection    []*FieldProjection `json:"projection,omitempty"`
}

type rawFetch struct {
	SubgraphEnum string   `json:"subgraphEnum"`
	Query        string   `json:"query"`
	Variables    []string `json:"variables,omitempty"`
}

type rawEntityFetch struct {
	SubgraphEnum   string   `json:"subgraphEnum"`
	TypeName       string   `json:"typeName"`
	KeyFields      []string `json:"keyFields"`
	RequiresFields []string `json:"requiresFields,omitempty"`
	Selection      string   `json:"selection"`
	Query          string   `json:"query,omitempty"`
	Variables      []string `json:"variables,omitempty"`
	ParentPath     []string `json:"parentPath"`
	IsParentList   bool     `json:"isParentList,omitempty"`
}

// urlSpec is the JSON-decodable form of a URL-keyed plan spec.
// URLs are embedded directly, so no enum-to-URL lookup is needed at runtime.
type urlSpec struct {
	Fetches       []urlFetch         `json:"fetches"`
	EntityFetches []urlEntityFetch   `json:"entityFetches,omitempty"`
	Projection    []*FieldProjection `json:"projection,omitempty"`
}

type urlFetch struct {
	URL       string   `json:"url"`
	Query     string   `json:"query"`
	Variables []string `json:"variables,omitempty"`
}

type urlEntityFetch struct {
	URL            string   `json:"url"`
	TypeName       string   `json:"typeName"`
	KeyFields      []string `json:"keyFields"`
	RequiresFields []string `json:"requiresFields,omitempty"`
	Selection      string   `json:"selection"`
	Query          string   `json:"query,omitempty"`
	Variables      []string `json:"variables,omitempty"`
	ParentPath     []string `json:"parentPath"`
	IsParentList   bool     `json:"isParentList,omitempty"`
}

// GraphQLError is a single error object in a GraphQL response.
type GraphQLError struct {
	Message    string           `json:"message"`
	Locations  []map[string]int `json:"locations,omitempty"`
	Path       []any            `json:"path,omitempty"`
	Extensions map[string]any   `json:"extensions,omitempty"`
}

// graphqlRequest is the JSON body sent to a subgraph.
type graphqlRequest struct {
	Query         string `json:"query"`
	OperationName string `json:"operationName,omitempty"`
	Variables     any    `json:"variables,omitempty"`
}

// rawMerged is the internal accumulator type: top-level field name → raw JSON bytes.
// Values are kept as json.RawMessage until final serialization to avoid
// interface{} boxing and intermediate allocations.
type rawMerged = map[string]json.RawMessage

// entityQuery returns the complete entity query string to send to the subgraph.
// If Query is set (built at plan time with all variable declarations), it is returned directly.
// Otherwise falls back to building the query from Selection at runtime for backward compat.
func (ef *EntityFetch) entityQuery() string {
	if ef.Query != "" {
		return ef.Query
	}
	return buildEntityQuery(ef.TypeName, ef.Selection)
}

// buildEntityFetchVars merges entity representations with any operation variables
// the entity fetch selection references. opVars must be map[string]any;
// struct-typed vars cannot be subset-extracted.
func buildEntityFetchVars(reps any, opVars any, varNames []string) map[string]any {
	m := map[string]any{"representations": reps}
	if len(varNames) == 0 {
		return m
	}
	opMap, ok := opVars.(map[string]any)
	if !ok {
		return m
	}
	for _, name := range varNames {
		if v, ok := opMap[name]; ok {
			m[name] = v
		}
	}
	return m
}

// Resolve decodes a JSON-encoded plan spec and returns a Plan with subgraph enum names
// substituted by their URLs from urls. Returns an error if any enum name is absent from urls.
func Resolve(specJSON string, urls map[string]string) (*Plan, error) {
	var raw rawSpec
	if err := json.Unmarshal([]byte(specJSON), &raw); err != nil {
		return nil, fmt.Errorf("execengine: decode plan spec: %w", err)
	}
	plan := &Plan{
		Fetches:       make([]Fetch, 0, len(raw.Fetches)),
		EntityFetches: make([]EntityFetch, 0, len(raw.EntityFetches)),
		Projection:    raw.Projection,
	}
	for _, f := range raw.Fetches {
		url, ok := urls[f.SubgraphEnum]
		if !ok {
			return nil, fmt.Errorf("execengine: subgraph enum %q not in URL map", f.SubgraphEnum)
		}
		plan.Fetches = append(plan.Fetches, Fetch{
			URL:       url,
			Query:     f.Query,
			Variables: f.Variables,
		})
	}
	for _, ef := range raw.EntityFetches {
		url, ok := urls[ef.SubgraphEnum]
		if !ok {
			return nil, fmt.Errorf("execengine: subgraph enum %q not in URL map", ef.SubgraphEnum)
		}
		plan.EntityFetches = append(plan.EntityFetches, EntityFetch{
			URL:            url,
			TypeName:       ef.TypeName,
			KeyFields:      ef.KeyFields,
			RequiresFields: ef.RequiresFields,
			Selection:      ef.Selection,
			Query:          ef.Query,
			Variables:      ef.Variables,
			ParentPath:     ef.ParentPath,
			IsParentList:   ef.IsParentList,
		})
	}
	return plan, nil
}

// ResolveURLSpec decodes a URL-keyed JSON plan spec into a Plan.
// Unlike Resolve, it requires no URL map — subgraph URLs are embedded in the spec JSON.
func ResolveURLSpec(specJSON string) (*Plan, error) {
	var raw urlSpec
	if err := json.Unmarshal([]byte(specJSON), &raw); err != nil {
		return nil, fmt.Errorf("execengine: decode url plan spec: %w", err)
	}
	plan := &Plan{
		Fetches:       make([]Fetch, 0, len(raw.Fetches)),
		EntityFetches: make([]EntityFetch, 0, len(raw.EntityFetches)),
		Projection:    raw.Projection,
	}
	for _, f := range raw.Fetches {
		plan.Fetches = append(plan.Fetches, Fetch(f))
	}
	for _, ef := range raw.EntityFetches {
		plan.EntityFetches = append(plan.EntityFetches, EntityFetch(ef))
	}
	return plan, nil
}

// ExecuteAndUnmarshal runs plan and JSON-unmarshals the merged result into dest.
//
// Fast path: when the plan has exactly one fetch and no entity fetches,
// the subgraph response is unmarshaled directly into dest — no intermediate
// map[string]any allocation or re-marshal step.
func ExecuteAndUnmarshal(
	ctx context.Context,
	plan *Plan,
	vars any,
	client *http.Client,
	dest any,
) error {
	if client == nil {
		client = http.DefaultClient
	}

	// Typed destinations (structs, named types) let json.Unmarshal drop planner-added
	// fields (__typename, key fields, @requires fields) silently — projection is redundant.
	// Only untyped destinations (*any, *map[string]any) need projection to avoid leaking
	// planner-internal fields to callers.
	_, isAny := dest.(*any)
	_, isMap := dest.(*map[string]any)
	skipProj := !isAny && !isMap

	// Single-subgraph fast path: skip execute() entirely.
	// doGraphQLInto decodes the wrapper and data in one pass by pre-setting the
	// decode target — no intermediate json.RawMessage allocation or second Unmarshal.
	if len(plan.Fetches) == 1 && len(plan.EntityFetches) == 0 {
		f := plan.Fetches[0]
		errs, err := doGraphQLInto(
			ctx,
			client,
			f.URL,
			f.Query,
			"",
			filterVars(vars, f.Variables),
			dest,
		)
		if err != nil {
			return fmt.Errorf("execengine: fetch %s: %w", f.URL, err)
		}
		if len(errs) > 0 {
			return fmt.Errorf("execengine: %v", errs)
		}
		return nil
	}

	merged, errs, err := execute(ctx, plan, vars, client, skipProj)
	if err != nil {
		return err
	}
	if len(errs) > 0 {
		return fmt.Errorf("execengine: %v", errs)
	}
	return unmarshalRawMergedInto(merged, dest)
}

func (e GraphQLError) Error() string { return e.Message }

// execute runs plan against real HTTP subgraph endpoints.
// Initial fetches run in parallel (or inline for a single fetch); entity fetches
// run sequentially in plan order.
// Returns the merged result as rawMerged (map[string]json.RawMessage) so the caller
// can decode directly into a typed destination without an intermediate serialization
// step. When skipProjection is true, applyProjection is skipped — the caller's
// json.Unmarshal into a typed struct silently drops planner-added fields.
func execute(
	ctx context.Context,
	plan *Plan,
	variables any,
	client *http.Client,
	skipProjection bool,
) (rawMerged, []GraphQLError, error) {
	if client == nil {
		client = http.DefaultClient
	}

	merged := make(rawMerged)
	var allErrors []GraphQLError

	if len(plan.Fetches) == 1 {
		// Single initial fetch: inline call avoids goroutine and WaitGroup overhead.
		f := plan.Fetches[0]
		data, errs, err := doGraphQLIntoMerged(
			ctx,
			client,
			f.URL,
			f.Query,
			"",
			filterVars(variables, f.Variables),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("execengine: fetch %s: %w", f.URL, err)
		}
		allErrors = append(allErrors, errs...)
		if data != nil {
			merged = data
		}
	} else {
		type fetchResult struct {
			data   rawMerged
			errors []GraphQLError
			err    error
		}
		results := make([]fetchResult, len(plan.Fetches))
		var wg sync.WaitGroup

		for i, fetch := range plan.Fetches {
			wg.Add(1)
			go func(i int, fetch Fetch) {
				defer wg.Done()
				data, errs, err := doGraphQLIntoMerged(
					ctx,
					client,
					fetch.URL,
					fetch.Query,
					"",
					filterVars(variables, fetch.Variables),
				)
				if err != nil {
					results[i] = fetchResult{
						err: fmt.Errorf("execengine: fetch %s: %w", fetch.URL, err),
					}
					return
				}
				results[i] = fetchResult{data: data, errors: errs}
			}(i, fetch)
		}
		wg.Wait()

		for _, r := range results {
			if r.err != nil {
				return nil, nil, r.err
			}
			allErrors = append(allErrors, r.errors...)
			for k, v := range r.data {
				merged[k] = v
			}
		}
	}

	for _, ef := range plan.EntityFetches {
		reps, err := collectRepresentations(
			merged,
			ef.ParentPath,
			ef.TypeName,
			ef.KeyFields,
			ef.RequiresFields,
			ef.IsParentList,
		)
		if err != nil {
			allErrors = append(allErrors, GraphQLError{
				Message: fmt.Sprintf("execengine: entity fetch for %s: %s", ef.TypeName, err),
			})
			continue
		}
		if len(reps) == 0 {
			continue
		}

		entityQuery := ef.entityQuery()
		entityVars := buildEntityFetchVars(reps, variables, ef.Variables)

		// Decode wrapper and _entities in one pass — no intermediate json.RawMessage.
		entityRaw, err := httpPost(ctx, client, ef.URL, entityQuery, "", entityVars)
		if err != nil {
			allErrors = append(allErrors, GraphQLError{
				Message: fmt.Sprintf(
					"execengine: entity fetch %s/%s: %s",
					ef.URL,
					ef.TypeName,
					err,
				),
			})
			continue
		}
		var entityWrapper struct {
			Data struct {
				Entities []json.RawMessage `json:"_entities"`
			} `json:"data"`
			Errors []GraphQLError `json:"errors,omitempty"`
		}
		if err := json.Unmarshal(entityRaw, &entityWrapper); err != nil {
			allErrors = append(allErrors, GraphQLError{
				Message: fmt.Sprintf(
					"execengine: decode entity response %s/%s: %s",
					ef.URL,
					ef.TypeName,
					err,
				),
			})
			continue
		}
		allErrors = append(allErrors, entityWrapper.Errors...)
		mergeEntityResults(merged, ef.ParentPath, entityWrapper.Data.Entities, ef.IsParentList)
	}

	if len(plan.Projection) > 0 && !skipProjection {
		var err error
		merged, err = applyProjection(merged, plan.Projection)
		if err != nil {
			return nil, allErrors, fmt.Errorf("execengine: apply projection: %w", err)
		}
	}
	return merged, allErrors, nil
}

// applyProjection trims data to only the fields in proj, discarding planner-added fields.
// Values for fields with children are decoded and recursively filtered; leaf values are
// kept as raw bytes.
//
//nolint:revive // cognitive complexity is inherent in recursive JSON projection
func applyProjection(data rawMerged, proj []*FieldProjection) (rawMerged, error) {
	if len(proj) == 0 {
		return data, nil
	}
	result := make(rawMerged, len(proj))
	for _, p := range proj {
		v, ok := data[p.Key]
		if !ok {
			continue
		}
		if len(p.Children) == 0 {
			result[p.Key] = v
			continue
		}
		// Nested field: v is a raw JSON object or array; decode to apply child projection.
		if len(v) > 0 && v[0] == '[' {
			var arr []json.RawMessage
			if err := json.Unmarshal(v, &arr); err != nil {
				return nil, fmt.Errorf("decode array at %q: %w", p.Key, err)
			}
			out := make([]json.RawMessage, len(arr))
			for i, elem := range arr {
				if len(elem) > 0 && elem[0] == '{' {
					var obj rawMerged
					if err := json.Unmarshal(elem, &obj); err != nil {
						return nil, fmt.Errorf("decode element %d at %q: %w", i, p.Key, err)
					}
					filtered, err := applyProjection(obj, p.Children)
					if err != nil {
						return nil, err
					}
					b, err := marshalRawMerged(filtered)
					if err != nil {
						return nil, err
					}
					out[i] = b
				} else {
					out[i] = elem
				}
			}
			result[p.Key] = marshalRawList(out)
		} else if len(v) > 0 && v[0] == '{' {
			var obj rawMerged
			if err := json.Unmarshal(v, &obj); err != nil {
				return nil, fmt.Errorf("decode object at %q: %w", p.Key, err)
			}
			filtered, err := applyProjection(obj, p.Children)
			if err != nil {
				return nil, err
			}
			b, err := marshalRawMerged(filtered)
			if err != nil {
				return nil, err
			}
			result[p.Key] = b
		} else {
			result[p.Key] = v
		}
	}
	return result, nil
}

// httpPost marshals a GraphQL request, POSTs it to url, and returns the raw response body.
func httpPost(
	ctx context.Context,
	client *http.Client,
	url, query, operationName string,
	variables any,
) ([]byte, error) {
	body, err := json.Marshal(graphqlRequest{
		Query:         query,
		OperationName: operationName,
		Variables:     variables,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return raw, nil
}

// doGraphQLIntoMerged sends a GraphQL POST and decodes the response wrapper and data
// field in one pass, populating a rawMerged map directly. This avoids the two-step
// pattern of decoding data into json.RawMessage first and then into rawMerged.
func doGraphQLIntoMerged(
	ctx context.Context,
	client *http.Client,
	url, query, operationName string,
	variables any,
) (rawMerged, []GraphQLError, error) {
	raw, err := httpPost(ctx, client, url, query, operationName, variables)
	if err != nil {
		return nil, nil, err
	}
	var wrapper struct {
		Data   rawMerged      `json:"data"`
		Errors []GraphQLError `json:"errors,omitempty"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, nil, fmt.Errorf("decode response: %w", err)
	}
	return wrapper.Data, wrapper.Errors, nil
}

// doGraphQLInto sends a GraphQL POST and decodes the response wrapper and data in one
// pass by pre-setting the decode target to dest before unmarshaling. When dest holds a
// non-nil pointer, encoding/json's indirect() follows the interface to the concrete type
// and populates it directly — eliminating the intermediate json.RawMessage allocation
// and the second Unmarshal call that doGraphQL+Unmarshal would require.
func doGraphQLInto(
	ctx context.Context,
	client *http.Client,
	url, query, operationName string,
	variables, dest any,
) ([]GraphQLError, error) {
	raw, err := httpPost(ctx, client, url, query, operationName, variables)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Data   any            `json:"data"`
		Errors []GraphQLError `json:"errors,omitempty"`
	}
	wrapper.Data = dest
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return wrapper.Errors, nil
}

// buildEntityQuery constructs the _entities query for entity resolution.
func buildEntityQuery(typeName, selection string) string {
	lines := strings.Split(strings.TrimRight(selection, "\n"), "\n")
	indented := make([]string, 0, len(lines))
	for _, l := range lines {
		indented = append(indented, "      "+l)
	}
	return fmt.Sprintf(
		"query($representations: [_Any!]!) {\n  _entities(representations: $representations) {\n    ... on %s {\n%s\n    }\n  }\n}",
		typeName,
		strings.Join(indented, "\n"),
	)
}

// collectRepresentations extracts entity representations from merged data at path.
// Arrays at any step are fanned out — all leaves across nested arrays are collected
// in traversal order, one representation per leaf.
func collectRepresentations(
	data rawMerged,
	path []string,
	typeName string,
	keyFields, requiresFields []string,
	isList bool,
) ([]map[string]json.RawMessage, error) {
	if len(path) == 0 {
		return nil, nil
	}
	v, ok := data[path[0]]
	if !ok {
		return nil, nil
	}
	leaves, err := collectLeavesRaw(v, path[1:], isList)
	if err != nil || len(leaves) == 0 {
		return nil, err
	}
	typeNameJSON, _ := json.Marshal(typeName)
	reps := make([]map[string]json.RawMessage, 0, len(leaves))
	for _, leaf := range leaves {
		var obj rawMerged
		if err := json.Unmarshal(leaf, &obj); err != nil {
			continue
		}
		rep, err := buildRepresentation(obj, typeNameJSON, keyFields, requiresFields)
		if err != nil {
			return nil, err
		}
		reps = append(reps, rep)
	}
	return reps, nil
}

// collectLeavesRaw traverses raw JSON following path, fanning out through any JSON arrays
// encountered at intermediate steps. Returns leaf values in traversal order.
// When isList is true the terminal JSON value is itself unwrapped as an array.
//
//nolint:revive // cognitive complexity is inherent in recursive JSON traversal
func collectLeavesRaw(raw json.RawMessage, path []string, isList bool) ([]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(path) == 0 {
		if isList {
			var list []json.RawMessage
			if err := json.Unmarshal(raw, &list); err != nil {
				return nil, err
			}
			return list, nil
		}
		return []json.RawMessage{raw}, nil
	}
	switch raw[0] {
	case '{':
		var obj rawMerged
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("decode at %q: %w", path[0], err)
		}
		v, ok := obj[path[0]]
		if !ok {
			return nil, nil
		}
		return collectLeavesRaw(v, path[1:], isList)
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, err
		}
		var results []json.RawMessage
		for _, elem := range arr {
			sub, err := collectLeavesRaw(elem, path, isList) // same path: array is transparent
			if err != nil {
				return nil, err
			}
			results = append(results, sub...)
		}
		return results, nil
	}
	return nil, nil
}

// buildRepresentation constructs a single entity representation from an object's raw fields.
// typeNameJSON is the pre-encoded JSON string for __typename.
func buildRepresentation(
	obj rawMerged,
	typeNameJSON json.RawMessage,
	keyFields, requiresFields []string,
) (map[string]json.RawMessage, error) {
	rep := make(map[string]json.RawMessage, 1+len(keyFields)+len(requiresFields))
	rep["__typename"] = typeNameJSON
	for _, kf := range keyFields {
		v, ok := obj[kf]
		if !ok {
			return nil, fmt.Errorf("key field %q not found in response", kf)
		}
		rep[kf] = v
	}
	for _, rf := range requiresFields {
		v, ok := obj[rf]
		if !ok {
			return nil, fmt.Errorf("requires field %q not found in response", rf)
		}
		rep[rf] = v
	}
	return rep, nil
}

// mergeEntityResults merges _entities response data back into merged at path,
// consuming entities in traversal order (same order collectRepresentations produced them).
// Returns the unconsumed tail of entities — callers that don't need the remainder
// may ignore the return value.
//
//nolint:revive // cognitive complexity is inherent in recursive merge with array fan-out
func mergeEntityResults(
	data rawMerged,
	path []string,
	entities []json.RawMessage,
	isList bool,
) []json.RawMessage {
	if len(path) == 0 || len(entities) == 0 {
		return entities
	}

	if len(path) == 1 {
		target := data[path[0]]
		if isList {
			var list []json.RawMessage
			if err := json.Unmarshal(target, &list); err != nil {
				return entities
			}
			for i := range list {
				if len(entities) == 0 {
					break
				}
				if m := mergeRawObjects(list[i], entities[0]); m != nil {
					list[i] = m
				}
				entities = entities[1:]
			}
			data[path[0]] = marshalRawList(list)
		} else {
			if m := mergeRawObjects(target, entities[0]); m != nil {
				data[path[0]] = m
			}
			entities = entities[1:]
		}
		return entities
	}

	// Navigate one level deeper, recursing into objects or each element of an array.
	// Arrays are transparent: fan through each element and consume entities in order.
	next := data[path[0]]
	if len(next) == 0 {
		return entities
	}
	if next[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(next, &arr); err != nil {
			return entities
		}
		for i, elem := range arr {
			if len(elem) == 0 || elem[0] != '{' {
				continue
			}
			var sub rawMerged
			if err := json.Unmarshal(elem, &sub); err != nil {
				continue
			}
			entities = mergeEntityResults(sub, path[1:], entities, isList)
			if b, err := marshalRawMerged(sub); err == nil {
				arr[i] = b
			}
		}
		data[path[0]] = marshalRawList(arr)
	} else if next[0] == '{' {
		var sub rawMerged
		if err := json.Unmarshal(next, &sub); err != nil {
			return entities
		}
		entities = mergeEntityResults(sub, path[1:], entities, isList)
		if b, err := marshalRawMerged(sub); err == nil {
			data[path[0]] = b
		}
	}
	return entities
}

// mergeRawObjects merges the fields of src into dst (both raw JSON objects) by byte
// splicing: trim the trailing '}' from dst, trim the leading '{' from src, join with ','.
// This avoids a decode→merge-map→encode cycle (2 unmarshals + 1 marshal) per call.
//
// Federation contract: entity subgraphs return only fields they own. Key fields may
// appear in both (the entity echoes back the lookup key), but their values are
// identical — duplicate JSON keys with the same value are benign in all JSON parsers.
// Returns nil if either argument is not a JSON object.
func mergeRawObjects(dst, src json.RawMessage) json.RawMessage {
	dst = bytes.TrimSpace(dst)
	src = bytes.TrimSpace(src)
	if len(dst) < 2 || dst[0] != '{' || len(src) < 2 || src[0] != '{' {
		return nil
	}
	if len(dst) == 2 { // "{}"
		return src
	}
	if len(src) == 2 { // "{}"
		return dst
	}
	out := make([]byte, 0, len(dst)+len(src))
	out = append(out, dst[:len(dst)-1]...) // everything except trailing '}'
	out = append(out, ',')
	out = append(out, src[1:]...) // everything except leading '{'
	return out
}

// marshalRawList encodes a []json.RawMessage as a JSON array by byte concatenation,
// avoiding json.Marshal's per-element reflection overhead.
func marshalRawList(list []json.RawMessage) []byte {
	if len(list) == 0 {
		return []byte("[]")
	}
	n := 2 // '[' + ']'
	for _, elem := range list {
		n += len(elem) + 1 // element + ','
	}
	out := make([]byte, 0, n)
	out = append(out, '[')
	for i, elem := range list {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, elem...)
	}
	out = append(out, ']')
	return out
}

// marshalRawMerged encodes a rawMerged (map[string]json.RawMessage) as a JSON object
// by byte concatenation. Keys are sorted for deterministic output.
// Key encoding uses json.Marshal (string quoting only; no struct reflection).
func marshalRawMerged(m rawMerged) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// sort for deterministic output
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	n := 2 // '{' + '}'
	for _, k := range keys {
		n += len(k) + 4 + len(m[k]) // '"k":v' + ','
	}
	out := make([]byte, 0, n)
	out = append(out, '{')
	for i, k := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, fmt.Errorf("marshal key %q: %w", k, err)
		}
		out = append(out, kb...)
		out = append(out, ':')
		out = append(out, m[k]...)
	}
	out = append(out, '}')
	return out, nil
}

// unmarshalRawMergedInto populates dest from merged without marshaling the whole map
// to intermediate bytes. For struct destinations it iterates top-level fields and
// calls json.Unmarshal on each field's raw bytes directly. For *any and *map[string]any
// it decodes each value independently. Unknown destination types fall back to a
// marshal+unmarshal round-trip to ensure correctness.
func unmarshalRawMergedInto(merged rawMerged, dest any) error {
	v := reflect.ValueOf(dest)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return marshalFallback(merged, dest)
	}
	elem := v.Elem()
	switch elem.Kind() {
	case reflect.Struct:
		return unmarshalMergedIntoStruct(merged, elem)
	case reflect.Interface, reflect.Map:
		// *any or *map[string]any: decode each raw value independently.
		m := make(map[string]any, len(merged))
		for k, raw := range merged {
			var val any
			if err := json.Unmarshal(raw, &val); err != nil {
				return fmt.Errorf("field %q: %w", k, err)
			}
			m[k] = val
		}
		if elem.Kind() == reflect.Interface {
			elem.Set(reflect.ValueOf(m))
		} else {
			elem.Set(reflect.ValueOf(m).Convert(elem.Type()))
		}
		return nil
	default:
		return marshalFallback(merged, dest)
	}
}

// unmarshalMergedIntoStruct fills exported struct fields from merged by looking up
// each field's JSON name in the map and calling json.Unmarshal on the raw bytes.
// One level of reflection only — json.Unmarshal handles all nested decoding.
func unmarshalMergedIntoStruct(merged rawMerged, v reflect.Value) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := jsonTagName(f)
		if name == "-" {
			continue
		}
		raw, ok := merged[name]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, v.Field(i).Addr().Interface()); err != nil {
			return fmt.Errorf("field %q: %w", name, err)
		}
	}
	return nil
}

// jsonTagName returns the JSON field name for a struct field: the first segment of
// the json tag if present, otherwise the field name. Returns "-" for skipped fields.
func jsonTagName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if idx := strings.IndexByte(tag, ','); idx >= 0 {
		tag = tag[:idx]
	}
	if tag == "" {
		return f.Name
	}
	return tag
}

// marshalFallback is the safe fallback for destination types that unmarshalRawMergedInto
// does not handle directly: marshal merged to bytes, then unmarshal into dest.
func marshalFallback(merged rawMerged, dest any) error {
	b, err := marshalRawMerged(merged)
	if err != nil {
		return fmt.Errorf("marshal merged: %w", err)
	}
	return json.Unmarshal(b, dest)
}

// filterVars returns only the variables whose names are in keep.
// When all is already a map[string]any the subset is built directly.
// When all is a typed struct, it is returned as-is: doGraphQL marshals it directly
// and GraphQL servers must ignore unrecognized variables per spec.
func filterVars(all any, keep []string) any {
	if len(keep) == 0 || all == nil {
		return nil
	}
	m, ok := all.(map[string]any)
	if !ok {
		return all
	}
	if len(m) == 0 {
		return nil
	}
	filtered := make(map[string]any, len(keep))
	for _, k := range keep {
		if v, ok := m[k]; ok {
			filtered[k] = v
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}
