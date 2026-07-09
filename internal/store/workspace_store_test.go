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

// newWorkspaceV2TestStore creates a fresh in-memory database with the
// spec 07 schema (including teams table for FK dependency) and returns
// a store backed by it.
//
// The schema used here matches the NEW workspace entity definition from
// spec 07-REQ-1.1, NOT the legacy workspaces table. This helper creates
// the teams table that spec 06 will rename, plus the new workspaces table.
func newWorkspaceV2TestStore(t *testing.T) *sqliteStore {
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

	// Enable FK enforcement — SQLite disables it by default.
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("failed to enable foreign_keys: %v", err)
	}

	// Create the spec 07 schema: users, teams (spec 06), and new workspaces.
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
		// teams table — renamed from old workspaces by spec 06.
		`CREATE TABLE IF NOT EXISTS teams (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			slug TEXT UNIQUE NOT NULL,
			status TEXT DEFAULT 'active',
			created_at TEXT,
			created_by TEXT REFERENCES users(id)
		)`,
		// team_members — membership association for team access checks.
		`CREATE TABLE IF NOT EXISTS team_members (
			user_id TEXT REFERENCES users(id),
			team_id TEXT REFERENCES teams(id),
			role TEXT NOT NULL,
			created_at TEXT,
			granted_by TEXT REFERENCES users(id),
			PRIMARY KEY (user_id, team_id)
		)`,
		// New workspaces table per spec 07-REQ-1.1.
		`CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT PRIMARY KEY,
			slug TEXT UNIQUE NOT NULL,
			git_url TEXT NOT NULL,
			branch TEXT,
			owner_id TEXT NOT NULL REFERENCES users(id),
			team_id TEXT REFERENCES teams(id),
			status TEXT NOT NULL DEFAULT 'active',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create schema: %v", err)
		}
	}

	return NewStore(db)
}

// seedTestUser creates a user in the test database and returns its ID.
func seedTestUser(t *testing.T, s *sqliteStore, username string) string {
	t.Helper()
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(
		`INSERT INTO users (id, username, email, provider, provider_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, 'local', ?, 'active', ?, ?)`,
		id, username, username+"@test.com", username+"_pid", now, now,
	)
	if err != nil {
		t.Fatalf("seedTestUser(%q) failed: %v", username, err)
	}
	return id
}

// seedTestTeam creates a team in the test database and returns its ID.
func seedTestTeam(t *testing.T, s *sqliteStore, slug, createdBy string) string {
	t.Helper()
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(
		`INSERT INTO teams (id, name, slug, status, created_at, created_by)
		 VALUES (?, ?, ?, 'active', ?, ?)`,
		id, "team-"+slug, slug, now, createdBy,
	)
	if err != nil {
		t.Fatalf("seedTestTeam(%q) failed: %v", slug, err)
	}
	return id
}

// seedWorkspaceV2 inserts a workspace row directly using the spec 07 schema.
func seedWorkspaceV2(t *testing.T, s *sqliteStore, slug, gitURL, ownerID string) string {
	t.Helper()
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(
		`INSERT INTO workspaces (id, slug, git_url, owner_id, status, created_at)
		 VALUES (?, ?, ?, ?, 'active', ?)`,
		id, slug, gitURL, ownerID, now,
	)
	if err != nil {
		t.Fatalf("seedWorkspaceV2(%q) failed: %v", slug, err)
	}
	return id
}

// TS-07-4: Verifies CreateWorkspace inserts a workspace record and returns
// a fully populated WorkspaceV2 struct with auto-generated UUID and defaults.
func TestCreateWorkspaceV2_Success(t *testing.T) {
	s := newWorkspaceV2TestStore(t)
	userID := seedTestUser(t, s, "ws-owner")

	ws, err := s.CreateWorkspaceV2(CreateWorkspaceParams{
		Slug:    "new-ws",
		GitURL:  "https://github.com/org/repo.git",
		Branch:  nil,
		OwnerID: userID,
		TeamID:  nil,
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceV2 returned error: %v", err)
	}
	if ws == nil {
		t.Fatal("CreateWorkspaceV2 returned nil workspace")
	}

	// Verify auto-generated UUID.
	if _, err := uuid.Parse(ws.ID); err != nil {
		t.Errorf("workspace ID %q is not a valid UUID: %v", ws.ID, err)
	}

	// Verify status defaults to 'active'.
	if ws.Status != "active" {
		t.Errorf("expected status 'active', got %q", ws.Status)
	}

	// Verify created_at is non-zero and a valid timestamp.
	if ws.CreatedAt == "" {
		t.Error("expected non-empty created_at")
	} else if _, err := time.Parse(time.RFC3339, ws.CreatedAt); err != nil {
		t.Errorf("created_at %q is not valid RFC 3339: %v", ws.CreatedAt, err)
	}

	// Verify field values match input.
	if ws.Slug != "new-ws" {
		t.Errorf("expected slug 'new-ws', got %q", ws.Slug)
	}
	if ws.GitURL != "https://github.com/org/repo.git" {
		t.Errorf("expected git_url 'https://github.com/org/repo.git', got %q", ws.GitURL)
	}
	if ws.OwnerID != userID {
		t.Errorf("expected owner_id %q, got %q", userID, ws.OwnerID)
	}

	// Verify nullable fields are nil when not provided.
	if ws.Branch != nil {
		t.Errorf("expected nil branch, got %q", *ws.Branch)
	}
	if ws.TeamID != nil {
		t.Errorf("expected nil team_id, got %q", *ws.TeamID)
	}
}

// TS-07-4 continued: Verify CreateWorkspaceV2 with optional branch and team_id.
func TestCreateWorkspaceV2_WithBranchAndTeam(t *testing.T) {
	s := newWorkspaceV2TestStore(t)
	userID := seedTestUser(t, s, "ws-team-owner")
	teamID := seedTestTeam(t, s, "my-team", userID)

	branch := "main"
	ws, err := s.CreateWorkspaceV2(CreateWorkspaceParams{
		Slug:    "team-ws",
		GitURL:  "git@github.com:org/repo.git",
		Branch:  &branch,
		OwnerID: userID,
		TeamID:  &teamID,
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceV2 returned error: %v", err)
	}
	if ws == nil {
		t.Fatal("CreateWorkspaceV2 returned nil workspace")
	}

	if ws.Branch == nil {
		t.Error("expected non-nil branch")
	} else if *ws.Branch != "main" {
		t.Errorf("expected branch 'main', got %q", *ws.Branch)
	}

	if ws.TeamID == nil {
		t.Error("expected non-nil team_id")
	} else if *ws.TeamID != teamID {
		t.Errorf("expected team_id %q, got %q", teamID, *ws.TeamID)
	}
}

// TS-07-5: Verifies CreateWorkspace returns a distinct typed duplicate-slug
// error (not a generic error) when a slug already exists.
func TestCreateWorkspaceV2_DuplicateSlugError(t *testing.T) {
	s := newWorkspaceV2TestStore(t)
	userID := seedTestUser(t, s, "dup-slug-user")

	// Seed existing workspace with slug 'existing-ws'.
	seedWorkspaceV2(t, s, "existing-ws", "https://github.com/org/repo.git", userID)

	// Attempt to create another workspace with the same slug.
	ws, err := s.CreateWorkspaceV2(CreateWorkspaceParams{
		Slug:    "existing-ws",
		GitURL:  "https://github.com/org/other.git",
		OwnerID: userID,
	})
	if ws != nil {
		t.Error("expected nil workspace on duplicate slug, got non-nil")
	}
	if err == nil {
		t.Fatal("expected error on duplicate slug, got nil")
	}

	// Must be the typed ErrDuplicateSlug sentinel, not a generic error.
	if !errors.Is(err, ErrDuplicateSlug) {
		t.Errorf("expected ErrDuplicateSlug, got: %v", err)
	}

	// Verify only one row exists with this slug.
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM workspaces WHERE slug='existing-ws'").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row with slug='existing-ws', got %d", count)
	}
}

// TS-07-6: Verifies the workspace store never calls os.Exit or log.Fatal;
// all errors are returned as Go error values to the caller.
func TestCreateWorkspaceV2_ReturnsErrorOnDBFailure(t *testing.T) {
	s := newWorkspaceV2TestStore(t)

	// Close the database to force errors on any DB operation.
	s.DB().Close()

	ws, err := s.CreateWorkspaceV2(CreateWorkspaceParams{
		Slug:    "any-ws",
		GitURL:  "https://github.com/org/repo.git",
		OwnerID: "user-uuid-1",
	})
	if ws != nil {
		t.Error("expected nil workspace when DB is closed")
	}
	if err == nil {
		t.Fatal("expected error when DB is closed, got nil")
	}

	// If we reach this point, the process did not exit — os.Exit/log.Fatal
	// were not called. The error should:
	// 1. Contain operation context (e.g. "store: create workspace").
	// 2. Wrap the underlying database error (not be a generic "not implemented").
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "store:") {
		t.Errorf("error should be wrapped with store context prefix 'store:', got: %s", err.Error())
	}
}

// TS-07-E3: Verifies CreateWorkspace returns a wrapped database error without
// retrying when the SQLite database is locked or unavailable.
func TestCreateWorkspaceV2_LockedDB(t *testing.T) {
	s := newWorkspaceV2TestStore(t)

	// Close the database to simulate unavailability.
	// (A true lock simulation would require a concurrent writer holding a
	// write lock, but closing the DB is a reliable way to produce a DB error.)
	s.DB().Close()

	ws, err := s.CreateWorkspaceV2(CreateWorkspaceParams{
		Slug:    "locked-ws",
		GitURL:  "https://github.com/org/repo.git",
		OwnerID: "user-uuid-1",
	})
	if ws != nil {
		t.Error("expected nil workspace on DB failure")
	}
	if err == nil {
		t.Fatal("expected error on DB failure, got nil")
	}

	// Verify the error is NOT a duplicate slug error — it's a DB-level error.
	if errors.Is(err, ErrDuplicateSlug) {
		t.Error("expected a database error, not ErrDuplicateSlug")
	}

	// Verify the error is wrapped with store context.
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "store:") {
		t.Errorf("error should be wrapped with store context prefix 'store:', got: %s", err.Error())
	}
}

// TS-07-E4: Verifies CreateWorkspace returns a foreign key constraint error
// without inserting a workspace row when team_id references a non-existent team.
func TestCreateWorkspaceV2_NonexistentTeamFK(t *testing.T) {
	s := newWorkspaceV2TestStore(t)
	userID := seedTestUser(t, s, "fk-test-user")

	nonexistentTeamID := "nonexistent-team-id"
	ws, err := s.CreateWorkspaceV2(CreateWorkspaceParams{
		Slug:    "fk-test-ws",
		GitURL:  "https://github.com/org/repo.git",
		OwnerID: userID,
		TeamID:  &nonexistentTeamID,
	})
	if ws != nil {
		t.Error("expected nil workspace on FK violation")
	}
	if err == nil {
		t.Fatal("expected error on FK violation for nonexistent team_id, got nil")
	}

	// Error should indicate a constraint/foreign key issue.
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "foreign key") && !strings.Contains(errMsg, "constraint") {
		t.Errorf("expected FK or constraint error, got: %s", err.Error())
	}

	// Verify no row was inserted.
	var count int
	if queryErr := s.db.QueryRow("SELECT count(*) FROM workspaces WHERE slug='fk-test-ws'").Scan(&count); queryErr != nil {
		// DB might be in a bad state if previous error caused issues;
		// but for FK violations the DB should still be usable.
		t.Logf("count query failed (DB may be closed): %v", queryErr)
	} else if count != 0 {
		t.Errorf("expected 0 rows with slug='fk-test-ws' after FK violation, got %d", count)
	}
}
