// Package execengine executes a pre-built federation Plan against subgraph HTTP endpoints.
// It has no dependency on the query planner — all routing decisions (which subgraph owns
// which field, key fields for entity resolution, etc.) are encoded in the Plan at build time.
package defederator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	ParentPath     []string `json:"parentPath"`
	IsParentList   bool     `json:"isParentList,omitempty"`
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
		plan.Fetches = append(plan.Fetches, Fetch{
			URL:       f.URL,
			Query:     f.Query,
			Variables: f.Variables,
		})
	}
	for _, ef := range raw.EntityFetches {
		plan.EntityFetches = append(plan.EntityFetches, EntityFetch{
			URL:            ef.URL,
			TypeName:       ef.TypeName,
			KeyFields:      ef.KeyFields,
			RequiresFields: ef.RequiresFields,
			Selection:      ef.Selection,
			ParentPath:     ef.ParentPath,
			IsParentList:   ef.IsParentList,
		})
	}
	return plan, nil
}

// ExecuteAndUnmarshal runs plan and JSON-unmarshals the merged result into dest.
// It is a convenience wrapper around Execute for use in generated code.
func ExecuteAndUnmarshal(ctx context.Context, plan *Plan, vars map[string]any, client *http.Client, dest any) error {
	data, errs, err := Execute(ctx, plan, vars, client)
	if err != nil {
		return err
	}
	if len(errs) > 0 {
		return fmt.Errorf("execengine: %v", errs)
	}
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("execengine: marshal result: %w", err)
	}
	return json.Unmarshal(b, dest)
}

// GraphQLError is a single error object in a GraphQL response.
type GraphQLError struct {
	Message    string                 `json:"message"`
	Locations  []map[string]int       `json:"locations,omitempty"`
	Path       []any                  `json:"path,omitempty"`
	Extensions map[string]any         `json:"extensions,omitempty"`
}

func (e GraphQLError) Error() string { return e.Message }

// graphqlRequest is the JSON body sent to a subgraph.
type graphqlRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
}

// graphqlResponse is the JSON body received from a subgraph.
type graphqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []GraphQLError  `json:"errors,omitempty"`
}

// Execute runs plan against real HTTP subgraph endpoints.
// Initial fetches run in parallel; entity fetches run sequentially in plan order.
// Returns the merged response data, any accumulated GraphQL errors, and any transport error.
func Execute(
	ctx context.Context,
	plan *Plan,
	variables map[string]any,
	client *http.Client,
) (map[string]any, []GraphQLError, error) {
	if client == nil {
		client = http.DefaultClient
	}

	type fetchResult struct {
		data   map[string]any
		errors []GraphQLError
		err    error
	}
	results := make([]fetchResult, len(plan.Fetches))
	var wg sync.WaitGroup

	for i, fetch := range plan.Fetches {
		wg.Add(1)
		go func(i int, fetch Fetch) {
			defer wg.Done()
			vars := filterVars(variables, fetch.Variables)
			resp, err := doGraphQL(ctx, client, fetch.URL, fetch.Query, "", vars)
			if err != nil {
				results[i] = fetchResult{err: fmt.Errorf("execengine: fetch %s: %w", fetch.URL, err)}
				return
			}
			var data map[string]any
			if len(resp.Data) > 0 && string(resp.Data) != "null" {
				if err := json.Unmarshal(resp.Data, &data); err != nil {
					results[i] = fetchResult{err: fmt.Errorf("execengine: decode %s data: %w", fetch.URL, err)}
					return
				}
			}
			results[i] = fetchResult{data: data, errors: resp.Errors}
		}(i, fetch)
	}
	wg.Wait()

	merged := make(map[string]any)
	var allErrors []GraphQLError

	for _, r := range results {
		if r.err != nil {
			return nil, nil, r.err
		}
		allErrors = append(allErrors, r.errors...)
		for k, v := range r.data {
			merged[k] = v
		}
	}

	for _, ef := range plan.EntityFetches {
		allKeyFields := append(append([]string{}, ef.KeyFields...), ef.RequiresFields...)
		reps, err := collectRepresentations(merged, ef.ParentPath, ef.TypeName, allKeyFields, ef.IsParentList)
		if err != nil {
			allErrors = append(allErrors, GraphQLError{
				Message: fmt.Sprintf("execengine: entity fetch for %s: %s", ef.TypeName, err),
			})
			continue
		}
		if len(reps) == 0 {
			continue
		}

		entityQuery := buildEntityQuery(ef.TypeName, ef.Selection)
		entityVars := map[string]any{"representations": reps}

		resp, err := doGraphQL(ctx, client, ef.URL, entityQuery, "", entityVars)
		if err != nil {
			allErrors = append(allErrors, GraphQLError{
				Message: fmt.Sprintf("execengine: entity fetch %s/%s: %s", ef.URL, ef.TypeName, err),
			})
			continue
		}
		allErrors = append(allErrors, resp.Errors...)

		if len(resp.Data) > 0 && string(resp.Data) != "null" {
			var entityData map[string]any
			if err := json.Unmarshal(resp.Data, &entityData); err == nil {
				if entities, ok := entityData["_entities"].([]any); ok {
					mergeEntityResults(merged, ef.ParentPath, entities, ef.IsParentList)
				}
			}
		}
	}

	if len(plan.Projection) > 0 {
		merged = ApplyProjection(merged, plan.Projection)
	}
	return merged, allErrors, nil
}

// ApplyProjection trims data to only the fields in proj, discarding planner-added fields.
func ApplyProjection(data map[string]any, proj []*FieldProjection) map[string]any {
	if len(proj) == 0 {
		return data
	}
	result := make(map[string]any, len(proj))
	for _, p := range proj {
		v, ok := data[p.Key]
		if !ok {
			continue
		}
		if len(p.Children) > 0 {
			switch vt := v.(type) {
			case map[string]any:
				result[p.Key] = ApplyProjection(vt, p.Children)
			case []any:
				list := make([]any, len(vt))
				for i, item := range vt {
					if m, ok := item.(map[string]any); ok {
						list[i] = ApplyProjection(m, p.Children)
					} else {
						list[i] = item
					}
				}
				result[p.Key] = list
			default:
				result[p.Key] = v
			}
		} else {
			result[p.Key] = v
		}
	}
	return result
}

// doGraphQL sends a GraphQL POST request to url and returns the parsed response.
func doGraphQL(
	ctx context.Context,
	client *http.Client,
	url, query, operationName string,
	variables map[string]any,
) (*graphqlResponse, error) {
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

	var gqlResp graphqlResponse
	if err := json.Unmarshal(raw, &gqlResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &gqlResp, nil
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
func collectRepresentations(
	data map[string]any,
	path []string,
	typeName string,
	keyFields []string,
	isList bool,
) ([]map[string]any, error) {
	target := walkPath(data, path)
	if target == nil {
		return nil, nil
	}

	if isList {
		list, ok := target.([]any)
		if !ok {
			return nil, fmt.Errorf("expected list at path %v, got %T", path, target)
		}
		reps := make([]map[string]any, 0, len(list))
		for _, item := range list {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			rep, err := buildRepresentation(itemMap, typeName, keyFields)
			if err != nil {
				return nil, err
			}
			reps = append(reps, rep)
		}
		return reps, nil
	}

	itemMap, ok := target.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object at path %v, got %T", path, target)
	}
	rep, err := buildRepresentation(itemMap, typeName, keyFields)
	if err != nil {
		return nil, err
	}
	return []map[string]any{rep}, nil
}

func buildRepresentation(obj map[string]any, typeName string, keyFields []string) (map[string]any, error) {
	rep := map[string]any{"__typename": typeName}
	for _, kf := range keyFields {
		v, ok := obj[kf]
		if !ok {
			return nil, fmt.Errorf("key field %q not found in response", kf)
		}
		rep[kf] = v
	}
	return rep, nil
}

// mergeEntityResults merges _entities response data back into the merged result map.
func mergeEntityResults(data map[string]any, path []string, entities []any, isList bool) {
	if len(path) == 0 || len(entities) == 0 {
		return
	}

	if len(path) == 1 {
		target := data[path[0]]
		if isList {
			list, ok := target.([]any)
			if !ok {
				return
			}
			for i, item := range list {
				if i >= len(entities) {
					break
				}
				itemMap, ok := item.(map[string]any)
				if !ok {
					continue
				}
				entityMap, ok := entities[i].(map[string]any)
				if !ok {
					continue
				}
				for k, v := range entityMap {
					itemMap[k] = v
				}
			}
		} else {
			targetMap, ok := target.(map[string]any)
			if !ok || len(entities) == 0 {
				return
			}
			entityMap, ok := entities[0].(map[string]any)
			if !ok {
				return
			}
			for k, v := range entityMap {
				targetMap[k] = v
			}
		}
		return
	}

	next := data[path[0]]
	switch v := next.(type) {
	case map[string]any:
		mergeEntityResults(v, path[1:], entities, isList)
	case []any:
		for _, item := range v {
			if itemMap, ok := item.(map[string]any); ok {
				mergeEntityResults(itemMap, path[1:], entities, isList)
			}
		}
	}
}

// walkPath traverses data following path and returns the value at the end.
func walkPath(data map[string]any, path []string) any {
	if len(path) == 0 {
		return data
	}
	v, ok := data[path[0]]
	if !ok {
		return nil
	}
	if len(path) == 1 {
		return v
	}
	switch next := v.(type) {
	case map[string]any:
		return walkPath(next, path[1:])
	default:
		return nil
	}
}

// filterVars returns only the variables whose names are in keep.
func filterVars(all map[string]any, keep []string) map[string]any {
	if len(keep) == 0 || len(all) == 0 {
		return nil
	}
	filtered := make(map[string]any, len(keep))
	for _, k := range keep {
		if v, ok := all[k]; ok {
			filtered[k] = v
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}
