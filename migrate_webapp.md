# Migrating webapp services to defederator

This guide covers running `defederator migrate` against each Go service in `~/khan/webapp` that uses genqlient for cross-service federation queries.

## Prerequisites

Build the defederator binary from the agent-orange workspace:

```sh
cd ~/Documents/agent-orange
GOWORK=off go build -o ~/go/bin/defederator ./defederator/cmd/defederator
```

Confirm it works:

```sh
defederator migrate --help
```

The supergraph SDL is at `~/khan/webapp/gengraphql/composed_schema.graphql`. The `migrate` subcommand reads it automatically when it finds `genqlient.yaml` in the service directory.

## What migrate does

For each service directory it:

1. Reads `genqlient.yaml` and derives `.defederator.yml` (schema path, client output path, scalar bindings, INPUT_OBJECT bindings for types owned by this service).
2. Reads the supergraph SDL and generates `cross_service/client.go` with a `newFederationClient` constructor and an `exampleSubgraphURLs` helper wired to service discovery.

Both files are written only if they do not exist, unless `--force` is passed.

## What migrate leaves incomplete

**You must review and fix these things after running migrate:**

### 1. Subgraph list is the full supergraph

`_subgraphServices` in `cross_service/client.go` contains every subgraph in the composed schema (~30 entries). You need to prune it to only the subgraphs this service's operations actually query. Leaving extra entries is harmless at runtime but wastes service-discovery calls and makes the map misleading.

To identify which subgraphs a service uses, search for `@join__field(graph:` annotations in the supergraph SDL for types that the service's operations reference, or run `defederator --dry-run` and observe which subgraph URLs appear in the generated plan specs.

### 2. Auth factory pattern is not generated

The generated `newFederationClient` accepts a single `_federationCtx`. Services like `ai-guide` need separate `NewUserFederationClient` / `NewAdminFederationClient` / `newLocaleUserFederationClient` constructors that each call an inner `newJobFederationClient(httpClient *http.Client, sd service_discovery.Client)`. The tool generates the simplest single-constructor form. Promote it to the multi-factory pattern manually if your service issues federation calls on behalf of multiple auth roles.

### 3. INPUT_OBJECT bindings may be a superset

`.defederator.yml` bindings include all INPUT_OBJECT types declared with `@join__type(graph: YOUR_SERVICE)` in the supergraph. If your service's operations never pass some of those types as arguments, the binding is unused. Unused bindings are harmless but add noise; remove them after confirming the generated code compiles without them.

### 4. Scalar bindings use graphql.String as placeholder

All scalar types that are not `time.Time`, `interface{}`, or `map[string]interface{}` are bound to `github.com/99designs/gqlgen/graphql.String`. Replace each one with the real Go type from your service's genqlient package (or leave as `graphql.String` if the field is never returned in a response your code reads).

### 5. cross_service/client.go is a scaffold, not final

The generated file has TODOs at the top documenting the multi-factory pattern and the INPUT_OBJECT binding reminder. Read them. Remove them once you have handled those cases.

### 6. defederator code generation not yet run

`migrate` only writes config and the client scaffold. After migration you still need to run:

```sh
cd ~/khan/webapp/services/<name>
defederator
```

This reads `.defederator.yml`, strips federation metadata from the supergraph SDL, generates typed operation structs, and writes `generated/defederator/client.go` and `generated/defederator/federation_exec.go`.

---

## Per-service migration commands

Run from the repo root or use absolute paths. `--dry-run` prints what would be written without touching the filesystem.

```sh
WEBAPP=~/khan/webapp
```

### admin

```sh
defederator migrate "$WEBAPP/services/admin"
```

### ai-guide

```sh
defederator migrate "$WEBAPP/services/ai-guide"
```

Post-migration: ai-guide has user, admin, and locale-user auth contexts. Replace the generated `newFederationClient` with the three-constructor pattern from `9805d36dc51b386531660f98c28d48982ccbe737`:

```go
func NewUserFederationClient(ctx _userCtx) defed.FederationClient { ... }
func NewAdminFederationClient(ctx _adminCtx) defed.FederationClient { ... }
func newLocaleUserFederationClient(ctx _localeCtx) defed.FederationClient { ... }
func newJobFederationClient(httpClient *http.Client, sd service_discovery.Client) defed.FederationClient { ... }
```

### assessments

```sh
defederator migrate "$WEBAPP/services/assessments"
```

### assign-content

```sh
defederator migrate "$WEBAPP/services/assign-content"
```

### assignments

```sh
defederator migrate "$WEBAPP/services/assignments"
```

### campaigns

```sh
defederator migrate "$WEBAPP/services/campaigns"
```

### certificates

```sh
defederator migrate "$WEBAPP/services/certificates"
```

### coaches

```sh
defederator migrate "$WEBAPP/services/coaches"
```

### content-editing

```sh
defederator migrate "$WEBAPP/services/content-editing"
```

### content-library

```sh
defederator migrate "$WEBAPP/services/content-library"
```

### content

```sh
defederator migrate "$WEBAPP/services/content"
```

### discussions

```sh
defederator migrate "$WEBAPP/services/discussions"
```

### districts

```sh
defederator migrate "$WEBAPP/services/districts"
```

Post-migration: districts uses a job-context pattern similar to ai-guide. The generated single-constructor `newFederationClient` is correct for the common case; add a `newJobFederationClient(httpClient *http.Client, sd service_discovery.Client)` variant if districts issues federation calls from background jobs that don't have a per-request context.

### donations

```sh
defederator migrate "$WEBAPP/services/donations"
```

### edu-organizations

```sh
defederator migrate "$WEBAPP/services/edu-organizations"
```

### emails

```sh
defederator migrate "$WEBAPP/services/emails"
```

### fastly-khanacademy-compute

```sh
defederator migrate "$WEBAPP/services/fastly-khanacademy-compute"
```

Note: this service runs on Fastly Compute, not a standard Go HTTP server. Verify that service-discovery and HTTP client wiring applies before using the generated scaffold.

### gap-finder

```sh
defederator migrate "$WEBAPP/services/gap-finder"
```

### learning-queue

```sh
defederator migrate "$WEBAPP/services/learning-queue"
```

### legal-docs

```sh
defederator migrate "$WEBAPP/services/legal-docs"
```

### mcp-gateway

```sh
defederator migrate "$WEBAPP/services/mcp-gateway"
```

### notifications

```sh
defederator migrate "$WEBAPP/services/notifications"
```

### programs

```sh
defederator migrate "$WEBAPP/services/programs"
```

### progress-reports

```sh
defederator migrate "$WEBAPP/services/progress-reports"
```

### progress

```sh
defederator migrate "$WEBAPP/services/progress"
```

### recommendations

```sh
defederator migrate "$WEBAPP/services/recommendations"
```

### rest-gateway

```sh
defederator migrate "$WEBAPP/services/rest-gateway"
```

### rewards

```sh
defederator migrate "$WEBAPP/services/rewards"
```

### search

```sh
defederator migrate "$WEBAPP/services/search"
```

### users

```sh
defederator migrate "$WEBAPP/services/users"
```

### zendesk

```sh
defederator migrate "$WEBAPP/services/zendesk"
```

---

## Batch migration

To migrate all services at once (inspect output before committing):

```sh
WEBAPP=~/khan/webapp
for svc in \
  admin ai-guide assessments assign-content assignments campaigns \
  certificates coaches content-editing content-library content \
  discussions districts donations edu-organizations emails \
  fastly-khanacademy-compute gap-finder learning-queue legal-docs \
  mcp-gateway notifications programs progress-reports progress \
  recommendations rest-gateway rewards search users zendesk; do
    echo "=== $svc ==="
    defederator migrate --dry-run "$WEBAPP/services/$svc"
done
```

Remove `--dry-run` once you are satisfied with the output. Pass `--force` to overwrite existing files.

---

## After migrate: generate defederator code

Once `.defederator.yml` is in place and you have pruned the subgraph list:

```sh
cd ~/khan/webapp/services/<name>
defederator
```

This writes:

- `generated/defederator/client.go` — typed operation structs and methods
- `generated/defederator/federation_exec.go` — self-contained execution engine

These files must be committed alongside the config and client scaffold.

---

## Common problems

**`join__Graph enum not found`** — the service's `genqlient.yaml` points at a schema file that is not the Federation v2 supergraph. Check that `schema:` resolves to `~/khan/webapp/gengraphql/composed_schema.graphql`.

**Binding type `graphql.String` causes compile error** — the operation returns a field typed as a custom scalar and the generated struct uses `graphql.String` where your code expects a specific Go type. Update the binding in `.defederator.yml` to the correct type.

**`cross_service/client.go` already exists** — migrate skips existing files by default. Pass `--force` to overwrite, or delete the file manually first.

**Service-discovery call fails at runtime for unknown subgraph** — a subgraph name in `_subgraphServices` does not match any registered service. Either the enum name is wrong or the service name derivation (SCREAMING_SNAKE → kebab-case) produced a name that differs from the actual registration. Check `sd.EndpointForServiceWithVersion` logs.
