// Package federationclient is the runtime imported by defederator-generated code.
// It wraps the gorouter federation engine with plan caching and typed result marshaling.
package federationclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/StevenACoffman/gorouter/federation"
)

// Supergraph is a type alias so callers don't need to import gorouter directly.
type Supergraph = federation.Supergraph

// ParseSupergraphSDL parses a Federation v2 supergraph SDL and returns a routing table.
func ParseSupergraphSDL(sdl string) (*Supergraph, error) {
	return federation.ParseSchema(sdl)
}

// Client holds a parsed supergraph, an HTTP client, and a plan cache.
// Plans are deterministic for a given (supergraph, query, operationName) triple;
// caching them avoids re-planning on every call.
type Client struct {
	sg        *Supergraph
	http      *http.Client
	planCache sync.Map // cacheKey string → *federation.Plan
}

// NewClient creates a federation client. If httpClient is nil, http.DefaultClient is used.
func NewClient(sg *Supergraph, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{sg: sg, http: httpClient}
}

// Execute runs a federated GraphQL operation and JSON-unmarshals the merged result into dest.
// doc is the full query document string; operationName selects which operation to run.
func (c *Client) Execute(ctx context.Context, doc, operationName string, vars map[string]any, dest any) error {
	cacheKey := doc + "\x00" + operationName
	v, ok := c.planCache.Load(cacheKey)
	if !ok {
		plan, err := federation.BuildPlan(c.sg, doc, operationName)
		if err != nil {
			return fmt.Errorf("federationclient: plan %q: %w", operationName, err)
		}
		v, _ = c.planCache.LoadOrStore(cacheKey, plan)
	}
	plan := v.(*federation.Plan)

	data, errs, err := federation.Execute(ctx, plan, vars, c.http)
	if err != nil {
		return fmt.Errorf("federationclient: execute %q: %w", operationName, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("federationclient: %q: %v", operationName, errs)
	}

	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("federationclient: marshal result: %w", err)
	}
	return json.Unmarshal(b, dest)
}
