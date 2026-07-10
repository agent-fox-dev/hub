package workspace

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// openTestDB opens an in-memory SQLite database with foreign keys enabled
// and the full schema applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory SQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatalf("failed to set %s: %v", pragma, err)
		}
	}

	// Apply schema: users, admin_tokens, api_keys, teams, team_members,
	// workspaces, workspace_tokens.
	if _, err := db.Exec(testSchemaDDL); err != nil {
		t.Fatalf("failed to initialize schema: %v", err)
	}

	return db
}

// testSchemaDDL is the complete database schema for testing.
const testSchemaDDL = `
CREATE TABLE IF NOT EXISTS users (
    id          TEXT PRIMARY KEY,
    username    TEXT NOT NULL UNIQUE,
    email       TEXT NOT NULL,
    full_name   TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active',
    provider    TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE (provider, provider_id)
);

CREATE TABLE IF NOT EXISTS teams (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    slug        TEXT NOT NULL UNIQUE,
    url         TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workspaces (
    id         TEXT PRIMARY KEY,
    slug       TEXT NOT NULL UNIQUE,
    git_url    TEXT NOT NULL,
    branch     TEXT,
    owner_id   TEXT NOT NULL REFERENCES users(id),
    team_id    TEXT REFERENCES teams(id),
    status     TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workspace_tokens (
    id           TEXT PRIMARY KEY,
    token_id     TEXT NOT NULL UNIQUE,
    secret_hash  TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    user_id      TEXT NOT NULL REFERENCES users(id),
    label        TEXT,
    expires_at   TEXT,
    created_at   TEXT NOT NULL,
    revoked_at   TEXT
);

CREATE INDEX IF NOT EXISTS idx_workspace_tokens_token_id ON workspace_tokens(token_id);
CREATE INDEX IF NOT EXISTS idx_workspaces_slug ON workspaces(slug);
`

// insertTestUser inserts a user record for foreign key satisfaction.
func insertTestUser(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	now := formatTime(time.Now().UTC())
	_, err := db.Exec(`INSERT INTO users (id, username, email, full_name, status, provider, provider_id, created_at, updated_at)
		VALUES (?, ?, ?, '', 'active', 'github', ?, ?, ?)`,
		userID, "user-"+userID, userID+"@test.com", "gh_"+userID, now, now)
	if err != nil {
		t.Fatalf("insertTestUser(%s): %v", userID, err)
	}
}

// insertTestTeam inserts an active team record.
func insertTestTeam(t *testing.T, db *sql.DB, teamID, name string) {
	t.Helper()
	now := formatTime(time.Now().UTC())
	_, err := db.Exec(`INSERT INTO teams (id, name, slug, url, status, created_at, updated_at)
		VALUES (?, ?, ?, '', 'active', ?, ?)`,
		teamID, name, "slug-"+teamID, now, now)
	if err != nil {
		t.Fatalf("insertTestTeam(%s): %v", teamID, err)
	}
}

// insertInactiveTeam inserts an inactive team record.
func insertInactiveTeam(t *testing.T, db *sql.DB, teamID, name string) {
	t.Helper()
	now := formatTime(time.Now().UTC())
	_, err := db.Exec(`INSERT INTO teams (id, name, slug, url, status, created_at, updated_at)
		VALUES (?, ?, ?, '', 'deleted', ?, ?)`,
		teamID, name, "slug-"+teamID, now, now)
	if err != nil {
		t.Fatalf("insertInactiveTeam(%s): %v", teamID, err)
	}
}

// ctx returns a background context for tests.
func ctx() context.Context {
	return context.Background()
}

// ==========================================================================
// InsertWorkspace tests (Task 4.1)
// ==========================================================================

func TestInsertWorkspace_Success(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, err := store.InsertWorkspace(ctx(), Workspace{
		Slug:        "test-workspace",
		GitURL:      "https://github.com/org/repo.git",
		OwnerUserID: "user-001",
	})
	if err != nil {
		t.Fatalf("InsertWorkspace: %v", err)
	}

	// Verify returned workspace has all fields.
	if ws.ID == "" {
		t.Error("ID should be non-empty")
	}
	if ws.Slug != "test-workspace" {
		t.Errorf("Slug = %q, want %q", ws.Slug, "test-workspace")
	}
	if ws.GitURL != "https://github.com/org/repo.git" {
		t.Errorf("GitURL = %q, want %q", ws.GitURL, "https://github.com/org/repo.git")
	}
	if ws.Branch != nil {
		t.Errorf("Branch = %v, want nil", ws.Branch)
	}
	if ws.TeamID != nil {
		t.Errorf("TeamID = %v, want nil", ws.TeamID)
	}
	if ws.OwnerUserID != "user-001" {
		t.Errorf("OwnerUserID = %q, want %q", ws.OwnerUserID, "user-001")
	}
	if ws.CreatedAt == "" {
		t.Error("CreatedAt should be non-empty")
	}
	if ws.UpdatedAt != ws.CreatedAt {
		t.Errorf("UpdatedAt (%q) != CreatedAt (%q)", ws.UpdatedAt, ws.CreatedAt)
	}
}

func TestInsertWorkspace_WithBranchAndTeam(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	insertTestTeam(t, db, "team-001", "Team One")
	store := NewStore(db)

	branch := "feature/branch"
	teamID := "team-001"
	ws, err := store.InsertWorkspace(ctx(), Workspace{
		Slug:        "branched-ws",
		GitURL:      "git@github.com:org/repo.git",
		Branch:      &branch,
		TeamID:      &teamID,
		OwnerUserID: "user-001",
	})
	if err != nil {
		t.Fatalf("InsertWorkspace: %v", err)
	}

	if ws.Branch == nil || *ws.Branch != "feature/branch" {
		t.Errorf("Branch = %v, want %q", ws.Branch, "feature/branch")
	}
	if ws.TeamID == nil || *ws.TeamID != "team-001" {
		t.Errorf("TeamID = %v, want %q", ws.TeamID, "team-001")
	}
}

func TestInsertWorkspace_DuplicateSlug(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	// First insert succeeds.
	_, err := store.InsertWorkspace(ctx(), Workspace{
		Slug:        "dup-slug",
		GitURL:      "https://github.com/org/repo.git",
		OwnerUserID: "user-001",
	})
	if err != nil {
		t.Fatalf("first InsertWorkspace: %v", err)
	}

	// Second insert with same slug returns ErrSlugConflict.
	_, err = store.InsertWorkspace(ctx(), Workspace{
		Slug:        "dup-slug",
		GitURL:      "https://github.com/org/other.git",
		OwnerUserID: "user-001",
	})
	if !errors.Is(err, ErrSlugConflict) {
		t.Errorf("InsertWorkspace duplicate slug: err = %v, want ErrSlugConflict", err)
	}
}

func TestInsertWorkspace_UpdatedAtEqualsCreatedAt(t *testing.T) {
	// 04-REQ-8.3, 04-PROP-6: updated_at == created_at on creation.
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, err := store.InsertWorkspace(ctx(), Workspace{
		Slug:        "timestamp-ws",
		GitURL:      "https://github.com/org/repo.git",
		OwnerUserID: "user-001",
	})
	if err != nil {
		t.Fatalf("InsertWorkspace: %v", err)
	}

	// Check API response.
	if ws.UpdatedAt != ws.CreatedAt {
		t.Errorf("response: UpdatedAt (%q) != CreatedAt (%q)", ws.UpdatedAt, ws.CreatedAt)
	}

	// Also verify directly in DB.
	var dbCreatedAt, dbUpdatedAt string
	err = db.QueryRow(`SELECT created_at, updated_at FROM workspaces WHERE id = ?`, ws.ID).
		Scan(&dbCreatedAt, &dbUpdatedAt)
	if err != nil {
		t.Fatalf("DB query: %v", err)
	}
	if dbCreatedAt != dbUpdatedAt {
		t.Errorf("DB: created_at (%q) != updated_at (%q)", dbCreatedAt, dbUpdatedAt)
	}
}

// ==========================================================================
// GetWorkspaceBySlug tests (Task 4.1)
// ==========================================================================

func TestGetWorkspaceBySlug_Found(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	inserted, err := store.InsertWorkspace(ctx(), Workspace{
		Slug:        "find-me",
		GitURL:      "https://github.com/org/repo.git",
		OwnerUserID: "user-001",
	})
	if err != nil {
		t.Fatalf("InsertWorkspace: %v", err)
	}

	found, err := store.GetWorkspaceBySlug(ctx(), "find-me")
	if err != nil {
		t.Fatalf("GetWorkspaceBySlug: %v", err)
	}

	if found.ID != inserted.ID {
		t.Errorf("ID = %q, want %q", found.ID, inserted.ID)
	}
	if found.Slug != "find-me" {
		t.Errorf("Slug = %q, want %q", found.Slug, "find-me")
	}
	if found.OwnerUserID != "user-001" {
		t.Errorf("OwnerUserID = %q, want %q", found.OwnerUserID, "user-001")
	}
}

func TestGetWorkspaceBySlug_NotFound(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	_, err := store.GetWorkspaceBySlug(ctx(), "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetWorkspaceBySlug(nonexistent): err = %v, want ErrNotFound", err)
	}
}

func TestGetWorkspaceBySlug_OwnerIDMappedToOwnerUserID(t *testing.T) {
	// Verify the column alias: DB 'owner_id' → struct 'OwnerUserID'.
	db := openTestDB(t)
	insertTestUser(t, db, "user-alias")
	store := NewStore(db)

	_, err := store.InsertWorkspace(ctx(), Workspace{
		Slug:        "alias-test",
		GitURL:      "https://github.com/org/repo.git",
		OwnerUserID: "user-alias",
	})
	if err != nil {
		t.Fatalf("InsertWorkspace: %v", err)
	}

	ws, err := store.GetWorkspaceBySlug(ctx(), "alias-test")
	if err != nil {
		t.Fatalf("GetWorkspaceBySlug: %v", err)
	}
	if ws.OwnerUserID != "user-alias" {
		t.Errorf("OwnerUserID = %q, want %q (owner_id column alias)", ws.OwnerUserID, "user-alias")
	}
}

func TestGetWorkspaceBySlug_AllFieldsPopulated(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	insertTestTeam(t, db, "team-x", "Team X")
	store := NewStore(db)

	branch := "main"
	teamID := "team-x"
	inserted, err := store.InsertWorkspace(ctx(), Workspace{
		Slug:        "full-fields",
		GitURL:      "git@github.com:org/repo.git",
		Branch:      &branch,
		TeamID:      &teamID,
		OwnerUserID: "user-001",
	})
	if err != nil {
		t.Fatalf("InsertWorkspace: %v", err)
	}

	found, err := store.GetWorkspaceBySlug(ctx(), "full-fields")
	if err != nil {
		t.Fatalf("GetWorkspaceBySlug: %v", err)
	}

	if found.ID != inserted.ID {
		t.Errorf("ID mismatch")
	}
	if found.GitURL != "git@github.com:org/repo.git" {
		t.Errorf("GitURL = %q", found.GitURL)
	}
	if found.Branch == nil || *found.Branch != "main" {
		t.Errorf("Branch = %v, want %q", found.Branch, "main")
	}
	if found.TeamID == nil || *found.TeamID != "team-x" {
		t.Errorf("TeamID = %v, want %q", found.TeamID, "team-x")
	}
}

// ==========================================================================
// ValidateTeamExists tests (Task 4.1)
// ==========================================================================

func TestValidateTeamExists_ActiveTeam(t *testing.T) {
	db := openTestDB(t)
	insertTestTeam(t, db, "team-active", "Active Team")
	store := NewStore(db)

	err := store.ValidateTeamExists(ctx(), "team-active")
	if err != nil {
		t.Errorf("ValidateTeamExists(active team) = %v, want nil", err)
	}
}

func TestValidateTeamExists_NonexistentTeam(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	err := store.ValidateTeamExists(ctx(), "nonexistent-team")
	if !errors.Is(err, ErrTeamNotFound) {
		t.Errorf("ValidateTeamExists(nonexistent) = %v, want ErrTeamNotFound", err)
	}
}

func TestValidateTeamExists_InactiveTeam(t *testing.T) {
	db := openTestDB(t)
	insertInactiveTeam(t, db, "team-inactive", "Inactive Team")
	store := NewStore(db)

	err := store.ValidateTeamExists(ctx(), "team-inactive")
	if !errors.Is(err, ErrTeamNotFound) {
		t.Errorf("ValidateTeamExists(inactive team) = %v, want ErrTeamNotFound", err)
	}
}

// ==========================================================================
// ListWorkspaces tests (Task 4.2)
// ==========================================================================

func TestListWorkspaces_UserSeesOnlyOwn(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-a")
	insertTestUser(t, db, "user-b")
	store := NewStore(db)

	_, _ = store.InsertWorkspace(ctx(), Workspace{Slug: "ws-by-a", GitURL: "https://a.com", OwnerUserID: "user-a"})
	_, _ = store.InsertWorkspace(ctx(), Workspace{Slug: "ws-by-b", GitURL: "https://b.com", OwnerUserID: "user-b"})
	_, _ = store.InsertWorkspace(ctx(), Workspace{Slug: "ws-by-a2", GitURL: "https://a2.com", OwnerUserID: "user-a"})

	ownerA := "user-a"
	workspaces, err := store.ListWorkspaces(ctx(), &ownerA)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(workspaces) != 2 {
		t.Fatalf("expected 2 workspaces for user-a, got %d", len(workspaces))
	}
	for _, ws := range workspaces {
		if ws.OwnerUserID != "user-a" {
			t.Errorf("workspace %q owned by %q, want user-a", ws.Slug, ws.OwnerUserID)
		}
	}
}

func TestListWorkspaces_AdminSeesAll(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-a")
	insertTestUser(t, db, "user-b")
	store := NewStore(db)

	_, _ = store.InsertWorkspace(ctx(), Workspace{Slug: "ws-a", GitURL: "https://a.com", OwnerUserID: "user-a"})
	_, _ = store.InsertWorkspace(ctx(), Workspace{Slug: "ws-b", GitURL: "https://b.com", OwnerUserID: "user-b"})

	workspaces, err := store.ListWorkspaces(ctx(), nil)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(workspaces) != 2 {
		t.Errorf("expected 2 workspaces for admin, got %d", len(workspaces))
	}
}

func TestListWorkspaces_EmptyReturnsEmptySlice(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-empty")
	store := NewStore(db)

	ownerID := "user-empty"
	workspaces, err := store.ListWorkspaces(ctx(), &ownerID)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if workspaces == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(workspaces) != 0 {
		t.Errorf("expected 0 workspaces, got %d", len(workspaces))
	}
}

func TestListWorkspaces_AdminEmptyReturnsEmptySlice(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	workspaces, err := store.ListWorkspaces(ctx(), nil)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if workspaces == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(workspaces) != 0 {
		t.Errorf("expected 0 workspaces, got %d", len(workspaces))
	}
}

func TestListWorkspaces_OrderedByCreatedAtAscIdAsc(t *testing.T) {
	// 04-PROP-9: ordering stability with same created_at.
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	// Insert workspaces with the same timestamp to test id ASC tiebreaker.
	now := formatTime(time.Now().UTC())
	// Insert directly to control IDs and timestamps.
	for _, rec := range []struct{ id, slug string }{
		{"zzz-id", "ws-z"},
		{"aaa-id", "ws-a"},
		{"mmm-id", "ws-m"},
	} {
		_, err := db.Exec(
			`INSERT INTO workspaces (id, slug, git_url, branch, owner_id, team_id, status, created_at, updated_at)
			 VALUES (?, ?, 'https://x.com', NULL, 'user-001', NULL, 'active', ?, ?)`,
			rec.id, rec.slug, now, now)
		if err != nil {
			t.Fatalf("insert %s: %v", rec.id, err)
		}
	}

	workspaces, err := store.ListWorkspaces(ctx(), nil)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(workspaces) != 3 {
		t.Fatalf("expected 3, got %d", len(workspaces))
	}

	// With same created_at, should be ordered by id ASC.
	expectedOrder := []string{"aaa-id", "mmm-id", "zzz-id"}
	for i, ws := range workspaces {
		if ws.ID != expectedOrder[i] {
			t.Errorf("workspace[%d].ID = %q, want %q", i, ws.ID, expectedOrder[i])
		}
	}
}

func TestListWorkspaces_OrderedByCreatedAtFirst(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	// Insert workspaces with different timestamps.
	early := formatTime(time.Now().UTC().Add(-2 * time.Hour))
	late := formatTime(time.Now().UTC().Add(-1 * time.Hour))

	_, _ = db.Exec(
		`INSERT INTO workspaces (id, slug, git_url, branch, owner_id, team_id, status, created_at, updated_at)
		 VALUES ('id-late', 'late-ws', 'https://x.com', NULL, 'user-001', NULL, 'active', ?, ?)`,
		late, late)
	_, _ = db.Exec(
		`INSERT INTO workspaces (id, slug, git_url, branch, owner_id, team_id, status, created_at, updated_at)
		 VALUES ('id-early', 'early-ws', 'https://x.com', NULL, 'user-001', NULL, 'active', ?, ?)`,
		early, early)

	workspaces, err := store.ListWorkspaces(ctx(), nil)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(workspaces) < 2 {
		t.Fatalf("expected at least 2, got %d", len(workspaces))
	}
	if workspaces[0].ID != "id-early" {
		t.Errorf("first workspace ID = %q, want %q (earlier created_at)", workspaces[0].ID, "id-early")
	}
	if workspaces[1].ID != "id-late" {
		t.Errorf("second workspace ID = %q, want %q (later created_at)", workspaces[1].ID, "id-late")
	}
}

// ==========================================================================
// InsertWorkspaceToken tests (Task 4.3)
// ==========================================================================

func TestInsertWorkspaceToken_Success(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "token-ws", GitURL: "https://x.com", OwnerUserID: "user-001",
	})

	label := "my-label"
	expiresAt := formatTime(time.Now().UTC().Add(30 * 24 * time.Hour))
	token, err := store.InsertWorkspaceToken(ctx(), WorkspaceToken{
		TokenID:     "tokID001",
		SecretHash:  "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		WorkspaceID: ws.ID,
		UserID:      "user-001",
		Label:       &label,
		ExpiresAt:   &expiresAt,
	})
	if err != nil {
		t.Fatalf("InsertWorkspaceToken: %v", err)
	}

	if token.ID == "" {
		t.Error("ID should be non-empty")
	}
	if token.TokenID != "tokID001" {
		t.Errorf("TokenID = %q, want %q", token.TokenID, "tokID001")
	}
	if token.CreatedAt == "" {
		t.Error("CreatedAt should be non-empty")
	}
	if token.RevokedAt != nil {
		t.Errorf("RevokedAt = %v, want nil", token.RevokedAt)
	}
	if token.Label == nil || *token.Label != "my-label" {
		t.Errorf("Label = %v, want %q", token.Label, "my-label")
	}
}

func TestInsertWorkspaceToken_TokenIDCollision(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "collision-ws", GitURL: "https://x.com", OwnerUserID: "user-001",
	})

	// First token succeeds.
	_, err := store.InsertWorkspaceToken(ctx(), WorkspaceToken{
		TokenID:     "sameTok1",
		SecretHash:  "hash1111111111111111111111111111111111111111111111111111111111111",
		WorkspaceID: ws.ID,
		UserID:      "user-001",
	})
	if err != nil {
		t.Fatalf("first InsertWorkspaceToken: %v", err)
	}

	// Second token with same token_id returns ErrTokenIDConflict.
	_, err = store.InsertWorkspaceToken(ctx(), WorkspaceToken{
		TokenID:     "sameTok1",
		SecretHash:  "hash2222222222222222222222222222222222222222222222222222222222222",
		WorkspaceID: ws.ID,
		UserID:      "user-001",
	})
	if !errors.Is(err, ErrTokenIDConflict) {
		t.Errorf("InsertWorkspaceToken duplicate token_id: err = %v, want ErrTokenIDConflict", err)
	}
}

func TestInsertWorkspaceToken_SecretHashNotPlaintext(t *testing.T) {
	// 04-PROP-2: plaintext secret never stored.
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "hash-ws", GitURL: "https://x.com", OwnerUserID: "user-001",
	})

	plaintextSecret := "abcdefghABCDEFGH0123456789abcdef"
	expectedHash := HashSecret(plaintextSecret)

	_, err := store.InsertWorkspaceToken(ctx(), WorkspaceToken{
		TokenID:     "hashTok1",
		SecretHash:  expectedHash,
		WorkspaceID: ws.ID,
		UserID:      "user-001",
	})
	if err != nil {
		t.Fatalf("InsertWorkspaceToken: %v", err)
	}

	// Query DB directly to verify hash is stored.
	var dbHash string
	err = db.QueryRow(`SELECT secret_hash FROM workspace_tokens WHERE token_id = ?`, "hashTok1").
		Scan(&dbHash)
	if err != nil {
		t.Fatalf("query secret_hash: %v", err)
	}

	if dbHash == plaintextSecret {
		t.Error("plaintext secret should not be stored in DB")
	}
	if dbHash != expectedHash {
		t.Errorf("stored hash = %q, want %q", dbHash, expectedHash)
	}
}

// ==========================================================================
// ListWorkspaceTokens tests (Task 4.3)
// ==========================================================================

func TestListWorkspaceTokens_ReturnsAllIncludingExpiredRevoked(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "list-tok-ws", GitURL: "https://x.com", OwnerUserID: "user-001",
	})

	now := formatTime(time.Now().UTC())
	past := formatTime(time.Now().UTC().Add(-48 * time.Hour))
	revokedAt := formatTime(time.Now().UTC().Add(-24 * time.Hour))

	// Active token.
	_, _ = db.Exec(`INSERT INTO workspace_tokens (id, token_id, secret_hash, workspace_id, user_id, label, expires_at, created_at, revoked_at)
		VALUES ('rec-1', 'active01', 'hash1', ?, 'user-001', 'active', ?, ?, NULL)`,
		ws.ID, formatTime(time.Now().UTC().Add(30*24*time.Hour)), now)

	// Expired token.
	_, _ = db.Exec(`INSERT INTO workspace_tokens (id, token_id, secret_hash, workspace_id, user_id, label, expires_at, created_at, revoked_at)
		VALUES ('rec-2', 'expird01', 'hash2', ?, 'user-001', NULL, ?, ?, NULL)`,
		ws.ID, past, formatTime(time.Now().UTC().Add(-72*time.Hour)))

	// Revoked token.
	_, _ = db.Exec(`INSERT INTO workspace_tokens (id, token_id, secret_hash, workspace_id, user_id, label, expires_at, created_at, revoked_at)
		VALUES ('rec-3', 'revokd01', 'hash3', ?, 'user-001', 'revoked', NULL, ?, ?)`,
		ws.ID, formatTime(time.Now().UTC().Add(-96*time.Hour)), revokedAt)

	tokens, err := store.ListWorkspaceTokens(ctx(), ws.ID)
	if err != nil {
		t.Fatalf("ListWorkspaceTokens: %v", err)
	}
	if len(tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d", len(tokens))
	}

	// Verify all three are returned.
	tokenIDs := make(map[string]bool)
	for _, tok := range tokens {
		tokenIDs[tok.TokenID] = true
	}
	for _, expected := range []string{"active01", "expird01", "revokd01"} {
		if !tokenIDs[expected] {
			t.Errorf("token %q not found in list", expected)
		}
	}
}

func TestListWorkspaceTokens_NeverReturnsSecretHash(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "nosecret-ws", GitURL: "https://x.com", OwnerUserID: "user-001",
	})

	_, _ = store.InsertWorkspaceToken(ctx(), WorkspaceToken{
		TokenID:     "noSec001",
		SecretHash:  "secrethash1234567890abcdef1234567890abcdef1234567890abcdef123456",
		WorkspaceID: ws.ID,
		UserID:      "user-001",
	})

	tokens, err := store.ListWorkspaceTokens(ctx(), ws.ID)
	if err != nil {
		t.Fatalf("ListWorkspaceTokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}

	// TokenListItem should not have a SecretHash field — verified at compile time
	// by the struct type. The SQL query should not SELECT secret_hash.
	// This test confirms the token list item has the right fields.
	if tokens[0].TokenID != "noSec001" {
		t.Errorf("TokenID = %q, want %q", tokens[0].TokenID, "noSec001")
	}
}

func TestListWorkspaceTokens_EmptyReturnsEmptySlice(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "empty-tok-ws", GitURL: "https://x.com", OwnerUserID: "user-001",
	})

	tokens, err := store.ListWorkspaceTokens(ctx(), ws.ID)
	if err != nil {
		t.Fatalf("ListWorkspaceTokens: %v", err)
	}
	if tokens == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens, got %d", len(tokens))
	}
}

func TestListWorkspaceTokens_OrderedByCreatedAtAscIdAsc(t *testing.T) {
	// 04-PROP-9: list ordering stability.
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "ordered-tok-ws", GitURL: "https://x.com", OwnerUserID: "user-001",
	})

	now := formatTime(time.Now().UTC())
	// Insert tokens with same created_at to test id ASC tiebreaker.
	for _, rec := range []struct{ id, tokenID string }{
		{"z-rec", "ztokenid"},
		{"a-rec", "atokenid"},
		{"m-rec", "mtokenid"},
	} {
		_, err := db.Exec(`INSERT INTO workspace_tokens (id, token_id, secret_hash, workspace_id, user_id, label, expires_at, created_at, revoked_at)
			VALUES (?, ?, 'hash', ?, 'user-001', NULL, NULL, ?, NULL)`,
			rec.id, rec.tokenID, ws.ID, now)
		if err != nil {
			t.Fatalf("insert %s: %v", rec.id, err)
		}
	}

	tokens, err := store.ListWorkspaceTokens(ctx(), ws.ID)
	if err != nil {
		t.Fatalf("ListWorkspaceTokens: %v", err)
	}
	if len(tokens) != 3 {
		t.Fatalf("expected 3, got %d", len(tokens))
	}

	// With same created_at, should be ordered by id ASC: a-rec, m-rec, z-rec.
	expectedTokenIDs := []string{"atokenid", "mtokenid", "ztokenid"}
	for i, tok := range tokens {
		if tok.TokenID != expectedTokenIDs[i] {
			t.Errorf("token[%d].TokenID = %q, want %q", i, tok.TokenID, expectedTokenIDs[i])
		}
	}
}

// ==========================================================================
// RevokeWorkspaceToken tests (Task 4.3)
// ==========================================================================

func TestRevokeWorkspaceToken_Success(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "revoke-ws", GitURL: "https://x.com", OwnerUserID: "user-001",
	})

	_, _ = store.InsertWorkspaceToken(ctx(), WorkspaceToken{
		TokenID:     "revTok01",
		SecretHash:  "hash00000000000000000000000000000000000000000000000000000000000",
		WorkspaceID: ws.ID,
		UserID:      "user-001",
	})

	err := store.RevokeWorkspaceToken(ctx(), ws.ID, "revTok01")
	if err != nil {
		t.Fatalf("RevokeWorkspaceToken: %v", err)
	}

	// Verify revoked_at is set in DB.
	var revokedAt *string
	err = db.QueryRow(`SELECT revoked_at FROM workspace_tokens WHERE token_id = ?`, "revTok01").
		Scan(&revokedAt)
	if err != nil {
		t.Fatalf("query revoked_at: %v", err)
	}
	if revokedAt == nil {
		t.Error("revoked_at should be non-null after revocation")
	}
}

func TestRevokeWorkspaceToken_Idempotent(t *testing.T) {
	// 04-PROP-8: revoking an already-revoked token returns nil (idempotent).
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "idem-ws", GitURL: "https://x.com", OwnerUserID: "user-001",
	})

	_, _ = store.InsertWorkspaceToken(ctx(), WorkspaceToken{
		TokenID:     "idemTok1",
		SecretHash:  "hash00000000000000000000000000000000000000000000000000000000000",
		WorkspaceID: ws.ID,
		UserID:      "user-001",
	})

	// First revocation.
	err := store.RevokeWorkspaceToken(ctx(), ws.ID, "idemTok1")
	if err != nil {
		t.Fatalf("first RevokeWorkspaceToken: %v", err)
	}

	// Record the original revoked_at.
	var firstRevokedAt string
	_ = db.QueryRow(`SELECT revoked_at FROM workspace_tokens WHERE token_id = ?`, "idemTok1").
		Scan(&firstRevokedAt)

	// Second revocation (should be idempotent).
	err = store.RevokeWorkspaceToken(ctx(), ws.ID, "idemTok1")
	if err != nil {
		t.Errorf("second RevokeWorkspaceToken: %v, want nil (idempotent)", err)
	}

	// Verify revoked_at is unchanged.
	var secondRevokedAt string
	_ = db.QueryRow(`SELECT revoked_at FROM workspace_tokens WHERE token_id = ?`, "idemTok1").
		Scan(&secondRevokedAt)
	if firstRevokedAt != secondRevokedAt {
		t.Errorf("revoked_at changed on second revocation: %q → %q", firstRevokedAt, secondRevokedAt)
	}
}

func TestRevokeWorkspaceToken_NonexistentToken(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "notoken-ws", GitURL: "https://x.com", OwnerUserID: "user-001",
	})

	err := store.RevokeWorkspaceToken(ctx(), ws.ID, "nonexist1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("RevokeWorkspaceToken(nonexistent): err = %v, want ErrNotFound", err)
	}
}

func TestRevokeWorkspaceToken_CrossWorkspaceReturnsNotFound(t *testing.T) {
	// 04-REQ-11.4: token belonging to different workspace → ErrNotFound.
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	wsA, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "ws-alpha", GitURL: "https://x.com", OwnerUserID: "user-001",
	})
	wsB, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "ws-beta", GitURL: "https://x.com", OwnerUserID: "user-001",
	})

	// Token belongs to ws-beta.
	_, _ = store.InsertWorkspaceToken(ctx(), WorkspaceToken{
		TokenID:     "betaTok1",
		SecretHash:  "hash00000000000000000000000000000000000000000000000000000000000",
		WorkspaceID: wsB.ID,
		UserID:      "user-001",
	})

	// Try to revoke via ws-alpha.
	err := store.RevokeWorkspaceToken(ctx(), wsA.ID, "betaTok1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("RevokeWorkspaceToken(cross-workspace): err = %v, want ErrNotFound", err)
	}
}

// ==========================================================================
// InsertWorkspace + GetWorkspaceBySlug roundtrip test
// ==========================================================================

func TestInsertAndGetRoundtrip(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	insertTestTeam(t, db, "team-rt", "Roundtrip Team")
	store := NewStore(db)

	branch := "develop"
	teamID := "team-rt"
	inserted, err := store.InsertWorkspace(ctx(), Workspace{
		Slug:        "roundtrip-ws",
		GitURL:      "git@github.com:org/repo.git",
		Branch:      &branch,
		TeamID:      &teamID,
		OwnerUserID: "user-001",
	})
	if err != nil {
		t.Fatalf("InsertWorkspace: %v", err)
	}

	retrieved, err := store.GetWorkspaceBySlug(ctx(), "roundtrip-ws")
	if err != nil {
		t.Fatalf("GetWorkspaceBySlug: %v", err)
	}

	// Compare all fields.
	if inserted.ID != retrieved.ID {
		t.Errorf("ID: %q != %q", inserted.ID, retrieved.ID)
	}
	if inserted.Slug != retrieved.Slug {
		t.Errorf("Slug: %q != %q", inserted.Slug, retrieved.Slug)
	}
	if inserted.GitURL != retrieved.GitURL {
		t.Errorf("GitURL: %q != %q", inserted.GitURL, retrieved.GitURL)
	}
	if (inserted.Branch == nil) != (retrieved.Branch == nil) {
		t.Errorf("Branch nil mismatch")
	} else if inserted.Branch != nil && *inserted.Branch != *retrieved.Branch {
		t.Errorf("Branch: %q != %q", *inserted.Branch, *retrieved.Branch)
	}
	if (inserted.TeamID == nil) != (retrieved.TeamID == nil) {
		t.Errorf("TeamID nil mismatch")
	} else if inserted.TeamID != nil && *inserted.TeamID != *retrieved.TeamID {
		t.Errorf("TeamID: %q != %q", *inserted.TeamID, *retrieved.TeamID)
	}
	if inserted.OwnerUserID != retrieved.OwnerUserID {
		t.Errorf("OwnerUserID: %q != %q", inserted.OwnerUserID, retrieved.OwnerUserID)
	}
	if inserted.CreatedAt != retrieved.CreatedAt {
		t.Errorf("CreatedAt: %q != %q", inserted.CreatedAt, retrieved.CreatedAt)
	}
	if inserted.UpdatedAt != retrieved.UpdatedAt {
		t.Errorf("UpdatedAt: %q != %q", inserted.UpdatedAt, retrieved.UpdatedAt)
	}
}

// ==========================================================================
// InsertWorkspaceToken + ListWorkspaceTokens roundtrip test
// ==========================================================================

func TestTokenInsertAndListRoundtrip(t *testing.T) {
	db := openTestDB(t)
	insertTestUser(t, db, "user-001")
	store := NewStore(db)

	ws, _ := store.InsertWorkspace(ctx(), Workspace{
		Slug: "tok-roundtrip-ws", GitURL: "https://x.com", OwnerUserID: "user-001",
	})

	label := "test-label"
	expiresAt := formatTime(time.Now().UTC().Add(30 * 24 * time.Hour))
	_, err := store.InsertWorkspaceToken(ctx(), WorkspaceToken{
		TokenID:     "rtTok001",
		SecretHash:  "hashvalue0000000000000000000000000000000000000000000000000000000",
		WorkspaceID: ws.ID,
		UserID:      "user-001",
		Label:       &label,
		ExpiresAt:   &expiresAt,
	})
	if err != nil {
		t.Fatalf("InsertWorkspaceToken: %v", err)
	}

	tokens, err := store.ListWorkspaceTokens(ctx(), ws.ID)
	if err != nil {
		t.Fatalf("ListWorkspaceTokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}

	tok := tokens[0]
	if tok.TokenID != "rtTok001" {
		t.Errorf("TokenID = %q, want %q", tok.TokenID, "rtTok001")
	}
	if tok.Label == nil || *tok.Label != "test-label" {
		t.Errorf("Label = %v, want %q", tok.Label, "test-label")
	}
	if tok.RevokedAt != nil {
		t.Errorf("RevokedAt = %v, want nil", tok.RevokedAt)
	}
}
