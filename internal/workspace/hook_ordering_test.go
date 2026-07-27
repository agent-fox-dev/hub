package workspace

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-04-34: Verify that the hub server initialization registers the personal
// org AfterUserCreateFunc hook via OnAfterUserCreate before calling
// MountWorkspaceHandlers (which internally calls MountHandlers).
// Requirement: 04-REQ-10.1
// ---------------------------------------------------------------------------
func TestHookOrdering_RegisterBeforeMountWorkspaceHandlers(t *testing.T) {
	// This test uses static analysis of the hub server main.go to verify
	// that OnAfterUserCreate is called before MountWorkspaceHandlers. This
	// is a code inspection test — it parses the Go AST of the main.go file
	// and checks the ordering of function calls.
	//
	// This approach is used because:
	// 1. The hook registration and handler mounting happen during server init
	//    and are wired at the integration level (not unit-testable without
	//    starting a real server).
	// 2. The spec requires that OnAfterUserCreate is called BEFORE
	//    MountWorkspaceHandlers; checking the source ensures this contract
	//    is maintained as the code evolves.

	root := findProjectRoot(t)
	mainPath := filepath.Join(root, "cmd", "af-hub", "main.go")

	src, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("failed to read main.go: %v", err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, mainPath, src, 0)
	if err != nil {
		t.Fatalf("failed to parse main.go: %v", err)
	}

	// Walk the AST and record the positions of OnAfterUserCreate and
	// MountWorkspaceHandlers calls in the main function.
	var onAfterPos, mountPos token.Pos

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		funcName := callExprName(call)
		if strings.Contains(funcName, "OnAfterUserCreate") && !onAfterPos.IsValid() {
			onAfterPos = call.Pos()
		}
		if strings.Contains(funcName, "MountWorkspaceHandlers") && !mountPos.IsValid() {
			mountPos = call.Pos()
		}
		return true
	})

	// The spec states: "registers the personal org AfterUserCreateFunc hook
	// via OnAfterUserCreate before calling MountWorkspaceHandlers".
	// This test will FAIL until group 11 adds the OnAfterUserCreate call
	// to main.go before MountWorkspaceHandlers.
	if !onAfterPos.IsValid() {
		t.Fatal("OnAfterUserCreate call not found in cmd/af-hub/main.go; " +
			"hub server must register the personal org hook before MountWorkspaceHandlers")
	}

	if !mountPos.IsValid() {
		t.Fatal("MountWorkspaceHandlers call not found in cmd/af-hub/main.go")
	}

	if onAfterPos >= mountPos {
		t.Errorf("OnAfterUserCreate (pos %d) must appear BEFORE MountWorkspaceHandlers (pos %d) in main.go",
			onAfterPos, mountPos)
	}
}

// callExprName extracts a readable function name from a call expression.
// Handles simple calls (foo()), method calls (x.foo()), and chained calls
// (x.y.foo()). Returns the final selector or function name.
func callExprName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return callExprReceiverName(fn.X) + "." + fn.Sel.Name
	default:
		return ""
	}
}

// callExprReceiverName extracts the receiver name from an expression.
func callExprReceiverName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return callExprReceiverName(e.X) + "." + e.Sel.Name
	default:
		return ""
	}
}

// findProjectRoot walks up from the current directory to find go.mod.
func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}

// ---------------------------------------------------------------------------
// TS-04-35: Verify that apikit Server uses the hook reference captured at
// MountHandlers call time; hooks registered after MountHandlers are not
// guaranteed to be invoked, but the system does not panic.
// Requirement: 04-REQ-10.2
// ---------------------------------------------------------------------------
// NOTE: The core test for TS-04-35 is in apikit/hook_test.go as
// TestAfterUserCreate_HookAfterMountHandlersNoPanic because it requires
// direct access to apikit.NewServer, Server.MountHandlers, and the ability
// to trigger user creation through the server's admin endpoint.
//
// This test in the workspace package verifies the complementary property:
// that the hub's MountWorkspaceHandlers (which calls MountHandlers internally)
// does not interfere with hooks registered beforehand.
func TestHookOrdering_MountWorkspaceHandlersPreservesHook(t *testing.T) {
	// This is a static analysis test verifying that MountWorkspaceHandlers
	// calls MountHandlers (which is the point at which hooks are captured).
	// The actual behavioral test is in apikit/hook_test.go.

	root := findProjectRoot(t)
	routesPath := filepath.Join(root, "internal", "workspace", "routes.go")

	src, err := os.ReadFile(routesPath)
	if err != nil {
		t.Fatalf("failed to read routes.go: %v", err)
	}

	content := string(src)

	// Verify MountWorkspaceHandlers calls s.MountHandlers.
	if !strings.Contains(content, "MountHandlers") {
		t.Error("MountWorkspaceHandlers in routes.go should call s.MountHandlers(); " +
			"hooks are captured at MountHandlers call time")
	}

	// Verify MountWorkspaceHandlers accepts an *apikit.Server parameter
	// (so it has access to the server's hook state).
	if !strings.Contains(content, "*apikit.Server") {
		t.Error("MountWorkspaceHandlers should accept *apikit.Server as first parameter; " +
			"this enables access to hooks registered via OnAfterUserCreate")
	}
}
