package execengine_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/StevenACoffman/defederator/execengine"
)

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
	plan, err := execengine.Resolve(specJSON, urls)
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
	_, err := execengine.Resolve(specJSON, map[string]string{"OTHER": "https://other.example.com"})
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
	plan, err := execengine.Resolve(specJSON, map[string]string{"SG": "https://sg.example.com"})
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

	plan := &execengine.Plan{
		Fetches: []execengine.Fetch{
			{URL: srv.URL, Query: `{ product(id: "apollo-federation") { id sku } }`},
		},
		Projection: []*execengine.FieldProjection{
			{Key: "product", Children: []*execengine.FieldProjection{
				{Key: "id"},
				{Key: "sku"},
			}},
		},
	}

	data, errs, err := execengine.Execute(context.Background(), plan, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	product, _ := data["product"].(map[string]any)
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

	plan := &execengine.Plan{
		Fetches: []execengine.Fetch{
			{URL: sgA.URL, Query: `{ product(id: "p1") { id __typename } }`},
		},
		EntityFetches: []execengine.EntityFetch{
			{
				URL:        sgB.URL,
				TypeName:   "Product",
				KeyFields:  []string{"id"},
				Selection:  "sku\n",
				ParentPath: []string{"product"},
			},
		},
		Projection: []*execengine.FieldProjection{
			{Key: "product", Children: []*execengine.FieldProjection{
				{Key: "id"},
				{Key: "sku"},
			}},
		},
	}

	data, errs, err := execengine.Execute(context.Background(), plan, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	product, _ := data["product"].(map[string]any)
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

	plan := &execengine.Plan{
		Fetches: []execengine.Fetch{
			{URL: srv.URL, Query: `{ ping }`},
		},
	}
	_, _, err := execengine.Execute(context.Background(), plan, nil, nil)
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

	plan := &execengine.Plan{
		Fetches: []execengine.Fetch{{URL: srv.URL, Query: `{ product { id } }`}},
	}
	_, errs, err := execengine.Execute(context.Background(), plan, nil, nil)
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
	data := map[string]any{
		"product": map[string]any{
			"id":         "p1",
			"sku":        "s1",
			"__typename": "Product", // planner-added; not in projection
		},
	}
	proj := []*execengine.FieldProjection{
		{Key: "product", Children: []*execengine.FieldProjection{
			{Key: "id"},
			{Key: "sku"},
		}},
	}
	result := execengine.ApplyProjection(data, proj)
	p := result["product"].(map[string]any)
	if _, ok := p["__typename"]; ok {
		t.Error("__typename should be stripped by projection")
	}
	if p["id"] != "p1" || p["sku"] != "s1" {
		t.Errorf("unexpected result: %v", p)
	}
}

func TestResolveURLSpec_RoundTrip(t *testing.T) {
	cases := map[string]struct {
		specJSON       string
		wantFetchURL   string
		wantQuery      string
		wantVars       []string
		wantEntityURL  string
		wantTypeName   string
		wantKeyFields  []string
		wantProjKey    string
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
			plan, err := execengine.ResolveURLSpec(tc.specJSON)
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
				if len(plan.Fetches[0].Variables) != len(tc.wantVars) || plan.Fetches[0].Variables[0] != tc.wantVars[0] {
					t.Errorf("fetch variables: got %v, want %v", plan.Fetches[0].Variables, tc.wantVars)
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
				if len(ef.KeyFields) != len(tc.wantKeyFields) || ef.KeyFields[0] != tc.wantKeyFields[0] {
					t.Errorf("entity keyFields: got %v, want %v", ef.KeyFields, tc.wantKeyFields)
				}
			}
			if tc.wantProjKey != "" {
				if len(plan.Projection) == 0 || plan.Projection[0].Key != tc.wantProjKey {
					t.Errorf("projection key: got %v, want %q", plan.Projection, tc.wantProjKey)
				}
				if len(plan.Projection[0].Children) != tc.wantProjChildren {
					t.Errorf("projection children: got %d, want %d", len(plan.Projection[0].Children), tc.wantProjChildren)
				}
			}
		})
	}
}

func TestResolveURLSpec_MalformedJSON(t *testing.T) {
	_, err := execengine.ResolveURLSpec(`not-json`)
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

	plan := &execengine.Plan{
		Fetches: []execengine.Fetch{{URL: srv.URL, Query: `{ product { id sku } }`}},
		Projection: []*execengine.FieldProjection{
			{Key: "product", Children: []*execengine.FieldProjection{
				{Key: "id"}, {Key: "sku"},
			}},
		},
	}

	var dest result
	if err := execengine.ExecuteAndUnmarshal(context.Background(), plan, nil, nil, &dest); err != nil {
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

	plan := &execengine.Plan{
		Fetches: []execengine.Fetch{{URL: srv.URL, Query: `{ q }`}},
	}
	var dest map[string]any
	err := execengine.ExecuteAndUnmarshal(context.Background(), plan, nil, nil, &dest)
	if err == nil {
		t.Fatal("expected error for GraphQL errors")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should mention 'boom', got: %v", err)
	}
}

func TestFilterVars_Subset(t *testing.T) {
	// filterVars is private; test indirectly via Execute variable forwarding.
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

	plan := &execengine.Plan{
		Fetches: []execengine.Fetch{
			{URL: srv.URL, Query: `query($id: ID!) { p(id: $id) { id } }`, Variables: []string{"id"}},
		},
	}
	data, _, err := execengine.Execute(context.Background(), plan, map[string]any{"id": "x1", "extra": "y"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if data["id"] != "x1" {
		t.Errorf("expected id=x1 in forwarded vars, got %v", data)
	}
	if _, ok := data["extra"]; ok {
		t.Error("extra variable should not have been forwarded")
	}
}

// TestExecuteAndUnmarshal_SingleFetch_NullData verifies the fast path handles
// a subgraph that returns null data without panicking.
func TestExecuteAndUnmarshal_SingleFetch_NullData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":null}`))
	}))
	defer srv.Close()

	plan := &execengine.Plan{
		Fetches: []execengine.Fetch{{URL: srv.URL, Query: `{ product { id } }`}},
	}
	type result struct {
		Product *struct{ ID string `json:"id"` } `json:"product"`
	}
	var dest result
	if err := execengine.ExecuteAndUnmarshal(context.Background(), plan, nil, nil, &dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest.Product != nil {
		t.Errorf("expected nil Product for null data, got %+v", dest.Product)
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

	plan := &execengine.Plan{
		Fetches: []execengine.Fetch{
			{URL: srvUser.URL, Query: `{ user { id name } }`},
			{URL: srvPost.URL, Query: `{ post { title } }`},
		},
	}

	var got dest
	if err := execengine.ExecuteAndUnmarshal(context.Background(), plan, nil, nil, &got); err != nil {
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

	plan := &execengine.Plan{
		Fetches: []execengine.Fetch{
			{URL: sgA.URL, Query: `{ product(id: "p1") { id __typename } }`},
		},
		EntityFetches: []execengine.EntityFetch{
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
	if err := execengine.ExecuteAndUnmarshal(context.Background(), plan, nil, nil, &got); err != nil {
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
