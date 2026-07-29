package workspace

import (
	"encoding/base64"
	"testing"
	"time"
)

// ========================================================================
// Spec 09 Task 2.4: Cascade deletion test for credential secrets
// (TS-09-29)
// Requirements: 09-REQ-8.1
// ========================================================================

// TS-09-29: deleteWorkspace cascade-deletes all workspace-scoped credential
// secrets (GIT_PAT, GIT_USERNAME, GIT_PASSWORD) when a workspace is deleted.
//
// After deletion, no secrets with owner_type='workspace' and owner_id=<slug>
// remain in the secrets table.
// Requirement: 09-REQ-8.1, 09-PROP-10
func TestCascadeDelete_CredentialSecrets(t *testing.T) {
	db := openTestDB(t)

	// Insert workspace.
	ws := &Workspace{
		Slug:    "my-ws",
		GitURL:  "https://github.com/acme/private",
		OwnerID: "user-1",
		Status:  "archived",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("insertWorkspace() returned error: %v", err)
	}

	// Insert credential secrets directly into the secrets table.
	now := time.Now().UTC().Format(time.RFC3339)
	secrets := []struct {
		key   string
		value string
	}{
		{"GIT_PAT", "ghp_abc123"},
		{"GIT_USERNAME", "alice"},
		{"GIT_PASSWORD", "s3cr3t"},
	}
	for _, s := range secrets {
		encoded := base64.StdEncoding.EncodeToString([]byte(s.value))
		_, err := db.Exec(
			"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			"workspace", "my-ws", s.key, encoded, now, now,
		)
		if err != nil {
			t.Fatalf("insert secret %q: %v", s.key, err)
		}
	}

	// Verify precondition: 3 secrets exist.
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM secrets WHERE owner_type = 'workspace' AND owner_id = 'my-ws'",
	).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 3 {
		t.Fatalf("precondition: expected 3 secrets; got %d", count)
	}

	// Delete the workspace (should cascade-delete all workspace-scoped secrets).
	if err := deleteWorkspace(db, "my-ws"); err != nil {
		t.Fatalf("deleteWorkspace() returned error: %v", err)
	}

	// Verify workspace is deleted.
	ws, _ = getWorkspaceBySlug(db, "my-ws")
	if ws != nil {
		t.Error("workspace should not exist after deletion")
	}

	// Verify all credential secrets are deleted.
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM secrets WHERE owner_type = 'workspace' AND owner_id = 'my-ws'",
	).Scan(&count); err != nil {
		t.Fatalf("count secrets query: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 secrets remaining after cascade delete; got %d", count)
	}
}

// 09-REQ-8.E1: deleteWorkspace succeeds without error when the workspace
// has no associated credential secrets — the cascade-delete query executes
// and deletes zero rows.
func TestCascadeDelete_NoCredentialSecrets(t *testing.T) {
	db := openTestDB(t)

	// Insert workspace without any secrets.
	ws := &Workspace{
		Slug:    "no-secrets-ws",
		GitURL:  "https://github.com/acme/public",
		OwnerID: "user-1",
		Status:  "archived",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("insertWorkspace() returned error: %v", err)
	}

	// Verify precondition: 0 secrets exist.
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM secrets WHERE owner_type = 'workspace' AND owner_id = 'no-secrets-ws'",
	).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Fatalf("precondition: expected 0 secrets; got %d", count)
	}

	// Delete the workspace — should succeed even without secrets.
	if err := deleteWorkspace(db, "no-secrets-ws"); err != nil {
		t.Fatalf("deleteWorkspace() returned error: %v; want nil (no secrets to cascade-delete)", err)
	}

	// Verify workspace is deleted.
	ws, _ = getWorkspaceBySlug(db, "no-secrets-ws")
	if ws != nil {
		t.Error("workspace should not exist after deletion")
	}
}

// 09-REQ-8.E2: If the secrets table delete fails during deleteWorkspace()
// due to a database error, the workspace row and its secrets remain.
func TestCascadeDelete_SecretsDeleteFails_RollsBack(t *testing.T) {
	db := openTestDB(t)

	// Insert workspace.
	ws := &Workspace{
		Slug:    "fail-cascade-ws",
		GitURL:  "https://github.com/acme/private",
		OwnerID: "user-1",
		Status:  "archived",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("insertWorkspace() returned error: %v", err)
	}

	// Insert a credential secret.
	now := time.Now().UTC().Format(time.RFC3339)
	encoded := base64.StdEncoding.EncodeToString([]byte("ghp_abc123"))
	_, err := db.Exec(
		"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		"workspace", "fail-cascade-ws", "GIT_PAT", encoded, now, now,
	)
	if err != nil {
		t.Fatalf("insert secret: %v", err)
	}

	// Drop the secrets table to force the cascade DELETE to fail.
	// Note: deleteWorkspace() operates in a transaction, so the workspace
	// DELETE should be rolled back when the secrets DELETE fails.
	_, err = db.Exec("DROP TABLE secrets")
	if err != nil {
		t.Fatalf("failed to drop secrets table: %v", err)
	}

	// deleteWorkspace should return an error.
	err = deleteWorkspace(db, "fail-cascade-ws")
	if err == nil {
		t.Error("deleteWorkspace() returned nil error; want non-nil when cascade-delete of secrets fails")
	}

	// Workspace row should still exist (transaction rolled back).
	ws, _ = getWorkspaceBySlug(db, "fail-cascade-ws")
	if ws == nil {
		t.Error("workspace should still exist after failed cascade-delete (transactional rollback)")
	}
}

// TS-09-29 (variant): Cascade delete removes ONLY secrets for the target
// workspace — other workspace secrets and non-workspace secrets are preserved.
func TestCascadeDelete_OnlyTargetWorkspaceSecrets(t *testing.T) {
	db := openTestDB(t)

	// Insert two workspaces.
	ws1 := &Workspace{
		Slug:    "ws-to-delete",
		GitURL:  "https://github.com/acme/private",
		OwnerID: "user-1",
		Status:  "archived",
	}
	ws2 := &Workspace{
		Slug:    "ws-to-keep",
		GitURL:  "https://github.com/acme/other",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws1); err != nil {
		t.Fatalf("insertWorkspace(ws1): %v", err)
	}
	if err := insertWorkspace(db, ws2); err != nil {
		t.Fatalf("insertWorkspace(ws2): %v", err)
	}

	// Insert secrets for both workspaces.
	now := time.Now().UTC().Format(time.RFC3339)
	for _, slug := range []string{"ws-to-delete", "ws-to-keep"} {
		encoded := base64.StdEncoding.EncodeToString([]byte("token-for-" + slug))
		_, err := db.Exec(
			"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			"workspace", slug, "GIT_PAT", encoded, now, now,
		)
		if err != nil {
			t.Fatalf("insert secret for %q: %v", slug, err)
		}
	}

	// Also insert a user-scoped secret to verify it's not affected.
	encoded := base64.StdEncoding.EncodeToString([]byte("user-secret"))
	_, err := db.Exec(
		"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		"user", "user-1", "USER_SECRET", encoded, now, now,
	)
	if err != nil {
		t.Fatalf("insert user secret: %v", err)
	}

	// Delete only ws-to-delete.
	if err := deleteWorkspace(db, "ws-to-delete"); err != nil {
		t.Fatalf("deleteWorkspace(): %v", err)
	}

	// ws-to-keep's secrets should still exist.
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM secrets WHERE owner_type = 'workspace' AND owner_id = 'ws-to-keep'",
	).Scan(&count); err != nil {
		t.Fatalf("count ws-to-keep secrets: %v", err)
	}
	if count != 1 {
		t.Errorf("ws-to-keep secrets count = %d; want 1", count)
	}

	// User-scoped secret should still exist.
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM secrets WHERE owner_type = 'user' AND owner_id = 'user-1'",
	).Scan(&count); err != nil {
		t.Fatalf("count user secrets: %v", err)
	}
	if count != 1 {
		t.Errorf("user secrets count = %d; want 1", count)
	}

	// ws-to-delete's secrets should be gone.
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM secrets WHERE owner_type = 'workspace' AND owner_id = 'ws-to-delete'",
	).Scan(&count); err != nil {
		t.Fatalf("count ws-to-delete secrets: %v", err)
	}
	if count != 0 {
		t.Errorf("ws-to-delete secrets count = %d; want 0", count)
	}
}
