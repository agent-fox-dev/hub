package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// ========================================================================
// Spec 05 Task 2.2: Clone job execution tests
// (TS-05-13, TS-05-14, TS-05-15, TS-05-16)
// ========================================================================

// createBareRepo creates a local bare git repository with a single initial
// commit, suitable for use as a clone source in tests. Returns the path to
// the bare repo directory.
func createBareRepo(t *testing.T) string {
	t.Helper()

	// Create a non-bare repo first to make a commit, then clone bare.
	srcDir := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("create src dir: %v", err)
	}

	cmds := [][]string{
		{"git", "init", "--initial-branch=main", srcDir},
		{"git", "-C", srcDir, "config", "user.email", "test@test.com"},
		{"git", "-C", srcDir, "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	// Create a file and commit.
	if err := os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("# Test"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	cmds = [][]string{
		{"git", "-C", srcDir, "add", "."},
		{"git", "-C", srcDir, "commit", "-m", "initial commit"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	// Clone to bare repo.
	bareDir := filepath.Join(t.TempDir(), "upstream.git")
	cmd := exec.Command("git", "clone", "--bare", srcDir, bareDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare failed: %v\n%s", err, out)
	}

	return bareDir
}

// createBareRepoWithBranch creates a bare repo with both a main branch and
// the specified feature branch. Returns the bare repo path.
func createBareRepoWithBranch(t *testing.T, branchName string) string {
	t.Helper()

	srcDir := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("create src dir: %v", err)
	}

	cmds := [][]string{
		{"git", "init", "--initial-branch=main", srcDir},
		{"git", "-C", srcDir, "config", "user.email", "test@test.com"},
		{"git", "-C", srcDir, "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	// Initial commit on main.
	if err := os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("# Test"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	cmds = [][]string{
		{"git", "-C", srcDir, "add", "."},
		{"git", "-C", srcDir, "commit", "-m", "initial commit"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	// Create feature branch with a separate commit.
	cmd := exec.Command("git", "-C", srcDir, "checkout", "-b", branchName)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b %s failed: %v\n%s", branchName, err, out)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "feature.txt"), []byte("feature"), 0o644); err != nil {
		t.Fatalf("write feature.txt: %v", err)
	}
	cmds = [][]string{
		{"git", "-C", srcDir, "add", "."},
		{"git", "-C", srcDir, "commit", "-m", "feature commit"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	// Clone to bare repo.
	bareDir := filepath.Join(t.TempDir(), "upstream.git")
	cmd = exec.Command("git", "clone", "--bare", srcDir, bareDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare failed: %v\n%s", err, out)
	}

	return bareDir
}

// TS-05-13 (TS-05-11 in task mapping): Worker sets clone_status='cloning',
// creates workspace dir, calls PlainCloneContext with Depth=1; on success
// sets clone_status='ready' and records head_sha.
// Requirement: 05-REQ-4.2, 05-REQ-4.3
func TestCloneWorker_SuccessfulClone(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()
	repoURL := createBareRepo(t)

	// Seed a workspace record.
	ws := &Workspace{
		Slug:    "test-ws-success",
		GitURL:  repoURL,
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Track clone function calls.
	var called int32
	var capturedDepth int
	fakeHeadSHA := "abcdef1234567890abcdef1234567890abcdef12"

	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, depth int, _ bool, _ string) (string, error) {
		atomic.AddInt32(&called, 1)
		capturedDepth = depth
		return fakeHeadSHA, nil
	}
	defer func() { cloneFn = oldFn }()

	// Process the clone job.
	job := CloneJob{Slug: ws.Slug, GitURL: repoURL, Branch: nil}
	processCloneJob(context.Background(), db, wsRoot, job)

	// Verify clone function was called.
	if atomic.LoadInt32(&called) == 0 {
		t.Fatal("clone function was not called by processCloneJob")
	}

	// Verify Depth=0 (full clone for git serving).
	if capturedDepth != 0 {
		t.Errorf("clone depth = %d; want 0 (full clone)", capturedDepth)
	}

	// Verify workspace directory was created with trunk subdirectory.
	trunkDir := filepath.Join(wsRoot, ws.Slug, "trunk")
	if _, err := os.Stat(trunkDir); os.IsNotExist(err) {
		t.Errorf("trunk directory %q should exist after successful clone", trunkDir)
	}

	// Verify DB state: clone_status=ready, head_sha set, clone_error null.
	cloneStatus, headSHA, cloneError, err := getCloneFields(db, ws.Slug)
	if err != nil {
		t.Fatalf("getCloneFields(%q): %v", ws.Slug, err)
	}
	if cloneStatus != "ready" {
		t.Errorf("clone_status = %q; want %q", cloneStatus, "ready")
	}
	if headSHA == nil {
		t.Error("head_sha is nil; want non-null 40-character hex string")
	} else if len(*headSHA) != 40 {
		t.Errorf("head_sha length = %d; want 40", len(*headSHA))
	}
	if cloneError != nil {
		t.Errorf("clone_error = %q; want nil", *cloneError)
	}
}

// TS-05-12: On successful clone, head_sha is recorded as a 40-character hex
// string and clone_error is cleared to NULL.
// Requirement: 05-REQ-4.3
func TestCloneWorker_HeadSHARecorded(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ws := &Workspace{
		Slug:    "test-ws-sha",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	expectedSHA := "0123456789abcdef0123456789abcdef01234567"
	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string) (string, error) {
		return expectedSHA, nil
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   ws.Slug,
		GitURL: ws.GitURL,
	})

	// Verify head_sha matches the SHA returned by the clone function.
	_, headSHA, cloneError, err := getCloneFields(db, ws.Slug)
	if err != nil {
		t.Fatalf("getCloneFields(%q): %v", ws.Slug, err)
	}
	if headSHA == nil {
		t.Fatal("head_sha is nil; want the SHA from the clone")
	}
	if *headSHA != expectedSHA {
		t.Errorf("head_sha = %q; want %q", *headSHA, expectedSHA)
	}
	if cloneError != nil {
		t.Errorf("clone_error = %q; want nil (cleared on success)", *cloneError)
	}
}

// TS-05-15 (TS-05-14 in task mapping): Clone with explicit branch sets
// SingleBranch=true and ReferenceName=refs/heads/<branch>.
// Requirement: 05-REQ-4.6
func TestCloneWorker_BranchCloneOptions(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	branch := "feature/my-branch"
	ws := &Workspace{
		Slug:    "test-ws-branch",
		GitURL:  "https://github.com/example/repo.git",
		Branch:  &branch,
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Capture clone options.
	var capturedSingleBranch bool
	var capturedRefName string
	var called bool

	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, singleBranch bool, refName string) (string, error) {
		called = true
		capturedSingleBranch = singleBranch
		capturedRefName = refName
		return "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", nil
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   ws.Slug,
		GitURL: ws.GitURL,
		Branch: &branch,
	})

	if !called {
		t.Fatal("clone function was not called")
	}
	if !capturedSingleBranch {
		t.Error("SingleBranch = false; want true for branch-specific clone")
	}
	if capturedRefName != "refs/heads/feature/my-branch" {
		t.Errorf("ReferenceName = %q; want %q",
			capturedRefName, "refs/heads/feature/my-branch")
	}
}

// TS-05-16 (TS-05-14 in task mapping): Clone without branch (null) omits
// ReferenceName from CloneOptions to clone the remote's default branch.
// Requirement: 05-REQ-4.7
func TestCloneWorker_DefaultBranchCloneOptions(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ws := &Workspace{
		Slug:    "test-ws-default-branch",
		GitURL:  "https://github.com/example/repo.git",
		Branch:  nil, // null = default branch
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	var capturedSingleBranch bool
	var capturedRefName string
	var called bool

	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, singleBranch bool, refName string) (string, error) {
		called = true
		capturedSingleBranch = singleBranch
		capturedRefName = refName
		return "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", nil
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   ws.Slug,
		GitURL: ws.GitURL,
		Branch: nil,
	})

	if !called {
		t.Fatal("clone function was not called")
	}
	// When branch is null, ReferenceName should be empty (zero value) and
	// SingleBranch should be false, allowing the remote's default branch.
	if capturedRefName != "" {
		t.Errorf("ReferenceName = %q; want empty (omitted for default branch)",
			capturedRefName)
	}
	if capturedSingleBranch {
		t.Error("SingleBranch = true; want false when cloning default branch")
	}
}

// TS-05-13 (TS-05-15 in task mapping): On clone failure, clone_status='failed',
// clone_error set to error message, partial workspace directory removed.
// Requirement: 05-REQ-4.4
func TestCloneWorker_FailedClone(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ws := &Workspace{
		Slug:    "test-ws-fail",
		GitURL:  "https://invalid.example.com/nonexistent.git",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Mock clone function that returns an error.
	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string) (string, error) {
		return "", fmt.Errorf("repository not found")
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   ws.Slug,
		GitURL: ws.GitURL,
	})

	// Verify DB state: clone_status=failed, clone_error set.
	cloneStatus, _, cloneError, err := getCloneFields(db, ws.Slug)
	if err != nil {
		t.Fatalf("getCloneFields(%q): %v", ws.Slug, err)
	}
	if cloneStatus != "failed" {
		t.Errorf("clone_status = %q; want %q", cloneStatus, "failed")
	}
	if cloneError == nil {
		t.Error("clone_error is nil; want non-null error message")
	} else if *cloneError == "" {
		t.Error("clone_error is empty; want non-empty error message")
	}

	// Verify workspace directory was removed.
	wsDir := filepath.Join(wsRoot, ws.Slug)
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("workspace directory %q should not exist after failed clone", wsDir)
	}
}

// TestCloneWorker_FailedClone_RemovesPartialDir verifies that if the
// workspace directory was partially created before the clone error, it is
// removed using os.RemoveAll.
// Requirement: 05-REQ-4.E3
func TestCloneWorker_FailedClone_RemovesPartialDir(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ws := &Workspace{
		Slug:    "test-ws-partial",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Mock clone function that creates partial files then fails.
	oldFn := cloneFn
	cloneFn = func(_ context.Context, path string, _ string, _ int, _ bool, _ string) (string, error) {
		// Simulate partial clone: create some files then error.
		if err := os.MkdirAll(path, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(path, "partial.txt"), []byte("partial"), 0o644)
		}
		return "", fmt.Errorf("connection reset by peer")
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   ws.Slug,
		GitURL: ws.GitURL,
	})

	// Verify DB state: clone_status=failed, clone_error set.
	cloneStatus, _, cloneError, err := getCloneFields(db, ws.Slug)
	if err != nil {
		t.Fatalf("getCloneFields(%q): %v", ws.Slug, err)
	}
	if cloneStatus != "failed" {
		t.Errorf("clone_status = %q; want %q", cloneStatus, "failed")
	}
	if cloneError == nil || *cloneError == "" {
		t.Error("clone_error should contain the error message")
	}

	// The workspace directory (including any partial files) should be removed.
	wsDir := filepath.Join(wsRoot, ws.Slug)
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("workspace directory %q should be removed after failed clone", wsDir)
	}
	trunkDir := filepath.Join(wsRoot, ws.Slug, "trunk")
	if _, err := os.Stat(trunkDir); !os.IsNotExist(err) {
		t.Errorf("trunk directory %q should be removed after failed clone", trunkDir)
	}
}

// TestCloneWorker_FailedClone_UnreachableURL verifies that an unreachable
// git URL results in clone_status=failed with the error recorded.
// Requirement: 05-REQ-4.E4
func TestCloneWorker_FailedClone_UnreachableURL(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ws := &Workspace{
		Slug:    "test-ws-unreachable",
		GitURL:  "https://invalid.example.com/nonexistent.git",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string) (string, error) {
		return "", fmt.Errorf("unable to access 'https://invalid.example.com/nonexistent.git': Could not resolve host")
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   ws.Slug,
		GitURL: ws.GitURL,
	})

	cloneStatus, _, cloneError, err := getCloneFields(db, ws.Slug)
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "failed" {
		t.Errorf("clone_status = %q; want %q", cloneStatus, "failed")
	}
	if cloneError == nil || *cloneError == "" {
		t.Error("clone_error should contain the network error message")
	}
}

// TestCloneWorker_FailedClone_BranchNotFound verifies that a non-existent
// branch results in clone_status=failed with the error recorded.
// Requirement: 05-REQ-4.E5
func TestCloneWorker_FailedClone_BranchNotFound(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	branch := "nonexistent-branch"
	ws := &Workspace{
		Slug:    "test-ws-bad-branch",
		GitURL:  "https://github.com/example/repo.git",
		Branch:  &branch,
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string) (string, error) {
		return "", fmt.Errorf("reference not found")
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   ws.Slug,
		GitURL: ws.GitURL,
		Branch: &branch,
	})

	cloneStatus, _, cloneError, err := getCloneFields(db, ws.Slug)
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "failed" {
		t.Errorf("clone_status = %q; want %q", cloneStatus, "failed")
	}
	if cloneError == nil || *cloneError == "" {
		t.Error("clone_error should contain the branch-not-found error message")
	}
}

// TestCloneWorker_FailedClone_HeadError verifies that if repo.Head() returns
// an error after a successful clone, the worker sets clone_status=failed and
// removes the workspace directory.
// Requirement: 05-REQ-4.E6
func TestCloneWorker_FailedClone_HeadError(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ws := &Workspace{
		Slug:    "test-ws-head-err",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Mock clone function: clone "succeeds" but returns empty SHA (simulating
	// a Head() error in the real implementation).
	oldFn := cloneFn
	cloneFn = func(_ context.Context, path string, _ string, _ int, _ bool, _ string) (string, error) {
		// Create the directory to simulate the clone succeeded at the fs level.
		_ = os.MkdirAll(path, 0o755)
		// Return error to signal a Head() error.
		return "", fmt.Errorf("reference not found: HEAD")
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   ws.Slug,
		GitURL: ws.GitURL,
	})

	cloneStatus, _, cloneError, err := getCloneFields(db, ws.Slug)
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "failed" {
		t.Errorf("clone_status = %q; want %q", cloneStatus, "failed")
	}
	if cloneError == nil || *cloneError == "" {
		t.Error("clone_error should contain the Head() error message")
	}

	// Workspace directory should be removed on failure.
	wsDir := filepath.Join(wsRoot, ws.Slug)
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("workspace directory %q should be removed after Head() error", wsDir)
	}
}

// TS-05-14 (TS-05-16 in task mapping): Idempotency — if workspace directory
// already exists under WORKSPACE_ROOT, the clone job skips PlainCloneContext
// and sets clone_status to 'ready' directly.
// Requirement: 05-REQ-4.5
func TestCloneWorker_IdempotentSkip(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ws := &Workspace{
		Slug:    "pre-existing-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Pre-create the workspace directory to trigger idempotency path.
	wsDir := filepath.Join(wsRoot, ws.Slug)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("create pre-existing directory: %v", err)
	}

	// Clone function should NOT be called.
	var called bool
	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string) (string, error) {
		called = true
		return "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", nil
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   ws.Slug,
		GitURL: ws.GitURL,
	})

	// Clone function should not have been called (idempotency).
	if called {
		t.Error("clone function was called; should be skipped when workspace directory already exists")
	}

	// Clone status should still be set to ready.
	cloneStatus, _, _, err := getCloneFields(db, ws.Slug)
	if err != nil {
		t.Fatalf("getCloneFields(%q): %v", ws.Slug, err)
	}
	if cloneStatus != "ready" {
		t.Errorf("clone_status = %q; want %q (idempotent skip)", cloneStatus, "ready")
	}
}

// ========================================================================
// Clone status state machine tests (05-REQ-9)
// ========================================================================

// TestCloneWorker_StatusTransition_PendingToCloning verifies that the worker
// transitions clone_status from pending to cloning before starting the clone.
// Requirement: 05-REQ-9.1 (pending -> cloning transition)
func TestCloneWorker_StatusTransition_PendingToCloning(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ws := &Workspace{
		Slug:    "test-ws-transition",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Clone function checks that clone_status was set to "cloning" before
	// the clone function is called.
	var statusDuringClone string
	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string) (string, error) {
		// Read clone_status from DB during the clone operation.
		status, _, _, err := getCloneFields(db, ws.Slug)
		if err != nil {
			statusDuringClone = "error: " + err.Error()
		} else {
			statusDuringClone = status
		}
		return "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", nil
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   ws.Slug,
		GitURL: ws.GitURL,
	})

	if statusDuringClone != "cloning" {
		t.Errorf("clone_status during clone = %q; want %q (pending -> cloning transition)",
			statusDuringClone, "cloning")
	}
}

// TestCloneWorker_StatusTransition_CloningToReady verifies the full
// pending -> cloning -> ready transition on successful clone.
// Requirement: 05-REQ-9.1 (cloning -> ready transition)
func TestCloneWorker_StatusTransition_CloningToReady(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ws := &Workspace{
		Slug:    "test-ws-ready-transition",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string) (string, error) {
		return "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", nil
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   ws.Slug,
		GitURL: ws.GitURL,
	})

	cloneStatus, _, _, err := getCloneFields(db, ws.Slug)
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "ready" {
		t.Errorf("clone_status = %q; want %q after successful clone", cloneStatus, "ready")
	}
}

// TestCloneWorker_StatusTransition_CloningToFailed verifies the
// pending -> cloning -> failed transition on clone error.
// Requirement: 05-REQ-9.1 (cloning -> failed transition)
func TestCloneWorker_StatusTransition_CloningToFailed(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ws := &Workspace{
		Slug:    "test-ws-fail-transition",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string) (string, error) {
		return "", fmt.Errorf("clone failed")
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   ws.Slug,
		GitURL: ws.GitURL,
	})

	cloneStatus, _, _, err := getCloneFields(db, ws.Slug)
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "failed" {
		t.Errorf("clone_status = %q; want %q after failed clone", cloneStatus, "failed")
	}
}
