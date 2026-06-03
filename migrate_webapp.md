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
3. By default, chains into the `defederator` generate step using the just-written `.defederator.yml`: writes `generated/defederator/client.go` (typed operation structs + enum types) and `generated/defederator/federation_exec.go` (self-contained execution engine). Pass `--no-generate` to skip this step and run `defederator` manually later.

Steps 1–2 are written only if they do not exist, unless `--force` is passed. Step 3, when enabled, always runs and overwrites the generated client.

## What migrate handles automatically

The migrate command derives a complete, ready-to-commit `.defederator.yml` and
`cross_service/client.go` from the service's `genqlient.yaml` plus its existing
cross-service Go files. Specifically:

- **Subgraph list is pruned** to only the subgraphs the service's operations
  actually query, determined by running the federation query planner against
  every operation and collecting the unique set of touched subgraphs. The own
  service is always kept regardless of self-references.
- **Auth factory shape matches the service's call patterns.** Migrate scans the
  cross_service `.go` files for `ctx.GraphQL().AsUser()`,
  `.AsServiceAdmin()`, and `WithKALocale(...).AsUser()` chains and emits one
  factory per detected flavor, all funneling through a shared
  `newJobFederationClient`. Services with a single auth flavor get the simpler
  `newFederationClient(ctx _federationCtx)` form.
- **INPUT_OBJECT bindings are pruned** to the intersection of (a) types this
  service owns in the supergraph and (b) types that appear as variable types
  in at least one operation. No noisy bindings for types nothing references.
- **Scalar bindings pass through verbatim** from `genqlient.yaml`. Defederator
  generate runs inside the webapp module so paths like
  `cloud.google.com/go/civil.Date` and `github.com/Khan/webapp/pkg/content.Author`
  resolve correctly.
- **Enums are auto-emitted** as typed Go strings (genqlient-style) directly into
  the generated client package; no binding is needed in `.defederator.yml`
  unless you want to override (e.g. point at an existing Go type elsewhere).
- **No `TODO` scaffolding** is left in the generated `cross_service/client.go`.

You should still review the generated files before committing, but no manual
edits are required for the cases above.

## defederator code generation (only with `--no-generate`)

By default `migrate` runs generation automatically as step 3 (see "What migrate does"). You only need this step if you passed `--no-generate` to inspect the config before generating, or if you regenerate after editing `.defederator.yml`:

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
export WEBAPP=~/khan/webapp
for svc in \
  admin ai-guide assessments assign-content assignments campaigns \
  certificates coaches content-editing content-library content \
  discussions districts donations edu-organizations emails \
  fastly-khanacademy-compute gap-finder learning-queue legal-docs \
  mcp-gateway notifications programs progress-reports progress \
  recommendations rest-gateway rewards search users zendesk; do
    echo "=== $svc ==="
    defederator migrate --force "$WEBAPP/services/$svc"
done
```

Remove `--dry-run` once you are satisfied with the output. Pass `--force` to overwrite existing files. Pass `--no-generate` in the batch loop if you want to review every `.defederator.yml` before any client is generated; otherwise each iteration also writes `generated/defederator/client.go` for that service.

---

## Regenerating after edits

`defederator migrate` already runs generation as part of step 3 above. You only need a manual generate when:

- You passed `--no-generate` to migrate.
- You have edited `.defederator.yml` (e.g. pruned the subgraph list, replaced a scalar placeholder) and want the typed client to match.

```sh
cd ~/khan/webapp/services/<name>
defederator
```

This writes:

- `generated/defederator/client.go` — typed operation structs, methods, and enum types
- `generated/defederator/federation_exec.go` — self-contained execution engine

These files must be committed alongside the config and client scaffold.

---

## Common problems

**`join__Graph enum not found`** — the service's `genqlient.yaml` points at a schema file that is not the Federation v2 supergraph. Check that `schema:` resolves to `~/khan/webapp/gengraphql/composed_schema.graphql`.

**Binding type `graphql.String` causes compile error** — the operation returns a field typed as a custom scalar and the generated struct uses `graphql.String` where your code expects a specific Go type. Update the binding in `.defederator.yml` to the correct type.

**`cross_service/client.go` already exists** — migrate skips existing files by default. Pass `--force` to overwrite, or delete the file manually first.

**Service-discovery call fails at runtime for unknown subgraph** — a subgraph name in `_subgraphServices` does not match any registered service. Either the enum name is wrong or the service name derivation (SCREAMING_SNAKE → kebab-case) produced a name that differs from the actual registration. Check `sd.EndpointForServiceWithVersion` logs.
