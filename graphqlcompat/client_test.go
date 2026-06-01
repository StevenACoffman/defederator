package graphqlcompat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Khan/genqlient/graphql"

	"github.com/StevenACoffman/defederator/graphqlcompat"
	"github.com/StevenACoffman/gorouter/federation"
)

// goldenBase is the path to the gorouter golden fixtures.
const goldenBase = "../../gorouter/federation/testdata/golden"

// compile-time assertion: NewClient must return graphql.Client.
var _ func(*federation.Supergraph, *http.Client) graphql.Client = graphqlcompat.NewClient

func TestGraphqlCompatClient_ImplementsInterface(t *testing.T) {
	sdlBytes, err := os.ReadFile(filepath.Join(goldenBase, "supergraph.graphql"))
	if err != nil {
		t.Skip("golden supergraph not found; skipping")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"product":{"id":"apollo-federation","sku":"federation"}}}`))
	}))
	defer srv.Close()

	sdl := patchURLs(string(sdlBytes), srv.URL)

	sg, err := federation.ParseSchema(sdl)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}

	c := graphqlcompat.NewClient(sg, nil)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
}

func TestMakeRequest_BasicQuery(t *testing.T) {
	sdlBytes, err := os.ReadFile(filepath.Join(goldenBase, "supergraph.graphql"))
	if err != nil {
		t.Skip("golden supergraph not found; skipping")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"product":{"id":"apollo-federation","sku":"federation"}}}`))
	}))
	defer srv.Close()

	sdl := patchURLs(string(sdlBytes), srv.URL)

	sg, err := federation.ParseSchema(sdl)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}

	c := graphqlcompat.NewClient(sg, nil)

	req := &graphql.Request{
		Query:  `query GetProductIdSku { product(id: "apollo-federation") { id sku } }`,
		OpName: "GetProductIdSku",
	}
	var resp graphql.Response
	if err := c.MakeRequest(context.Background(), req, &resp); err != nil {
		t.Fatalf("MakeRequest: %v", err)
	}

	b, _ := json.Marshal(resp.Data)
	t.Logf("resp.Data: %s", b)

	m, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("resp.Data is %T, want map[string]any", resp.Data)
	}
	if m["product"] == nil {
		t.Error("resp.Data[product] is nil")
	}
}

// patchURLs replaces SUBGRAPH_URL placeholders in the SDL with serverURL.
func patchURLs(sdl, serverURL string) string {
	for _, subgraph := range []string{"ACCOUNTS", "PRODUCTS", "INVENTORY", "USERS", "REVIEWS"} {
		sdl = strings.ReplaceAll(sdl, subgraph+"_URL", serverURL)
	}
	return sdl
}
