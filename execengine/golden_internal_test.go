package execengine

// Golden fixture tests (Layer 2 + Layer 3):
// Drives all five federation golden fixtures through scripted HTTP subgraph servers
// and validates both the request variables our planner sends (Layer 2 — federation
// entity resolution protocol) and the merged result (Layer 3 — cross-subgraph
// merge correctness).
//
// The fixture files in gorouter/federation/testdata/golden/*/subgraph_responses/
// were captured from Apollo's reference implementation and encode the canonical
// request/response for each step. Files with a top-level "request" field are the
// authoritative spec; files without (RAW_ONLY, e.g. PRODUCTS_1.json) are
// intermediate duplicates and are skipped.
//
// This file imports gorouter/federation to build plans. That import appears only in
// _test.go files and does not affect execengine's production dependency graph.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/StevenACoffman/gorouter/federation"
)

const goldenFixtureBase = "../../gorouter/federation/testdata/golden"

// scriptedCall pairs an expected request variables map (for validation) with
// the response body to return. wantVars is nil for calls that carry no variables
// (initial fetches), in which case only the response is checked.
type scriptedCall struct {
	wantVars map[string]any // nil = skip variable validation
	response json.RawMessage
}

// goldenFixture holds the parsed contents of one golden fixture directory.
type goldenFixture struct {
	name     string
	query    string
	vars     map[string]any
	expected map[string]any
	// calls maps subgraph enum name (e.g. "PRODUCTS") to an ordered slice of
	// scripted calls. Only files that carry a "request" field are included.
	calls map[string][]scriptedCall
}

// loadGoldenFixtures is a pure function: reads fixture directories and returns
// structured data. No servers, no network calls.
func loadGoldenFixtures(t *testing.T, base string) []goldenFixture {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Skipf("golden fixture dir not found (%s): skipping", base)
		return nil
	}
	var fixtures []goldenFixture
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(base, e.Name())
		if fx := loadOneFixture(t, e.Name(), dir); fx != nil {
			fixtures = append(fixtures, *fx)
		}
	}
	return fixtures
}

func loadOneFixture(t *testing.T, name, dir string) *goldenFixture {
	t.Helper()
	queryBytes, err := os.ReadFile(filepath.Join(dir, "query.graphql"))
	if err != nil {
		t.Logf("skipping %s: no query.graphql", name)
		return nil
	}
	expectedBytes, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		t.Logf("skipping %s: no expected.json", name)
		return nil
	}
	var expected map[string]any
	if err := json.Unmarshal(expectedBytes, &expected); err != nil {
		t.Fatalf("fixture %s: decode expected.json: %v", name, err)
	}
	var vars map[string]any
	if vb, err2 := os.ReadFile(filepath.Join(dir, "variables.json")); err2 == nil {
		_ = json.Unmarshal(vb, &vars)
	}
	calls := loadSubgraphCalls(t, name, filepath.Join(dir, "subgraph_responses"))
	return &goldenFixture{
		name:     name,
		query:    string(queryBytes),
		vars:     vars,
		expected: expected,
		calls:    calls,
	}
}

// loadSubgraphCalls reads subgraph_responses/*.json, keeping only files that
// have a top-level "request" key (the Apollo-captured request+response pairs).
// Files are sorted by name to establish call order within each subgraph.
// The "request.variables" field (when present) becomes the wantVars for
// validation; calls with no variables (initial fetches) get wantVars=nil.
func loadSubgraphCalls(t *testing.T, fixtureName, dir string) map[string][]scriptedCall {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	bySubgraph := make(map[string][]scriptedCall)
	for _, fname := range names {
		raw, err := os.ReadFile(filepath.Join(dir, fname))
		if err != nil {
			t.Fatalf("fixture %s: read %s: %v", fixtureName, fname, err)
		}
		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			t.Fatalf("fixture %s: decode %s: %v", fixtureName, fname, err)
		}
		// Skip RAW_ONLY files (no "request" key) — they are duplicates of the
		// response portion of a corresponding HAS_REQUEST file.
		reqRaw, hasRequest := wrapper["request"]
		if !hasRequest {
			continue
		}
		var reqSpec struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(reqRaw, &reqSpec)

		subgraph := subgraphFromFilename(fname)
		bySubgraph[subgraph] = append(bySubgraph[subgraph], scriptedCall{
			wantVars: reqSpec.Variables, // nil when request has no variables
			response: wrapper["response"],
		})
	}
	return bySubgraph
}

// subgraphFromFilename extracts the subgraph enum name from a fixture filename.
// "PRODUCTS.json" → "PRODUCTS", "PRODUCTS_2.json" → "PRODUCTS".
func subgraphFromFilename(fname string) string {
	base := strings.TrimSuffix(fname, ".json")
	if idx := strings.LastIndex(base, "_"); idx >= 0 {
		suffix := base[idx+1:]
		allDigits := suffix != ""
		for _, c := range suffix {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return base[:idx]
		}
	}
	return base
}

// scriptedServer starts a server that validates the request variables (when
// wantVars is non-nil) and returns the scripted response on each successive
// call. It fails the test if called more times than calls are scripted.
func scriptedServer(t *testing.T, calls []scriptedCall) *httptest.Server {
	t.Helper()
	var callCount int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := callCount
		callCount++
		if n >= len(calls) {
			t.Errorf("subgraph server called %d times but only %d calls scripted", n+1, len(calls))
			http.Error(w, "unexpected call", http.StatusInternalServerError)
			return
		}
		call := calls[n]

		// Validate variables when the fixture specifies them.
		// This is the core Apollo Federation spec compliance check:
		// representations must carry __typename + key fields + @requires fields.
		if call.wantVars != nil {
			var actualReq struct {
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(body, &actualReq); err != nil {
				t.Errorf("call %d: decode request body: %v", n+1, err)
			} else if !reflect.DeepEqual(actualReq.Variables, call.wantVars) {
				gotJSON, _ := json.MarshalIndent(actualReq.Variables, "", "  ")
				wantJSON, _ := json.MarshalIndent(call.wantVars, "", "  ")
				t.Errorf("call %d variables mismatch:\ngot:\n%s\nwant:\n%s",
					n+1, gotJSON, wantJSON)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(call.response)
	}))
}

// patchSupergraphURLs replaces *_URL placeholders in the SDL with the provided
// server URLs.
func patchSupergraphURLs(sdl string, urlMap map[string]string) string {
	for enum, url := range urlMap {
		sdl = strings.ReplaceAll(sdl, enum+"_URL", url)
	}
	return sdl
}

// TestGoldenFixtures drives all five golden fixture scenarios through scripted
// subgraph servers, validating:
//   - Layer 2: the variables (representations) sent to entity-fetch subgraphs
//     match the canonical values captured from Apollo's reference implementation
//   - Layer 3: the merged output matches expected.json exactly
func TestGoldenFixtures(t *testing.T) {
	sdlBytes, err := os.ReadFile(filepath.Join(goldenFixtureBase, "supergraph.graphql"))
	if err != nil {
		t.Skip("supergraph.graphql not found; skipping golden tests")
	}

	fixtures := loadGoldenFixtures(t, goldenFixtureBase)
	if len(fixtures) == 0 {
		t.Skip("no golden fixtures found")
	}

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			runGoldenFixture(t, fx, sdlBytes)
		})
	}
}

// runGoldenFixture executes one golden-fixture subtest. Factored out to keep
// TestGoldenFixtures under the cognitive-complexity cap.
func runGoldenFixture(t *testing.T, fx goldenFixture, sdlBytes []byte) {
	t.Helper()
	urlMap, closeAll := startSubgraphServers(t, fx.calls)
	defer closeAll()
	epPlan := buildGoldenPlan(t, string(sdlBytes), urlMap, fx.query)
	raw, errs, err := execute(context.Background(), epPlan, fx.vars, nil, false)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(errs) > 0 {
		t.Fatalf("GraphQL errors: %v", errs)
	}
	gotData := mergedToMap(t, raw)
	wantData, _ := fx.expected["data"].(map[string]any)
	if !reflect.DeepEqual(gotData, wantData) {
		gotJSON, _ := json.MarshalIndent(gotData, "", "  ")
		wantJSON, _ := json.MarshalIndent(wantData, "", "  ")
		t.Errorf("merged output mismatch:\ngot:\n%s\nwant:\n%s", gotJSON, wantJSON)
	}
}

// startSubgraphServers starts one scripted server per subgraph with its
// ordered call sequence. Returns the subgraph→URL map and a cleanup func.
func startSubgraphServers(
	t *testing.T,
	calls map[string][]scriptedCall,
) (map[string]string, func()) {
	t.Helper()
	urlMap := make(map[string]string, len(calls))
	var servers []*httptest.Server
	for subgraph, cs := range calls {
		srv := scriptedServer(t, cs)
		servers = append(servers, srv)
		urlMap[subgraph] = srv.URL
	}
	return urlMap, func() {
		for _, srv := range servers {
			srv.Close()
		}
	}
}

// buildGoldenPlan patches the supergraph SDL with the test server URLs and
// builds the executable plan for the given query.
func buildGoldenPlan(
	t *testing.T,
	sdl string,
	urlMap map[string]string,
	query string,
) *Plan {
	t.Helper()
	patchedSDL := patchSupergraphURLs(sdl, urlMap)
	sg, err := federation.ParseSchema(patchedSDL)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	plan, err := federation.BuildPlan(sg, query, "")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	return planToExecPlan(t, plan)
}

// planToExecPlan converts a *federation.Plan to *Plan by directly
// mapping the resolved *Subgraph URL pointers — no JSON round-trip needed here.
func planToExecPlan(t *testing.T, plan *federation.Plan) *Plan {
	t.Helper()
	ep := &Plan{
		Fetches:    make([]Fetch, 0, len(plan.Fetches)),
		Projection: convertProjection(plan.Projection),
	}
	for _, f := range plan.Fetches {
		ep.Fetches = append(ep.Fetches, Fetch{
			URL:       f.Subgraph.URL,
			Query:     f.Query,
			Variables: f.Variables,
		})
	}
	for _, ef := range plan.EntityFetches {
		ep.EntityFetches = append(ep.EntityFetches, EntityFetch{
			URL:            ef.Subgraph.URL,
			TypeName:       ef.TypeName,
			KeyFields:      ef.KeyFields,
			RequiresFields: ef.RequiresFields,
			Selection:      ef.Selection,
			ParentPath:     ef.ParentPath,
			IsParentList:   ef.IsParentList,
		})
	}
	return ep
}

func convertProjection(fps []*federation.FieldProjection) []*FieldProjection {
	if fps == nil {
		return nil
	}
	out := make([]*FieldProjection, len(fps))
	for i, fp := range fps {
		out[i] = &FieldProjection{
			Key:      fp.Key,
			Children: convertProjection(fp.Children),
		}
	}
	return out
}

func mergedToMap(t *testing.T, m rawMerged) map[string]any {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("mergedToMap marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("mergedToMap unmarshal: %v", err)
	}
	return out
}
