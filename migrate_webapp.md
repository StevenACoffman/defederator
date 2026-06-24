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

## Design constraints (validated end-to-end on `services/districts`)

Four properties must hold for a service migration. All four were verified on
districts (24 `cross_service` files, full build + `go test ./services/districts/...`
green, `ka-context-interface` lint clean). They refine the "What migrate does"
description below and supersede it where they differ.

### 1. `defederator migrate` writes only inside the target service directory

For `defederator migrate --force "$WEBAPP/services/<svc>"`, every file written or
rewritten is under `services/<svc>/`:

- `services/<svc>/.defederator.yml`
- `services/<svc>/cross_service/client.go` — the adapter, the per-flavor
  constructors, the op→PlanSpec map, the process-level service-discovery handle,
  `extractHTTPClient`, and `<svc>SubgraphURLs`
- `services/<svc>/cross_service/*.go` — the call-site rewrites
- `services/<svc>/generated/defederator/{client.go,federation_exec.go}`

It must **not** touch `pkg/`, other `services/*`, `gengraphql/`, or
`generated/genqlient/`. The supergraph SDL is read-only input. The single change
migrate cannot write for you — registering the service-discovery handle (§3) — is
one line the human adds to the service's own `cmd/serve`, still inside
`services/<svc>/`. No part of the migration edits code in another package.

### 2. Use the compat adapter, not the typed `FederationClient`

Rewrite each call to **swap only the `graphql.Client` argument**, keeping the
`genqlient.<Op>(...)` function and its response types:

```go
genqlient.<Op>(ctx, ctx.GraphQL().AsServiceAdmin(), …)
→ genqlient.<Op>(ctx, NewAdminGraphQLClient(ctx), …)
```

`New<Flavor>GraphQLClient` returns a `graphql.Client` backed by the generated
`defed` planner (the `graphqlcompat` idea, templated into the package so it needs
no `gorouter` dependency). Because genqlient's generated functions and **response
structs are unchanged**, this avoids:

- the **response-object-type cascade**: the typed `FederationClient` returns
  `defederator`-package response structs (independent of genqlient's, differently
  named, pointer-sliced). Districts surfaced **111** genqlient response-type
  aliases through `rostering`/`resolvers`; the compat swap changes **none** of
  them, whereas the typed route would force edits across all of them.
- addendum categories **3d, 3e, 5a–5d** — all artifacts of callers seeing
  defederator's typed enums/pointers/scalars. With genqlient types preserved, they
  do not arise.

Three adapter details the template must bake in (each learned the hard way on
districts):

- **Export an op→PlanSpec map.** The generated `defed` package builds
  `opName → PlanSpec` only function-locally inside `NewClientWithHTTPFactory`, and
  `DocumentOperationNames` maps the other direction. Emit a package-level
  `var operationPlanSpecs = map[string]string{ "<Svc>_<Op>": defed.<Svc><Op>PlanSpec, … }`
  so the adapter's `MakeRequest` can look a plan up by `req.OpName`.
- **Normalize `req.Variables`.** genqlient passes a typed struct, but the planner's
  `filterVars` only subsets a `map[string]any` — a struct is sent whole to every
  subgraph fetch. JSON round-trip it into a map first.
- **Gate test/prod on `testing.Testing()`**, not on `extractHTTPClient` failing.
  gqltest / servicetest in-process clients expose an `httpClient`, so the
  extraction heuristic misfires; under test, return the supplied client so
  `gqlclient.Mux` mocks and in-process federated servers keep working (this is what
  preserves the §8 mock-dispatch behavior).

### 3. Source service discovery in-process (B2) — no `ctx` cascade

The adapter needs a `service_discovery.Client` to resolve subgraph URLs. Do **not**
take it from `ctx.ServiceDiscovery()`: that puts `service_discovery.KAContext` on
every constructor's `ctx`, which ADR-429 then forces onto every transitive caller
— the ~140-site cascade described in addendum §6. Service discovery is
process/environment-scoped (subgraphs resolve at the default version), so hold it
in a package-level handle set once at startup:

```go
// cross_service/client.go
var serviceDiscovery service_discovery.Client

// SetServiceDiscovery wires the process-level service-discovery client used to
// resolve subgraph endpoints. Call once from the service's cmd/serve setup.
func SetServiceDiscovery(sd service_discovery.Client) { serviceDiscovery = sd }
```

The adapter resolves URLs with `serviceDiscovery` plus the request's plain
`context.Context` (which `MakeRequest` already receives). Consequences:

- **No caller changes.** Constructors require only `gqlclient.KAContext` (to reach
  `ctx.GraphQL().As…()` for the auth-bound transport) — the interface those
  wrappers already had. The ADR-429 §6 cascade does **not** occur, so the migration
  stays confined to `cross_service/` plus the one `cmd/serve` registration line.
- migrate emits `SetServiceDiscovery`; the human adds one
  `cross_service.SetServiceDiscovery(sd)` call in `cmd/serve` (in-service), where
  the service-discovery client is already available among the startup
  dependencies. A first-use guard (panic if unset in prod) catches a forgotten
  wiring.

Trade-off: a package-level handle instead of pure ADR-429 ctx-threading, chosen to
keep the migration package-local and cascade-free. The cleaner alternative — a
`ServiceDiscovery()` accessor on the `pkg/web/gqlclient` adapter (parallel to the
`HTTPClient()` accessor the `extractHTTPClient` reflection hack already
anticipates) — would be even tidier but is an out-of-service `pkg/` change, so it
is deliberately out of scope here.

### 4. The generated + rewritten output passes webapp lint

Lint-clean under webapp's custom linters when the template observes:

- **`generated/` is path-excluded.** `.golangci.yml` excludes `generated($|/)`
  (`generated: lax`), so `generated/defederator/{client.go,federation_exec.go}` are
  not linted — their internal `fmt.Errorf`/stdlib-`errors` use is fine. Keep those
  files under `generated/`.
- **Hand-maintained `cross_service/*.go` must be clean.** The emitted
  `client.go` (and the rewritten wrappers) use `pkg/lib/errors`
  (`errors.Wrap`/`errors.Internal`), never `fmt.Errorf` or stdlib `errors`; no
  `time.Now`, no `sync.WaitGroup` (`ka-banned-symbol`/`depguard`).
- **`ka-visibility`** — no leading-underscore identifier referenced from another
  file in the package. The op→PlanSpec map, the service-discovery handle, and any
  context-interface helper must be plain lowercase (`operationPlanSpecs`, not
  `_operationPlanSpecs`). This is the same rule as addendum 3c/7 — emit no
  `_`-prefixed cross-file names.
- **`ka-context-interface` (ADR-429)** — with B2 there is **no** new
  `service_discovery` requirement, so the migration introduces no new violations
  and no cascade. (Any debt a recompile surfaces in callers is pre-existing, not
  created here — contrast addendum §6, which describes the cascade you get only if
  you ignore §3 and source service discovery from `ctx`.)
- **`ka-genqlient` (§4a)** — keep `var _ = genqlient.<Op>_Operation` (or the call)
  for every retained `# @genqlient` block so the operation still registers as used.
- **`ka-graphql-task` (§4b)** — leave `tasks.GraphQLTask(...)` sites untouched;
  they must keep passing `genqlient.<Op>_Operation`, not a defederator `Document`.

Verify per service: `go build ./services/<svc>/...`, `go test ./services/<svc>/...`,
a cache-cleared `tools/runlint.sh services/<svc>/cross_service/*.go`, and
`defederator check services/<svc>`.

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

### Not yet automated (gap vs. the Design constraints)

Today `migrate` generates the config + scaffold + typed client; reaching the
Design-constraints end state (compat adapter, no cascade, package-local) still
needs these steps, which the tool should grow to perform (all confined to
`services/<svc>/`):

1. **Emit the compat adapter + `operationPlanSpecs` map** in `cross_service/client.go`
   (§2) instead of, or alongside, the typed `newFederationClient` — and a
   `New<Flavor>GraphQLClient` returning `graphql.Client` per detected flavor.
2. **Rewrite the call sites** — swap `ctx.GraphQL()[.WithService(…)].As<Flavor>()`
   for `New<Flavor>GraphQLClient(ctx)`, drop `WithService`, leave task-dispatch
   calls alone. (migrate already parses these files for flavor detection, so it
   has the AST it needs.)
3. **Emit the B2 handle** — `var serviceDiscovery` + `SetServiceDiscovery` (§3);
   the human adds the one `SetServiceDiscovery(sd)` call in `cmd/serve`.

Until then those three are the manual follow-up after `defederator migrate`; the
districts reference implementation shows the target output.

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

> **Superseded by the Design constraints.** This historical snippet uses the typed
> `defed.FederationClient` route (the response-object-type cascade, §2) and
> `_`-prefixed context types (which `ka-visibility` rejects across files —
> addendum 3c/7). Prefer the compat constructors returning `graphql.Client` and
> taking only `gqlclient.KAContext` (`NewUserGraphQLClient` /
> `NewAdminGraphQLClient` / a locale variant), with service discovery sourced
> in-process (§3). Keep one constructor per detected auth flavor; no underscore
> names.

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

Districts is the **reference implementation** of the Design-constraints approach
(compat adapter + B2 service discovery). All 24 in-scope `cross_service` files were
migrated by swapping the `graphql.Client` (`New{Admin,User}GraphQLClient`),
keeping every genqlient function/response type; task-dispatch wrappers
(`districts.go`, `recompute_roles.go`) were left untouched. Build, `go test
./services/districts/...`, and `ka-context-interface` lint are all green, and —
because service discovery comes from the in-process handle (§3) — no caller
outside `cross_service/` changed. Use it as the template for the next service.

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

## Verifying after migration

After running `defederator migrate` — and after any subsequent edit that touches `cross_service/` files — run `defederator check` to detect orphaned `genqlient.<Op>(...)` calls (calls whose operation has no backing `# @genqlient` annotation block in any of the files globbed by `genqlient.yaml:operations`).

```sh
defederator check ~/khan/webapp/services/<name>
```

Exit code `0` means clean. Non-zero exit prints each orphan as `<file>:<line>: genqlient.<Op> (no @genqlient annotation declaring it)` and is suitable for CI gates.

**Why this matters:** the genqlient generator picks up operations only from `# @genqlient` annotation blocks. A migration that strips those blocks but leaves the call sites compiles only until the next `make genqlient` regen drops the unbacked operations from `generated/genqlient/queries.go`. The build then breaks at every orphaned call site, and tracing the regression back to the migration that removed the annotation can take hours. `defederator check` surfaces the same set in seconds, before the regen.

Run it as part of the standard post-migration loop:

```sh
defederator migrate --force services/<name>
defederator check services/<name> || exit 1
# … then commit
```

`defederator check` also catches the inverse class — when a follow-up commit removes an annotation block as part of "cleanup" but the call site survives. Wiring `check` into pre-commit or CI makes that class of regression fail loudly instead of silently.

See addendum §9 for the historical incident that motivated this subcommand.

---

## Common problems

**`join__Graph enum not found`** — the service's `genqlient.yaml` points at a schema file that is not the Federation v2 supergraph. Check that `schema:` resolves to `~/khan/webapp/gengraphql/composed_schema.graphql`.

**Binding type `graphql.String` causes compile error** — the operation returns a field typed as a custom scalar and the generated struct uses `graphql.String` where your code expects a specific Go type. Update the binding in `.defederator.yml` to the correct type.

**`cross_service/client.go` already exists** — migrate skips existing files by default. Pass `--force` to overwrite, or delete the file manually first.

**Service-discovery call fails at runtime for unknown subgraph** — a subgraph name in `_subgraphServices` does not match any registered service. Either the enum name is wrong or the service name derivation (SCREAMING_SNAKE → kebab-case) produced a name that differs from the actual registration. Check `sd.EndpointForServiceWithVersion` logs.

---

## Addendum: bug categories uncovered by webapp migration

Migrating real webapp services (recommendations, users, content-library, districts, ai-guide) surfaced bugs in five layers of the stack — most in defederator itself, some in webapp's lint rules, and some pre-existing user-code defects that the previous string-typed generation masked. Each category is grouped with the root cause, the symptom, and a concrete example seen during migration.

### 1. Codegen recursion bugs (fragment conversion getters)

Defederator generates `Get<FragmentName>()` conversion getters that reconstruct a named fragment from an owner type that spreads it. The original implementation recursed into nested struct fields unconditionally, which broke when the nested type wasn't a per-call-site shape.

**1a. Recursion into foreign-package types.** Generated code expanded `time.Time` field-by-field, copying `wall`, `ext`, `loc` — unexported fields that the receiving package can't reference. Fix: `localStructIfRecursable` only recurses into types defined in the client package.

> recommendations service, `CourseProgressFields.LastWorkedOn` (`*time.Time`):
> ```go
> LastWorkedOn: &Time{ wall: t.LastWorkedOn.wall, ext: t.LastWorkedOn.ext, loc: Location{...} }
> ```
> Compile error: `cannot refer to unexported field wall`.

**1b. Recursion into named-fragment types.** Gqlgenc's internal `types.Struct` for a named fragment is the *pre-flattening* shape (with the spread declared as a literal field), but the emitted Go struct has those fields *flattened in*. Recursing produced a literal referencing fields that don't exist on the emitted struct.

> recommendations service, `Wrapper` fragment:
> ```go
> &CurationNodeWrapper{ CurationNodeFields: ... }   // emitted struct has ID/ContentKind/RelativeURL/Parent, not CurationNodeFields
> ```
> Compile error: `unknown field CurationNodeFields in struct literal of type CurationNodeWrapper`.

**1c. Pointer literal type mismatch.** When the target field is `*PerCallSiteStruct` rather than `PerCallSiteStruct`, the literal needs `&T{…}` not `T{…}`. Fix: `localStructIfRecursable` returns an `isPtr` flag.

> content-library service:
> ```go
> CurrentMasteryV2: CurationNodeProgressFields_CurrentMasteryV2{…}   // field type is *T, not T
> ```

**1d. Slice of per-call-site struct.** Direct-assignment fails because element types differ across owner and target Go names. Fix: emit an inline IIFE that allocates a new slice and copies element-by-element.

> content-library service, `[]*ContentLibraryArticle_…_URLPaths` → `[]*ContentLibraryLearnableContent_URLPaths`.

**1e. Element-type name resolution for slices.** Initial slice-rebuild used `types.Type.String()` for the element, producing `&github.com/Khan/.../pkg.X_URLPaths{…}` — invalid Go. Fix: new `sliceElementName` walks `Slice → Pointer → Named` to extract the bare last-segment name.

### 2. Codegen type-registration bugs (lazy/eager enums and __schema)

**2a. Enum-only-in-variable-type panic.** `UsedEnumsInOperations` originally walked only response selection sets, missing enums that appear only as operation variable types. gqlgenc's `OperationArguments` then panicked.

> users service, `Users_WriteUserAdminLog($kind: UserAdminLogKind!)`:
> ```
> panic: no model configured for GraphQL type "UserAdminLogKind"
> ```
> Fix: also walk `op.VariableDefinitions`.

**2b. Enum-name vs fragment-name collision.** Eagerly registering every schema enum as a model meant gqlgenc bailed when a user fragment shared the name.

> recommendations service had `enum CompletionState` in the schema and a user fragment also named `CompletionState`:
> ```
> defederator: generate: generate fragments: CompletionState is duplicated
> ```
> Fix: register enums lazily — only enums referenced in operations get a model entry. Matches genqlient.

**2c. `__schema` has no subgraph owner.** Federation routing tables don't own `__schema` / `__type`; the gateway serves them. Defederator skips the gateway, so introspection operations failed planning.

> users service, `Users_ListMutations { __schema { mutationType { fields { name } } } }`:
> ```
> defederator: generate: plan "Users_ListMutations": federation: field "__schema" has no subgraph owner in routing table
> ```
> Fix: evaluate introspection queries against the supergraph schema at codegen time and emit a baked JSON constant + a method that unmarshals it.

### 3. Migrate output bugs (.defederator.yml and cross_service/client.go)

**3a. YAML alias parsing of glob patterns.** `*.go` starts with `*` which YAML 1.2 treats as an alias reference. The genqlient `operations:` list of glob patterns broke when copied verbatim.

> rest-gateway service:
> ```
> could not find alias '.go'
> ```
> Fix: single-quote every operation pattern (`yamlSingleQuote` with proper `''`-escaping).

**3b. Multi-flavor context type loss.** The migrate template branched on `AuthFlavors.Multi` and emitted *either* `_federationCtx` + `newFederationClient` *or* `_jobsCtx` + per-flavor constructors. Districts has both auth flavors *and* hand-written wrappers that use `newFederationClient(ctx)`, so the Multi branch broke compilation.

> districts service, many cross_service files:
> ```go
> client := newFederationClient(ctx)   // undefined in multi-flavor template
> ```
> Fix: always emit `federationCtx` + `newFederationClient`; Multi flavors get the per-flavor constructors *in addition*.

**3c. Underscore-prefixed type names trigger `ka-visibility`.** Webapp treats leading-underscore identifiers as file-private (enforced by `dev/linters/visibility_lint.go`). The migrate template's `_federationCtx` / `_jobsCtx` could only be referenced from `client.go`, so every cross-service file that took one as a parameter failed lint.

> ~140 lint failures across `services/districts/cross_service/*.go`:
> ```
> cannot refer to file-private _federationCtx
> ```
> Fix: drop the leading underscore in the migrate template (and migrate-test golden file).

**3d. Owned-INPUT_OBJECT filter was too narrow.** Migrate originally bound only INPUT_OBJECTs owned by the local subgraph *and* used in operations. But cross-service code routinely passes input objects owned by *other* subgraphs (the local service is calling into them), so the intersection dropped them.

> districts service: `Districts_UpdateUnitMasteryAssignments` takes `UpdateMasteryAssignmentInput` (owned by `ASSIGNMENTS`, not `DISTRICTS`). The generated method's parameter type was `[]string` because the binding wasn't emitted:
> ```
> cannot use []*genqlient.UpdateMasteryAssignmentInput as []string
> ```
> Fix: drop the ownership filter; bind any INPUT_OBJECT used as an operation variable.

**3e. Enum bindings missing.** Without explicit enum bindings, defederator emits its own `type X string` distinct from `genqlient.X`. Every caller then needed a per-call-site `defederator.X(x)` cast.

> Multiple services. Fix: `OperationUsedEnums` discovers enums in variable and response positions, migrate emits one binding per used enum pointing at the genqlient type.

### 4. Binding & operation-reference gaps (lint-driven)

**4a. `ka-genqlient` requires `_Operation` references.** Webapp's `genqlient_lint.go` registers a genqlient operation as "used" only when the file calls `genqlient.X(...)` *or* references `genqlient.X_Operation`. Defederator method calls don't satisfy either.

> Every cross_service file that has `_ = ``# @genqlient mutation Foo …``` but no longer calls `genqlient.Foo`:
> ```
> genqlient operation NOT used in file; add `_ = genqlient.Foo` somewhere
> ```
> Fix (user-side): add `var _ = genqlient.X_Operation` near the imports for each `# @genqlient` block kept for safelist purposes.

**4b. `ka-graphql-task` requires `_Operation` strings, not defederator `Document` consts.** Tasks dispatched via `tasks.GraphQLTask(...)` need a recognised genqlient `_Operation` constant as the mutation argument.

> ai-guide service, `tasks.GraphQLTask(defederator.AiGuideTaskCheckForIdleThreadDocument, ...)`:
> ```
> graphql_task: mutation argument must look like `genqlient.<service>_Task_<something>_Operation`
> ```
> Fix (user-side): swap `defed.XDocument` for `genqlient.X_Operation` at each `tasks.GraphQLTask(...)` call.

### 5. Latent user-code bugs surfaced by stricter typing

When defederator started emitting typed enum / typed pointer fields where the genqlient client previously emitted strings, several existing usage patterns stopped compiling. These were pre-existing defects masked by string-flavored generation.

**5a. Pointer-typed getter compared to string literal.** `errorResult.GetCode()` returns `*EnumType`. Comparison with a string literal was always a programmer error; the typed pointer just made it visible.

> districts service, `coaches_mutations.go`:
> ```go
> if errorResult.GetCode() == "INTERNAL" { … }   // *EnumType vs string
> ```
> Fix: `if c := errorResult.GetCode(); c != nil && *c == "INTERNAL"`.

**5b. Redundant `string(enum)` casts.** With genqlient and defederator now sharing the same Go enum type (via bindings), `string(genqlient.X)` flattens to `string` and breaks methods that expect the typed enum.

> Many cross_service files:
> ```go
> optOutStatus := string(genqlient.OptOutStatusOptedIn)   // now `string`, method wants OptOutStatus
> ```
> Fix: drop the cast — `optOutStatus := genqlient.OptOutStatusOptedIn`.

**5c. Cross-package enum cast lost type.** `permissions.CapabilityName` is a type alias for `permissions/genqlient.CapabilityName` (a different package than the service's local genqlient). `string(capability)` collapses to a plain string, which the method (typed against the service's local genqlient) rejects.

> districts service, `admin.go`:
> ```go
> client.DistrictsUserHasCapability(ctx, kaid, string(capability), …)   // string vs genqlient.CapabilityName
> ```
> Fix: `genqlient.CapabilityName(capability)` to re-type into the target package.

**5d. `civil.Date` ↔ string boundary.** Genqlient maps the `Date` scalar to `civil.Date` (per the user binding). Code that built a `*string` for the GraphQL arg or expected a `string` back broke at the type boundary.

> districts service, `course_progress_stp.go`:
> ```go
> rowFrom, _ := civil.ParseDate(r.GetFrom())   // r.GetFrom() returns *civil.Date, not string
> ```
> Fix: use the date directly — `rowFrom := generic.Deref(r.GetFrom())`.

### 6. Pre-existing webapp lint debt revealed by recompilation

Stricter recompilation of every cross-service caller surfaces `ka-context-interface` (ADR-429) violations — functions whose `ctx interface { … }` declaration doesn't list every transitively-used KAContext. These are not bugs in defederator; they're tech debt the package previously hid because nothing forced rebuilds at the touched call sites.

> **Avoidable — and avoided by the recommended design.** This cascade only happens
> when the adapter sources service discovery from `ctx.ServiceDiscovery()`, which
> adds `service_discovery.KAContext` to the constructors and forces it onto every
> transitive caller. With the in-process service-discovery handle (Design
> constraints §3 / "B2"), the constructors require only `gqlclient.KAContext` — the
> interface the wrappers already had — so the recompile adds **no** new requirement
> and the migration touches no callers. The ~140-site figure below is the cost of
> the `ctx`-sourced ("B3") variant; prefer §3 to skip it entirely. The auto-fix
> path below still applies to any genuinely pre-existing debt the rebuild exposes.

**Auto-fix:** the `kacontextinterface` analyzer (`pkg/kacontext/linters/fix_interface_lint.go`) emits `analysis.SuggestedFix` records covering both the missing embed and the missing import. Run:

```sh
tools/runlint.sh --fix <changed-files-or-packages>
```

Webapp's runlint wrapper passes `--fix` through to `golangci-lint run --fix`, which applies the analyzer's edits in place. No hand-editing and no goimports pass required — the analyzer's `buildAddFix` writes the embed and adds the import path together. Rerun lint after the fix to confirm clean.

> rostering files, ~140 sites:
> ```
> ctx uses but does not explicitly request interface(s) context.Context, httpctx.KAContext, service_discovery.KAContext, web.ServiceVersionContext
> ```
> Each site is fixable with `tools/runlint.sh --fix <file>`.

### 7. Visibility convention applied to user type aliases

Webapp's `ka-visibility` lint also rejects underscore-prefixed identifiers in user code. Local interface aliases that pre-dated the lint surfaced once the surrounding code was recompiled.

> districts service, `admin_insights/metrics_fetch.go`:
> ```go
> type _metricsCtx interface { … }   // file-private to definition, used by sibling files
> ```
> Fix: rename `_metricsCtx` → `metricsCtx`. Generalises: any underscore-prefixed type used across files in a package will need renaming during migration.

### 8. Hand-edited mock response field name mismatches

The defederator's design preserves the gqlclient.Mux dispatch path so existing `cross_service/*_mocks.go` files keep working unchanged (see `genqlient.md`). The trap: when manually broadening a mock that previously returned `js.Obj{}` to a fuller response shape during migration cleanup, the top-level field in the response object must match the **GraphQL selection name in the @genqlient query string**, not the operation name.

This is easy to get wrong when the operation is named for *what it does* but the selected field has a different name. The operation name often appears in the variable identifier and in the Go method name, masking the divergence.

> ai-guide service, `cross_service/user_mocks.go`:
> ```go
> // Query selects updateUserRole, but mock returned markUserAsRole:
> _ = `# @genqlient
>     mutation AiGuide_MarkUserAsRole($kaid: String!, $role: UserRole!) {
>         updateUserRole(role: $role, operation: ADD, kaid: $kaid) {
>             error { code }
>         }
>     }
> `
>
> // Wrong — field name matches the operation, not the selection:
> js.Obj{
>     "markUserAsRole": js.Obj{"kaid": "some-kaid"},
> }
>
> // Right — field name matches the GraphQL selection:
> js.Obj{
>     "updateUserRole": js.Obj{"error": nil},
> }
> ```
> Symptom: `gqlclient.testing.go` validates the mocked response against the federated schema and panics with `field not in query, path = markUserAsRole`. The original `js.Obj{}` mock was fine — there is no need to broaden the response shape during migration unless the test actually inspects the returned data.

Rule of thumb: if a pre-migration mock returned `js.Obj{}` and the corresponding `cross_service/` function no longer uses `gqlclient.ErrorInFrameworkOrResponse(err, resp)` (i.e. the response value isn't consulted), leave the mock at `js.Obj{}`. Only expand the mock response when the caller actually reads fields out of it.

### 9. Orphaned `genqlient.<Op>` calls — annotation stripped, call site left in place

This was the most expensive class encountered. A migration commit removed `_ = `# @genqlient ...`` annotation blocks from `cross_service/` files but kept the calls to `genqlient.<Op>(...)`. The build kept compiling because `generated/genqlient/queries.go` still contained the operations from before the migration; the regression was invisible until the next `make genqlient` regen, which silently dropped the unbacked operations and broke 39 call sites across 19 files (37 in ai-guide, 2 in donations).

> ai-guide service, `cross_service/fetch_all_sets_of_standards.go`:
> ```go
> // Migration commit stripped:
> //   _ = `# @genqlient
> //       query AiGuide_AllSetsOfStandards { … }
> //   `
> // … but kept:
> resp, err := genqlient.AiGuide_AllSetsOfStandards(ctx, ctx.GraphQL().AsUser())
> ```
> Symptom (only after `make genqlient` regen): `undefined: genqlient.AiGuide_AllSetsOfStandards`.

**Root cause:** the migration's intent was to switch these calls to the defederator-generated client (`cross_service/client.go`). Stripping the genqlient annotation was a step toward that end. But the call-site rewrite never happened — leaving the codebase in a half-migrated state that compiled by accident.

**Detection:** `defederator check <service-dir>` scans the service for `genqlient.<Op>(...)` calls lacking a backing `@genqlient` annotation. It would have surfaced all 39 orphans in one CI run rather than at the next regen.

**Prevention:** strip a `@genqlient` annotation only as part of the same commit that migrates the call site to the defederator client. Wire `defederator check` into the post-migration workflow (see "Verifying after migration" above). The check is also useful as a pre-commit hook for any commit that touches `cross_service/` files.

> Sub-class: the same migration also stripped sub-directives (`# @genqlient(pointer: true)`, `# @genqlient(for: "...", pointer: true)`) from annotation blocks it kept. These don't show up as orphans — the operation is still declared — but they change the generated Go types from pointers to values (or value-typed input fields), producing `cannot use &x (type *T) as T` errors at the call site after the next regen. `defederator check` does not detect this class; the fix is to restore the directive by hand or revert the file to its pre-migration state.

---

### Takeaways for future migrations

The categories above sort roughly by how often they recurred:

1. **Codegen recursion bugs (1a–1e)** surfaced once per new fragment shape. Each fix narrowed the recursion criterion; further fragment shapes may surface more.
2. **Type-registration bugs (2a–2c)** surfaced once per new schema-feature use case. Lazy registration solved most.
3. **Migrate output bugs (3a–3e)** were caught by the first webapp service. Subsequent services hit the same fixes.
4. **Binding gaps (4a–4b)** are inherent to keeping both clients — the safelist + task-dispatch lints need genqlient references regardless of defederator.
5. **Latent user-code bugs (5a–5d)** are one-time costs per service: typed generation surfaces them at compile time, which is the point.
6. **Lint debt (6, 7)** is the long tail. Mostly mechanical; `tools/runlint.sh --fix` applies the kacontextinterface analyzer's SuggestedFix records in place, including imports.
7. **Hand-edited mocks (8)** are avoidable. Leave mocks alone unless the caller actually reads the response; the defederator preserves the dispatch path so `js.Obj{}` mocks keep working unchanged.
8. **Orphaned genqlient calls (9)** are the costliest class — invisible until the next genqlient regen, then they break dozens of call sites at once. Run `defederator check <service-dir>` after every migration and in CI. Strip a `@genqlient` annotation only in the same commit that migrates its call site to the defederator client.

When migrating a new service, expect to encounter (3a)–(3e) and (5a)–(5d) consistently. The codegen and registration categories (1, 2) are mostly closed; new instances would represent genuinely new schema shapes. Run `defederator check` before committing — it pays for itself the first time it catches a stripped annotation.
