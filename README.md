# defederator

`defederator` is a code generator that produces typed Go GraphQL clients that bypass a gateway entirely. Given a Federation v2 supergraph SDL and `.graphql` operation files, it generates a Go client whose methods execute federation query plans against subgraph services directly — resolving cross-subgraph joins, entity fetches, `@requires`, and `@provides` in process — without a running Apollo Router or similar gateway.

## How it works

```
supergraph.graphql + queries/*.graphql
        │
        ▼
  defederator (code generator)
        │
        ├── strips federation metadata from the SDL
        ├── uses gqlgenc's type pipeline to generate typed response structs
        ├── compiles each operation into a URL-keyed execution plan (JSON)
        └── renders client.go + federation_exec.go
        │
        ▼
  generated/
    client.go           ← typed methods; plan specs baked in as string constants
    federation_exec.go  ← copy of the execution engine (stdlib-only, no imports)
        │
        ▼  at runtime
  generated.NewClient(httpClient) → *Client
        │
        └── each method: ResolveURLSpec → Execute → unmarshal typed response
```

Generated code imports only the Go standard library. No `defederator` or `gorouter` packages are imported at runtime.

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

generate:
  clientInterfaceName: FederationClient   # optional; generates a named interface for mocking
  optional: pointer                        # "pointer" (default) or "value"
  export_operations: operations.json       # optional; JSON manifest for APQ pre-registration
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

The generator writes two files into the output directory:
- `client.go` — typed request/response structs and methods for each operation
- `federation_exec.go` — the execution engine, renamed to the output package

### 5. Use the generated client

```go
import "yourmodule/generated"

// URLs are baked in from the supergraph SDL at generation time.
// Pass nil to use http.DefaultClient.
client, err := generated.NewClient(nil)
if err != nil { ... }

res, err := client.GetProduct(ctx, &generated.GetProductRequest{ID: "apollo-federation"})
if err != nil { ... }
fmt.Println(res.Product.Sku)
```

`NewClient` resolves the embedded plan specs once and caches them. It is safe for concurrent use across goroutines.

## Package layout

```
defederator/
├── cmd/defederator/        # CLI binary: reads config, calls generator.Generate
├── config/
│   ├── config.go           # .defederator.yml loader and Config type
│   └── genqlient.go        # genqlient.yaml loader → *Config adapter
├── execengine/             # Execution engine (source-embedded into generated code)
│   ├── execengine.go       #   Execute, ResolveURLSpec, ExecuteAndUnmarshal, ApplyProjection
│   └── source.go           #   //go:embed execengine.go — lets generator copy it
├── generator/
│   ├── generator.go        # Orchestrates schema strip → gqlgenc pipeline → render
│   ├── schema.go           # StripFederationTypes: supergraph SDL → clean SDL
│   ├── template.go         # RenderFederationTemplate
│   ├── template.gotpl      # Federation-specific Go template
│   ├── urlspec.go          # MarshalURLPlanSpec, WriteExecFile
│   ├── glob.go             # Doublestar glob expansion with deduplication
│   ├── goextract.go        # Extracts # @genqlient-annotated queries from Go literals
│   ├── export.go           # Writes JSON manifest of generated operations
│   └── *_test.go           # Unit + codegen compile tests for all 5 scenarios
├── migrate/                # defederator migrate subcommand
│   ├── migrate.go          # CLI entry point; orchestrates file generation
│   ├── convert.go          # DefederatorYAML: genqlient.yaml → .defederator.yml text
│   ├── subgraphs.go        # ParseSubgraphs, ParseInputObjectsForService
│   ├── clientgen.go        # Render: Data → cross_service/client.go text
│   ├── client.gotpl        # Go template for the client scaffold
│   └── *_test.go           # Unit tests + golden file for client scaffold
├── federationclient/       # Legacy runtime package (kept for graphqlcompat)
├── graphqlcompat/
│   ├── client.go           # genqlient graphql.Client adapter
│   └── client_test.go
└── gqlgencfed/
    ├── plugin.go           # gqlgenc plugin (replaces "clientgen" via api.ReplacePlugin)
    └── plugin_test.go      # End-to-end plugin path test
```

## Config files

### `.defederator.yml` (native format)

```yaml
schema: supergraph.graphql

query:
  - queries/**/*.graphql

client:
  filename: ./generated/client.go
  package:  generated

generate:
  clientInterfaceName: FederationClient
  optional: pointer          # "pointer" (default) | "value"
  export_operations: ops.json
```

### `genqlient.yaml` (drop-in for existing genqlient projects)

defederator reads `genqlient.yaml` directly, converting its fields to the equivalent defederator config.

```yaml
schema: supergraph.graphql
operations:
  - queries/**/*.graphql
generated: ./generated/client.go
package: generated
```

## Query sources

### `.graphql` files

Standard named GraphQL operations:

```graphql
query GetUser($id: ID!) {
  user(id: $id) { name email }
}
```

### Embedded queries in Go files (genqlient style)

Go string literals beginning with `# @genqlient` are extracted and treated as query sources. Source positions are preserved so parse errors point back to the original Go file and line.

```go
var getUserQuery = `# @genqlient
query GetUser($id: ID!) {
  user(id: $id) { name email }
}`
```

Include the Go file in `query:`:

```yaml
query:
  - queries/**/*.graphql
  - internal/api/**/*.go
```

### Glob patterns

Both `.graphql` and `.go` path entries support doublestar globs. A glob that matches no files is an error rather than a silent no-op.

## `generate:` options

| Option | Default | Description |
|---|---|---|
| `clientInterfaceName` | _(none)_ | Emit a named interface; useful for mocking in tests |
| `optional` | `"pointer"` | Nullable field representation: `"pointer"` → `*T`, `"value"` → `T` |
| `export_operations` | _(none)_ | Path to write a JSON manifest of all generated operations |

## Generated code architecture

### Plan specs baked at generation time

For each operation, the generator calls `federation.BuildPlan` against the supergraph SDL and serializes the result to a URL-keyed JSON string that is embedded as a constant in `client.go`:

```go
const GetProductPlanSpec = `{"fetches":[{"url":"https://products.svc/graphql","query":"..."}],"entityFetches":[...]}`
```

The URLs come from the supergraph SDL. There is no runtime SDL parsing.

### `federation_exec.go`

The generator copies `execengine.go` into the output package with the package declaration renamed. This gives the generated client a self-contained execution engine with no external imports. The file provides:

- `ResolveURLSpec(specJSON string) (*Plan, error)` — decodes a URL-keyed plan spec
- `Execute(ctx, plan, vars, client) (map[string]any, []GraphQLError, error)` — runs the plan
- `ExecuteAndUnmarshal(ctx, plan, vars, client, dest) error` — Execute + JSON unmarshal into a typed struct
- `ApplyProjection(data, projection) map[string]any` — strips planner-added fields from the response

### `NewClient` initialization

```go
func NewClient(httpClient *http.Client) (*Client, error) {
    if httpClient == nil { httpClient = http.DefaultClient }
    // Resolve each operation's plan spec once at startup.
    plans := map[string]*Plan{
        "GetProduct": must(ResolveURLSpec(GetProductPlanSpec)),
        ...
    }
    return &Client{http: httpClient, plans: plans}, nil
}
```

If any plan spec fails to parse (malformed JSON), `NewClient` returns an error rather than panicking later during execution.

## Two generation paths

### Path A — standalone binary (recommended)

```sh
defederator -c .defederator.yml
```

Calls `generator.Generate` directly. No dependency on gqlgenc's generator.

### Path B — gqlgenc plugin

For existing gqlgenc projects that want to swap the transport layer:

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

## `defederator migrate` — scaffold from genqlient.yaml

`defederator migrate` reads an existing `genqlient.yaml` and the Federation v2 supergraph SDL to generate two files that bootstrap defederator adoption for a service:

- **`.defederator.yml`** — native defederator config derived from `genqlient.yaml`, with scalar bindings substituted for `graphql.String` and INPUT_OBJECT bindings for types owned by this subgraph.
- **`cross_service/client.go`** — Go scaffold with a `newFederationClient` constructor and a `<service>SubgraphURLs` helper wired to service discovery.

```sh
# Preview without writing
defederator migrate --dry-run ./services/districts

# Write (skips existing files)
defederator migrate ./services/districts

# Overwrite existing files
defederator migrate --force ./services/districts
```

The tool reads the supergraph SDL from the path in `genqlient.yaml`'s `schema:` field, so no extra flags are needed when the schema path is `../../gengraphql/composed_schema.graphql` (the webapp convention).

### Flags

| Flag | Description |
|---|---|
| `--dry-run` | Print generated content to stdout; write nothing |
| `--force` | Overwrite existing `.defederator.yml` and `cross_service/client.go` |

### What migrate leaves incomplete

- **Subgraph list** — `_subgraphServices` in the generated client lists every subgraph in the supergraph. Prune it to only those this service's operations actually touch.
- **Auth factory pattern** — services that issue federation calls under multiple auth roles (user / admin / locale) need separate constructor variants. The tool generates a single `newFederationClient(ctx _federationCtx)`.
- **INPUT_OBJECT bindings** — may include types owned by this subgraph that no operation actually passes as arguments. Remove unused ones after confirming compilation.
- **Scalar bindings** — non-standard scalars are bound to `graphql.String` as a placeholder. Replace with the real Go type if the field is read by your code.
- **Code generation** — migrate only writes config and scaffold. Run `defederator` in the service directory afterward to generate the typed client and execution engine.

For step-by-step instructions covering all 31 webapp services, see [migrate_webapp.md](migrate_webapp.md).

## Migration from genqlient (graphqlcompat adapter)

If you have an existing genqlient project, `graphqlcompat.NewClient` provides a drop-in adapter that implements genqlient's `graphql.Client` interface backed by the federation-aware execution engine.

```go
import (
    "github.com/StevenACoffman/defederator/federationclient"
    "github.com/StevenACoffman/defederator/graphqlcompat"
    "yourmodule/generated" // your existing genqlient-generated package
)

sg, err := federationclient.ParseSupergraphSDL(sdl)
if err != nil { ... }

// Drop-in replacement for graphql.NewClient(url, httpClient)
client := graphqlcompat.NewClient(sg, httpClient)

// Existing genqlient-generated code works unchanged
resp, err := generated.GetUser(ctx, client, userID)
```

## Schema stripping

The supergraph SDL contains federation metadata (`@join__type`, `@join__field`, `join__Graph` enum, etc.) that gqlgen's type mapper does not understand. `generator.StripFederationTypes` removes all federation-specific directives, type definitions, and per-field annotations before passing the schema to gqlgenc's pipeline. User-defined directives are preserved.

## Verification

The test suite verifies generated client correctness at three layers:

**Layer 1 — GraphQL protocol** (`execengine/protocol_test.go`): The executor correctly handles all legal HTTP response shapes — `data: null` with and without errors, malformed JSON, pre-cancelled contexts — without panicking or silently discarding information.

**Layer 2 — Federation entity resolution protocol** (`execengine/entities_test.go`, `execengine/golden_test.go`): `_entities` calls carry the correct representations (`__typename` + key fields + `@requires` fields, no extras). Variables are validated against Apollo-captured fixture files.

**Layer 3 — Cross-subgraph merge correctness** (`execengine/golden_test.go`): The merged output matches `expected.json` from Apollo's reference implementation exactly.

### Golden fixtures

All five federation patterns are covered:

| Fixture | Pattern |
|---|---|
| `01_product_id_sku` | Single-subgraph lookup |
| `02_product_delivery` | Cross-subgraph join + `@requires` |
| `03_product_creator_name` | Three-subgraph join |
| `04_product_creator_requires` | `@requires` (same-subgraph, multi-step plan) |
| `05_product_creator_provides` | `@provides` (key pre-fetched, no extra entity fetch) |

Fixture responses were captured from Apollo's reference implementation and live in `gorouter/federation/testdata/golden/`.

## Dependencies

| Dependency | Role |
|---|---|
| `github.com/StevenACoffman/gorouter/federation` | Query planner (`BuildPlan`, `ParseSchema`) — used at generation time only |
| `github.com/gqlgo/gqlgenc` | Type-generation pipeline (`clientgenv2`, `parsequery`, `querydocument`) |
| `github.com/99designs/gqlgen` | Code rendering (`codegen/templates`) |
| `github.com/vektah/gqlparser/v2` | SDL parsing and formatting (schema stripping) |
| `github.com/goccy/go-yaml` | Config file parsing |
| `github.com/Khan/genqlient` | `graphql.Client` interface (graphqlcompat adapter) |
| `github.com/bmatcuk/doublestar/v4` | Doublestar glob expansion for query paths |

Generated code has **no runtime dependencies** beyond the Go standard library.

## Workspace setup

```
agent-orange/
├── go.work             # use ./defederator ./gorouter ./gqlgenc
├── defederator/        # this module
├── gorouter/
└── gqlgenc/
```
