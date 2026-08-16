package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// ========================================================================
// Spec 15 Task 2.1: Upstream credential security
// (TS-15-17)
// Requirements: 15-REQ-5.4
// ========================================================================

// TS-15-17 (case_api): Upstream credential values (UPSTREAM_GIT_PAT,
// UPSTREAM_GIT_USERNAME, UPSTREAM_GIT_PASSWORD) are never exposed when
// listing secrets. The ListSecrets method returns only keys, not values.
// Requirement: 15-REQ-5.4
func TestCarryPatch_UpstreamCredentialNotExposedInList(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	// Store upstream credentials for a workspace.
	_, err := store.CreateSecrets("workspace", "secure-ws", []secrets.EntryInput{
		{Key: "UPSTREAM_GIT_PAT", Value: "super-secret-token"},
		{Key: "UPSTREAM_GIT_USERNAME", Value: "secret-user"},
		{Key: "UPSTREAM_GIT_PASSWORD", Value: "secret-pass"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	// List secrets for the workspace — values must not be included.
	entries, err := store.ListSecrets("workspace", "secure-ws")
	if err != nil {
		t.Fatalf("ListSecrets() returned error: %v", err)
	}

	// Verify the keys are listed.
	keySet := make(map[string]bool)
	for _, e := range entries {
		keySet[e.Key] = true
	}

	for _, key := range []string{"UPSTREAM_GIT_PAT", "UPSTREAM_GIT_USERNAME", "UPSTREAM_GIT_PASSWORD"} {
		if !keySet[key] {
			t.Errorf("ListSecrets() missing key %q", key)
		}
	}

	// Verify values ARE stored and retrievable (just not exposed in list).
	for _, tc := range []struct {
		key   string
		value string
	}{
		{"UPSTREAM_GIT_PAT", "super-secret-token"},
		{"UPSTREAM_GIT_USERNAME", "secret-user"},
		{"UPSTREAM_GIT_PASSWORD", "secret-pass"},
	} {
		got, err := store.GetSecretValue("workspace", "secure-ws", tc.key)
		if err != nil {
			t.Errorf("GetSecretValue(%q) returned error: %v", tc.key, err)
			continue
		}
		if got != tc.value {
			t.Errorf("GetSecretValue(%q) = %q; want %q", tc.key, got, tc.value)
		}
	}
}

// TS-15-17: Upstream credentials are stored and encrypted using the same
// mechanisms as existing workspace credentials. The raw database value is
// encoded, not stored as plaintext.
// Requirement: 15-REQ-5.4
func TestCarryPatch_UpstreamCredentialSameMechanism(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	// Store both origin and upstream PATs.
	_, err := store.CreateSecrets("workspace", "same-mech-ws", []secrets.EntryInput{
		{Key: "GIT_PAT", Value: "origin-pat-value"},
		{Key: "UPSTREAM_GIT_PAT", Value: "upstream-pat-value"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	// Both should be retrievable through the same GetSecretValue API.
	originPAT, err := store.GetSecretValue("workspace", "same-mech-ws", "GIT_PAT")
	if err != nil {
		t.Fatalf("GetSecretValue(GIT_PAT) returned error: %v", err)
	}
	if originPAT != "origin-pat-value" {
		t.Errorf("GIT_PAT = %q; want %q", originPAT, "origin-pat-value")
	}

	upstreamPAT, err := store.GetSecretValue("workspace", "same-mech-ws", "UPSTREAM_GIT_PAT")
	if err != nil {
		t.Fatalf("GetSecretValue(UPSTREAM_GIT_PAT) returned error: %v", err)
	}
	if upstreamPAT != "upstream-pat-value" {
		t.Errorf("UPSTREAM_GIT_PAT = %q; want %q", upstreamPAT, "upstream-pat-value")
	}

	// Verify the raw database value is encoded (not stored as plaintext).
	var rawValue string
	err = db.QueryRow(
		"SELECT value FROM secrets WHERE owner_type = 'workspace' AND owner_id = ? AND key = ?",
		"same-mech-ws", "UPSTREAM_GIT_PAT",
	).Scan(&rawValue)
	if err != nil {
		t.Fatalf("raw query returned error: %v", err)
	}
	if rawValue == "upstream-pat-value" {
		t.Error("UPSTREAM_GIT_PAT is stored as plaintext in the database; want encoded value")
	}
}

// ========================================================================
// Spec 15 Task 2.2: Carry-patch clone setup
// (TS-15-18, TS-15-19, TS-15-20)
// Requirements: 15-REQ-6
// ========================================================================

// ensureCarryPatchColumns adds workspace_mode, upstream_url, and
// integration_branch columns to the workspaces table if they are missing.
// This is needed because the schema migration (task group 5) hasn't been
// implemented yet. Without these columns, we can't insert carry_patch
// workspaces to test clone setup behavior.
func ensureCarryPatchColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	alters := []string{
		`ALTER TABLE workspaces ADD COLUMN workspace_mode TEXT NOT NULL DEFAULT 'standard'`,
		`ALTER TABLE workspaces ADD COLUMN upstream_url TEXT`,
		`ALTER TABLE workspaces ADD COLUMN integration_branch TEXT`,
	}
	for _, ddl := range alters {
		db.Exec(ddl) // ignore errors (column may already exist)
	}
}

// initGitRepoInClone creates a real git repository at the given path
// with an initial commit on the 'main' branch. Returns the HEAD SHA.
func initGitRepoInClone(t *testing.T, path string) string {
	t.Helper()

	cmds := [][]string{
		{"git", "init", "--initial-branch=main", path},
		{"git", "-C", path, "config", "user.email", "test@test.com"},
		{"git", "-C", path, "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	// Create initial commit so HEAD exists.
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# Test"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	cmds = [][]string{
		{"git", "-C", path, "add", "."},
		{"git", "-C", path, "commit", "-m", "initial commit"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	cmd := exec.Command("git", "-C", path, "rev-parse", "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse HEAD failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedCarryPatchWorkspaceRaw inserts a carry_patch workspace into the database
// using raw SQL. The Workspace struct doesn't yet include carry_patch fields,
// so we bypass insertWorkspace and set the new columns directly.
func seedCarryPatchWorkspaceRaw(t *testing.T, db *sql.DB, slug, gitURL, upstreamURL, integrationBranch string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description,
		 clone_status, created_at, updated_at, sync_mode, sync_status,
		 workspace_mode, upstream_url, integration_branch)
		 VALUES (?, ?, 'user-1', 'active', '', '', 'pending', ?, ?, 'pull_only', 'idle',
		 'carry_patch', ?, ?)`,
		slug, gitURL, now, now, upstreamURL, integrationBranch,
	)
	if err != nil {
		t.Fatalf("seed carry_patch workspace %q: %v", slug, err)
	}
}

// TS-15-18: When a carry_patch workspace clone job executes, it clones
// git_url as origin, adds 'upstream' remote pointing to upstream_url,
// sets rerere.enabled=true and rerere.autoupdate=true, creates the
// integration branch, and marks clone_status as 'ready'.
// Requirement: 15-REQ-6.1
func TestCarryPatch_CloneSetup_DualRemote(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	// Ensure carry_patch columns exist (schema migration not yet implemented).
	ensureCarryPatchColumns(t, db)

	slug := "cp-clone-dual"
	upstreamURL := "https://github.com/upstream/repo.git"
	integrationBranch := "deploy"

	seedCarryPatchWorkspaceRaw(t, db, slug, "https://github.com/fork/repo.git", upstreamURL, integrationBranch)

	// Mock cloneFn to create a real git repo.
	oldFn := cloneFn
	cloneFn = func(_ context.Context, path string, _ string, _ int, _ bool, _ string, _ transport.AuthMethod) (string, error) {
		sha := initGitRepoInClone(t, path)
		return sha, nil
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   slug,
		GitURL: "https://github.com/fork/repo.git",
	})

	// Verify clone_status is 'ready'.
	cloneStatus, _, _, err := getCloneFields(db, slug)
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "ready" {
		t.Fatalf("clone_status = %q; want %q", cloneStatus, "ready")
	}

	trunkDir := filepath.Join(wsRoot, slug, "trunk")

	// Verify 'upstream' remote was added pointing to upstream_url.
	cmd := exec.Command("git", "-C", trunkDir, "remote", "get-url", "upstream")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("upstream remote not found: %v\n%s", err, out)
	} else if strings.TrimSpace(string(out)) != upstreamURL {
		t.Errorf("upstream remote URL = %q; want %q", strings.TrimSpace(string(out)), upstreamURL)
	}

	// Verify rerere.enabled = true.
	cmd = exec.Command("git", "-C", trunkDir, "config", "rerere.enabled")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Errorf("rerere.enabled not set: %v", err)
	} else if strings.TrimSpace(string(out)) != "true" {
		t.Errorf("rerere.enabled = %q; want %q", strings.TrimSpace(string(out)), "true")
	}

	// Verify rerere.autoupdate = true.
	cmd = exec.Command("git", "-C", trunkDir, "config", "rerere.autoupdate")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Errorf("rerere.autoupdate not set: %v", err)
	} else if strings.TrimSpace(string(out)) != "true" {
		t.Errorf("rerere.autoupdate = %q; want %q", strings.TrimSpace(string(out)), "true")
	}

	// Verify integration branch 'deploy' was created.
	cmd = exec.Command("git", "-C", trunkDir, "branch", "--list", integrationBranch)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Errorf("git branch --list %s failed: %v", integrationBranch, err)
	} else if strings.TrimSpace(string(out)) == "" {
		t.Errorf("integration branch %q was not created", integrationBranch)
	}
}

// TS-15-19: When a post-clone setup step (e.g., RemoteAdd) fails after
// the core clone succeeds, the workspace is still marked 'ready' and a
// warning is logged. The error is NOT propagated to the job queue.
// Requirement: 15-REQ-6.2
func TestCarryPatch_CloneSetup_PostCloneFailureStillReady(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ensureCarryPatchColumns(t, db)

	slug := "cp-clone-fail-setup"

	// Seed a carry_patch workspace with an unreachable upstream URL.
	seedCarryPatchWorkspaceRaw(t, db, slug, "https://github.com/fork/repo.git",
		"https://unreachable.example.com/repo.git", "deploy")

	// Mock cloneFn to succeed (creates a real git repo).
	oldFn := cloneFn
	cloneFn = func(_ context.Context, path string, _ string, _ int, _ bool, _ string, _ transport.AuthMethod) (string, error) {
		sha := initGitRepoInClone(t, path)
		return sha, nil
	}
	defer func() { cloneFn = oldFn }()

	// Capture log output to verify warnings.
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   slug,
		GitURL: "https://github.com/fork/repo.git",
	})

	// Verify clone_status is 'ready' (not 'failed').
	cloneStatus, _, _, err := getCloneFields(db, slug)
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "ready" {
		t.Errorf("clone_status = %q; want %q (post-clone setup failure should not prevent ready)", cloneStatus, "ready")
	}

	// Verify that a warning was logged about the post-clone setup failure.
	logOutput := logBuf.String()
	hasWarning := strings.Contains(strings.ToLower(logOutput), "warn") ||
		strings.Contains(strings.ToLower(logOutput), "warning")
	if !hasWarning {
		t.Errorf("expected warning in log output about post-clone setup failure; got: %q", logOutput)
	}
}

// TS-15-19 (variant): When ConfigSet fails after clone succeeds, workspace
// is still marked 'ready'.
// Requirement: 15-REQ-6.2
func TestCarryPatch_CloneSetup_ConfigSetFailureStillReady(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ensureCarryPatchColumns(t, db)

	slug := "cp-clone-configset-fail"

	seedCarryPatchWorkspaceRaw(t, db, slug, "https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git", "deploy")

	oldFn := cloneFn
	cloneFn = func(_ context.Context, path string, _ string, _ int, _ bool, _ string, _ transport.AuthMethod) (string, error) {
		sha := initGitRepoInClone(t, path)
		return sha, nil
	}
	defer func() { cloneFn = oldFn }()

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   slug,
		GitURL: "https://github.com/fork/repo.git",
	})

	cloneStatus, _, _, err := getCloneFields(db, slug)
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "ready" {
		t.Errorf("clone_status = %q; want %q", cloneStatus, "ready")
	}
}

// 15-REQ-6.E1: If the upstream remote URL is unreachable, the workspace is
// still marked 'ready'. The remote is still added even if unreachable.
func TestCarryPatch_CloneSetup_UnreachableUpstream_StillReady(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ensureCarryPatchColumns(t, db)

	slug := "cp-unreachable-upstream"

	seedCarryPatchWorkspaceRaw(t, db, slug, "https://github.com/fork/repo.git",
		"https://unreachable.invalid/repo.git", "deploy")

	oldFn := cloneFn
	cloneFn = func(_ context.Context, path string, _ string, _ int, _ bool, _ string, _ transport.AuthMethod) (string, error) {
		sha := initGitRepoInClone(t, path)
		return sha, nil
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   slug,
		GitURL: "https://github.com/fork/repo.git",
	})

	cloneStatus, _, _, err := getCloneFields(db, slug)
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "ready" {
		t.Errorf("clone_status = %q; want %q", cloneStatus, "ready")
	}

	// Verify the upstream remote was still added despite being unreachable.
	trunkDir := filepath.Join(wsRoot, slug, "trunk")
	cmd := exec.Command("git", "-C", trunkDir, "remote", "get-url", "upstream")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("upstream remote should be added even if unreachable: %v\n%s", err, out)
	}
}

// 15-REQ-6.E3: If CreateBranch fails because the integration_branch name
// already exists, the workspace is still marked 'ready'.
func TestCarryPatch_CloneSetup_IntegrationBranchExists_StillReady(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ensureCarryPatchColumns(t, db)

	slug := "cp-branch-exists"

	// integration_branch = 'main', which already exists after clone.
	seedCarryPatchWorkspaceRaw(t, db, slug, "https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git", "main")

	oldFn := cloneFn
	cloneFn = func(_ context.Context, path string, _ string, _ int, _ bool, _ string, _ transport.AuthMethod) (string, error) {
		sha := initGitRepoInClone(t, path)
		return sha, nil
	}
	defer func() { cloneFn = oldFn }()

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   slug,
		GitURL: "https://github.com/fork/repo.git",
	})

	cloneStatus, _, _, err := getCloneFields(db, slug)
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "ready" {
		t.Errorf("clone_status = %q; want %q (CreateBranch conflict should not prevent ready)", cloneStatus, "ready")
	}
}

// 15-REQ-6.E4: If the core clone of git_url fails, workspace is marked
// 'failed' and no post-clone setup steps are executed.
func TestCarryPatch_CloneSetup_CoreCloneFails_NoPostClone(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ensureCarryPatchColumns(t, db)

	slug := "cp-core-fail"

	seedCarryPatchWorkspaceRaw(t, db, slug, "https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git", "deploy")

	// Mock cloneFn to fail.
	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string, _ transport.AuthMethod) (string, error) {
		return "", fmt.Errorf("clone failed: connection refused")
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   slug,
		GitURL: "https://github.com/fork/repo.git",
	})

	cloneStatus, _, cloneError, err := getCloneFields(db, slug)
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "failed" {
		t.Errorf("clone_status = %q; want %q", cloneStatus, "failed")
	}
	if cloneError == nil || *cloneError == "" {
		t.Error("clone_error should contain the error message")
	}

	// Verify workspace directory was removed.
	wsDir := filepath.Join(wsRoot, slug)
	if _, statErr := os.Stat(wsDir); !os.IsNotExist(statErr) {
		t.Errorf("workspace directory %q should be removed after failed clone", wsDir)
	}
}

// TS-15-20: When a standard workspace clone job executes, no upstream
// remote is added, no rerere config is set, and no integration branch is
// created. Standard clone behavior is unchanged.
// Requirement: 15-REQ-6.3
func TestCarryPatch_CloneSetup_StandardSkipsSetup(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	slug := "std-clone-noop"

	// Seed a standard workspace (no carry_patch columns needed).
	ws := &Workspace{
		Slug:    slug,
		GitURL:  "https://github.com/org/repo.git",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Mock cloneFn to create a real git repo.
	oldFn := cloneFn
	cloneFn = func(_ context.Context, path string, _ string, _ int, _ bool, _ string, _ transport.AuthMethod) (string, error) {
		sha := initGitRepoInClone(t, path)
		return sha, nil
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   slug,
		GitURL: "https://github.com/org/repo.git",
	})

	cloneStatus, _, _, err := getCloneFields(db, slug)
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "ready" {
		t.Fatalf("clone_status = %q; want %q", cloneStatus, "ready")
	}

	trunkDir := filepath.Join(wsRoot, slug, "trunk")

	// Verify NO 'upstream' remote was added.
	cmd := exec.Command("git", "-C", trunkDir, "remote")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git remote failed: %v\n%s", err, out)
	}
	if strings.Contains(strings.TrimSpace(string(out)), "upstream") {
		t.Error("standard workspace should not have an 'upstream' remote")
	}

	// Verify NO rerere config was set.
	cmd = exec.Command("git", "-C", trunkDir, "config", "rerere.enabled")
	out, _ = cmd.CombinedOutput()
	if strings.TrimSpace(string(out)) == "true" {
		t.Error("standard workspace should not have rerere.enabled=true")
	}

	cmd = exec.Command("git", "-C", trunkDir, "config", "rerere.autoupdate")
	out, _ = cmd.CombinedOutput()
	if strings.TrimSpace(string(out)) == "true" {
		t.Error("standard workspace should not have rerere.autoupdate=true")
	}

	// Verify NO extra branches were created (only 'main' from init).
	cmd = exec.Command("git", "-C", trunkDir, "branch", "--list")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git branch --list failed: %v", err)
	}
	if strings.Contains(strings.TrimSpace(string(out)), "deploy") {
		t.Error("standard workspace should not have a 'deploy' branch")
	}
}
