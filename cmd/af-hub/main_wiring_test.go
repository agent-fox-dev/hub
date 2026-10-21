package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

// wiringStep is a call of package forge in main, with where it is in main.go.
type wiringStep struct {
	name string
	pos  token.Pos
}

// wiring is what main.go does with package forge: its calls in source order, whether it starts
// one with go, which of them stop the hub when they fail, whether the ingress is mounted on
// server.Echo(), and the fields of the forge.Config literals.
type wiring struct {
	steps        []wiringStep
	fatalOnError map[string]bool
	configFields []string
	mountsOnEcho bool
	goStarts     map[string]bool
}

// parseMainWiring parses main.go and reads its wiring of package forge.
func parseMainWiring(t *testing.T) wiring {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	w := wiring{fatalOnError: map[string]bool{}, goStarts: map[string]bool{}}

	var mainFunc *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "main" && fn.Recv == nil {
			mainFunc = fn
		}
	}
	if mainFunc == nil {
		t.Fatal("main.go has no func main")
	}

	forgeCall := func(n ast.Node) (string, *ast.CallExpr) {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return "", nil
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return "", nil
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "forge" {
			return sel.Sel.Name, call
		}
		return "", nil
	}

	ast.Inspect(mainFunc.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GoStmt:
			if name, _ := forgeCall(n.Call); name != "" {
				w.goStarts[name] = true
			}
		case *ast.IfStmt:
			// if err := forge.X(...); err != nil { log.Fatal(err) }
			if assign, ok := n.Init.(*ast.AssignStmt); ok && len(assign.Rhs) == 1 {
				if name, _ := forgeCall(assign.Rhs[0]); name != "" {
					ast.Inspect(n.Body, func(b ast.Node) bool {
						if call, ok := b.(*ast.CallExpr); ok {
							if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Fatal" {
								w.fatalOnError[name] = true
							}
						}
						return true
					})
				}
			}
		case *ast.CompositeLit:
			if sel, ok := n.Type.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "forge" && sel.Sel.Name == "Config" {
					for _, elt := range n.Elts {
						if kv, ok := elt.(*ast.KeyValueExpr); ok {
							if key, ok := kv.Key.(*ast.Ident); ok {
								w.configFields = append(w.configFields, key.Name)
							}
						}
					}
				}
			}
		}
		if name, call := forgeCall(n); name != "" {
			w.steps = append(w.steps, wiringStep{name: name, pos: call.Pos()})
			if name == "MountWebhookIngress" && len(call.Args) > 0 {
				// The first argument is server.Echo().
				if echoCall, ok := call.Args[0].(*ast.CallExpr); ok {
					if sel, ok := echoCall.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Echo" {
						w.mountsOnEcho = true
					}
				}
			}
		}
		return true
	})
	return w
}

// firstPos is the position of the first call of forge.name, or token.NoPos.
func (w wiring) firstPos(name string) token.Pos {
	for _, s := range w.steps {
		if s.name == name {
			return s.pos
		}
	}
	return token.NoPos
}

// TS-32-56: main initialises the delivery log with a fatal error path, then starts the retention
// loop with go, mounts the ingress on the Echo root, and gives forge.Config the audit emitter, the
// secrets store and the external URL.
// Requirement: 32-REQ-9.7
func TestMainWiresTheWebhookIngress_TS_32_56(t *testing.T) {
	w := parseMainWiring(t)

	initPos, startPos, mountPos := w.firstPos("InitSchema"), w.firstPos("StartDeliveryRetention"), w.firstPos("MountWebhookIngress")
	for name, pos := range map[string]token.Pos{"forge.InitSchema": initPos, "forge.StartDeliveryRetention": startPos, "forge.MountWebhookIngress": mountPos} {
		if pos == token.NoPos {
			t.Errorf("main.go never calls %s", name)
		}
	}
	if !w.fatalOnError["InitSchema"] {
		t.Errorf("main.go does not stop when forge.InitSchema fails")
	}
	if !w.goStarts["StartDeliveryRetention"] {
		t.Errorf("main.go does not start forge.StartDeliveryRetention with go")
	}
	if initPos != token.NoPos && startPos != token.NoPos && initPos >= startPos {
		t.Errorf("forge.InitSchema (at %d) is not called before forge.StartDeliveryRetention (at %d)", initPos, startPos)
	}
	if !w.mountsOnEcho {
		t.Errorf("main.go does not mount the ingress on server.Echo()")
	}
	for _, field := range []string{"Audit", "Secrets", "ExternalURL"} {
		if !slices.Contains(w.configFields, field) {
			t.Errorf("the forge.Config of main.go has no %s; its fields are %v", field, w.configFields)
		}
	}
}

// packageCalls returns, in source order, the calls of package pkg in func main
// of main.go ("pkg.Func" with its position), and the ones started with go.
func packageCalls(t *testing.T, pkgs ...string) (calls []wiringStep, goStarts map[string]bool) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	goStarts = map[string]bool{}
	name := func(n ast.Node) string {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return ""
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && slices.Contains(pkgs, pkg.Name) {
			return pkg.Name + "." + sel.Sel.Name
		}
		return ""
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" || fn.Recv != nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if g, ok := n.(*ast.GoStmt); ok {
				if s := name(g.Call); s != "" {
					goStarts[s] = true
				}
			}
			if s := name(n); s != "" {
				calls = append(calls, wiringStep{s, n.Pos()})
			}
			return true
		})
	}
	return calls, goStarts
}

// TS-34-40: main starts the binding sweep with go after the binding table
// exists, registers the bound-token routes right after
// MountWorkspaceHandlers, and registers the refusal reporter.
// Requirement: 34-REQ-6.6
func TestMainWiresBoundTokens_TS_34_40(t *testing.T) {
	calls, goStarts := packageCalls(t, "boundtoken", "workspace")
	pos := map[string]token.Pos{}
	order := []string{}
	for _, c := range calls {
		if _, seen := pos[c.name]; !seen {
			pos[c.name] = c.pos
			order = append(order, c.name)
		}
	}
	for _, want := range []string{"workspace.MountWorkspaceHandlers", "boundtoken.RegisterRoutes", "boundtoken.Install", "boundtoken.StartBindingSweep"} {
		if !pos[want].IsValid() {
			t.Fatalf("main does not call %s (calls %v)", want, order)
		}
	}
	if !goStarts["boundtoken.StartBindingSweep"] {
		t.Error("StartBindingSweep is not started with go")
	}
	// MountWorkspaceHandlers creates the binding table (through the
	// workspace schema), so the sweep must start after it.
	if pos["workspace.MountWorkspaceHandlers"] >= pos["boundtoken.StartBindingSweep"] {
		t.Error("the sweep starts before MountWorkspaceHandlers has created the binding table")
	}
	i := slices.Index(order, "workspace.MountWorkspaceHandlers")
	if i < 0 || i+1 >= len(order) || order[i+1] != "boundtoken.RegisterRoutes" {
		t.Errorf("calls %v; want boundtoken.RegisterRoutes right after workspace.MountWorkspaceHandlers", order)
	}
}
