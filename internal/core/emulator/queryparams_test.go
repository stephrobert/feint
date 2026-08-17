package emulator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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

// A parameter the contract declares and a handler never reads is invisible to
// every other gate this project has: the route mounts, the response validates,
// the drift report joins on the operation name, and the client does not always
// crash. What the client gets is an answer that ignores its request —
// `?visibility=private` returning the public catalogue was #271, and the same
// discarded-request signature sat on four more operations with declared
// filters when it was first measured (five of the twelve Exoscale operations
// that declare query parameters, none of Scaleway's fifty-four read nothing,
// two read only the path).
//
// So the gate is structural: for every mounted route whose contract operation
// declares query parameters, the handler must read the request's query —
// itself, or through a same-package function it hands the request to. Reading
// the query is necessary for any parameter to have an effect, so a handler
// that provably never looks cannot be honouring anything. What this cannot
// see is a handler that reads the query and drops one declared parameter of
// several; that depth needs value-level analysis, and the behavioural tests
// in each pack carry it for the operations fixed under #271.
//
// The handler is attributed from the pack's own route table in source, not
// from the mounted function pointer: packs wrap their handlers (Exoscale's
// guardSplitClients, Outscale's dryRunnable), and every wrapped pointer
// resolves to the same closure.
func TestDeclaredQueryParametersAreRead(t *testing.T) {
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
		readers := queryReaders(files)
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
			params := make([]string, 0, len(op.Query))
			for param := range op.Query {
				params = append(params, param)
			}
			sort.Strings(params)

			handler := handlerOf[name]
			if handler == "" {
				t.Errorf("%s: %s declares query parameters (%s) and the gate cannot attribute its handler from the route table; declare the route with Handler: p.<method> or teach the gate the builder",
					provider, route.Operation, strings.Join(params, ", "))
				continue
			}
			if !readers[handler] {
				t.Errorf("%s: %s declares query parameters (%s) and its handler %s never reads the request's query — implement them or refuse them, never drop them (#271)",
					provider, route.Operation, strings.Join(params, ", "), handler)
			}
		}
	}
}

// packSources parses every non-test file of one pack package.
func packSources(dir string) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

// routeHandlers reads the pack's route table: every composite literal carrying
// an Operation and a Handler key yields the operation's declared name and the
// method that serves it. The operation is a string literal (Scaleway) or a
// one-argument helper call like operation("list-templates") (Exoscale); the
// handler is the p.<method> the literal names, before any wrapping.
func routeHandlers(files []*ast.File) map[string]string {
	out := map[string]string{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			var operation, handler string
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "Operation":
					operation = operationLiteral(kv.Value)
				case "Handler":
					if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
						handler = sel.Sel.Name
					}
				}
			}
			if operation != "" && handler != "" {
				out[operation] = handler
			}
			return true
		})
	}
	return out
}

// operationLiteral unwraps the two spellings a route table uses for its
// operation name.
func operationLiteral(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.BasicLit:
		return strings.Trim(v.Value, `"`)
	case *ast.CallExpr:
		if len(v.Args) == 1 {
			if arg, ok := v.Args[0].(*ast.BasicLit); ok {
				return strings.Trim(arg.Value, `"`)
			}
		}
	}
	return ""
}

// queryReaders reports, for every function of one pack package, whether it
// reads its *http.Request parameter's query — directly (r.URL, r.Form,
// r.FormValue, r.PostForm) or by handing the request to a same-package
// function that does.
func queryReaders(files []*ast.File) map[string]bool {
	direct := map[string]bool{}
	handsTo := map[string][]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			request := requestParamName(fn)
			if request == "" || request == "_" {
				continue
			}
			reads, callees := requestUse(fn.Body, request)
			direct[fn.Name.Name] = reads
			handsTo[fn.Name.Name] = callees
		}
	}

	// Propagate through the calls that carry the request, to a fixed point:
	// listServerTypes reads nothing itself and hands the request to parsePage,
	// which does.
	for changed := true; changed; {
		changed = false
		for name, callees := range handsTo {
			if direct[name] {
				continue
			}
			for _, callee := range callees {
				if direct[callee] {
					direct[name] = true
					changed = true
					break
				}
			}
		}
	}
	return direct
}

// requestParamName names the *http.Request parameter, or "" when there is none.
func requestParamName(fn *ast.FuncDecl) string {
	for _, field := range fn.Type.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Request" {
			continue
		}
		for _, name := range field.Names {
			return name.Name
		}
	}
	return ""
}

// requestUse walks one body: does it touch the request's query surface, and
// which functions does it pass the request to.
func requestUse(body *ast.BlockStmt, request string) (reads bool, callees []string) {
	ast.Inspect(body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if base, ok := sel.X.(*ast.Ident); ok && base.Name == request {
				switch sel.Sel.Name {
				case "URL", "Form", "PostForm", "FormValue", "PostFormValue":
					reads = true
				}
			}
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, arg := range call.Args {
			if id, ok := arg.(*ast.Ident); ok && id.Name == request {
				callees = append(callees, calleeName(call.Fun))
			}
		}
		return true
	})
	return reads, callees
}

// calleeName is the bare name a call resolves to inside one package:
// parsePage for parsePage(r), serverOf for p.serverOf(w, r). Package-qualified
// calls resolve to their bare selector too, which can only cause a false
// "reads" if an external helper shares its name with a query-reading local
// one — accepted, since the gate errs loudly in the other direction.
func calleeName(fun ast.Expr) string {
	switch v := fun.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}
