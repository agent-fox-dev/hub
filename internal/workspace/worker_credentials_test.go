package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// ========================================================================
// Spec 09 Task 2.2: Clone worker credential lookup tests
// (TS-09-23, TS-09-24, TS-09-25, TS-09-26)
// Requirements: 09-REQ-6.1, 09-REQ-6.2, 09-REQ-6.3, 09-REQ-6.4
// ========================================================================

// TS-09-23: Clone worker calls GetSecretValue for GIT_PAT first and passes
// BasicAuth with Username='x-token-auth' to CloneFuncType when PAT is found.
// Requirement: 09-REQ-6.1
func TestWorkerCredential_PATLookup(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	// Store a PAT secret for the workspace.
	_, err := store.CreateSecrets("workspace", "my-ws", []secrets.EntryInput{
		{Key: "GIT_PAT", Value: "ghp_abc123"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	// Call resolveCloneAuth — should return BasicAuth with x-token-auth.
	auth, err := resolveCloneAuth(store, "my-ws")
	if err != nil {
		t.Fatalf("resolveCloneAuth() returned error: %v", err)
	}

	if auth == nil {
		t.Fatal("resolveCloneAuth() returned nil auth; want BasicAuth with PAT")
	}

	basicAuth, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", auth)
	}
	if basicAuth.Username != "x-token-auth" {
		t.Errorf("auth.Username = %q; want %q", basicAuth.Username, "x-token-auth")
	}
	if basicAuth.Password != "ghp_abc123" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "ghp_abc123")
	}
}

// TS-09-23 (variant): Verify that PAT lookup is attempted before
// username/password — even when both exist, PAT takes precedence.
// Requirement: 09-REQ-6.1
func TestWorkerCredential_PATTakesPrecedence(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	// Store both PAT and username/password.
	_, err := store.CreateSecrets("workspace", "my-ws", []secrets.EntryInput{
		{Key: "GIT_PAT", Value: "ghp_priority"},
		{Key: "GIT_USERNAME", Value: "alice"},
		{Key: "GIT_PASSWORD", Value: "s3cr3t"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	auth, err := resolveCloneAuth(store, "my-ws")
	if err != nil {
		t.Fatalf("resolveCloneAuth() returned error: %v", err)
	}

	if auth == nil {
		t.Fatal("resolveCloneAuth() returned nil auth; want BasicAuth with PAT")
	}

	basicAuth, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", auth)
	}

	// PAT should take precedence — Username must be x-token-auth, not alice.
	if basicAuth.Username != "x-token-auth" {
		t.Errorf("auth.Username = %q; want %q (PAT takes precedence)", basicAuth.Username, "x-token-auth")
	}
	if basicAuth.Password != "ghp_priority" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "ghp_priority")
	}
}

// TS-09-24: Clone worker constructs BasicAuth from GIT_USERNAME and
// GIT_PASSWORD and passes it to CloneFuncType when GIT_PAT is not found
// but both username and password are found.
// Requirement: 09-REQ-6.2
func TestWorkerCredential_UsernamePassword(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	// Store only username/password (no PAT).
	_, err := store.CreateSecrets("workspace", "my-ws", []secrets.EntryInput{
		{Key: "GIT_USERNAME", Value: "alice"},
		{Key: "GIT_PASSWORD", Value: "s3cr3t"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	auth, err := resolveCloneAuth(store, "my-ws")
	if err != nil {
		t.Fatalf("resolveCloneAuth() returned error: %v", err)
	}

	if auth == nil {
		t.Fatal("resolveCloneAuth() returned nil auth; want BasicAuth with username/password")
	}

	basicAuth, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", auth)
	}
	if basicAuth.Username != "alice" {
		t.Errorf("auth.Username = %q; want %q", basicAuth.Username, "alice")
	}
	if basicAuth.Password != "s3cr3t" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "s3cr3t")
	}
}

// TS-09-25: Clone worker passes nil as the auth argument to CloneFuncType
// when no credential secrets are found for the workspace.
// Requirement: 09-REQ-6.3
func TestWorkerCredential_NoCredentials_NilAuth(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	// No secrets exist for workspace 'public-ws'.
	auth, err := resolveCloneAuth(store, "public-ws")
	if err != nil {
		t.Fatalf("resolveCloneAuth() returned error: %v", err)
	}

	if auth != nil {
		t.Errorf("resolveCloneAuth() returned %v; want nil (no credentials = public repo)", auth)
	}
}

// TS-09-26: CloneFuncType signature includes the auth transport.AuthMethod
// parameter and defaultCloneFn passes it to PlainCloneContext CloneOptions.Auth.
//
// This test verifies that processCloneJob passes the resolved auth to cloneFn.
// Until the implementation extends CloneFuncType with an auth parameter (task
// group 6), the clone is invoked without auth — so this test fails because
// the spy records nil auth even when credentials exist.
// Requirement: 09-REQ-6.4
func TestWorkerCredential_ProcessCloneJob_PassesAuth(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	// Seed a workspace record.
	ws := &Workspace{
		Slug:    "auth-ws",
		GitURL:  "https://github.com/acme/private",
		OwnerID: "clone-user-001",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Store a PAT credential for this workspace.
	store := secrets.NewStore(db)
	_, err := store.CreateSecrets("workspace", "auth-ws", []secrets.EntryInput{
		{Key: "GIT_PAT", Value: "ghp_abc123"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	// Install a clone function spy that records the call.
	var cloneCalled bool
	var capturedAuth transport.AuthMethod
	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string, auth transport.AuthMethod) (string, error) {
		cloneCalled = true
		capturedAuth = auth
		return "abcdef1234567890abcdef1234567890abcdef12", nil
	}
	defer func() { cloneFn = oldFn }()

	// Process the clone job.
	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   "auth-ws",
		GitURL: "https://github.com/acme/private",
	})

	if !cloneCalled {
		t.Fatal("clone function was not called")
	}

	// Verify that processCloneJob passed the correct auth to cloneFn.
	if capturedAuth == nil {
		t.Fatal("cloneFn was called with nil auth; want BasicAuth with PAT credentials")
	}

	basicAuth, ok := capturedAuth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", capturedAuth)
	}
	if basicAuth.Username != "x-token-auth" {
		t.Errorf("auth.Username = %q; want %q", basicAuth.Username, "x-token-auth")
	}
	if basicAuth.Password != "ghp_abc123" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "ghp_abc123")
	}
}

// 09-REQ-6.E1: If GetSecretValue returns an error other than NotFoundError
// when looking up GIT_PAT, the clone worker should fail the clone job.
func TestWorkerCredential_GetSecretValueError(t *testing.T) {
	db := openTestDB(t)

	// Drop the secrets table to force GetSecretValue errors.
	_, err := db.Exec("DROP TABLE secrets")
	if err != nil {
		t.Fatalf("failed to drop secrets table: %v", err)
	}

	store := secrets.NewStore(db)

	// resolveCloneAuth should return an error (not silently proceed).
	_, err = resolveCloneAuth(store, "my-ws")
	if err == nil {
		t.Error("resolveCloneAuth() returned nil error; want non-nil error when GetSecretValue fails")
	}
}

// 09-REQ-6.E2: If GIT_USERNAME is found but GIT_PASSWORD returns NotFoundError
// (inconsistent secret state), the clone worker should log a warning and
// either proceed without auth or fail the clone.
func TestWorkerCredential_InconsistentCredentials_UsernameOnly(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	// Store only GIT_USERNAME (no GIT_PASSWORD).
	_, err := store.CreateSecrets("workspace", "my-ws", []secrets.EntryInput{
		{Key: "GIT_USERNAME", Value: "alice"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	// resolveCloneAuth should handle the inconsistency — either return nil
	// auth (treat as public) or return an error. The spec says log a
	// warning and either proceed without auth or fail.
	auth, err := resolveCloneAuth(store, "my-ws")
	if err != nil {
		// Acceptable: clone marked as failed.
		return
	}
	if auth != nil {
		t.Error("resolveCloneAuth() returned non-nil auth for incomplete credentials; want nil or error")
	}
}

// 09-REQ-6.E4: Reclone on workspace reactivation follows the same credential
// lookup sequence as the initial clone.
func TestWorkerCredential_Reclone_UsesCredentials(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	// Seed an archived workspace that was previously created with a PAT.
	ws := &Workspace{
		Slug:        "reclone-ws",
		GitURL:      "https://github.com/acme/private",
		OwnerID:     "clone-user-001",
		Status:      "active",
		CloneStatus: "pending",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Store a PAT credential (persists across archive/reactivate).
	store := secrets.NewStore(db)
	_, err := store.CreateSecrets("workspace", "reclone-ws", []secrets.EntryInput{
		{Key: "GIT_PAT", Value: "ghp_reclone_token"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	// Install a clone function spy.
	var cloneCalled bool
	var capturedAuth transport.AuthMethod
	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string, auth transport.AuthMethod) (string, error) {
		cloneCalled = true
		capturedAuth = auth
		return "abcdef1234567890abcdef1234567890abcdef12", nil
	}
	defer func() { cloneFn = oldFn }()

	// Simulate reclone by processing the clone job (same code path).
	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   "reclone-ws",
		GitURL: "https://github.com/acme/private",
	})

	if !cloneCalled {
		t.Fatal("clone function was not called for reclone")
	}

	// Verify that processCloneJob passed the correct auth to cloneFn.
	if capturedAuth == nil {
		t.Fatal("cloneFn was called with nil auth; credentials should persist across reclone")
	}

	basicAuth, ok := capturedAuth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", capturedAuth)
	}
	if basicAuth.Username != "x-token-auth" {
		t.Errorf("auth.Username = %q; want %q", basicAuth.Username, "x-token-auth")
	}
	if basicAuth.Password != "ghp_reclone_token" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "ghp_reclone_token")
	}
}

// 09-REQ-6.E3: If the clone fails with an authentication error despite
// credentials being present, the clone worker marks the job as failed.
func TestWorkerCredential_CloneAuthError(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ws := &Workspace{
		Slug:    "auth-fail-ws",
		GitURL:  "https://github.com/acme/private",
		OwnerID: "clone-user-001",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Store credentials that will be "revoked" (simulated by clone failure).
	store := secrets.NewStore(db)
	_, err := store.CreateSecrets("workspace", "auth-fail-ws", []secrets.EntryInput{
		{Key: "GIT_PAT", Value: "ghp_revoked_token"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	// Clone function returns an auth error.
	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string, _ transport.AuthMethod) (string, error) {
		return "", transport.ErrAuthenticationRequired
	}
	defer func() { cloneFn = oldFn }()

	processCloneJob(context.Background(), db, wsRoot, CloneJob{
		Slug:   "auth-fail-ws",
		GitURL: "https://github.com/acme/private",
	})

	// Verify workspace is marked as failed.
	cloneStatus, _, cloneError, err := getCloneFields(db, "auth-fail-ws")
	if err != nil {
		t.Fatalf("getCloneFields() returned error: %v", err)
	}
	if cloneStatus != "failed" {
		t.Errorf("clone_status = %q; want %q", cloneStatus, "failed")
	}
	if cloneError == nil || *cloneError == "" {
		t.Error("clone_error should contain the authentication error message")
	}

	// Workspace directory should be removed on failure.
	wsDir := filepath.Join(wsRoot, "auth-fail-ws")
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("workspace directory %q should be removed after auth failure", wsDir)
	}
}
