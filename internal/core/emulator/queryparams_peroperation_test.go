package emulator_test

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// TestDeclaredQueryParametersAreRead (queryparams_test.go) catches the flagrant
// half of #271: a handler that never looks at its query at all. Its own comment
// names what it cannot see — a handler that reads *some* declared parameters
// and drops the others. That residual class was measured at 24 dropped
// parameters across 13 Scaleway list operations (#277): every one of those
// handlers calls parsePage, so the pack-level gate saw a query reader and moved
// on, while `?order_by=created_at_desc` kept answering ascending.
//
// This gate carries the per-parameter half, still statically: for every mounted
// route whose contract operation declares query parameters, each declared name
// must appear as a query-key string literal somewhere in the handler's own
// call graph — the handler, plus every same-package function it hands the
// request (or its url.Values) to, to a fixed point. A name that appears nowhere
// in that graph cannot be read on that operation, whatever the rest of the pack
// does with it; attributing per operation is exactly what turns #277's
// pack-wide floor into a verdict.
//
// The collection is deliberately permissive about *how* a key is read — .Get
// and .Has arguments, map-index keys, string literals handed to a helper along
// with the query, and []string literals inside query-reading functions (the
// `for _, key := range []string{"per_page", "page_size"}` idiom). A literal
// that names something else entirely can therefore mask a dropped parameter of
// the same spelling: the gate errs green, never red, because a gate that
// rougit on legitimate code teaches people to route around it. What stays
// invisible at this depth — a parameter read and then ignored — is what the
// packs' behavioural tests carry (order_by asked desc must come back desc).
func TestNoDeclaredQueryParameterIsDroppedByItsHandler(t *testing.T) {
	env := emulator.DefaultEnv()
	packs := map[string]emulator.Pack{
		scaleway.Name: scaleway.New(env),
		outscale.Name: outscale.New(env),
		exoscale.Name: exoscale.New(env),
	}

	for provider, pack := range packs {
		doc, err := contract.Load(filepath.Join("..", "..", "..", "contracts", provider+".json"))
		if err != nil {
			t.Fatalf("%s: load contract: %v", provider, err)
		}
		files, err := packSources(filepath.Join("..", "..", "providers", provider))
		if err != nil {
			t.Fatalf("%s: parse pack sources: %v", provider, err)
		}
		keys := queryKeysByFunction(files)
		handlerOf := map[string]string{}
		for rawOperation, handler := range routeHandlers(files) {
			if _, name, ok := doc.OperationFor(rawOperation); ok {
				handlerOf[name] = handler
			}
		}

		for _, route := range pack.Routes() {
			op, name, known := doc.OperationFor(route.Operation)
			if !known || len(op.Query) == 0 {
				continue
			}
			handler := handlerOf[name]
			if handler == "" {
				// The pack-level gate already errors on an unattributable
				// handler; erroring here again would say the same thing twice.
				continue
			}
			var missing []string
			for param := range op.Query {
				if !keys[handler][param] {
					missing = append(missing, param)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				t.Errorf("%s: %s declares query parameters its handler %s never names (%s) — serve each one or refuse it with its reason, never drop it (#277)",
					provider, route.Operation, handler, strings.Join(missing, ", "))
			}
		}
	}
}

// queryKeysByFunction returns, for every function of one pack package, the set
// of query-key string literals reachable from it: the ones its own body uses,
// united with those of every same-package function it hands its *http.Request
// or a url.Values to, to a fixed point.
func queryKeysByFunction(files []*ast.File) map[string]map[string]bool {
	own := map[string]map[string]bool{}
	handsTo := map[string][]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			carriers := carrierParams(fn)
			literals, callees := queryKeyUse(fn.Body, carriers)
			own[fn.Name.Name] = literals
			handsTo[fn.Name.Name] = callees
		}
	}

	// Propagate along the calls that carry the request or the query, to a
	// fixed point: listServers itself names no page key, parsePage does.
	for changed := true; changed; {
		changed = false
		for name, callees := range handsTo {
			for _, callee := range callees {
				for key := range own[callee] {
					if !own[name][key] {
						if own[name] == nil {
							own[name] = map[string]bool{}
						}
						own[name][key] = true
						changed = true
					}
				}
			}
		}
	}
	return own
}

// carrierParams names the parameters of fn that can carry query state into a
// callee: the *http.Request, and any url.Values.
func carrierParams(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	for _, field := range fn.Type.Params.List {
		if !carriesQuery(field.Type) {
			continue
		}
		for _, name := range field.Names {
			if name.Name != "_" {
				out[name.Name] = true
			}
		}
	}
	return out
}

// carriesQuery recognises the two types query state travels under:
// *http.Request and url.Values (package-qualified or dot-imported).
func carriesQuery(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.StarExpr:
		sel, ok := v.X.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Request"
	case *ast.SelectorExpr:
		return v.Sel.Name == "Values"
	case *ast.Ident:
		return v.Name == "Values"
	}
	return false
}

// queryKeyUse walks one body and collects the string literals used as query
// keys, and the same-package callees the body hands a carrier to.
//
// A literal counts as a query key when it is:
//   - the argument of a .Get or .Has call — r.URL.Query().Get("name"),
//     q.Get("state"), whatever the receiver spells like;
//   - the key of an index expression — q["ip_ids"];
//   - a string literal passed to a call that also receives a carrier —
//     csvValues(q, "server_ids");
//   - an element of a []string composite literal, when the function touches a
//     carrier at all — the `for _, key := range []string{...}` idiom of
//     parsePage and listEvents.
//
// Local variables assigned from a carrier (q := r.URL.Query()) become carriers
// themselves for the third rule.
func queryKeyUse(body *ast.BlockStmt, carriers map[string]bool) (map[string]bool, []string) {
	literals := map[string]bool{}
	var callees []string

	// First pass: locals holding the query itself join the carrier set, so
	// q := r.URL.Query() then filterServers(all, q) is an edge. Only the
	// .Query() shape taints: a string drawn from the query (name := q.Get(...))
	// carries one value, not the query, and tainting it would hand every
	// function it reaches a spurious set of keys.
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range assign.Rhs {
			if i >= len(assign.Lhs) {
				break
			}
			lhs, ok := assign.Lhs[i].(*ast.Ident)
			if !ok {
				continue
			}
			if isQueryCall(rhs, carriers) {
				carriers[lhs.Name] = true
			}
		}
		return true
	})

	touches := false
	ast.Inspect(body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && carriers[id.Name] {
			touches = true
		}
		switch v := n.(type) {
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "Get", "Has":
					if len(v.Args) == 1 {
						if lit := stringLiteral(v.Args[0]); lit != "" {
							literals[lit] = true
						}
					}
				}
			}
			carried := false
			for _, arg := range v.Args {
				// mentionsCarrier rather than a plain identifier match:
				// filterServers(all, r.URL.Query()) hands the query over
				// without ever naming a bare carrier argument.
				if mentionsCarrier(arg, carriers) {
					carried = true
				}
			}
			if carried {
				callees = append(callees, calleeName(v.Fun))
				for _, arg := range v.Args {
					if lit := stringLiteral(arg); lit != "" {
						literals[lit] = true
					}
				}
			}
		case *ast.IndexExpr:
			// Only an index on a carrier reads the query — q["ip_ids"].
			// Counting every map index would let res.Attrs["is_default"], a
			// store write, stand in for a filter nobody implemented.
			if id, ok := v.X.(*ast.Ident); ok && carriers[id.Name] {
				if lit := stringLiteral(v.Index); lit != "" {
					literals[lit] = true
				}
			}
		}
		return true
	})

	if touches {
		ast.Inspect(body, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			arr, ok := lit.Type.(*ast.ArrayType)
			if !ok {
				return true
			}
			if id, ok := arr.Elt.(*ast.Ident); !ok || id.Name != "string" {
				return true
			}
			for _, elt := range lit.Elts {
				if s := stringLiteral(elt); s != "" {
					literals[s] = true
				}
			}
			return true
		})
	}
	return literals, callees
}

// isQueryCall recognises the expression shapes that hold the whole query:
// X.Query() where X mentions a carrier, or a bare carrier identifier.
func isQueryCall(expr ast.Expr, carriers map[string]bool) bool {
	switch v := expr.(type) {
	case *ast.Ident:
		return carriers[v.Name]
	case *ast.CallExpr:
		sel, ok := v.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Query" && mentionsCarrier(sel.X, carriers)
	}
	return false
}

// mentionsCarrier reports whether the expression names one of the carriers —
// r.URL.Query() mentions r.
func mentionsCarrier(expr ast.Expr, carriers map[string]bool) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && carriers[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

func stringLiteral(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	return strings.Trim(lit.Value, `"`)
}
