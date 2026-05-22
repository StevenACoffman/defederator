# defederator

`defederator` is a code generator that produces typed Go GraphQL clients that bypass a gateway entirely. Given a Federation v2 supergraph SDL and `.graphql` operation files, it generates a Go client whose methods use the federation query planner to call subgraph services directly—resolving cross-subgraph joins, entity fetches, `@requires`, and `@provides` in process—without a running Apollo Router or similar gateway.

## How it works

```
supergraph.graphql + queries/*.graphql
        │
        ▼
  defederator (code generator)
        │
        ├── strips federation metadata from the SDL
        ├── uses gqlgenc's type pipeline to generate typed response structs
        └── renders with a federation-aware template
        │
        ▼
  generated/client.go
        │
        ▼  at runtime
  federationclient.Client.Execute(ctx, doc, opName, vars, &dest)
        │
        ├── BuildPlan (cached per query+operationName)
        └── Execute → parallel subgraph fetches → entity fetches → merge
```

The generated client is a drop-in for a gqlgenc-generated client, with one difference: instead of `NewClient(httpClient, baseURL, ...)` it takes `NewClient(sg *federationclient.Supergraph, httpClient *http.Client)`.

## Quick start

### 1. Install the generator

```sh
go install github.com/StevenACoffman/defederator/cmd/defederator@latest
```

### 2. Write a config file

```yaml
# .defederator.yml
schema: supergraph.graphql      # Federation v2 supergraph SDL

query:
  - queries/**/*.graphql        # GraphQL operation files (named operations only)

client:
  filename: ./generated/client.go
  package:  generated

# Optional: override subgraph URLs per environment
subgraph_urls:
  ACCOUNTS: http://localhost:4001/graphql
  PRODUCTS: http://localhost:4002/graphql

# Optional: generate a named interface
generate:
  clientInterfaceName: FederationClient
```

### 3. Write named operations

```graphql
# queries/products.graphql
query GetProduct($id: ID!) {
  product(id: $id) {
    id
    sku
    createdBy {
      email
      name
    }
  }
}
```

### 4. Generate

```sh
defederator                        # reads .defederator.yml in current or parent dir
defederator -c path/to/.defederator.yml
```

### 5. Use the generated client

```go
import (
    "os"

    "github.com/StevenACoffman/defederator/federationclient"
    "yourmodule/generated"
)

func main() {
    sdl, _ := os.ReadFile("supergraph.graphql")
    sg, err := federationclient.ParseSupergraphSDL(string(sdl))
    if err != nil { ... }

    // Optional: override URLs at runtime (e.g. from env vars)
    sg = sg.WithURLOverrides(map[string]string{
        "PRODUCTS": os.Getenv("PRODUCTS_URL"),
    })

    client := generated.NewClient(sg, nil) // nil → http.DefaultClient

    res, err := client.GetProduct(ctx, "apollo-federation")
    if err != nil { ... }
    fmt.Println(res.Product.Sku)
}
```

## Package layout

```
defederator/
├── cmd/defederator/        # CLI binary: reads .defederator.yml, calls generator.Generate
├── config/                 # .defederator.yml loader
├── federationclient/       # Runtime package imported by generated code
│   ├── client.go           #   Client with sync.Map plan cache + JSON round-trip
│   └── client_test.go      #   Golden tests: all 5 federation scenarios
├── generator/
│   ├── generator.go        # Orchestrates schema strip → gqlgenc pipeline → render
│   ├── schema.go           # StripFederationTypes: supergraph SDL → clean SDL
│   ├── template.go         # RenderFederationTemplate (mirrors clientgenv2.RenderTemplate)
│   ├── template.gotpl      # Federation-specific Go template
│   └── *_test.go           # Unit + codegen compile tests for all 5 scenarios
└── gqlgencfed/
    ├── plugin.go           # gqlgenc plugin (replaces "clientgen" via api.ReplacePlugin)
    └── plugin_test.go      # End-to-end plugin path test
```

## Two generation paths

### Path A — standalone binary (recommended)

Calls `generator.Generate` directly. No dependency on gqlgenc's `generator.Generate`.

```sh
defederator -c .defederator.yml
```

### Path B — gqlgenc plugin

Requires the modified `gqlgenc/generator/generator.go` (one-line signature change to accept `options ...api.Option`).

```go
import (
    "github.com/99designs/gqlgen/api"
    "github.com/gqlgo/gqlgenc/generator"
    "github.com/StevenACoffman/defederator/gqlgencfed"
)

err = generator.Generate(ctx, cfg,
    api.ReplacePlugin(gqlgencfed.NewWithFilePaths(queryPaths, cfg.Client, cfg.Generate)),
)
```

Use this path when you have an existing gqlgenc workflow and want to swap the transport layer without changing your config or query files.

## Runtime package: `federationclient`

Generated code imports `github.com/StevenACoffman/defederator/federationclient`.

```go
// Parse the supergraph SDL once at startup.
sg, err := federationclient.ParseSupergraphSDL(sdl)

// Optionally override subgraph URLs (e.g. per environment).
sg = sg.WithURLOverrides(map[string]string{"PRODUCTS": productionURL})

// Create the client. Plans are cached in a sync.Map; safe for concurrent use.
client := federationclient.NewClient(sg, httpClient)

// Execute a named operation. dest must be a pointer to a struct or map[string]any.
var result MyResponseType
err = client.Execute(ctx, queryDocument, "OperationName", variables, &result)
```

`Client` is safe for concurrent use. Plan compilation (the expensive part) happens once per `(doc, operationName)` pair and is cached for the lifetime of the client.

## Schema stripping

The supergraph SDL contains federation metadata (`@join__type`, `@join__field`, `join__Graph` enum, etc.) that gqlgen's type mapper does not understand. `generator.StripFederationTypes` removes all federation-specific directive definitions, type definitions, and per-field annotations before passing the schema to gqlgenc's pipeline. User-defined directives (e.g. `@custom`) are preserved.

## Verified scenarios

The test suite covers all five federation patterns from the Apollo Federation compatibility suite:

| Fixture | Pattern |
|---|---|
| `01_product_id_sku` | Simple single-subgraph lookup |
| `02_product_delivery` | Cross-subgraph join with entity fetch + `@requires` (cross-subgraph) |
| `03_product_creator_name` | Three-subgraph join |
| `04_product_creator_requires` | `@requires` (same-subgraph, multi-step plan) |
| `05_product_creator_provides` | `@provides` (key field pre-fetched, no extra entity fetch) |

Each scenario is tested at three layers:
1. **`federationclient` golden tests** — `Client.Execute` produces output matching recorded Apollo Router responses; second call verifies plan cache hit.
2. **Codegen compile tests** — generator produces syntactically valid Go with correct function signatures, type names, and `Execute` call sites for every scenario.
3. **`gqlgencfed` plugin test** — plugin path through modified `gqlgenc/generator.Generate` produces identical output.

## Dependencies

| Dependency | Role |
|---|---|
| `github.com/StevenACoffman/gorouter/federation` | Query planner (`BuildPlan`) and executor (`Execute`) |
| `github.com/gqlgo/gqlgenc` | Type-generation pipeline (`clientgenv2`, `parsequery`, `querydocument`) |
| `github.com/99designs/gqlgen` | Code rendering (`codegen/templates`) |
| `github.com/vektah/gqlparser/v2` | SDL parsing and formatting (schema stripping) |
| `github.com/goccy/go-yaml` | Config file parsing |

## Workspace setup

This module lives alongside `gorouter` and `gqlgenc` in a Go workspace:

```
agent-orange/
├── go.work             # use ./defederator ./gorouter ./gqlgenc
├── defederator/
├── gorouter/
└── gqlgenc/
```

The `go.mod` `replace` directives point to the sibling directories. When published, these would be replaced with real module versions.
