package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ========================================================================
// Spec 05 Task 1.2: WORKSPACE_ROOT directory creation on boot
// (TS-05-5, TS-05-6)
// ========================================================================

// TS-05-5: Server boot creates the WORKSPACE_ROOT directory (including missing
// parents) when it does not already exist.
// Requirement: 05-REQ-2.1
func TestEnsureWorkspaceRoot_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ws-root-new")

	// Verify directory does not exist before the call.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected %q to not exist before test", dir)
	}

	if err := EnsureWorkspaceRoot(dir); err != nil {
		t.Fatalf("EnsureWorkspaceRoot(%q) returned error: %v", dir, err)
	}

	// Verify directory now exists.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("os.Stat(%q) failed after EnsureWorkspaceRoot: %v", dir, err)
	}
	if !info.IsDir() {
		t.Errorf("%q exists but is not a directory", dir)
	}
}

// 05-REQ-2.E2: When WORKSPACE_ROOT path contains multiple missing parent
// directories, EnsureWorkspaceRoot creates all of them.
func TestEnsureWorkspaceRoot_CreatesNestedParents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c", "ws-root")

	if err := EnsureWorkspaceRoot(dir); err != nil {
		t.Fatalf("EnsureWorkspaceRoot(%q) returned error: %v", dir, err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("os.Stat(%q) failed: %v", dir, err)
	}
	if !info.IsDir() {
		t.Errorf("%q exists but is not a directory", dir)
	}
}

// 05-REQ-2.E1: When WORKSPACE_ROOT already exists, EnsureWorkspaceRoot
// proceeds without error and does not modify the existing directory.
func TestEnsureWorkspaceRoot_ExistingDirectoryNoError(t *testing.T) {
	dir := t.TempDir() // already exists

	// Place a sentinel file so we can verify the directory is not recreated.
	sentinel := filepath.Join(dir, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatalf("failed to create sentinel file: %v", err)
	}

	if err := EnsureWorkspaceRoot(dir); err != nil {
		t.Fatalf("EnsureWorkspaceRoot(%q) returned error: %v", dir, err)
	}

	// Verify sentinel file is still present (directory was not recreated).
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel file missing after EnsureWorkspaceRoot: %v", err)
	}
}

// TS-05-6: Server exits with a non-zero exit code and logs a fatal error when
// WORKSPACE_ROOT cannot be created due to insufficient permissions.
// Requirement: 05-REQ-2.2
func TestEnsureWorkspaceRoot_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission test not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("cannot test permission denial as root")
	}

	// Create a parent directory with no write permissions.
	parent := filepath.Join(t.TempDir(), "no-write")
	if err := os.Mkdir(parent, 0o555); err != nil {
		t.Fatalf("failed to create unwritable parent: %v", err)
	}
	t.Cleanup(func() {
		// Restore permissions so cleanup can remove the directory.
		os.Chmod(parent, 0o755)
	})

	target := filepath.Join(parent, "workspaces")
	err := EnsureWorkspaceRoot(target)
	if err == nil {
		t.Fatal("EnsureWorkspaceRoot should return error when parent is unwritable")
	}
}
