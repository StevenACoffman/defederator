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

Create `cross_service/client.go` to hold the shared setup code. This file wires service discovery to subgraph URL resolution and feeds `ctx.HTTP()` as the per-request transport.

```go
package cross_service

import (
    "context"
    "fmt"
    "net/http"

    "github.com/Khan/webapp/pkg/lib/httpctx"
    "github.com/Khan/webapp/pkg/lib/service_discovery"
    "github.com/Khan/webapp/pkg/web"
    defed "github.com/Khan/webapp/services/myservice/generated/defederator"
)

// _federationCtx is the minimal context the federation client needs.
type _federationCtx interface {
    context.Context
    web.ServiceVersionContext
    httpctx.KAContext
    service_discovery.KAContext
}

// newFederationClient returns a FederationClient whose HTTP transport and
// subgraph URLs are resolved fresh from ctx on every operation call.
// Callers do not need to cache this — construction is cheap.
func newFederationClient(ctx _federationCtx) defed.FederationClient {
    return defed.NewClientWithFactories(
        func(callCtx context.Context) *http.Client {
            if h, ok := callCtx.(httpctx.KAContext); ok {
                return h.HTTP()
            }
            return http.DefaultClient
        },
        func(callCtx context.Context, specJSON string) (*defed.Plan, error) {
            urls, err := _subgraphURLs(callCtx, ctx.ServiceDiscovery())
            if err != nil {
                return nil, err
            }
            return defed.Resolve(specJSON, urls)
        },
    )
}

// _subgraphServiceNames maps join__Graph enum names to service-discovery names.
// The enum names come from the `join__Graph` enum in the supergraph SDL.
// The service names are the values of the `name:` argument in @join__graph.
// Add an entry here whenever a new subgraph appears in a cross-service query.
var _subgraphServiceNames = map[string]string{
    "AI_GUIDE":  "ai-guide",
    "CONTENT":   "content",
    "DISTRICTS": "districts",
    "EMAILS":    "emails",
    "REWARDS":   "rewards",
    "USERS":     "users",
    // Add subgraphs as needed for your service's operations.
}

// _subgraphURLs resolves each subgraph enum name to its HTTP endpoint.
// Follows webapp convention: the GraphQL endpoint is at /backend-graphql/ on
// the service's base URL.
func _subgraphURLs(ctx context.Context, sd service_discovery.Client) (map[string]string, error) {
    urls := make(map[string]string, len(_subgraphServiceNames))
    for enumName, svcName := range _subgraphServiceNames {
        u, err := sd.EndpointForServiceWithVersion(ctx, svcName, "")
        if err != nil {
            return nil, fmt.Errorf("resolve subgraph URL for %s (%s): %w", enumName, svcName, err)
        }
        u.Path = "/backend-graphql/"
        urls[enumName] = u.String()
    }
    return urls, nil
}
```

### Why `NewClientWithFactories` instead of `NewClient`

`NewClient(httpClient, subgraphURLs)` resolves URLs once at construction time. In webapp, subgraph URLs depend on the service version in the request context (e.g. `znd-myversion.ka.org` vs. `www.ka.org`). Using `NewClientWithFactories` defers both the HTTP client choice and the URL resolution to call time, so each request uses the correct context-scoped values.

`service_discovery.Client.EndpointForServiceWithVersion` is internally cached, so per-call resolution adds no meaningful latency.

## Step 4 — Migrate cross_service functions

### Context type

Replace the genqlient context requirement with `_federationCtx`. The full `*kaContext` used in request handlers already satisfies this interface. Test contexts from `servicetest.Suite.KAContext()` also satisfy it because `kacontext.TestContext` includes `service_discovery.KAContext`, `httpctx.KAContext`, and `web.ServiceVersionContext`.

```go
// Before
func GetUserEmail(ctx gqlclient.KAContext, kaid string) (string, error) {

// After
func GetUserEmail(ctx _federationCtx, kaid string) (string, error) {
```

If a function previously used an inline interface with `log.KAContext` and `gqlclient.KAContext`, replace the whole thing with `_federationCtx`. Any caller that previously satisfied the narrower interface will satisfy `_federationCtx` in production.

### Calling pattern

```go
// Before (genqlient)
resp, err := genqlient.MyService_GetUser(ctx, ctx.GraphQL().AsServiceAdmin(), kaid)
if err != nil {
    return "", errors.Wrap(err, "kaid", kaid)
}
return resp.User.Email, nil

// After (defederator)
client := newFederationClient(ctx)
resp, err := client.MyServiceGetUser(ctx, &kaid)
if err != nil {
    return "", errors.Wrap(err, "kaid", kaid)
}
if resp.GetUser() == nil {
    return "", nil
}
return generic.Deref(resp.GetUser().GetEmail()), nil
```

### Pointer fields

With `optional: pointer`, all nullable response fields are `*T`. Use:

- `resp.GetFoo()` — the generated getter returns a zero value instead of panicking on nil
- `generic.Deref(ptr)` — for `*string`, `*bool`, `*int`, etc.
- `generic.DerefWithDefault(ptr, fallback)` — when a specific zero value is wrong

```go
import "github.com/Khan/webapp/pkg/lib/generic"

// *string
email := generic.Deref(resp.GetUser().GetEmail())

// *bool
hasSendableEmail := generic.Deref(resp.GetUser().GetHasSendableEmail())

// nested nullable struct
if pl := resp.GetUser().GetPreferredKaLocale(); pl != nil {
    kaLocale = generic.Deref(pl.GetKaLocale())
}
```

### Operation name casing

defederator renders operation names in Go-exported PascalCase. A genqlient operation `MyService_GetUser` becomes `client.MyServiceGetUser`. The `_` is dropped and the next letter is uppercased.

### Remove genqlient artifacts

Delete the `_ = \`# @genqlient ... \`` assignment. The query string is no longer needed in the Go source — defederator extracted it during code generation and baked the resulting plan spec into `client.go`.

Also remove the `genqlient` import if no other functions in the file still use it.

### Error handling

No change needed. Continue using `errors.Wrap` and `errors.NotFound` from `github.com/Khan/webapp/pkg/lib/errors`. Do not use `fmt.Errorf` (webapp linter rule).

```go
// Still correct after migration
return "", errors.Wrap(err, "kaid", kaid)
return BasicUserInfo{}, errors.NotFound("User not found", errors.Fields{"kaid": kaid})
```

## Step 5 — Migrate tests

### Old pattern (genqlient / gqlclient.Mux)

```go
func (suite *getUserSuite) TestSuccess() {
    ctx := suite.KAContext().Clone()
    mux := gqlclient.NewMux()
    mux.HandleOperation("MyService_GetUser", js.Obj{"user": js.Obj{"email": "a@b.com"}})
    ctx.GraphQLClient = gqlclient.NewMockClient(mux)

    email, err := GetUserEmail(ctx, "kaid_1234")
    suite.Require().NoError(err)
    suite.Require().Equal("a@b.com", email)
}
```

### New pattern (httptest.Server + service_discovery mock)

defederator calls subgraph HTTP endpoints directly, so mocking must happen at the HTTP level. The test spins up a local `httptest.Server` that returns canned GraphQL responses, then points service discovery at it.

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

// subgraphErrorServer returns a mock subgraph that replies with a GraphQL error.
func subgraphErrorServer(msg string) *httptest.Server {
    return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]interface{}{
            "errors": []map[string]interface{}{
                {"message": msg},
            },
        })
    }))
}

// mockSD returns a service_discovery.Client that routes all subgraphs to server.
func mockSD(server *httptest.Server) service_discovery.Client {
    u, _ := url.Parse(server.URL)
    endpoints := make(map[string]*url.URL, len(_subgraphServiceNames))
    for _, svc := range _subgraphServiceNames {
        endpoints[svc] = u
    }
    return service_discovery.NewMockClient(endpoints)
}

func (suite *getUserSuite) TestSuccess() {
    server := subgraphServer(map[string]interface{}{
        "user": map[string]interface{}{"email": "a@b.com"},
    })
    defer server.Close()

    ctx := suite.KAContext().Clone()
    ctx.ServiceDiscoveryClient = mockSD(server)

    email, err := GetUserEmail(ctx, "kaid_1234")
    suite.Require().NoError(err)
    suite.Require().Equal("a@b.com", email)
}

func (suite *getUserSuite) TestError() {
    server := subgraphErrorServer("user service unavailable")
    defer server.Close()

    ctx := suite.KAContext().Clone()
    ctx.ServiceDiscoveryClient = mockSD(server)

    _, err := GetUserEmail(ctx, "kaid_1234")
    suite.Require().Error(err)
    suite.Require().Contains(err.Error(), "user service unavailable")
}
```

`ctx.ServiceDiscoveryClient` is a public field on `*kacontext.kaContext` (the concrete type returned by `suite.KAContext().Clone()`). Setting it directly replaces the real service discovery client for the duration of the test.

The mock server receives all subgraph traffic regardless of the operation name — `mockSD` points every subgraph enum to the same `httptest.Server`. If a test calls multiple operations that need different responses, use separate servers mapped to specific subgraph enum names:

```go
usersServer := subgraphServer(map[string]interface{}{"user": ...})
contentServer := subgraphServer(map[string]interface{}{"locale": ...})
defer usersServer.Close()
defer contentServer.Close()

usersURL, _ := url.Parse(usersServer.URL)
contentURL, _ := url.Parse(contentServer.URL)
ctx.ServiceDiscoveryClient = service_discovery.NewMockClient(map[string]*url.URL{
    "users":   usersURL,
    "content": contentURL,
})
```

### Migrating mock helper files

If your service has a `*_mocks.go` file with functions like `MockGetUserEmail(mux *gqlclient.Mux, ...)` that are used by tests in OTHER packages (e.g. integration tests in `resolvers/`), those functions must also be updated. Replace the `gqlclient.Mux` parameter with the new HTTP-server pattern:

```go
// Before
func MockGetUserEmail(mux *gqlclient.Mux, email string) {
    mux.HandleOperation("MyService_GetUserEmail", js.Obj{"user": js.Obj{"email": email}})
}

// After
// MockGetUserEmail registers a mock users subgraph response for GetUserEmail.
// The returned closer must be called (typically via defer) to shut down the server.
// The caller must set ctx.ServiceDiscoveryClient = mockSD(server) before calling
// the function under test.
func MockGetUserEmail(email string) (server *httptest.Server, setSD func(ctx *kacontext.kaContext)) {
    srv := subgraphServer(map[string]interface{}{
        "user": map[string]interface{}{"email": email},
    })
    return srv, func(ctx *kacontext.kaContext) {
        ctx.ServiceDiscoveryClient = mockSD(srv)
    }
}
```

Callers update to:

```go
srv, setSD := cross_service.MockGetUserEmail("a@b.com")
defer srv.Close()
ctx := suite.KAContext().Clone()
setSD(ctx)
```

This pattern is more verbose than the original but accurately reflects what the code under test does: it makes real HTTP calls to an endpoint. The test controls that endpoint.

## Step 6 — Subgraph name mapping

When a new cross-service query accesses a subgraph not yet in `_subgraphServiceNames`, add it. The mapping is derived from the `join__Graph` enum in `gengraphql/composed_schema.graphql`:

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

The pilot implementation lives at `services/donations/cross_service/`. It demonstrates:

- `.defederator.yml` at `services/donations/.defederator.yml`
- `cross_service/client.go` — `newFederationClient` + `_subgraphURLs` + `_subgraphServiceNames`
- `cross_service/user_email_by_kaid.go` — migrated production function
- `cross_service/user_info_by_kaid.go` — migrated production function with nested field access
- `cross_service/user_email_by_kaid_test.go` — migrated tests using `httptest.Server`
- `generated/defederator/client.go` — 27 operations, `FederationClient` interface, `NewClientWithFactories`
- `generated/defederator/federation_exec.go` — execution engine, `package defederator`
