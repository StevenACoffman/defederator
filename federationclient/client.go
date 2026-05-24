// Package federationclient is the slim runtime shim imported by defederator-generated code.
// It resolves pre-built plan specs against a subgraph URL map at startup, then dispatches
// operations to subgraphs via execengine. It has no dependency on the federation planner.
package federationclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/StevenACoffman/defederator/execengine"
)

// Client holds pre-resolved federation plans and dispatches operations to subgraphs.
// It is safe for concurrent use.
type Client struct {
	http  *http.Client
	plans map[string]*execengine.Plan // opName → pre-resolved plan with URLs
}

// NewClient decodes planSpecs against urls and returns a Client ready to execute any
// registered operation. planSpecs maps operation name to a compact JSON-encoded plan spec
// (as embedded by the code generator). urls maps subgraph enum names to their HTTP endpoints.
// Returns an error if any spec is malformed or references a subgraph enum absent from urls.
func NewClient(urls map[string]string, httpClient *http.Client, planSpecs map[string]string) (*Client, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	plans := make(map[string]*execengine.Plan, len(planSpecs))
	for opName, specJSON := range planSpecs {
		plan, err := execengine.Resolve(specJSON, urls)
		if err != nil {
			return nil, fmt.Errorf("federationclient: resolve plan for %q: %w", opName, err)
		}
		plans[opName] = plan
	}
	return &Client{http: httpClient, plans: plans}, nil
}

// Execute runs the pre-built plan for opName and JSON-unmarshals the merged result into dest.
// Returns an error if no plan was registered for opName at NewClient time.
func (c *Client) Execute(ctx context.Context, opName string, vars map[string]any, dest any) error {
	plan, ok := c.plans[opName]
	if !ok {
		return fmt.Errorf("federationclient: no plan for operation %q", opName)
	}

	data, errs, err := execengine.Execute(ctx, plan, vars, c.http)
	if err != nil {
		return fmt.Errorf("federationclient: execute %q: %w", opName, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("federationclient: %q: %v", opName, errs)
	}

	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("federationclient: marshal result: %w", err)
	}
	return json.Unmarshal(b, dest)
}
