package execengine_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/StevenACoffman/defederator/execengine"
)

// captureRequest starts a server that saves the most recent request body and
// returns response on every call. The returned getBody func returns the last
// captured body. Callers own starting/stopping via defer srv.Close().
func captureRequest(t *testing.T, response json.RawMessage) (srv *httptest.Server, getBody func() []byte) {
	t.Helper()
	var last []byte
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		last = b
		w.Header().Set("Content-Type", "application/json")
		w.Write(response)
	}))
	return srv, func() []byte { return last }
}

// TestEntities_Protocol verifies Layer 2: the _entities HTTP call carries the correct
// GraphQL representations (shape, field contents) for each entity fetch configuration.
func TestEntities_Protocol(t *testing.T) {
	// entityResponse wraps entities in the standard _entities response shape.
	entityResponse := func(entities ...map[string]any) json.RawMessage {
		resp := map[string]any{
			"data": map[string]any{
				"_entities": entities,
			},
		}
		b, _ := json.Marshal(resp)
		return b
	}

	// parentServer starts a server that returns the given object at the parentPath key.
	parentServer := func(t *testing.T, pathKey string, obj map[string]any) *httptest.Server {
		t.Helper()
		return httptest.NewServer(jsonResp(t, map[string]any{pathKey: obj}))
	}

	cases := map[string]struct {
		// parent object returned by the initial fetch
		parentObj     map[string]any
		parentPathKey string // top-level key in initial fetch response

		// EntityFetch configuration
		typeName       string
		keyFields      []string
		requiresFields []string
		isParentList   bool

		// entity server response (one entity per parent item)
		entityResp json.RawMessage

		// assertions on the captured _entities request
		wantRepCount      int
		wantRepTypeName   string
		wantRepKeyField   string
		wantRepKeyValue   any
		wantRepReqField   string // requiresField that must appear in representation
		wantRepReqValue   any
		// merged result assertion
		wantMergedKey   string
		wantMergedValue any
	}{
		"typename_always_present": {
			parentObj:     map[string]any{"id": "p1"},
			parentPathKey: "product",
			typeName:      "Product",
			keyFields:     []string{"id"},
			entityResp:    entityResponse(map[string]any{"sku": "fed"}),
			wantRepCount:    1,
			wantRepTypeName: "Product",
			wantRepKeyField: "id",
			wantRepKeyValue: "p1",
			wantMergedKey:   "sku",
			wantMergedValue: "fed",
		},
		"key_fields_extracted": {
			parentObj:     map[string]any{"id": "p2", "extraField": "ignored"},
			parentPathKey: "product",
			typeName:      "Product",
			keyFields:     []string{"id"},
			entityResp:    entityResponse(map[string]any{"name": "Apollo"}),
			wantRepCount:    1,
			wantRepTypeName: "Product",
			wantRepKeyField: "id",
			wantRepKeyValue: "p2",
			wantMergedKey:   "name",
			wantMergedValue: "Apollo",
		},
		"requires_fields_in_representation": {
			parentObj:     map[string]any{"email": "user@example.com", "totalProductsCreated": float64(42)},
			parentPathKey: "user",
			typeName:      "User",
			keyFields:     []string{"email"},
			requiresFields: []string{"totalProductsCreated"},
			entityResp:    entityResponse(map[string]any{"averagePerYear": float64(7)}),
			wantRepCount:    1,
			wantRepTypeName: "User",
			wantRepKeyField: "email",
			wantRepKeyValue: "user@example.com",
			wantRepReqField: "totalProductsCreated",
			wantRepReqValue: float64(42),
			wantMergedKey:   "averagePerYear",
			wantMergedValue: float64(7),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Initial fetch server returns parent object.
			initSrv := parentServer(t, tc.parentPathKey, tc.parentObj)
			defer initSrv.Close()

			// Entity fetch server: captures request, returns canned entity response.
			entitySrv, getBody := captureRequest(t, tc.entityResp)
			defer entitySrv.Close()

			plan := &execengine.Plan{
				Fetches: []execengine.Fetch{
					{URL: initSrv.URL, Query: `{ result { id email totalProductsCreated extraField } }`},
				},
				EntityFetches: []execengine.EntityFetch{
					{
						URL:            entitySrv.URL,
						TypeName:       tc.typeName,
						KeyFields:      tc.keyFields,
						RequiresFields: tc.requiresFields,
						Selection:      tc.wantMergedKey + "\n",
						ParentPath:     []string{tc.parentPathKey},
						IsParentList:   tc.isParentList,
					},
				},
				Projection: []*execengine.FieldProjection{
					{Key: tc.parentPathKey},
				},
			}
			_, _, err := execengine.Execute(context.Background(), plan, nil, nil)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}

			// Decode the captured _entities request.
			body := getBody()
			if len(body) == 0 {
				t.Fatal("entity server was not called")
			}
			var req struct {
				Variables struct {
					Representations []map[string]any `json:"representations"`
				} `json:"variables"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode captured request: %v", err)
			}

			reps := req.Variables.Representations
			if len(reps) != tc.wantRepCount {
				t.Fatalf("representation count: got %d, want %d", len(reps), tc.wantRepCount)
			}

			rep := reps[0]
			if rep["__typename"] != tc.wantRepTypeName {
				t.Errorf("__typename: got %v, want %q", rep["__typename"], tc.wantRepTypeName)
			}
			if rep[tc.wantRepKeyField] != tc.wantRepKeyValue {
				t.Errorf("key field %q: got %v, want %v", tc.wantRepKeyField, rep[tc.wantRepKeyField], tc.wantRepKeyValue)
			}
			if tc.wantRepReqField != "" {
				if rep[tc.wantRepReqField] != tc.wantRepReqValue {
					t.Errorf("requires field %q: got %v, want %v", tc.wantRepReqField, rep[tc.wantRepReqField], tc.wantRepReqValue)
				}
			}
			// extraField must NOT appear in the representation (only key + requires fields).
			if _, ok := rep["extraField"]; ok {
				t.Error("extraField should not appear in representation — only key and requires fields allowed")
			}
		})
	}
}

// TestEntities_ListParent verifies that when IsParentList=true, each list item produces
// one representation and receives its corresponding entity merged back by index.
func TestEntities_ListParent(t *testing.T) {
	items := []any{
		map[string]any{"id": "a"},
		map[string]any{"id": "b"},
		map[string]any{"id": "c"},
	}

	initSrv := httptest.NewServer(jsonResp(t, map[string]any{"products": items}))
	defer initSrv.Close()

	entityResp := map[string]any{
		"data": map[string]any{
			"_entities": []any{
				map[string]any{"sku": "sku-a"},
				map[string]any{"sku": "sku-b"},
				map[string]any{"sku": "sku-c"},
			},
		},
	}
	entitySrv, getBody := captureRequest(t, mustMarshal(t, entityResp))
	defer entitySrv.Close()

	plan := &execengine.Plan{
		Fetches: []execengine.Fetch{
			{URL: initSrv.URL, Query: `{ products { id } }`},
		},
		EntityFetches: []execengine.EntityFetch{
			{
				URL:          entitySrv.URL,
				TypeName:     "Product",
				KeyFields:    []string{"id"},
				Selection:    "sku\n",
				ParentPath:   []string{"products"},
				IsParentList: true,
			},
		},
		Projection: []*execengine.FieldProjection{
			{Key: "products", Children: []*execengine.FieldProjection{
				{Key: "id"}, {Key: "sku"},
			}},
		},
	}

	data, errs, err := execengine.Execute(context.Background(), plan, nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(errs) > 0 {
		t.Fatalf("unexpected GraphQL errors: %v", errs)
	}

	// Verify 3 representations were sent.
	var req struct {
		Variables struct {
			Representations []map[string]any `json:"representations"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(getBody(), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(req.Variables.Representations) != 3 {
		t.Fatalf("expected 3 representations, got %d", len(req.Variables.Representations))
	}

	// Verify each representation has the right id.
	wantIDs := []string{"a", "b", "c"}
	for i, rep := range req.Variables.Representations {
		if rep["id"] != wantIDs[i] {
			t.Errorf("rep[%d].id: got %v, want %q", i, rep["id"], wantIDs[i])
		}
		if rep["__typename"] != "Product" {
			t.Errorf("rep[%d].__typename: got %v, want Product", i, rep["__typename"])
		}
	}

	// Verify entities merged at correct indices.
	products, _ := data["products"].([]any)
	if len(products) != 3 {
		t.Fatalf("expected 3 products, got %d", len(products))
	}
	wantSKUs := []string{"sku-a", "sku-b", "sku-c"}
	for i, item := range products {
		m, _ := item.(map[string]any)
		if m["sku"] != wantSKUs[i] {
			t.Errorf("product[%d].sku: got %v, want %q", i, m["sku"], wantSKUs[i])
		}
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustMarshal: %v", err)
	}
	return b
}
