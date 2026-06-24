package migrate_test

import (
	"testing"

	"github.com/StevenACoffman/defederator/migrate"
)

// wrap puts body inside a minimal parseable file. RewriteCallSites only parses
// (never type-checks) its input, so undefined identifiers are fine.
func wrap(body string) string {
	return "package p\n\nfunc f() {\n" + body + "\n}\n"
}

func TestRewriteCallSites(t *testing.T) {
	// Lifted out of the table so the deep struct-field indentation does not push
	// the chain past the line-length limit.
	const wsIn = "_ = genqlient.X(c, c.GraphQL().WithService(\"s\").AsServiceAdmin())"
	const wsWant = "_ = genqlient.X(c, NewAdminGraphQLClient(c))"
	const localeIn = "_ = genqlient.X(c, c.GraphQL().WithKALocale(l).AsUser())"
	const localeWant = "_ = genqlient.X(c, NewLocaleUserGraphQLClient(c, l))"
	cases := []struct {
		name        string
		in          string
		want        string
		wantChanged bool
	}{
		{
			name:        "admin",
			in:          wrap("\t_, _ = genqlient.Op(ctx, ctx.GraphQL().AsServiceAdmin(), a)"),
			want:        wrap("\t_, _ = genqlient.Op(ctx, NewAdminGraphQLClient(ctx), a)"),
			wantChanged: true,
		},
		{
			name:        "user",
			in:          wrap("\t_, _ = genqlient.Op(ctx, ctx.GraphQL().AsUser(), a, b)"),
			want:        wrap("\t_, _ = genqlient.Op(ctx, NewUserGraphQLClient(ctx), a, b)"),
			wantChanged: true,
		},
		{
			name:        "withservice dropped",
			in:          wrap(wsIn),
			want:        wrap(wsWant),
			wantChanged: true,
		},
		{
			name:        "locale user",
			in:          wrap(localeIn),
			want:        wrap(localeWant),
			wantChanged: true,
		},
		{
			name:        "non-ctx receiver preserved",
			in:          wrap("\t_, _ = genqlient.Op(gctx, gctx.GraphQL().AsUser(), a)"),
			want:        wrap("\t_, _ = genqlient.Op(gctx, NewUserGraphQLClient(gctx), a)"),
			wantChanged: true,
		},
		{
			name:        "task dispatch untouched",
			in:          wrap("\t_, _ = tasks.GraphQLTask(genqlient.Op_Operation, vars)"),
			want:        wrap("\t_, _ = tasks.GraphQLTask(genqlient.Op_Operation, vars)"),
			wantChanged: false,
		},
		{
			name:        "non-graphql client arg untouched",
			in:          wrap("\t_, _ = genqlient.Op(ctx, someClient, a)"),
			want:        wrap("\t_, _ = genqlient.Op(ctx, someClient, a)"),
			wantChanged: false,
		},
		{
			name:        "no genqlient call",
			in:          wrap("\tfoo(ctx)"),
			want:        wrap("\tfoo(ctx)"),
			wantChanged: false,
		},
		{
			name: "multiple calls all rewritten",
			in: wrap("\t_, _ = genqlient.A(ctx, ctx.GraphQL().AsServiceAdmin(), x)\n" +
				"\t_, _ = genqlient.B(ctx, ctx.GraphQL().AsUser(), y)"),
			want: wrap("\t_, _ = genqlient.A(ctx, NewAdminGraphQLClient(ctx), x)\n" +
				"\t_, _ = genqlient.B(ctx, NewUserGraphQLClient(ctx), y)"),
			wantChanged: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := migrate.RewriteCallSites([]byte(tc.in))
			if err != nil {
				t.Fatalf("RewriteCallSites: %v", err)
			}
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}
			if string(got) != tc.want {
				t.Errorf("output mismatch\ngot:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestRewriteCallSites_ParseError(t *testing.T) {
	_, _, err := migrate.RewriteCallSites([]byte("package p\nfunc f( {"))
	if err == nil {
		t.Fatal("expected a parse error for malformed input")
	}
}
