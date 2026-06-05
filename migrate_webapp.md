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

When migrating a new service, expect to encounter (3a)–(3e) and (5a)–(5d) consistently. The codegen and registration categories (1, 2) are mostly closed; new instances would represent genuinely new schema shapes.
