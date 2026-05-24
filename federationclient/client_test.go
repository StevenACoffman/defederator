package federationclient_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/StevenACoffman/defederator/federationclient"
	"github.com/StevenACoffman/gorouter/federation"
)

// goldenBase is the path to the gorouter golden fixtures, relative to this file's directory.
const goldenBase = "../../gorouter/federation/testdata/golden"

// TestClientGolden drives execution through federationclient using the golden fixtures.
// It verifies:
//  1. The JSON round-trip (map[string]any → json.Marshal → json.Unmarshal) preserves data.
//  2. Plan specs are serialized and resolved correctly (BuildPlanSpec → JSON → Resolve).
func TestClientGolden(t *testing.T) {
	sdlTemplate := clientMustReadFile(t, filepath.Join(goldenBase, "supergraph.graphql"))

	entries, err := os.ReadDir(goldenBase)
	if err != nil {
		t.Skip("testdata/golden not present; run scripts/record_golden.sh first")
	}

	cases := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			cases[e.Name()] = filepath.Join(goldenBase, e.Name())
		}
	}
	if len(cases) == 0 {
		t.Skip("no golden fixtures found")
	}

	for name, dir := range cases {
		name, dir := name, dir
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runClientGoldenFixture(t, sdlTemplate, dir)
		})
	}
}

func runClientGoldenFixture(t *testing.T, sdlTemplate, dir string) {
	t.Helper()

	query := clientMustReadFile(t, filepath.Join(dir, "query.graphql"))

	var variables map[string]interface{}
	if data, err := os.ReadFile(filepath.Join(dir, "variables.json")); err == nil {
		_ = json.Unmarshal(data, &variables)
		if len(variables) == 0 {
			variables = nil
		}
	}

	// Build one mock httptest.Server per subgraph.
	respDir := filepath.Join(dir, "subgraph_responses")
	entries, err := os.ReadDir(respDir)
	if err != nil {
		t.Fatalf("read subgraph_responses: %v", err)
	}

	seqFiles := make(map[string][]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".json")
		var enum string
		if idx := strings.LastIndex(base, "_"); idx >= 0 {
			if _, err2 := fmt.Sscanf(base[idx+1:], "%d", new(int)); err2 == nil {
				enum = base[:idx]
			} else {
				enum = base
			}
		} else {
			enum = base
		}
		seqFiles[enum] = append(seqFiles[enum], filepath.Join(respDir, e.Name()))
	}
	for enum := range seqFiles {
		sort.Slice(seqFiles[enum], func(i, j int) bool {
			return seqFiles[enum][i] < seqFiles[enum][j]
		})
	}

	servers := make(map[string]*httptest.Server)
	for enum, files := range seqFiles {
		bodies := make([][]byte, 0, len(files))
		for _, f := range files {
			raw := clientMustReadBytes(t, f)
			var wrapper struct {
				Response json.RawMessage `json:"response"`
			}
			if json.Unmarshal(raw, &wrapper) == nil && len(wrapper.Response) > 0 {
				raw = wrapper.Response
			}
			bodies = append(bodies, raw)
		}
		var ctr atomic.Int64
		capturedBodies := bodies
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			idx := int(ctr.Add(1)) - 1
			if idx >= len(capturedBodies) {
				idx = len(capturedBodies) - 1
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(capturedBodies[idx])
		}))
		t.Cleanup(srv.Close)
		servers[enum] = srv
	}

	// Patch mock server URLs into the SDL, then extract the URL map.
	sdl := sdlTemplate
	for enum, srv := range servers {
		sdl = strings.ReplaceAll(sdl, enum+"_URL", srv.URL)
	}
	urls, err := federation.SubgraphURLs(sdl)
	if err != nil {
		t.Fatalf("SubgraphURLs: %v", err)
	}

	// Build the plan spec against the full supergraph (needed for planning metadata).
	sg, err := federation.ParseSchema(sdl)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	spec, err := federation.BuildPlanSpec(sg, query, "")
	if err != nil {
		t.Fatalf("BuildPlanSpec: %v", err)
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal PlanSpec: %v", err)
	}

	client, err := federationclient.NewClient(urls, nil, map[string]string{
		"": string(specJSON),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	goldenPath := filepath.Join(dir, "expected.json")
	expected := clientMustReadBytes(t, goldenPath)

	var result map[string]any
	if err := client.Execute(context.Background(), "", variables, &result); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	actualBytes := clientMustMarshalIndent(t, map[string]any{"data": result})
	if !clientJSONEqual(expected, actualBytes) {
		t.Errorf("response mismatch\nwant: %s\n got: %s",
			clientNormalize(expected), clientNormalize(actualBytes))
	}
}

// TestNewClient_UnknownSubgraph verifies that NewClient returns an error when a spec
// references a subgraph enum not present in the URL map.
func TestNewClient_UnknownSubgraph(t *testing.T) {
	badSpec := `{"fetches":[{"subgraphEnum":"NONEXISTENT","query":"{ product { id } }"}]}`
	_, clientErr := federationclient.NewClient(
		map[string]string{"OTHER": "https://other.example.com"},
		nil,
		map[string]string{"GetProduct": badSpec},
	)
	if clientErr == nil {
		t.Error("NewClient with unknown subgraph enum should return an error")
	}
	if !strings.Contains(clientErr.Error(), "NONEXISTENT") {
		t.Errorf("error should name the unknown enum, got: %v", clientErr)
	}
}

// --- test helpers ---

func clientMustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func clientMustReadBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func clientMustMarshalIndent(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// clientJSONEqual compares two JSON byte slices for semantic equality, ignoring key order.
func clientJSONEqual(a, b []byte) bool {
	var av, bv interface{}
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	an, _ := json.Marshal(av)
	bn, _ := json.Marshal(bv)
	return bytes.Equal(an, bn)
}

func clientNormalize(b []byte) []byte {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return out
}
