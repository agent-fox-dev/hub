package carrypatch_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestTS21_36_WorkspaceAndCarrypatchDoNotImportEachOther verifies that
// internal/workspace does not depend on internal/carrypatch and vice versa.
// This ensures the two packages remain independent, which is required for
// the resolver hook pattern (21-REQ-9.4).
func TestTS21_36_WorkspaceAndCarrypatchDoNotImportEachOther(t *testing.T) {
	// List transitive deps of internal/workspace.
	wsDeps := goListDeps(t, "./internal/workspace")
	if containsPkg(wsDeps, "github.com/agent-fox-dev/hub/internal/carrypatch") {
		t.Errorf("internal/workspace depends on internal/carrypatch; the two packages must be independent")
	}

	// List transitive deps of internal/carrypatch.
	cpDeps := goListDeps(t, "./internal/carrypatch")
	if containsPkg(cpDeps, "github.com/agent-fox-dev/hub/internal/workspace") {
		t.Errorf("internal/carrypatch depends on internal/workspace; the two packages must be independent")
	}
}

// goListDeps runs `go list -deps <pkg>` and returns the output lines.
// It sets the working directory to the module root so that relative or
// absolute import paths resolve correctly regardless of where the test
// binary runs.
func goListDeps(t *testing.T, pkg string) []string {
	t.Helper()
	// Determine the module root via `go env GOMOD`.
	modCmd := exec.Command("go", "env", "GOMOD")
	modOut, modErr := modCmd.CombinedOutput()
	if modErr != nil {
		t.Fatalf("go env GOMOD failed: %v\n%s", modErr, modOut)
	}
	modRoot := strings.TrimSpace(string(modOut))
	// modRoot is the path to go.mod; we need its directory.
	if idx := strings.LastIndex(modRoot, "/"); idx >= 0 {
		modRoot = modRoot[:idx]
	}

	cmd := exec.Command("go", "list", "-deps", pkg)
	cmd.Dir = modRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s failed: %v\n%s", pkg, err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// containsPkg checks whether pkgName appears in the list of packages.
func containsPkg(deps []string, pkgName string) bool {
	for _, d := range deps {
		if d == pkgName {
			return true
		}
	}
	return false
}
