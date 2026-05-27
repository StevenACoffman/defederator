# Replacing genqlient with defederator in webapp

This guide covers the specific steps to replace genqlient cross-service GraphQL clients with defederator in Khan Academy's webapp monorepo.

## Why replace genqlient

genqlient routes all cross-service GraphQL calls through graphql-gateway. This adds a latency hop and a single point of failure for every call. defederator generates clients that call subgraphs directly, executing the federation query plan in-process: it resolves cross-subgraph entity fetches, `@requires`, and `@provides` without a running gateway.

The generated code imports only the Go standard library at runtime. No defederator or gorouter packages ship to production.

## How webapp is different from the defaults

The webapp supergraph SDL (`gengraphql/composed_schema.graphql`) uses `url: "unused"` for every subgraph in the `join__Graph` enum. Subgraph URLs must be resolved at runtime from service discovery rather than baked in at generation time. This requires `url_mode: enum` in the config file and the `NewClientWithFactories` constructor in the calling code.

## Step 1 — Write the config file

Place `.defederator.yml` at the root of the service directory (next to your existing `genqlient.yaml` or `go.generate` invocation).

```yaml
# services/myservice/.defederator.yml
schema: ../../gengraphql/composed_schema.graphql

query:
  - cross_service/*.go      # files containing # @genqlient-annotated query strings

client:
  filename: ./generated/defederator/client.go
  package:  defederator

url_mode: enum              # required: webapp SDL uses url: "unused" for all subgraphs

generate:
  clientInterfaceName: FederationClient   # generates a named interface for mocking
  optional: pointer                        # nullable fields become *T

bindings:
  # Map GraphQL scalars to their Go types. Add any scalars your queries reference.
  Date:
    type: cloud.google.com/go/civil.Date
  DateTime:
    type: time.Time
  JSONString:
    type: string
  KALocale:
    type: string
  Map:
    type: map[string]interface{}
  Any:
    type: interface{}
  # Add domain-specific scalars from pkg/content as needed:
  # Author:
  #   type: github.com/Khan/webapp/pkg/content.Author
  # DownloadUrl:
  #   type: github.com/Khan/webapp/pkg/content.DownloadURL
  # TopicPathSegment:
  #   type: github.com/Khan/webapp/pkg/content.URLPathEntry
  # TopicPath:
  #   type: github.com/Khan/webapp/pkg/content.URLPath
```

### Query source: `.go` files with embedded queries

If your existing cross_service functions embed queries as `# @genqlient`-annotated string literals, point `query:` at those Go files directly. defederator extracts the query strings, and the `_ = \`...\`` assignment that suppresses "unused variable" warnings for genqlient is simply ignored:

```go
// cross_service/get_user.go (existing genqlient pattern)
func GetUser(ctx gqlclient.KAContext, kaid string) (string, error) {
    _ = `# @genqlient
        query MyService_GetUser($kaid: String!) {
            user(kaid: $kaid) { email }
        }
    `
    resp, err := genqlient.MyService_GetUser(ctx, ctx.GraphQL().AsServiceAdmin(), kaid)
    return resp.User.Email, errors.Wrap(err, "kaid", kaid)
}
```

No changes are required to the query strings themselves.

## Step 2 — Generate

```sh
cd services/myservice
go run github.com/StevenACoffman/defederator/cmd/defederator@latest -c .defederator.yml
```

This writes two files:
- `generated/defederator/client.go` — typed methods, response structs, plan spec constants
- `generated/defederator/federation_exec.go` — the execution engine (stdlib-only copy, renamed to `package defederator`)

Add both to version control. Regenerate whenever queries or the supergraph SDL change.

## Step 3 — Create the federation client helper

Create `cross_service/client.go` to hold the shared setup code. This file defines the context types, the client constructors, and the subgraph URL resolver.

```go
package cross_service

import (
    "context"
    "net/http"
    "net/url"

    "github.com/Khan/webapp/pkg/lib/errors"
    "github.com/Khan/webapp/pkg/lib/httpctx"
    "github.com/Khan/webapp/pkg/lib/service_discovery"
    "github.com/Khan/webapp/pkg/web"
    "github.com/Khan/webapp/pkg/web/gqlclient"
    defed "github.com/Khan/webapp/services/myservice/generated/defederator"
)

// _federationCtx is for user-facing operations that carry the caller's HTTP
// session (cookies, auth headers) through ctx.HTTP().
type _federationCtx interface {
    context.Context
    web.ServiceVersionContext
    httpctx.KAContext
    service_discovery.KAContext
}

// _jobsCtx is for operations that need an explicit auth-level HTTP client
// (service-admin or user-proxied) selected at the call site via
// ctx.GraphQL().AsServiceAdmin().HTTPClient() or ctx.GraphQL().AsUser().HTTPClient().
type _jobsCtx interface {
    context.Context
    gqlclient.KAContext
    service_discovery.KAContext
}

// newFederationClient returns a FederationClient whose HTTP transport is
// resolved from ctx.HTTP() and whose subgraph URLs come from service discovery.
// Use this for user-facing operations where the caller's session is in ctx.
func newFederationClient(ctx _federationCtx) defed.FederationClient {
    return defed.NewClientWithFactories(
        func(callCtx context.Context) *http.Client {
            if h, ok := callCtx.(httpctx.KAContext); ok {
                return h.HTTP()
            }
            return http.DefaultClient
        },
        func(callCtx context.Context, specJSON string) (*defed.Plan, error) {
            urls, err := myserviceSubgraphURLs(callCtx, ctx.ServiceDiscovery())
            if err != nil {
                return nil, err
            }
            return defed.Resolve(specJSON, urls)
        },
    )
}

// newJobFederationClient returns a FederationClient using an explicit HTTP
// client (e.g. obtained from ctx.GraphQL().AsServiceAdmin().HTTPClient() or
// ctx.GraphQL().AsUser().HTTPClient()) and an explicit service-discovery client.
// Use this for background jobs, tasks, and mutations that require a specific
// auth level rather than the caller's user session.
func newJobFederationClient(httpClient *http.Client, sd service_discovery.Client) defed.FederationClient {
    return defed.NewClientWithFactories(
        func(_ context.Context) *http.Client {
            return httpClient
        },
        func(callCtx context.Context, specJSON string) (*defed.Plan, error) {
            urls, err := myserviceSubgraphURLs(callCtx, sd)
            if err != nil {
                return nil, err
            }
            return defed.Resolve(specJSON, urls)
        },
    )
}

// _subgraphServices maps join__Graph enum names to service-discovery names.
// Add new entries whenever a new subgraph is referenced in a cross_service query.
var _subgraphServices = map[string]string{
    "AI_GUIDE":  "ai-guide",
    "CONTENT":   "content",
    "DISTRICTS": "districts",
    "EMAILS":    "emails",
    "USERS":     "users",
    // Add subgraphs as needed for your service's operations.
}

// myserviceSubgraphURLs resolves each subgraph enum name to its HTTP endpoint.
// Follows webapp convention: the GraphQL endpoint is at /backend-graphql/.
func myserviceSubgraphURLs(ctx context.Context, sd service_discovery.Client) (map[string]string, error) {
    urls := make(map[string]string, len(_subgraphServices))
    for enumName, svcName := range _subgraphServices {
        u, err := sd.EndpointForServiceWithVersion(ctx, svcName, "")
        if err != nil {
            return nil, errors.Wrap(err, "enumName", enumName, "svcName", svcName)
        }
        urls[enumName] = (&url.URL{
            Scheme: u.Scheme,
            Host:   u.Host,
            Path:   "/backend-graphql/",
        }).String()
    }
    return urls, nil
}
```

Note: use `errors.Wrap` (not `fmt.Errorf`) to comply with the webapp linter rule. Construct the endpoint URL from a `url.URL` struct literal rather than mutating `u.Path` directly.

### Why `NewClientWithFactories` instead of `NewClient`

`NewClient(httpClient, subgraphURLs)` resolves URLs once at construction time. In webapp, subgraph URLs depend on the service version in the request context (e.g. `znd-myversion.ka.org` vs. `www.ka.org`). Using `NewClientWithFactories` defers both the HTTP client choice and the URL resolution to call time, so each request uses the correct context-scoped values.

`service_discovery.Client.EndpointForServiceWithVersion` is internally cached, so per-call resolution adds no meaningful latency.

## Step 4 — Migrate cross_service functions

### Choosing the right context type

There are two context types depending on how the operation authenticates:

**`_federationCtx`** — for user-facing operations that carry the caller's HTTP session. The HTTP client comes from `ctx.HTTP()`. Use when the caller's cookies/auth headers should flow through.

```go
// Before
func GetUserEmail(ctx gqlclient.KAContext, kaid string) (string, error) {

// After
func GetUserEmail(ctx _federationCtx, kaid string) (string, error) {
    client := newFederationClient(ctx)
    resp, err := client.MyServiceGetUser(ctx, kaid)
    ...
```

**`_jobsCtx`** — for background jobs, tasks, and mutations that need a specific auth level. The HTTP client is selected explicitly at the call site using `ctx.GraphQL()`. Use this when you previously called `ctx.GraphQL().AsServiceAdmin()` or `ctx.GraphQL().WithService("x").AsUser()`.

```go
// Before
func DisableThing(ctx gqlclient.KAContext, kaid string) error {
    _, err := genqlient.MyService_DisableThing(ctx, ctx.GraphQL().AsServiceAdmin(), kaid)
    ...

// After
func DisableThing(ctx _jobsCtx, kaid string) error {
    client := newJobFederationClient(ctx.GraphQL().AsServiceAdmin().HTTPClient(), ctx.ServiceDiscovery())
    _, err := client.MyServiceDisableThing(ctx, kaid)
    ...
```

Callers of functions that change from `gqlclient.KAContext` to `_jobsCtx` must add `service_discovery.KAContext` to their own context interface if it is not already present.

### Auth level routing with `newJobFederationClient`

The auth level is selected by which HTTP client you pass:

```go
// Service-admin auth (bypasses per-user permissions)
client := newJobFederationClient(
    ctx.GraphQL().AsServiceAdmin().HTTPClient(),
    ctx.ServiceDiscovery(),
)

// User-proxied auth (call acts as the authenticated user)
client := newJobFederationClient(
    ctx.GraphQL().AsUser().HTTPClient(),
    ctx.ServiceDiscovery(),
)
```

The `WithService("x")` call that was required with genqlient to route to a specific gateway path is **redundant with defed** — defed's plan spec already knows which subgraph each operation targets. Only the auth level matters:

```go
// Before (genqlient): WithService routed to a specific gateway path
_, err := genqlient.MyService_DoThing(ctx, ctx.GraphQL().WithService("assignments").AsUser(), input)

// After (defed): WithService dropped; only .AsUser() auth level matters
client := newJobFederationClient(ctx.GraphQL().AsUser().HTTPClient(), ctx.ServiceDiscovery())
_, err := client.MyServiceDoThing(ctx, input)
```

### `@genqlient` annotations — keep or delete?

**If both genqlient and defed scan the same Go source files, keep the `_ = \`# @genqlient ... \`` assignment.** Removing it would break genqlient's `make genqlient` target for that operation.

Only delete the annotation if you have confirmed that genqlient no longer needs it (e.g. the operation has been fully removed from `genqlient.yaml`).

```go
// Keep this: both tools extract the query from it
_ = `# @genqlient
    query MyService_GetUser($kaid: String!) {
        user(kaid: $kaid) { email }
    }
`
```

### `genqlient` import retention

Even after migrating a function to defed, the `genqlient` import often must stay in the file because:
- Input types defined in the generated genqlient package are still used as function parameters (e.g. `genqlient.CreateMasteryAssignmentsInput`, `genqlient.KaidCourseIds`)
- Enum types from genqlient are referenced in type aliases or comparisons (e.g. `genqlient.ModerationFlagSeverity`, `genqlient.StudentMasteryAssignmentStatusAssigned`)

Only remove the `genqlient` import when no symbol from it remains in the file.

### Calling pattern

```go
// Before (genqlient, service-admin)
resp, err := genqlient.MyService_GetUser(ctx, ctx.GraphQL().AsServiceAdmin(), kaid)
if err != nil {
    return "", errors.Wrap(err, "kaid", kaid)
}
return resp.User.Email, nil

// After (defed, service-admin via _jobsCtx)
client := newJobFederationClient(ctx.GraphQL().AsServiceAdmin().HTTPClient(), ctx.ServiceDiscovery())
resp, err := client.MyServiceGetUser(ctx, kaid)
if err != nil {
    return "", errors.Wrap(err, "kaid", kaid)
}
return generic.Deref(resp.GetUser().GetEmail()), nil
```

### Pointer fields and pointer slices

With `optional: pointer`, nullable scalar fields become `*T` and nullable list fields become `[]*T`. Use:

- `resp.GetFoo()` — generated getter returns a zero value instead of panicking on nil receiver
- `generic.Deref(ptr)` — dereferences `*string`, `*bool`, `*int`, etc., returning the zero value if nil
- `generic.DerefWithDefault(ptr, fallback)` — when the zero value is semantically wrong
- `generic.Pointers(slice)` — converts `[]T` to `[]*T` when passing value slices to defed input parameters that expect pointer slices

```go
import "github.com/Khan/webapp/pkg/lib/generic"

// *string
email := generic.Deref(resp.GetUser().GetEmail())

// *bool
hasSendableEmail := generic.Deref(resp.GetUser().GetHasSendableEmail())

// []*T — iterate with pointer element
for _, item := range resp.GetItems() {
    fmt.Println(item.GetName())  // item is *T; getter is nil-safe
}

// []T input parameter that defed expects as []*T
client.MyServiceUpdate(ctx, generic.Pointers(mySlice))
```

### Field naming: `Id` → `ID`

defed generates Go-idiomatic field names: well-known abbreviations are uppercased. The most common change is `Id string` in genqlient becoming `ID string` in defed. Update all field accesses and struct literal assignments:

```go
// Before (genqlient)
assignmentsToUpdateByID[assignment.Id] = ...
previousAssignment.Unit.Id == placement.UnitID

// After (defed)
assignmentsToUpdateByID[assignment.ID] = ...
previousAssignment.Unit.GetID() == placement.UnitID  // Unit is *T so use getter
```

### Operation name casing

defederator renders operation names in Go-exported PascalCase. A genqlient operation `MyService_GetUser` becomes `client.MyServiceGetUser`. The `_` is dropped and the next letter is uppercased.

### Error handling

No change needed. Continue using `errors.Wrap` and `errors.NotFound` from `github.com/Khan/webapp/pkg/lib/errors`. Do not use `fmt.Errorf` (webapp linter rule).

```go
// Still correct after migration
return "", errors.Wrap(err, "kaid", kaid)
return BasicUserInfo{}, errors.NotFound("User not found", errors.Fields{"kaid": kaid})
```

## Step 5 — Migrate tests

### Keep using `gqlclient.Mux` when both tools coexist

When a service runs both genqlient and defed (i.e. the `@genqlient` annotations are kept and `make genqlient` still regenerates `generated/genqlient/queries.go`), existing `*_mocks.go` files and test helpers that use `gqlclient.Mux.HandleOperation` continue to work unchanged for defed calls too. defed routes through the same `gqlclient.Mux` transport as genqlient in test contexts, because `ctx.GraphQL().AsServiceAdmin().HTTPClient()` returns the mock client when `ctx.GraphQLClient` is a `gqlclient.MockClient`.

```go
// This existing mock still works for both genqlient and defed calls:
func MockDisableThing(mux *gqlclient.Mux, kaid string) *gqlclient.SimpleHandler {
    vars := gqlclient.Vars{"kaid": kaid}
    response := js.Obj{"disableThing": js.Obj{"error": nil}}
    return mux.HandleOperationWithVars("MyService_DisableThing", vars, response)
}
```

The operation name in `HandleOperation` / `HandleOperationWithVars` must match the operation name in the `@genqlient`-annotated query string (e.g. `Districts_GetThread`), not the generated Go method name (`DistrictsGetThread`).

### New pattern (httptest.Server + service_discovery mock)

If a service has fully removed genqlient and no longer uses `gqlclient.Mux`, tests must mock at the HTTP level instead, since defed calls subgraph endpoints directly via service discovery.

```go
import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "net/url"

    "github.com/Khan/webapp/pkg/lib/service_discovery"
)

// subgraphServer returns a mock subgraph that replies with the given data payload.
func subgraphServer(data map[string]interface{}) *httptest.Server {
    return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
    }))
}

func (suite *getUserSuite) TestSuccess() {
    server := subgraphServer(map[string]interface{}{
        "user": map[string]interface{}{"email": "a@b.com"},
    })
    defer server.Close()

    ctx := suite.KAContext().Clone()
    u, _ := url.Parse(server.URL)
    ctx.ServiceDiscoveryClient = service_discovery.NewMockClient(map[string]*url.URL{
        "users": u,
    })

    email, err := GetUserEmail(ctx, "kaid_1234")
    suite.Require().NoError(err)
    suite.Require().Equal("a@b.com", email)
}
```

`ctx.ServiceDiscoveryClient` is a public field on the concrete type returned by `suite.KAContext().Clone()`.

## Step 6 — Subgraph name mapping

When a new cross-service query accesses a subgraph not yet in `_subgraphServices`, add it. The mapping is derived from the `join__Graph` enum in `gengraphql/composed_schema.graphql`:

```graphql
enum join__Graph {
  AI_GUIDE  @join__graph(name: "ai-guide",  url: "unused")
  CONTENT   @join__graph(name: "content",   url: "unused")
  USERS     @join__graph(name: "users",     url: "unused")
  ...
}
```

The `name:` argument is the service-discovery name. The enum value (e.g. `AI_GUIDE`) is the key in `_subgraphServiceNames`.

## Known limitations

### INPUT_OBJECT types map to `string`

GraphQL input types (e.g. `SailthruVar`, `AIGuideAccessPlanReferenceKey`) that appear as query arguments and do not have an explicit binding in `.defederator.yml` are currently mapped to `string` in the generated function signatures. This means the caller must pass a JSON-serialized string for those arguments, or you must add a binding:

```yaml
bindings:
  SailthruVar:
    type: github.com/Khan/webapp/services/emails/generated/defederator.SailthruVar
```

If the input type is complex and no suitable Go type exists, consider defining one in a shared package and binding to it.

### All nullable fields are `*T`

With `optional: pointer`, every nullable field in the schema becomes `*T`. Use `generic.Deref` throughout. The generated getter methods (`GetFoo()`) return the zero value of `T` when called on a nil receiver, which is safe to call in chains.

### Mutation support

Mutations work identically to queries. The generated method name follows the same PascalCase conversion from the operation name.

### No subscription support

Subscription operations are not supported and will cause a generation error. Leave subscription-based operations on genqlient.

## Complete example

The most complete reference implementation is `services/districts/cross_service/`. It demonstrates both context patterns, both client constructors, and the `gqlclient.Mux` test approach used when genqlient and defed coexist:

- `.defederator.yml` at `services/districts/.defederator.yml`
- `cross_service/client.go` — both `_federationCtx`/`newFederationClient` (user-session) and `_jobsCtx`/`newJobFederationClient` (explicit auth) with `districtSubgraphURLs`
- `cross_service/progress.go` — service-admin call via `_jobsCtx` + type alias pointing to defed type
- `cross_service/assignments.go` — 4 operations: one `AsServiceAdmin`, three `AsUser`; pointer slice inputs via `generic.Pointers`
- `cross_service/ai_guide.go` — 7 type aliases to defed types; `ModerationFlagSeverity` kept pointing to genqlient
- `cross_service/*_mocks.go` — `gqlclient.Mux`-based mocks unchanged and still working
- `generated/defederator/client.go` — `FederationClient` interface, `NewClientWithFactories`, plan spec constants

The earlier pilot at `services/donations/cross_service/` uses only `_federationCtx`/`newFederationClient` (no service-admin auth pattern) and `httptest.Server`-based tests.
