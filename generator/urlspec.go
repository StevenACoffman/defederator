package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/StevenACoffman/defederator/execengine"
	"github.com/StevenACoffman/gorouter/federation"
)

// urlSpecFetch is the JSON form of a URL-keyed fetch step.
type urlSpecFetch struct {
	URL       string   `json:"url"`
	Query     string   `json:"query"`
	Variables []string `json:"variables,omitempty"`
}

// urlSpecEntityFetch is the JSON form of a URL-keyed entity fetch step.
type urlSpecEntityFetch struct {
	URL            string   `json:"url"`
	TypeName       string   `json:"typeName"`
	KeyFields      []string `json:"keyFields"`
	RequiresFields []string `json:"requiresFields,omitempty"`
	Selection      string   `json:"selection"`
	ParentPath     []string `json:"parentPath"`
	IsParentList   bool     `json:"isParentList,omitempty"`
}

// urlSpecDoc is the top-level JSON object for a URL-keyed plan spec.
type urlSpecDoc struct {
	Fetches       []urlSpecFetch        `json:"fetches"`
	EntityFetches []urlSpecEntityFetch  `json:"entityFetches,omitempty"`
	Projection    []*federation.FieldProjection `json:"projection,omitempty"`
}

// MarshalURLPlanSpec converts a *federation.Plan to a compact JSON string using
// URL-keyed format. The subgraph URLs come from the resolved *Subgraph pointers
// in the plan, so no URL map lookup is needed at runtime.
func MarshalURLPlanSpec(plan *federation.Plan) (string, error) {
	doc := urlSpecDoc{
		Fetches:    make([]urlSpecFetch, 0, len(plan.Fetches)),
		Projection: plan.Projection,
	}
	for _, f := range plan.Fetches {
		doc.Fetches = append(doc.Fetches, urlSpecFetch{
			URL:       f.Subgraph.URL,
			Query:     f.Query,
			Variables: f.Variables,
		})
	}
	for _, ef := range plan.EntityFetches {
		doc.EntityFetches = append(doc.EntityFetches, urlSpecEntityFetch{
			URL:            ef.Subgraph.URL,
			TypeName:       ef.TypeName,
			KeyFields:      ef.KeyFields,
			RequiresFields: ef.RequiresFields,
			Selection:      ef.Selection,
			ParentPath:     ef.ParentPath,
			IsParentList:   ef.IsParentList,
		})
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("generator: marshal url plan spec: %w", err)
	}
	return string(b), nil
}

// MarshalEnumPlanSpec converts a *federation.Plan to a compact JSON string using
// enum-keyed format. Unlike MarshalURLPlanSpec, the subgraph URLs are NOT
// embedded in the spec — instead each fetch uses the join__Graph enum name
// (e.g. "USERS"). Call execengine.Resolve with a URL map at runtime to get a
// Plan with real URLs. Use this when the supergraph SDL has placeholder URLs.
func MarshalEnumPlanSpec(plan *federation.Plan) (string, error) {
	spec := federation.PlanToSpec(plan)
	b, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("generator: marshal enum plan spec: %w", err)
	}
	return string(b), nil
}

// WriteExecFile reads the embedded execengine source, replaces its package
// declaration with pkg, and writes the result to <outDir>/federation_exec.go.
// The written file gives generated code access to the executor without any
// defederator import.
func WriteExecFile(outDir, pkg string) error {
	src := execengine.Source
	src = strings.Replace(src, "package execengine\n", "package "+pkg+"\n", 1)
	// Strip the source.go embed directive line — it's in a separate file.
	// (execengine.go does not contain any embed directives itself, so no-op.)
	dest := filepath.Join(outDir, "federation_exec.go")
	if err := os.WriteFile(dest, []byte(src), 0644); err != nil {
		return fmt.Errorf("generator: write federation_exec.go: %w", err)
	}
	return nil
}
