package migrate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
)

// RewriteCallSites rewrites a single cross_service Go source file so that every
// synchronous cross-service call swaps its gqlclient transport for a defederator
// compat constructor:
//
//	genqlient.Op(ctx, ctx.GraphQL()[.WithService("x")].AsServiceAdmin(), args…)
//	  → genqlient.Op(ctx, NewAdminGraphQLClient(ctx), args…)
//	genqlient.Op(ctx, ctx.GraphQL().AsUser(), args…)
//	  → genqlient.Op(ctx, NewUserGraphQLClient(ctx), args…)
//	genqlient.Op(ctx, ctx.GraphQL().WithKALocale(l).AsUser(), args…)
//	  → genqlient.Op(ctx, NewLocaleUserGraphQLClient(ctx, l), args…)
//
// Only the second argument (the graphql.Client) of a genqlient.<Op>(…) call is
// touched, and only when it is exactly a `recv.GraphQL()[.WithService(…)].As…()`
// chain — the genqlient function, the leading ctx, every other argument, the
// response handling, and all surrounding source (including comments and
// formatting) are left byte-for-byte unchanged. `.WithService(…)` is dropped
// because the planner routes per operation by subgraph.
//
// tasks.GraphQLTask(genqlient.<Op>_Operation, …) is naturally excluded: its
// operation reference is a const selector, not a genqlient.<Op>(…) call, so it
// never matches.
//
// Requires: src is a parseable Go file.
// Ensures:  on success returns the rewritten source and changed=true iff at least
//
//	one call site was rewritten; returns src unchanged with changed=false when
//	there is nothing to rewrite. It never reorders or reformats untouched code.
//
// Pure: no I/O. The read/write shell lives in the caller.
func RewriteCallSites(src []byte) (out []byte, changed bool, err error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, false, fmt.Errorf("rewrite: parse: %w", err)
	}

	// A replacement of the client argument's exact byte range.
	type replacement struct {
		start, end int
		text       string
	}
	var repls []replacement

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isGenqlientOpCall(call) || len(call.Args) < 2 {
			return true
		}
		newText, ok := compatClientText(src, fset, call.Args[1])
		if !ok {
			return true
		}
		repls = append(repls, replacement{
			start: fset.Position(call.Args[1].Pos()).Offset,
			end:   fset.Position(call.Args[1].End()).Offset,
			text:  newText,
		})
		return true
	})
	if len(repls) == 0 {
		return src, false, nil
	}

	// Apply highest-offset-first so earlier offsets stay valid as we splice.
	slices.SortFunc(repls, func(a, b replacement) int { return b.start - a.start })
	out = src
	for _, r := range repls {
		spliced := make([]byte, 0, len(out)-(r.end-r.start)+len(r.text))
		spliced = append(spliced, out[:r.start]...)
		spliced = append(spliced, r.text...)
		spliced = append(spliced, out[r.end:]...)
		out = spliced
	}
	return out, true, nil
}

// isGenqlientOpCall reports whether call is a `genqlient.<Op>(…)` invocation
// (selector on the genqlient package identifier). It does not distinguish
// operations from other genqlient exports; the client-argument shape check in
// compatClientText is what confirms a real cross-service call.
func isGenqlientOpCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "genqlient"
}

// compatClientText returns the replacement source for arg when arg is a
// `recv.GraphQL()[.WithService(…)].As{User,ServiceAdmin}()` chain (optionally
// with .WithKALocale(l) before .AsUser()), or ok=false for any other expression.
// The returned text is the matching New<Flavor>GraphQLClient(...) constructor
// call, with receiver and locale sub-expressions copied verbatim from src.
func compatClientText(src []byte, fset *token.FileSet, arg ast.Expr) (string, bool) {
	flavor, chainStart, ok := terminalFlavor(arg)
	if !ok {
		return "", false
	}
	recv, locale, hasLocale, ok := graphQLChain(chainStart)
	if !ok {
		return "", false
	}
	if hasLocale {
		if flavor != "User" {
			return "", false // WithKALocale only composes with AsUser
		}
		flavor = "LocaleUser"
	}

	recvText := exprText(src, fset, recv)
	if flavor == "LocaleUser" {
		return fmt.Sprintf(
			"NewLocaleUserGraphQLClient(%s, %s)",
			recvText, exprText(src, fset, locale),
		), true
	}
	return fmt.Sprintf("New%sGraphQLClient(%s)", flavor, recvText), true
}

// terminalFlavor matches the terminal .AsUser()/.AsServiceAdmin() call of a
// client chain, returning the flavor ("User"/"Admin") and the chain expression
// the terminal was invoked on. ok is false for any other expression.
func terminalFlavor(arg ast.Expr) (flavor string, chainStart ast.Expr, ok bool) {
	term, ok := arg.(*ast.CallExpr)
	if !ok {
		return "", nil, false
	}
	sel, ok := term.Fun.(*ast.SelectorExpr)
	if !ok || len(term.Args) != 0 {
		return "", nil, false
	}
	switch sel.Sel.Name {
	case "AsUser":
		return "User", sel.X, true
	case "AsServiceAdmin":
		return "Admin", sel.X, true
	default:
		return "", nil, false
	}
}

// graphQLChain walks the selector chain a terminal .As{User,ServiceAdmin}() is
// invoked on — recv.GraphQL()[.WithService("x")][.WithKALocale(l)] — and returns
// the GraphQL() receiver plus any WithKALocale argument. WithService(…) is
// accepted and ignored (the plan routes per subgraph). ok is false for any
// expression that is not such a chain.
func graphQLChain(start ast.Expr) (recv, locale ast.Expr, hasLocale, ok bool) {
	for cur := start; ; {
		c, isCall := cur.(*ast.CallExpr)
		if !isCall {
			return nil, nil, false, false
		}
		s, isSel := c.Fun.(*ast.SelectorExpr)
		if !isSel {
			return nil, nil, false, false
		}
		switch s.Sel.Name {
		case "GraphQL":
			return s.X, locale, hasLocale, true
		case "WithKALocale":
			if len(c.Args) != 1 {
				return nil, nil, false, false
			}
			locale = c.Args[0]
			hasLocale = true
		case "WithService":
			// Dropped: the defederator plan routes per operation by subgraph.
		default:
			return nil, nil, false, false
		}
		cur = s.X
	}
}

// exprText returns the exact source slice spanning e, so receiver and locale
// sub-expressions are reproduced verbatim (preserving any spacing the author used).
func exprText(src []byte, fset *token.FileSet, e ast.Expr) string {
	return string(src[fset.Position(e.Pos()).Offset:fset.Position(e.End()).Offset])
}
