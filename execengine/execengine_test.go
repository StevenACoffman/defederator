package execengine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestDoGraphQLInto verifies that the fast-path decoder populates dest directly from
// the HTTP response without a separate json.RawMessage intermediate.
// TestDoGraphQLIntoMerged verifies the single-pass initial-fetch decoder populates
// rawMerged directly without a two-step wrapper-then-data unmarshal.
func TestDoGraphQLIntoMerged(t *testing.T) {
	cases := map[string]struct {
		body      string
		wantKeys  []string
		wantErrs  int
		wantNil   bool
		wantError bool
	}{
		"normal_response": {
			body:     `{"data":{"id":"p1","sku":"s1"}}`,
			wantKeys: []string{"id", "sku"},
		},
		"graphql_errors_returned": {
			body:     `{"errors":[{"message":"boom"}]}`,
			wantNil:  true,
			wantErrs: 1,
		},
		"null_data": {
			body:    `{"data":null}`,
			wantNil: true,
		},
		"invalid_json": {
			body:      `not json`,
			wantError: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := tc.body
			srv := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(body))
				}),
			)
			defer srv.Close()

			data, errs, err := doGraphQLIntoMerged(
				context.Background(),
				http.DefaultClient,
				srv.URL,
				`{ q }`,
				"",
				nil,
			)
			if tc.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(errs) != tc.wantErrs {
				t.Errorf("errs count: got %d, want %d", len(errs), tc.wantErrs)
			}
			if tc.wantNil {
				if data != nil {
					t.Errorf("expected nil data, got %v", data)
				}
				return
			}
			for _, k := range tc.wantKeys {
				if _, ok := data[k]; !ok {
					t.Errorf("key %q missing from merged data", k)
				}
			}
		})
	}
}

func TestDoGraphQLInto(t *testing.T) {
	type product struct {
		ID  string `json:"id"`
		SKU string `json:"sku"`
	}

	cases := map[string]struct {
		body      string
		wantID    string
		wantSKU   string
		wantErrs  int
		wantError bool
	}{
		"typed_struct_populated": {
			body:    `{"data":{"id":"p1","sku":"s1"}}`,
			wantID:  "p1",
			wantSKU: "s1",
		},
		"graphql_errors_returned": {
			body:     `{"errors":[{"message":"boom"}]}`,
			wantErrs: 1,
		},
		"data_null_noop": {
			body: `{"data":null}`,
			// dest unchanged; no error
		},
		"invalid_json_errors": {
			body:      `not json`,
			wantError: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := tc.body
			srv := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(body))
				}),
			)
			defer srv.Close()

			var dest product
			errs, err := doGraphQLInto(
				context.Background(),
				http.DefaultClient,
				srv.URL,
				`{ p { id sku } }`,
				"",
				nil,
				&dest,
			)
			if tc.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(errs) != tc.wantErrs {
				t.Errorf("errs count: got %d, want %d", len(errs), tc.wantErrs)
			}
			if dest.ID != tc.wantID {
				t.Errorf("ID: got %q, want %q", dest.ID, tc.wantID)
			}
			if dest.SKU != tc.wantSKU {
				t.Errorf("SKU: got %q, want %q", dest.SKU, tc.wantSKU)
			}
		})
	}

	// Untyped dest: when dest is *any, json decodes data into the interface.
	t.Run("untyped_any_populated", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":{"id":"p1"}}`))
		}))
		defer srv.Close()

		var dest any
		_, err := doGraphQLInto(
			context.Background(),
			http.DefaultClient,
			srv.URL,
			`{ p { id } }`,
			"",
			nil,
			&dest,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m, ok := dest.(map[string]any)
		if !ok {
			t.Fatalf("expected map[string]any, got %T", dest)
		}
		if m["id"] != "p1" {
			t.Errorf("id: got %v, want p1", m["id"])
		}
	})
}

func jsonResp(t *testing.T, data any) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(map[string]any{"data": data})
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}
}

func TestResolve_RoundTrip(t *testing.T) {
	specJSON := `{
		"fetches":[{"subgraphEnum":"PRODUCTS","query":"{ product { id } }","variables":["id"]}],
		"entityFetches":[{
			"subgraphEnum":"USERS","typeName":"User",
			"keyFields":["id"],"selection":"name\n",
			"parentPath":["product","createdBy"]
		}]
	}`
	urls := map[string]string{
		"PRODUCTS": "https://products.example.com",
		"USERS":    "https://users.example.com",
	}
	plan, err := Resolve(specJSON, urls)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Fetches) != 1 {
		t.Fatalf("expected 1 fetch, got %d", len(plan.Fetches))
	}
	if plan.Fetches[0].URL != "https://products.example.com" {
		t.Errorf("wrong URL: %s", plan.Fetches[0].URL)
	}
	if plan.Fetches[0].Variables[0] != "id" {
		t.Errorf("wrong variable: %v", plan.Fetches[0].Variables)
	}
	if len(plan.EntityFetches) != 1 {
		t.Fatalf("expected 1 entity fetch, got %d", len(plan.EntityFetches))
	}
	if plan.EntityFetches[0].URL != "https://users.example.com" {
		t.Errorf("wrong entity URL: %s", plan.EntityFetches[0].URL)
	}
}

func TestResolve_MissingEnum(t *testing.T) {
	specJSON := `{"fetches":[{"subgraphEnum":"NONEXISTENT","query":"{ q }"}]}`
	_, err := Resolve(specJSON, map[string]string{"OTHER": "https://other.example.com"})
	if err == nil {
		t.Fatal("expected error for missing enum")
	}
	if !strings.Contains(err.Error(), "NONEXISTENT") {
		t.Errorf("error should name the missing enum, got: %v", err)
	}
}

func TestResolve_ProjectionPreserved(t *testing.T) {
	specJSON := `{
		"fetches":[{"subgraphEnum":"SG","query":"{ q }"}],
		"projection":[{"Key":"product","Children":[{"Key":"id"},{"Key":"sku"}]}]
	}`
	plan, err := Resolve(specJSON, map[string]string{"SG": "https://sg.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Projection) != 1 || plan.Projection[0].Key != "product" {
		t.Errorf("projection not preserved: %v", plan.Projection)
	}
	if len(plan.Projection[0].Children) != 2 {
		t.Errorf("projection children not preserved: %v", plan.Projection[0].Children)
	}
}

func TestExecute_SingleFetch(t *testing.T) {
	srv := httptest.NewServer(jsonResp(t, map[string]any{
		"product": map[string]any{"id": "apollo-federation", "sku": "federation"},
	}))
	defer srv.Close()

	plan := &Plan{
		Fetches: []Fetch{
			{URL: srv.URL, Query: `{ product(id: "apollo-federation") { id sku } }`},
		},
		Projection: []*FieldProjection{
			{Key: "product", Children: []*FieldProjection{
				{Key: "id"},
				{Key: "sku"},
			}},
		},
	}

	raw, errs, err := execute(context.Background(), plan, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	var product map[string]any
	if err := json.Unmarshal(raw["product"], &product); err != nil {
		t.Fatalf("unmarshal product: %v", err)
	}
	if product["id"] != "apollo-federation" {
		t.Errorf("got id=%v", product["id"])
	}
	if product["sku"] != "federation" {
		t.Errorf("got sku=%v", product["sku"])
	}
}

func TestExecute_EntityFetch(t *testing.T) {
	// Subgraph A: returns product with id only.
	sgA := httptest.NewServer(jsonResp(t, map[string]any{
		"product": map[string]any{"id": "p1", "__typename": "Product"},
	}))
	defer sgA.Close()

	// Subgraph B: _entities resolver returns sku.
	sgB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"data": map[string]any{
				"_entities": []any{
					map[string]any{"sku": "fed-sku"},
				},
			},
		}
		b, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	defer sgB.Close()

	plan := &Plan{
		Fetches: []Fetch{
			{URL: sgA.URL, Query: `{ product(id: "p1") { id __typename } }`},
		},
		EntityFetches: []EntityFetch{
			{
				URL:        sgB.URL,
				TypeName:   "Product",
				KeyFields:  []string{"id"},
				Selection:  "sku\n",
				ParentPath: []string{"product"},
			},
		},
		Projection: []*FieldProjection{
			{Key: "product", Children: []*FieldProjection{
				{Key: "id"},
				{Key: "sku"},
			}},
		},
	}

	raw, errs, err := execute(context.Background(), plan, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	var product map[string]any
	if err := json.Unmarshal(raw["product"], &product); err != nil {
		t.Fatalf("unmarshal product: %v", err)
	}
	if product["id"] != "p1" {
		t.Errorf("got id=%v", product["id"])
	}
	if product["sku"] != "fed-sku" {
		t.Errorf("got sku=%v", product["sku"])
	}
	if _, hasTypename := product["__typename"]; hasTypename {
		t.Error("projection should have stripped __typename")
	}
}

func TestExecute_NilClientUsesDefault(t *testing.T) {
	srv := httptest.NewServer(jsonResp(t, map[string]any{"ping": "pong"}))
	defer srv.Close()

	plan := &Plan{
		Fetches: []Fetch{
			{URL: srv.URL, Query: `{ ping }`},
		},
	}
	_, _, err := execute(context.Background(), plan, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecute_GraphQLErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"data":   nil,
			"errors": []map[string]any{{"message": "not found"}},
		}
		b, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	defer srv.Close()

	plan := &Plan{
		Fetches: []Fetch{{URL: srv.URL, Query: `{ product { id } }`}},
	}
	_, errs, err := execute(context.Background(), plan, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) == 0 {
		t.Error("expected GraphQL errors to be returned")
	}
	if errs[0].Message != "not found" {
		t.Errorf("got message=%q", errs[0].Message)
	}
}

func TestApplyProjection_StripsExtraFields(t *testing.T) {
	// Build rawMerged input by marshaling a typed map.
	productJSON, _ := json.Marshal(map[string]any{
		"id":         "p1",
		"sku":        "s1",
		"__typename": "Product",
	})
	data := rawMerged{
		"product": productJSON,
	}
	proj := []*FieldProjection{
		{Key: "product", Children: []*FieldProjection{
			{Key: "id"},
			{Key: "sku"},
		}},
	}
	result, err := applyProjection(data, proj)
	if err != nil {
		t.Fatalf("applyProjection: %v", err)
	}
	var p map[string]any
	if err := json.Unmarshal(result["product"], &p); err != nil {
		t.Fatalf("unmarshal product: %v", err)
	}
	if _, ok := p["__typename"]; ok {
		t.Error("__typename should be stripped by projection")
	}
	if p["id"] != "p1" || p["sku"] != "s1" {
		t.Errorf("unexpected result: %v", p)
	}
}

// TestMergeRawObjects verifies the byte-splice merge implementation handles all
// edge cases: empty objects on either side, non-empty merges, key overlap, and
// whitespace in input (which Go's json.Marshal never produces but HTTP servers can).
func TestMergeRawObjects(t *testing.T) {
	cases := map[string]struct {
		dst        string
		src        string
		wantNil    bool
		wantKeys   []string // keys that must be present after round-tripping through map
		wantAbsent []string // keys that must be absent
	}{
		"empty_dst_returns_src": {
			dst:      `{}`,
			src:      `{"a":1}`,
			wantKeys: []string{"a"},
		},
		"empty_src_returns_dst": {
			dst:      `{"a":1}`,
			src:      `{}`,
			wantKeys: []string{"a"},
		},
		"both_non_empty_merged": {
			dst:      `{"a":1}`,
			src:      `{"b":2}`,
			wantKeys: []string{"a", "b"},
		},
		"key_overlap_same_value": {
			// Federation: entity echoes back the key field; both copies are identical.
			// JSON parsers take the last value — same value, so the result is correct.
			dst:      `{"id":"x","name":"A"}`,
			src:      `{"id":"x","sku":"S"}`,
			wantKeys: []string{"id", "name", "sku"},
		},
		"whitespace_in_dst": {
			// Internal whitespace in dst (from HTTP response) must not break the splice.
			dst:      `{"a": 1 }`,
			src:      `{"b":2}`,
			wantKeys: []string{"a", "b"},
		},
		"invalid_dst_not_object": {
			dst:     `[1,2]`,
			src:     `{"b":2}`,
			wantNil: true,
		},
		"invalid_src_not_object": {
			dst:     `{"a":1}`,
			src:     `"string"`,
			wantNil: true,
		},
		"empty_dst_bytes": {
			dst:     ``,
			src:     `{"b":2}`,
			wantNil: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := mergeRawObjects(json.RawMessage(tc.dst), json.RawMessage(tc.src))
			if tc.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %s", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			// Round-trip through map to verify the result is valid JSON with correct keys.
			var m map[string]any
			if err := json.Unmarshal(got, &m); err != nil {
				t.Fatalf("result is not valid JSON: %v (raw: %s)", err, got)
			}
			for _, k := range tc.wantKeys {
				if _, ok := m[k]; !ok {
					t.Errorf("key %q missing from result %v", k, m)
				}
			}
			for _, k := range tc.wantAbsent {
				if _, ok := m[k]; ok {
					t.Errorf("key %q should be absent from result %v", k, m)
				}
			}
		})
	}
}

func TestResolveURLSpec_RoundTrip(t *testing.T) {
	cases := map[string]struct {
		specJSON         string
		wantFetchURL     string
		wantQuery        string
		wantVars         []string
		wantEntityURL    string
		wantTypeName     string
		wantKeyFields    []string
		wantProjKey      string
		wantProjChildren int
	}{
		"fetch_only": {
			specJSON: `{
				"fetches":[{"url":"https://products.example.com","query":"{ product { id } }","variables":["id"]}],
				"projection":[{"Key":"product","Children":[{"Key":"id"},{"Key":"sku"}]}]
			}`,
			wantFetchURL:     "https://products.example.com",
			wantQuery:        "{ product { id } }",
			wantVars:         []string{"id"},
			wantProjKey:      "product",
			wantProjChildren: 2,
		},
		"with_entity_fetch": {
			specJSON: `{
				"fetches":[{"url":"https://products.example.com","query":"{ q }"}],
				"entityFetches":[{
					"url":"https://users.example.com","typeName":"User",
					"keyFields":["email"],"requiresFields":["totalProductsCreated"],
					"selection":"name\n","parentPath":["product","createdBy"]
				}]
			}`,
			wantFetchURL:  "https://products.example.com",
			wantEntityURL: "https://users.example.com",
			wantTypeName:  "User",
			wantKeyFields: []string{"email"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			plan, err := ResolveURLSpec(tc.specJSON)
			if err != nil {
				t.Fatalf("ResolveURLSpec: %v", err)
			}
			if len(plan.Fetches) == 0 {
				t.Fatal("expected at least one fetch")
			}
			if plan.Fetches[0].URL != tc.wantFetchURL {
				t.Errorf("fetch URL: got %q, want %q", plan.Fetches[0].URL, tc.wantFetchURL)
			}
			if tc.wantQuery != "" && plan.Fetches[0].Query != tc.wantQuery {
				t.Errorf("fetch query: got %q, want %q", plan.Fetches[0].Query, tc.wantQuery)
			}
			if tc.wantVars != nil {
				if len(plan.Fetches[0].Variables) != len(tc.wantVars) ||
					plan.Fetches[0].Variables[0] != tc.wantVars[0] {
					t.Errorf(
						"fetch variables: got %v, want %v",
						plan.Fetches[0].Variables,
						tc.wantVars,
					)
				}
			}
			if tc.wantEntityURL != "" {
				if len(plan.EntityFetches) == 0 {
					t.Fatal("expected entity fetch")
				}
				ef := plan.EntityFetches[0]
				if ef.URL != tc.wantEntityURL {
					t.Errorf("entity URL: got %q, want %q", ef.URL, tc.wantEntityURL)
				}
				if ef.TypeName != tc.wantTypeName {
					t.Errorf("entity typeName: got %q, want %q", ef.TypeName, tc.wantTypeName)
				}
				if len(ef.KeyFields) != len(tc.wantKeyFields) ||
					ef.KeyFields[0] != tc.wantKeyFields[0] {
					t.Errorf("entity keyFields: got %v, want %v", ef.KeyFields, tc.wantKeyFields)
				}
			}
			if tc.wantProjKey != "" {
				if len(plan.Projection) == 0 || plan.Projection[0].Key != tc.wantProjKey {
					t.Errorf("projection key: got %v, want %q", plan.Projection, tc.wantProjKey)
				}
				if len(plan.Projection[0].Children) != tc.wantProjChildren {
					t.Errorf(
						"projection children: got %d, want %d",
						len(plan.Projection[0].Children),
						tc.wantProjChildren,
					)
				}
			}
		})
	}
}

func TestResolveURLSpec_MalformedJSON(t *testing.T) {
	_, err := ResolveURLSpec(`not-json`)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestExecuteAndUnmarshal_Success(t *testing.T) {
	type product struct {
		ID  string `json:"id"`
		Sku string `json:"sku"`
	}
	type result struct {
		Product *product `json:"product"`
	}

	srv := httptest.NewServer(jsonResp(t, map[string]any{
		"product": map[string]any{"id": "fed-1", "sku": "fed-sku"},
	}))
	defer srv.Close()

	plan := &Plan{
		Fetches: []Fetch{{URL: srv.URL, Query: `{ product { id sku } }`}},
		Projection: []*FieldProjection{
			{Key: "product", Children: []*FieldProjection{
				{Key: "id"}, {Key: "sku"},
			}},
		},
	}

	var dest result
	if err := ExecuteAndUnmarshal(context.Background(), plan, nil, nil, &dest); err != nil {
		t.Fatalf("ExecuteAndUnmarshal: %v", err)
	}
	if dest.Product == nil {
		t.Fatal("dest.Product is nil")
	}
	if dest.Product.ID != "fed-1" {
		t.Errorf("ID: got %q, want %q", dest.Product.ID, "fed-1")
	}
	if dest.Product.Sku != "fed-sku" {
		t.Errorf("Sku: got %q, want %q", dest.Product.Sku, "fed-sku")
	}
}

func TestExecuteAndUnmarshal_GraphQLErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":null,"errors":[{"message":"boom"}]}`))
	}))
	defer srv.Close()

	plan := &Plan{
		Fetches: []Fetch{{URL: srv.URL, Query: `{ q }`}},
	}
	var dest map[string]any
	err := ExecuteAndUnmarshal(context.Background(), plan, nil, nil, &dest)
	if err == nil {
		t.Fatal("expected error for GraphQL errors")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should mention 'boom', got: %v", err)
	}
}

func TestFilterVars_Subset(t *testing.T) {
	// filterVars is private; test indirectly via execute variable forwarding.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		// Echo the variables back as data so we can inspect them.
		b, _ := json.Marshal(map[string]any{"data": req.Variables})
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	defer srv.Close()

	plan := &Plan{
		Fetches: []Fetch{
			{
				URL:       srv.URL,
				Query:     `query($id: ID!) { p(id: $id) { id } }`,
				Variables: []string{"id"},
			},
		},
	}
	raw, _, err := execute(
		context.Background(),
		plan,
		map[string]any{"id": "x1", "extra": "y"},
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := json.Unmarshal(raw["id"], &id); err != nil {
		t.Fatalf("unmarshal id: %v", err)
	}
	if id != "x1" {
		t.Errorf("expected id=x1 in forwarded vars, got %v", id)
	}
	if _, ok := raw["extra"]; ok {
		t.Error("extra variable should not have been forwarded")
	}
}

// TestExecuteAndUnmarshal_SingleFetch_EmptyData verifies the fast path
// handles subgraph responses with absent or null data without error.
func TestExecuteAndUnmarshal_SingleFetch_EmptyData(t *testing.T) {
	type result struct {
		Product *struct {
			ID string `json:"id"`
		} `json:"product"`
	}

	cases := map[string]string{
		// data field present but null: json.Unmarshal([]byte("null"), dest) is safe.
		"null_data": `{"data":null}`,
		// data field absent entirely: resp.Data is nil; the len guard prevents
		// json.Unmarshal(nil, dest) which would fail with a syntax error.
		"no_data_field": `{}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			body := body
			srv := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(body))
				}),
			)
			defer srv.Close()

			plan := &Plan{
				Fetches: []Fetch{{URL: srv.URL, Query: `{ product { id } }`}},
			}
			var dest result
			if err := ExecuteAndUnmarshal(context.Background(), plan, nil, nil, &dest); err != nil {
				t.Fatalf("%s: unexpected error: %v", name, err)
			}
			if dest.Product != nil {
				t.Errorf("%s: expected nil Product, got %+v", name, dest.Product)
			}
		})
	}
}

// TestExecuteAndUnmarshal_MultiFetch_SlowPath verifies the general path merges
// data from two fetches and populates the typed destination struct.
func TestExecuteAndUnmarshal_MultiFetch_SlowPath(t *testing.T) {
	type userResult struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type postResult struct {
		Title string `json:"title"`
	}
	type dest struct {
		User *userResult `json:"user"`
		Post *postResult `json:"post"`
	}

	srvUser := httptest.NewServer(jsonResp(t, map[string]any{
		"user": map[string]any{"id": "u1", "name": "Alice"},
	}))
	defer srvUser.Close()

	srvPost := httptest.NewServer(jsonResp(t, map[string]any{
		"post": map[string]any{"title": "Hello"},
	}))
	defer srvPost.Close()

	plan := &Plan{
		Fetches: []Fetch{
			{URL: srvUser.URL, Query: `{ user { id name } }`},
			{URL: srvPost.URL, Query: `{ post { title } }`},
		},
	}

	var got dest
	if err := ExecuteAndUnmarshal(context.Background(), plan, nil, nil, &got); err != nil {
		t.Fatalf("ExecuteAndUnmarshal: %v", err)
	}
	if got.User == nil || got.User.Name != "Alice" {
		t.Errorf("user: got %+v", got.User)
	}
	if got.Post == nil || got.Post.Title != "Hello" {
		t.Errorf("post: got %+v", got.Post)
	}
}

// TestExecuteAndUnmarshal_EntityFetch_SlowPath verifies that entity-fetch
// operations still take the general path and correctly merge entity fields.
func TestExecuteAndUnmarshal_EntityFetch_SlowPath(t *testing.T) {
	type product struct {
		ID  string `json:"id"`
		Sku string `json:"sku"`
	}
	type result struct {
		Product *product `json:"product"`
	}

	sgA := httptest.NewServer(jsonResp(t, map[string]any{
		"product": map[string]any{"id": "p1", "__typename": "Product"},
	}))
	defer sgA.Close()

	sgB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(map[string]any{
			"data": map[string]any{
				"_entities": []any{
					map[string]any{"sku": "fed-sku"},
				},
			},
		})
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	defer sgB.Close()

	plan := &Plan{
		Fetches: []Fetch{
			{URL: sgA.URL, Query: `{ product(id: "p1") { id __typename } }`},
		},
		EntityFetches: []EntityFetch{
			{
				URL:        sgB.URL,
				TypeName:   "Product",
				KeyFields:  []string{"id"},
				Selection:  "sku\n",
				ParentPath: []string{"product"},
			},
		},
	}

	var got result
	if err := ExecuteAndUnmarshal(context.Background(), plan, nil, nil, &got); err != nil {
		t.Fatalf("ExecuteAndUnmarshal: %v", err)
	}
	if got.Product == nil {
		t.Fatal("Product is nil")
	}
	if got.Product.ID != "p1" {
		t.Errorf("ID: got %q, want %q", got.Product.ID, "p1")
	}
	if got.Product.Sku != "fed-sku" {
		t.Errorf("Sku: got %q, want %q", got.Product.Sku, "fed-sku")
	}
}

// TestProjection_Gating verifies the skipProjection flag in execute().
//
// The fast path in ExecuteAndUnmarshal bypasses execute() entirely (no entity
// fetches → no planner-added fields → projection is a no-op regardless). Gating
// matters on the slow path, which is what this test exercises directly via execute().
//
// Invariant: skipProj=false → projection applied, planner fields stripped.
//
//	skipProj=true  → projection skipped, planner fields present in raw output
//	                 (typed struct callers drop them silently via json.Unmarshal).
func TestProjection_Gating(t *testing.T) {
	// Server simulates an initial fetch response that includes __typename — a field
	// the planner adds to subgraph queries when entity fetches are needed. The
	// Projection tree keeps only {id, sku}, so __typename should be stripped when
	// projection runs.
	srv := httptest.NewServer(jsonResp(t, map[string]any{
		"product": map[string]any{
			"id":         "p1",
			"sku":        "s1",
			"__typename": "Product",
		},
	}))
	defer srv.Close()

	projection := []*FieldProjection{
		{Key: "product", Children: []*FieldProjection{
			{Key: "id"}, {Key: "sku"},
		}},
	}

	cases := map[string]struct {
		skipProj            bool
		wantTypenamePresent bool // in the raw JSON returned by execute()
	}{
		// skipProj=false: projection runs, strips __typename from raw output.
		"projection_applied": {skipProj: false, wantTypenamePresent: false},
		// skipProj=true: projection skipped, __typename remains in raw output.
		// (A typed struct caller's json.Unmarshal silently drops it.)
		"projection_skipped": {skipProj: true, wantTypenamePresent: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			plan := &Plan{
				Fetches:    []Fetch{{URL: srv.URL, Query: `{ product { id sku __typename } }`}},
				Projection: projection,
			}
			raw, _, err := execute(context.Background(), plan, nil, nil, tc.skipProj)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			var p map[string]any
			if err := json.Unmarshal(raw["product"], &p); err != nil {
				t.Fatalf("unmarshal product: %v", err)
			}
			if p["id"] != "p1" {
				t.Errorf("id: got %v, want p1", p["id"])
			}
			_, hasTypename := p["__typename"]
			if tc.wantTypenamePresent && !hasTypename {
				t.Error("__typename should be present (projection was skipped)")
			}
			if !tc.wantTypenamePresent && hasTypename {
				t.Error("__typename should be absent (projection was applied)")
			}
		})
	}

	// End-to-end: ExecuteAndUnmarshal sets skipProj=true for typed struct destinations.
	// The struct's json.Unmarshal drops __typename, so only declared fields are populated.
	// Use the slow path by adding a no-op entity fetch so execute() is called.
	t.Run("ExecuteAndUnmarshal_typed_uses_skip", func(t *testing.T) {
		// Entity server: returns empty _entities (entity fetch is a no-op here; we just
		// need the slow path to be taken so execute() is called with skipProj=true).
		entitySrv := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resp := map[string]any{"data": map[string]any{"_entities": []any{}}}
				b, _ := json.Marshal(resp)
				w.Header().Set("Content-Type", "application/json")
				w.Write(b)
			}),
		)
		defer entitySrv.Close()

		type product struct {
			ID  string `json:"id"`
			SKU string `json:"sku"`
		}
		type result struct {
			Product product `json:"product"`
		}
		plan := &Plan{
			Fetches: []Fetch{{URL: srv.URL, Query: `{ product { id sku __typename } }`}},
			EntityFetches: []EntityFetch{{
				URL: entitySrv.URL, TypeName: "Product",
				KeyFields: []string{"id"}, Selection: "sku\n",
				ParentPath: []string{"product"},
			}},
			Projection: []*FieldProjection{
				{Key: "product", Children: []*FieldProjection{{Key: "id"}, {Key: "sku"}}},
			},
		}
		var got result
		if err := ExecuteAndUnmarshal(context.Background(), plan, nil, nil, &got); err != nil {
			t.Fatalf("ExecuteAndUnmarshal: %v", err)
		}
		// Typed struct: __typename in raw JSON is silently dropped; id and sku populate.
		if got.Product.ID != "p1" || got.Product.SKU != "s1" {
			t.Errorf("got %+v", got.Product)
		}
	})

	// End-to-end: ExecuteAndUnmarshal sets skipProj=false for *map[string]any destinations,
	// so projection runs and planner-added fields are stripped from the output map.
	t.Run("ExecuteAndUnmarshal_untyped_applies_projection", func(t *testing.T) {
		entitySrv := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resp := map[string]any{"data": map[string]any{"_entities": []any{}}}
				b, _ := json.Marshal(resp)
				w.Header().Set("Content-Type", "application/json")
				w.Write(b)
			}),
		)
		defer entitySrv.Close()

		plan := &Plan{
			Fetches: []Fetch{{URL: srv.URL, Query: `{ product { id sku __typename } }`}},
			EntityFetches: []EntityFetch{{
				URL: entitySrv.URL, TypeName: "Product",
				KeyFields: []string{"id"}, Selection: "sku\n",
				ParentPath: []string{"product"},
			}},
			Projection: []*FieldProjection{
				{Key: "product", Children: []*FieldProjection{{Key: "id"}, {Key: "sku"}}},
			},
		}
		var dest map[string]any
		if err := ExecuteAndUnmarshal(context.Background(), plan, nil, nil, &dest); err != nil {
			t.Fatalf("ExecuteAndUnmarshal: %v", err)
		}
		p, _ := dest["product"].(map[string]any)
		if p["id"] != "p1" {
			t.Errorf("id: got %v, want p1", p["id"])
		}
		if _, has := p["__typename"]; has {
			t.Error("__typename should have been stripped by projection for untyped dest")
		}
	})
}

func TestUnmarshalRawMergedInto(t *testing.T) {
	type Product struct {
		ID  string `json:"id"`
		SKU string `json:"sku"`
	}

	mustRaw := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}

	cases := map[string]struct {
		merged   rawMerged
		dest     any
		wantErr  bool
		validate func(t *testing.T, dest any)
	}{
		"struct_fields_populated": {
			merged: rawMerged{"id": mustRaw("p1"), "sku": mustRaw("s1")},
			dest:   &Product{},
			validate: func(t *testing.T, dest any) {
				p := dest.(*Product)
				if p.ID != "p1" {
					t.Errorf("ID: got %q, want p1", p.ID)
				}
				if p.SKU != "s1" {
					t.Errorf("SKU: got %q, want s1", p.SKU)
				}
			},
		},
		"unknown_key_ignored": {
			merged: rawMerged{"id": mustRaw("p1"), "unknown": mustRaw("x")},
			dest:   &Product{},
			validate: func(t *testing.T, dest any) {
				p := dest.(*Product)
				if p.ID != "p1" {
					t.Errorf("ID: got %q, want p1", p.ID)
				}
			},
		},
		"missing_key_leaves_zero": {
			merged: rawMerged{"id": mustRaw("p1")},
			dest:   &Product{SKU: "preset"},
			validate: func(t *testing.T, dest any) {
				p := dest.(*Product)
				if p.SKU != "preset" {
					t.Errorf("SKU should remain preset, got %q", p.SKU)
				}
			},
		},
		"json_minus_tag_skipped": {
			merged: rawMerged{"-": mustRaw("should-not-set")},
			dest: &struct {
				Ignored string `json:"-"`
			}{},
			validate: func(t *testing.T, dest any) {
				d := dest.(*struct {
					Ignored string `json:"-"`
				})
				if d.Ignored != "" {
					t.Errorf("field tagged json:\"-\" should not be set, got %q", d.Ignored)
				}
			},
		},
		"any_dest_produces_map": {
			merged: rawMerged{"id": mustRaw("p1"), "sku": mustRaw("s1")},
			dest:   new(any),
			validate: func(t *testing.T, dest any) {
				m, ok := (*dest.(*any)).(map[string]any)
				if !ok {
					t.Fatalf("expected map[string]any, got %T", *dest.(*any))
				}
				if m["id"] != "p1" {
					t.Errorf("id: got %v, want p1", m["id"])
				}
			},
		},
		"map_dest_populated": {
			merged: rawMerged{"id": mustRaw("p1")},
			dest:   &map[string]any{},
			validate: func(t *testing.T, dest any) {
				m := *dest.(*map[string]any)
				if m["id"] != "p1" {
					t.Errorf("id: got %v, want p1", m["id"])
				}
			},
		},
		"fallback_for_unknown_type": {
			merged: rawMerged{"0": mustRaw("a"), "1": mustRaw("b")},
			dest:   &[]string{},
			validate: func(t *testing.T, dest any) {
				// fallback marshals merged (a JSON object) then unmarshals into []string,
				// which will fail — so we just verify wantErr=true handles it.
			},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := unmarshalRawMergedInto(tc.merged, tc.dest)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.validate != nil {
				tc.validate(t, tc.dest)
			}
		})
	}
}

// ── marshalRawList ────────────────────────────────────────────────────────────

func TestMarshalRawList(t *testing.T) {
	cases := map[string]struct {
		input []json.RawMessage
		want  string
	}{
		"empty":         {nil, "[]"},
		"empty_slice":   {[]json.RawMessage{}, "[]"},
		"single_object": {[]json.RawMessage{json.RawMessage(`{"a":1}`)}, `[{"a":1}]`},
		"two_objects": {
			[]json.RawMessage{json.RawMessage(`{"a":1}`), json.RawMessage(`{"b":2}`)},
			`[{"a":1},{"b":2}]`,
		},
		"null_element": {
			[]json.RawMessage{json.RawMessage(`null`), json.RawMessage(`{"b":2}`)},
			`[null,{"b":2}]`,
		},
		"string_element": {
			[]json.RawMessage{json.RawMessage(`"hello"`), json.RawMessage(`1`)},
			`["hello",1]`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := string(marshalRawList(tc.input))
			if got != tc.want {
				t.Errorf("marshalRawList: want %q, got %q", tc.want, got)
			}
		})
	}
}

// ── marshalRawMerged ──────────────────────────────────────────────────────────

func TestMarshalRawMerged(t *testing.T) {
	cases := map[string]struct {
		input   rawMerged
		wantErr bool
	}{
		"empty":      {rawMerged{}, false},
		"single_key": {rawMerged{"a": json.RawMessage(`1`)}, false},
		"two_keys": {
			rawMerged{"a": json.RawMessage(`1`), "b": json.RawMessage(`"hello"`)},
			false,
		},
		"nested": {rawMerged{"x": json.RawMessage(`{"y":2}`)}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := marshalRawMerged(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Round-trip: unmarshal back and compare maps.
			var decoded rawMerged
			if err := json.Unmarshal(got, &decoded); err != nil {
				t.Fatalf("round-trip unmarshal failed: %v (input: %s)", err, got)
			}
			if len(decoded) != len(tc.input) {
				t.Fatalf("round-trip key count: want %d, got %d", len(tc.input), len(decoded))
			}
			for k, wantRaw := range tc.input {
				gotRaw, ok := decoded[k]
				if !ok {
					t.Errorf("key %q missing in round-trip output", k)
					continue
				}
				var wantVal, gotVal any
				_ = json.Unmarshal(wantRaw, &wantVal)
				_ = json.Unmarshal(gotRaw, &gotVal)
				if !reflect.DeepEqual(wantVal, gotVal) {
					t.Errorf("key %q: want %v, got %v", k, wantVal, gotVal)
				}
			}
		})
	}
}

func TestMarshalRawMergedDeterministic(t *testing.T) {
	m := rawMerged{
		"zebra": json.RawMessage(`1`),
		"apple": json.RawMessage(`2`),
		"mango": json.RawMessage(`3`),
	}
	b, err := marshalRawMerged(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Parse keys in order from result.
	var ordered []string
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.Token() // '{'
	for dec.More() {
		tok, _ := dec.Token()
		key, _ := tok.(string)
		ordered = append(ordered, key)
		dec.Token() // skip value
	}
	sorted := make([]string, len(ordered))
	copy(sorted, ordered)
	sort.Strings(sorted)
	if !reflect.DeepEqual(ordered, sorted) {
		t.Errorf("keys not sorted: %v", ordered)
	}
}

// ── buildEntityFetchVars ──────────────────────────────────────────────────────

func TestBuildEntityFetchVars_NoVarNames(t *testing.T) {
	reps := []map[string]any{{"__typename": "User", "id": "1"}}
	got := buildEntityFetchVars(reps, map[string]any{"region": "US"}, nil)
	if _, ok := got["representations"]; !ok {
		t.Error("missing representations key")
	}
	if _, ok := got["region"]; ok {
		t.Error("region should not be forwarded when varNames is empty")
	}
}

func TestBuildEntityFetchVars_WithVars(t *testing.T) {
	reps := []map[string]any{{"__typename": "C", "id": "x"}}
	opVars := map[string]any{"region": "EU", "unrelated": "ignored"}
	got := buildEntityFetchVars(reps, opVars, []string{"region"})
	if got["region"] != "EU" {
		t.Errorf("region: want EU, got %v", got["region"])
	}
	if _, ok := got["unrelated"]; ok {
		t.Error("unrelated var should not be forwarded")
	}
	if got["representations"] == nil {
		t.Error("representations must be present")
	}
}

func TestBuildEntityFetchVars_StructOpVars(t *testing.T) {
	type myVars struct{ Region string }
	got := buildEntityFetchVars("reps", myVars{Region: "US"}, []string{"region"})
	if _, ok := got["region"]; ok {
		t.Error("struct opVars: should not extract vars (can't subset struct)")
	}
	if got["representations"] != "reps" {
		t.Error("representations must still be present")
	}
}

func TestBuildEntityFetchVars_MissingVar(t *testing.T) {
	opVars := map[string]any{"other": "val"}
	got := buildEntityFetchVars("reps", opVars, []string{"region"})
	if _, ok := got["region"]; ok {
		t.Error("missing var should not appear in result")
	}
}

// ── collectLeavesRaw + collectRepresentations with intermediate arrays ─────────

func TestCollectLeavesRaw_IntermediateArray(t *testing.T) {
	// Mirrors DistrictsJobsGetLearningPathsTests:
	// districtById → learningPathsTests[] → courses[] → course
	data := rawMerged{
		"districtById": json.RawMessage(`{
			"learningPathsTests": [
				{"courses": [
					{"course": {"contentId": "c1", "kaLocale": "en"}},
					{"course": {"contentId": "c2", "kaLocale": "en"}}
				]},
				{"courses": [
					{"course": {"contentId": "c3", "kaLocale": "es"}}
				]}
			]
		}`),
	}
	reps, err := collectRepresentations(data,
		[]string{"districtById", "learningPathsTests", "courses", "course"},
		"Course",
		[]string{"contentId", "kaLocale"},
		nil,   // requiresFields
		false, // isList: terminal is a single object per courses element
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reps) != 3 {
		t.Fatalf("expected 3 representations, got %d", len(reps))
	}
	wantIDs := []string{"c1", "c2", "c3"}
	for i, rep := range reps {
		var id string
		if err := json.Unmarshal(rep["contentId"], &id); err != nil {
			t.Fatalf("rep[%d] contentId: %v", i, err)
		}
		if id != wantIDs[i] {
			t.Errorf("rep[%d] contentId = %q, want %q", i, id, wantIDs[i])
		}
	}
}

func TestMergeEntityResults_IntermediateArray(t *testing.T) {
	data := rawMerged{
		"districtById": json.RawMessage(`{
			"learningPathsTests": [
				{"courses": [
					{"course": {"contentId": "c1", "kaLocale": "en"}},
					{"course": {"contentId": "c2", "kaLocale": "en"}}
				]},
				{"courses": [
					{"course": {"contentId": "c3", "kaLocale": "es"}}
				]}
			]
		}`),
	}
	entities := []json.RawMessage{
		json.RawMessage(`{"id": "ID1"}`),
		json.RawMessage(`{"id": "ID2"}`),
		json.RawMessage(`{"id": "ID3"}`),
	}
	mergeEntityResults(data,
		[]string{"districtById", "learningPathsTests", "courses", "course"},
		entities, false)

	// Decode merged result and check each course got the right id.
	var district struct {
		LearningPathsTests []struct {
			Courses []struct {
				Course struct {
					ContentID string `json:"contentId"`
					ID        string `json:"id"`
				} `json:"course"`
			} `json:"courses"`
		} `json:"learningPathsTests"`
	}
	if err := json.Unmarshal(data["districtById"], &district); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cases := []struct{ contentID, wantID string }{
		{"c1", "ID1"},
		{"c2", "ID2"},
		{"c3", "ID3"},
	}
	idx := 0
	for _, lpt := range district.LearningPathsTests {
		for _, c := range lpt.Courses {
			if idx >= len(cases) {
				t.Fatal("more courses than expected")
			}
			if c.Course.ContentID != cases[idx].contentID {
				t.Errorf(
					"course[%d] contentID = %q, want %q",
					idx,
					c.Course.ContentID,
					cases[idx].contentID,
				)
			}
			if c.Course.ID != cases[idx].wantID {
				t.Errorf(
					"course[%d] id = %q, want %q (entity not merged or wrong one merged)",
					idx,
					c.Course.ID,
					cases[idx].wantID,
				)
			}
			idx++
		}
	}
	if idx != 3 {
		t.Errorf("visited %d courses, want 3", idx)
	}
}

// TestExecute_EntityFetch_IntermediateArray exercises the full execute() path
// with an entity fetch whose parentPath traverses an intermediate array.
func TestExecute_EntityFetch_IntermediateArray(t *testing.T) {
	// Initial fetch returns a nested structure with an intermediate array.
	sgA := httptest.NewServer(jsonResp(t, map[string]any{
		"district": map[string]any{
			"tests": []any{
				map[string]any{"items": []any{
					map[string]any{"course": map[string]any{"contentId": "c1"}},
					map[string]any{"course": map[string]any{"contentId": "c2"}},
				}},
				map[string]any{"items": []any{
					map[string]any{"course": map[string]any{"contentId": "c3"}},
				}},
			},
		},
	}))
	defer sgA.Close()

	// Entity fetch: returns id for each course.
	var entityCallCount int
	sgB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entityCallCount++
		resp := map[string]any{
			"data": map[string]any{
				"_entities": []any{
					map[string]any{"id": "ID1"},
					map[string]any{"id": "ID2"},
					map[string]any{"id": "ID3"},
				},
			},
		}
		b, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	defer sgB.Close()

	plan := &Plan{
		Fetches: []Fetch{
			{URL: sgA.URL, Query: `{ district { tests { items { course { contentId } } } } }`},
		},
		EntityFetches: []EntityFetch{
			{
				URL:          sgB.URL,
				TypeName:     "Course",
				KeyFields:    []string{"contentId"},
				Selection:    "id\n",
				Query:        `query($representations: [_Any!]!) { _entities(representations: $representations) { ... on Course { id } } }`,
				ParentPath:   []string{"district", "tests", "items", "course"},
				IsParentList: false,
			},
		},
	}

	raw, errs, err := execute(context.Background(), plan, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if entityCallCount != 1 {
		t.Errorf("entity fetch called %d times, want 1", entityCallCount)
	}

	var result struct {
		District struct {
			Tests []struct {
				Items []struct {
					Course struct {
						ContentID string `json:"contentId"`
						ID        string `json:"id"`
					} `json:"course"`
				} `json:"items"`
			} `json:"tests"`
		} `json:"district"`
	}
	if err := json.Unmarshal(raw["district"], &result.District); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	wantIDs := map[string]string{"c1": "ID1", "c2": "ID2", "c3": "ID3"}
	for _, test := range result.District.Tests {
		for _, item := range test.Items {
			c := item.Course
			want, ok := wantIDs[c.ContentID]
			if !ok {
				t.Errorf("unexpected course contentId %q", c.ContentID)
				continue
			}
			if c.ID != want {
				t.Errorf("course %q: got id=%q, want %q", c.ContentID, c.ID, want)
			}
		}
	}
}
