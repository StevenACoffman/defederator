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
)

// goldenBase is the path to the gorouter golden fixtures, relative to this file's directory.
const goldenBase = "../../gorouter/federation/testdata/golden"

// TestClientGolden mirrors the gorouter golden_test.go but calls
// federationclient.Client.Execute instead of federation.Execute directly.
// It verifies:
//  1. Client.Execute correctly wraps federation.Execute.
//  2. The JSON round-trip (map[string]any → json.Marshal → json.Unmarshal) preserves data.
//  3. Plan caching: calling Execute twice on the same client returns the same result.
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

// resetableServer wraps an httptest.Server whose response-sequence counter can
// be reset between Execute calls so the plan gets the correct subgraph responses
// on both the first and the second (plan-cache hit) call.
type resetableServer struct {
	srv     *httptest.Server
	counter *atomic.Int64
}

// reset sets the response counter back to zero.
func (rs *resetableServer) reset() { rs.counter.Store(0) }

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
	// Responses are served in order: SUBGRAPH.json on call 1, SUBGRAPH_2.json on
	// call 2, etc. (for @requires multi-step plans).
	respDir := filepath.Join(dir, "subgraph_responses")
	entries, err := os.ReadDir(respDir)
	if err != nil {
		t.Fatalf("read subgraph_responses: %v", err)
	}

	// Collect response files grouped by subgraph enum name.
	seqFiles := make(map[string][]string) // enum → ordered response file paths
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
	// Sort each subgraph's files: SUBGRAPH.json first, then SUBGRAPH_2.json, etc.
	for enum := range seqFiles {
		sort.Slice(seqFiles[enum], func(i, j int) bool {
			return seqFiles[enum][i] < seqFiles[enum][j]
		})
	}

	// Build mock servers with resettable counters.
	rsMap := map[string]*resetableServer{}
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
		rsMap[enum] = &resetableServer{srv: srv, counter: &ctr}
	}

	// Patch SDL placeholder URLs with actual server addresses.
	sdl := sdlTemplate
	for enum, rs := range rsMap {
		sdl = strings.ReplaceAll(sdl, enum+"_URL", rs.srv.URL)
	}

	sg, err := federationclient.ParseSupergraphSDL(sdl)
	if err != nil {
		t.Fatalf("ParseSupergraphSDL: %v", err)
	}

	client := federationclient.NewClient(sg, nil)

	goldenPath := filepath.Join(dir, "expected.json")
	expected := clientMustReadBytes(t, goldenPath)

	// --- First Execute call ---
	var result1 map[string]any
	if err := client.Execute(context.Background(), query, "", variables, &result1); err != nil {
		t.Fatalf("Execute (call 1): %v", err)
	}

	actual1 := map[string]any{"data": result1}
	actual1Bytes := clientMustMarshalIndent(t, actual1)

	if !clientJSONEqual(expected, actual1Bytes) {
		t.Errorf("call 1 response mismatch\nwant: %s\n got: %s",
			clientNormalize(expected), clientNormalize(actual1Bytes))
	}

	// --- Second Execute call: verifies plan caching ---
	// Reset all mock server counters to 0 so the same response sequence is
	// replayed. The client is reused (same instance), so the plan is looked up
	// from the cache rather than rebuilt via federation.BuildPlan.
	for _, rs := range rsMap {
		rs.reset()
	}

	var result2 map[string]any
	if err := client.Execute(context.Background(), query, "", variables, &result2); err != nil {
		t.Fatalf("Execute (call 2 / cache hit): %v", err)
	}

	actual2 := map[string]any{"data": result2}
	actual2Bytes := clientMustMarshalIndent(t, actual2)

	if !clientJSONEqual(expected, actual2Bytes) {
		t.Errorf("call 2 (cache hit) response mismatch\nwant: %s\n got: %s",
			clientNormalize(expected), clientNormalize(actual2Bytes))
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
