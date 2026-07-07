package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// TS-01-14: Verify that the store layer provides typed CRUD functions for all
// five entities and returns a sentinel not-found error for missing records.
func TestGetUserByID_NotFound(t *testing.T) {
	s := newTestStore(t)
	user, err := s.GetUserByID("nonexistent-uuid")
	if user != nil {
		t.Error("expected nil user for nonexistent ID")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestGetWorkspaceByID_NotFound(t *testing.T) {
	s := newTestStore(t)
	ws, err := s.GetWorkspaceByID("nonexistent-uuid")
	if ws != nil {
		t.Error("expected nil workspace for nonexistent ID")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestGetWorkspaceMember_NotFound(t *testing.T) {
	s := newTestStore(t)
	m, err := s.GetWorkspaceMember("nonexistent-user", "nonexistent-ws")
	if m != nil {
		t.Error("expected nil member for nonexistent pair")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestGetAPIKeyByID_NotFound(t *testing.T) {
	s := newTestStore(t)
	k, err := s.GetAPIKeyByID("nonexistent-uuid")
	if k != nil {
		t.Error("expected nil api key for nonexistent ID")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestGetAdminToken_NotFound(t *testing.T) {
	s := newTestStore(t)
	tok, err := s.GetAdminToken()
	if tok != nil {
		t.Error("expected nil admin token when table is empty")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

// TS-01-15: Verify that the store layer generates UUIDs for new entity IDs
// and formats timestamps as RFC 3339 strings.
func TestCreateUser_GeneratesUUIDAndTimestamp(t *testing.T) {
	s := newTestStore(t)
	user, err := s.CreateUser(&User{
		Username:   "bob",
		Email:      "bob@test.com",
		Provider:   "local",
		ProviderID: "bob1",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("CreateUser returned error: %v", err)
	}
	if user == nil {
		t.Fatal("CreateUser returned nil user")
	}

	// Verify UUID is valid.
	if _, err := uuid.Parse(user.ID); err != nil {
		t.Errorf("user ID %q is not a valid UUID: %v", user.ID, err)
	}

	// Verify timestamps are valid RFC 3339.
	if _, err := time.Parse(time.RFC3339, user.CreatedAt); err != nil {
		t.Errorf("created_at %q is not valid RFC 3339: %v", user.CreatedAt, err)
	}
	if _, err := time.Parse(time.RFC3339, user.UpdatedAt); err != nil {
		t.Errorf("updated_at %q is not valid RFC 3339: %v", user.UpdatedAt, err)
	}
}

// TS-01-14 continued: Test full CRUD lifecycle for users.
func TestUserCRUD(t *testing.T) {
	s := newTestStore(t)

	// Create.
	user, err := s.CreateUser(&User{
		Username:   "alice",
		Email:      "alice@test.com",
		Provider:   "local",
		ProviderID: "alice1",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if user == nil {
		t.Fatal("CreateUser returned nil")
	}

	// Read by ID.
	got, err := s.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}
	if got.Username != "alice" {
		t.Errorf("expected username 'alice', got %q", got.Username)
	}

	// Read by username.
	got, err = s.GetUserByUsername("alice")
	if err != nil {
		t.Fatalf("GetUserByUsername failed: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("expected ID %q, got %q", user.ID, got.ID)
	}

	// Read by provider ID.
	got, err = s.GetUserByProviderID("local", "alice1")
	if err != nil {
		t.Fatalf("GetUserByProviderID failed: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("expected ID %q, got %q", user.ID, got.ID)
	}

	// Update.
	user.Email = "alice2@test.com"
	updated, err := s.UpdateUser(user)
	if err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}
	if updated.Email != "alice2@test.com" {
		t.Errorf("expected updated email, got %q", updated.Email)
	}

	// Delete.
	if err := s.DeleteUser(user.ID); err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}

	// Confirm deleted.
	_, err = s.GetUserByID(user.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got: %v", err)
	}
}

// TS-01-14 continued: Test CRUD for workspaces.
func TestWorkspaceCRUD(t *testing.T) {
	s := newTestStore(t)

	// Create a user for created_by FK.
	user, err := s.CreateUser(&User{
		Username:   "ws_owner",
		Email:      "owner@test.com",
		Provider:   "local",
		ProviderID: "ws_owner1",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if user == nil {
		t.Fatal("CreateUser returned nil")
	}

	// Create workspace.
	ws, err := s.CreateWorkspace(&Workspace{
		Name:      "test-ws",
		Slug:      "test-ws",
		URL:       "https://test-ws.example.com",
		Status:    "active",
		CreatedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}
	if ws == nil {
		t.Fatal("CreateWorkspace returned nil")
	}

	// Read by ID.
	got, err := s.GetWorkspaceByID(ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID failed: %v", err)
	}
	if got.Name != "test-ws" {
		t.Errorf("expected name 'test-ws', got %q", got.Name)
	}

	// Read by slug.
	got, err = s.GetWorkspaceBySlug("test-ws")
	if err != nil {
		t.Fatalf("GetWorkspaceBySlug failed: %v", err)
	}
	if got.ID != ws.ID {
		t.Errorf("expected ID %q, got %q", ws.ID, got.ID)
	}

	// Delete.
	if err := s.DeleteWorkspace(ws.ID); err != nil {
		t.Fatalf("DeleteWorkspace failed: %v", err)
	}
	_, err = s.GetWorkspaceByID(ws.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got: %v", err)
	}
}

// TS-01-14 continued: Test CRUD for workspace members.
func TestWorkspaceMemberCRUD(t *testing.T) {
	s := newTestStore(t)

	user, _ := s.CreateUser(&User{
		Username:   "member_user",
		Email:      "mu@test.com",
		Provider:   "local",
		ProviderID: "mu1",
		Status:     "active",
	})
	if user == nil {
		t.Fatal("CreateUser returned nil")
	}

	ws, _ := s.CreateWorkspace(&Workspace{
		Name:      "member-ws",
		Slug:      "member-ws",
		URL:       "https://member-ws.test",
		Status:    "active",
		CreatedBy: user.ID,
	})
	if ws == nil {
		t.Fatal("CreateWorkspace returned nil")
	}

	// Create member.
	m, err := s.CreateWorkspaceMember(&WorkspaceMember{
		UserID:      user.ID,
		WorkspaceID: ws.ID,
		Role:        "admin",
		GrantedBy:   user.ID,
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceMember failed: %v", err)
	}
	if m == nil {
		t.Fatal("CreateWorkspaceMember returned nil")
	}

	// Read.
	got, err := s.GetWorkspaceMember(user.ID, ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceMember failed: %v", err)
	}
	if got.Role != "admin" {
		t.Errorf("expected role 'admin', got %q", got.Role)
	}

	// List.
	members, err := s.ListWorkspaceMembers(ws.ID)
	if err != nil {
		t.Fatalf("ListWorkspaceMembers failed: %v", err)
	}
	if len(members) != 1 {
		t.Errorf("expected 1 member, got %d", len(members))
	}

	// Delete.
	if err := s.DeleteWorkspaceMember(user.ID, ws.ID); err != nil {
		t.Fatalf("DeleteWorkspaceMember failed: %v", err)
	}
	_, err = s.GetWorkspaceMember(user.ID, ws.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got: %v", err)
	}
}

// TS-01-14 continued: Test CRUD for API keys.
func TestAPIKeyCRUD(t *testing.T) {
	s := newTestStore(t)

	user, _ := s.CreateUser(&User{
		Username:   "key_user",
		Email:      "ku@test.com",
		Provider:   "local",
		ProviderID: "ku1",
		Status:     "active",
	})
	if user == nil {
		t.Fatal("CreateUser returned nil")
	}

	k, err := s.CreateAPIKey(&APIKey{
		KeyID:   "kid_123",
		KeyHash: "hash_abc",
		UserID:  user.ID,
		Label:   "test key",
	})
	if err != nil {
		t.Fatalf("CreateAPIKey failed: %v", err)
	}
	if k == nil {
		t.Fatal("CreateAPIKey returned nil")
	}

	// Read by ID.
	got, err := s.GetAPIKeyByID(k.ID)
	if err != nil {
		t.Fatalf("GetAPIKeyByID failed: %v", err)
	}
	if got.KeyID != "kid_123" {
		t.Errorf("expected key_id 'kid_123', got %q", got.KeyID)
	}

	// Read by key_id.
	got, err = s.GetAPIKeyByKeyID("kid_123")
	if err != nil {
		t.Fatalf("GetAPIKeyByKeyID failed: %v", err)
	}
	if got.ID != k.ID {
		t.Errorf("expected ID %q, got %q", k.ID, got.ID)
	}

	// Revoke.
	if err := s.RevokeAPIKey(k.ID); err != nil {
		t.Fatalf("RevokeAPIKey failed: %v", err)
	}

	// Delete.
	if err := s.DeleteAPIKey(k.ID); err != nil {
		t.Fatalf("DeleteAPIKey failed: %v", err)
	}
	_, err = s.GetAPIKeyByID(k.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got: %v", err)
	}
}

// TS-01-14 continued: Test CRUD for admin tokens.
func TestAdminTokenCRUD(t *testing.T) {
	s := newTestStore(t)

	tok, err := s.CreateAdminToken(&AdminToken{
		TokenHash: "deadbeef",
	})
	if err != nil {
		t.Fatalf("CreateAdminToken failed: %v", err)
	}
	if tok == nil {
		t.Fatal("CreateAdminToken returned nil")
	}

	// Read.
	got, err := s.GetAdminToken()
	if err != nil {
		t.Fatalf("GetAdminToken failed: %v", err)
	}
	if got.TokenHash != "deadbeef" {
		t.Errorf("expected token_hash 'deadbeef', got %q", got.TokenHash)
	}

	// Update.
	tok.TokenHash = "cafebabe"
	updated, err := s.UpdateAdminToken(tok)
	if err != nil {
		t.Fatalf("UpdateAdminToken failed: %v", err)
	}
	if updated.TokenHash != "cafebabe" {
		t.Errorf("expected updated token_hash 'cafebabe', got %q", updated.TokenHash)
	}

	// Delete.
	if err := s.DeleteAdminToken(tok.ID); err != nil {
		t.Fatalf("DeleteAdminToken failed: %v", err)
	}
	_, err = s.GetAdminToken()
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got: %v", err)
	}
}

// TS-01-17: Verify that the store layer wraps database errors with contextual
// information before returning to callers.
func TestStoreWrapsErrors(t *testing.T) {
	s := newTestStore(t)

	// Close the database to force errors.
	s.DB().Close()

	user, err := s.CreateUser(&User{
		Username:   "x",
		Email:      "x@test.com",
		Provider:   "local",
		ProviderID: "x1",
		Status:     "active",
	})
	if user != nil {
		t.Error("expected nil user when DB is closed")
	}
	if err == nil {
		t.Fatal("expected error when DB is closed, got nil")
	}

	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "create") && !strings.Contains(errMsg, "user") {
		t.Errorf("error should contain operation context ('create' or 'user'), got: %s", err.Error())
	}
}

// TS-01-E7: Verify that the store layer returns a typed constraint-violation
// error when a create operation violates a UNIQUE constraint.
func TestCreateUser_DuplicateUsername(t *testing.T) {
	s := newTestStore(t)

	_, err := s.CreateUser(&User{
		Username:   "duplicate",
		Email:      "d1@test.com",
		Provider:   "local",
		ProviderID: "dup1",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("first CreateUser failed: %v", err)
	}

	_, err = s.CreateUser(&User{
		Username:   "duplicate",
		Email:      "d2@test.com",
		Provider:   "local",
		ProviderID: "dup2",
		Status:     "active",
	})
	if err == nil {
		t.Fatal("expected constraint violation error for duplicate username, got nil")
	}
	if !errors.Is(err, ErrConstraintViolation) {
		t.Errorf("expected ErrConstraintViolation, got: %v", err)
	}
}

// TS-01-E8: Verify that the store layer returns a wrapped error with operation
// context and does not call os.Exit when the database connection is lost.
func TestStoreErrorOnClosedDB(t *testing.T) {
	s := newTestStore(t)
	s.DB().Close()

	_, err := s.GetUserByID("some-uuid")
	if err == nil {
		t.Fatal("expected error on closed DB, got nil")
	}
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "get") && !strings.Contains(errMsg, "user") {
		t.Errorf("error should contain operation context, got: %s", err.Error())
	}
}

// TS-01-P4: Verify that all timestamp columns in all five tables are valid
// RFC 3339 strings.
func TestAllTimestampsAreRFC3339(t *testing.T) {
	s := newTestStore(t)

	// Create a user.
	user, err := s.CreateUser(&User{
		Username:   "ts_user",
		Email:      "ts@test.com",
		Provider:   "local",
		ProviderID: "ts1",
		Status:     "active",
	})
	if err != nil || user == nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Check user timestamps.
	assertRFC3339(t, "user.CreatedAt", user.CreatedAt)
	assertRFC3339(t, "user.UpdatedAt", user.UpdatedAt)

	// Create workspace.
	ws, err := s.CreateWorkspace(&Workspace{
		Name:      "ts-ws",
		Slug:      "ts-ws",
		URL:       "https://ts-ws.test",
		Status:    "active",
		CreatedBy: user.ID,
	})
	if err != nil || ws == nil {
		t.Fatalf("CreateWorkspace failed: %v", err)
	}
	assertRFC3339(t, "workspace.CreatedAt", ws.CreatedAt)

	// Create workspace member.
	m, err := s.CreateWorkspaceMember(&WorkspaceMember{
		UserID:      user.ID,
		WorkspaceID: ws.ID,
		Role:        "admin",
		GrantedBy:   user.ID,
	})
	if err != nil || m == nil {
		t.Fatalf("CreateWorkspaceMember failed: %v", err)
	}
	assertRFC3339(t, "member.CreatedAt", m.CreatedAt)

	// Create API key.
	k, err := s.CreateAPIKey(&APIKey{
		KeyID:   "ts_kid",
		KeyHash: "ts_hash",
		UserID:  user.ID,
		Label:   "ts key",
	})
	if err != nil || k == nil {
		t.Fatalf("CreateAPIKey failed: %v", err)
	}
	assertRFC3339(t, "apikey.CreatedAt", k.CreatedAt)

	// Create admin token.
	tok, err := s.CreateAdminToken(&AdminToken{
		TokenHash: "ts_tok_hash",
	})
	if err != nil || tok == nil {
		t.Fatalf("CreateAdminToken failed: %v", err)
	}
	assertRFC3339(t, "admintoken.CreatedAt", tok.CreatedAt)
}

func assertRFC3339(t *testing.T, field, value string) {
	t.Helper()
	if value == "" {
		t.Errorf("%s is empty", field)
		return
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		t.Errorf("%s value %q is not valid RFC 3339: %v", field, value, err)
	}
}

// newTestStore creates a fresh in-memory database with schema initialized
// and returns a Store backed by it.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Enable WAL mode.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("failed to enable WAL: %v", err)
	}

	// Create the schema directly for store tests.
	schema := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT UNIQUE NOT NULL,
			email TEXT,
			full_name TEXT,
			provider TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			status TEXT DEFAULT 'active',
			created_at TEXT,
			updated_at TEXT,
			UNIQUE(provider, provider_id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			slug TEXT UNIQUE NOT NULL,
			url TEXT UNIQUE NOT NULL,
			status TEXT DEFAULT 'active',
			created_at TEXT,
			created_by TEXT REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspace_members (
			user_id TEXT REFERENCES users(id),
			workspace_id TEXT REFERENCES workspaces(id),
			role TEXT NOT NULL,
			created_at TEXT,
			granted_by TEXT REFERENCES users(id),
			PRIMARY KEY (user_id, workspace_id)
		)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			key_id TEXT UNIQUE,
			key_hash TEXT,
			user_id TEXT REFERENCES users(id),
			workspace_id TEXT REFERENCES workspaces(id),
			label TEXT,
			expires_at TEXT,
			revoked_at TEXT,
			created_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS admin_tokens (
			id TEXT PRIMARY KEY,
			token_hash TEXT,
			created_at TEXT
		)`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create schema: %v", err)
		}
	}

	return NewStore(db)
}

