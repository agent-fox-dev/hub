package workspace

import (
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// ========================================================================
// Spec 15 Task 2.1: Upstream credential resolution
// (TS-15-14)
// Requirements: 15-REQ-5.1
// ========================================================================

// TS-15-14 (case_pat): When UPSTREAM_GIT_PAT is present in workspace
// secrets, resolveUpstreamAuth returns BasicAuth with Username='x-token-auth'
// and Password equal to the PAT value.
// Requirement: 15-REQ-5.1
func TestCarryPatch_ResolveUpstreamAuth_PATPresent(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	_, err := store.CreateSecrets("workspace", "up-ws", []secrets.EntryInput{
		{Key: "UPSTREAM_GIT_PAT", Value: "mytoken"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	auth, err := resolveUpstreamAuth(store, "up-ws")
	if err != nil {
		t.Fatalf("resolveUpstreamAuth() returned error: %v", err)
	}
	if auth == nil {
		t.Fatal("resolveUpstreamAuth() returned nil; want BasicAuth with PAT")
	}

	basicAuth, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", auth)
	}
	if basicAuth.Username != "x-token-auth" {
		t.Errorf("auth.Username = %q; want %q", basicAuth.Username, "x-token-auth")
	}
	if basicAuth.Password != "mytoken" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "mytoken")
	}
}

// TS-15-14 (case_userpass): When UPSTREAM_GIT_USERNAME and UPSTREAM_GIT_PASSWORD
// are set (without PAT), resolveUpstreamAuth returns BasicAuth with those values.
// Requirement: 15-REQ-5.1
func TestCarryPatch_ResolveUpstreamAuth_UsernamePassword(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	_, err := store.CreateSecrets("workspace", "up-ws2", []secrets.EntryInput{
		{Key: "UPSTREAM_GIT_USERNAME", Value: "user"},
		{Key: "UPSTREAM_GIT_PASSWORD", Value: "pass"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	auth, err := resolveUpstreamAuth(store, "up-ws2")
	if err != nil {
		t.Fatalf("resolveUpstreamAuth() returned error: %v", err)
	}
	if auth == nil {
		t.Fatal("resolveUpstreamAuth() returned nil; want BasicAuth with username/password")
	}

	basicAuth, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", auth)
	}
	if basicAuth.Username != "user" {
		t.Errorf("auth.Username = %q; want %q", basicAuth.Username, "user")
	}
	if basicAuth.Password != "pass" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "pass")
	}
}

// TS-15-14 (case_fallback): When no UPSTREAM_GIT_* secrets exist,
// resolveUpstreamAuth falls back to resolveCloneAuth.
// Requirement: 15-REQ-5.1
func TestCarryPatch_ResolveUpstreamAuth_FallbackToCloneAuth(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	// Store only an origin PAT (no UPSTREAM_ secrets).
	_, err := store.CreateSecrets("workspace", "up-ws3", []secrets.EntryInput{
		{Key: "GIT_PAT", Value: "origin-pat"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	auth, err := resolveUpstreamAuth(store, "up-ws3")
	if err != nil {
		t.Fatalf("resolveUpstreamAuth() returned error: %v", err)
	}

	// Should fall back to origin PAT.
	if auth == nil {
		t.Fatal("resolveUpstreamAuth() returned nil; want fallback to origin credentials")
	}

	basicAuth, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", auth)
	}
	if basicAuth.Username != "x-token-auth" {
		t.Errorf("auth.Username = %q; want %q (origin PAT fallback)", basicAuth.Username, "x-token-auth")
	}
	if basicAuth.Password != "origin-pat" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "origin-pat")
	}
}

// TS-15-14 (fallback, no creds at all): When no secrets exist at all,
// resolveUpstreamAuth returns nil (same as resolveCloneAuth with no creds).
// Requirement: 15-REQ-5.1
func TestCarryPatch_ResolveUpstreamAuth_FallbackNoCreds(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	auth, err := resolveUpstreamAuth(store, "no-creds-ws")
	if err != nil {
		t.Fatalf("resolveUpstreamAuth() returned error: %v", err)
	}

	// Both resolveUpstreamAuth and resolveCloneAuth should return nil.
	originAuth, originErr := resolveCloneAuth(store, "no-creds-ws")
	if originErr != nil {
		t.Fatalf("resolveCloneAuth() returned error: %v", originErr)
	}

	if auth != originAuth {
		t.Errorf("resolveUpstreamAuth() = %v; want %v (same as resolveCloneAuth)", auth, originAuth)
	}
}

// 15-REQ-5.E1: UPSTREAM_GIT_USERNAME is set but UPSTREAM_GIT_PASSWORD is
// absent. resolveUpstreamAuth skips username/password branch and falls
// back to resolveCloneAuth.
func TestCarryPatch_ResolveUpstreamAuth_UsernameOnly_Fallback(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	// Store only UPSTREAM_GIT_USERNAME (no password).
	_, err := store.CreateSecrets("workspace", "up-ws-nopass", []secrets.EntryInput{
		{Key: "UPSTREAM_GIT_USERNAME", Value: "user-only"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	// Also store an origin PAT so fallback has something to return.
	_, err = store.CreateSecrets("workspace", "up-ws-nopass", []secrets.EntryInput{
		{Key: "GIT_PAT", Value: "origin-fallback"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets(GIT_PAT) returned error: %v", err)
	}

	auth, err := resolveUpstreamAuth(store, "up-ws-nopass")
	if err != nil {
		t.Fatalf("resolveUpstreamAuth() returned error: %v", err)
	}

	// Should fall back to origin credentials, NOT use incomplete upstream creds.
	if auth == nil {
		t.Fatal("resolveUpstreamAuth() returned nil; want fallback to origin credentials")
	}

	basicAuth, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", auth)
	}
	if basicAuth.Username != "x-token-auth" {
		t.Errorf("auth.Username = %q; want %q (origin PAT fallback)", basicAuth.Username, "x-token-auth")
	}
	if basicAuth.Password != "origin-fallback" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "origin-fallback")
	}
}

// 15-REQ-5.E2: If the secrets store returns an error when looking up
// upstream credentials, resolveUpstreamAuth propagates the error rather
// than silently falling back.
func TestCarryPatch_ResolveUpstreamAuth_StoreError(t *testing.T) {
	db := openTestDB(t)

	// Drop the secrets table so GetSecretValue returns a DB error.
	_, err := db.Exec("DROP TABLE secrets")
	if err != nil {
		t.Fatalf("failed to drop secrets table: %v", err)
	}

	store := secrets.NewStore(db)

	_, err = resolveUpstreamAuth(store, "error-ws")
	if err == nil {
		t.Error("resolveUpstreamAuth() returned nil error; want error propagated from store")
	}
}

// Additional coverage: When both UPSTREAM_GIT_PAT and UPSTREAM_GIT_USERNAME/
// PASSWORD exist, PAT takes precedence.
func TestCarryPatch_ResolveUpstreamAuth_PATTakesPrecedence(t *testing.T) {
	db := openTestDB(t)
	store := secrets.NewStore(db)

	_, err := store.CreateSecrets("workspace", "up-ws-both", []secrets.EntryInput{
		{Key: "UPSTREAM_GIT_PAT", Value: "upstream-pat"},
		{Key: "UPSTREAM_GIT_USERNAME", Value: "user"},
		{Key: "UPSTREAM_GIT_PASSWORD", Value: "pass"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	auth, err := resolveUpstreamAuth(store, "up-ws-both")
	if err != nil {
		t.Fatalf("resolveUpstreamAuth() returned error: %v", err)
	}
	if auth == nil {
		t.Fatal("resolveUpstreamAuth() returned nil; want BasicAuth with PAT")
	}

	basicAuth, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", auth)
	}
	if basicAuth.Username != "x-token-auth" {
		t.Errorf("auth.Username = %q; want %q (PAT takes precedence)", basicAuth.Username, "x-token-auth")
	}
	if basicAuth.Password != "upstream-pat" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "upstream-pat")
	}
}
