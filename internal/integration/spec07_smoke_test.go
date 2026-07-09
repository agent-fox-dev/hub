// Package integration_test contains smoke tests for the spec 07 workspace
// entity. These tests exercise the four execution paths defined in the
// spec with real components (Echo server, auth middleware, workspace V2
// handler, SQLite store).
//
// Test IDs map to the test specification:
//   TS-07-SMOKE-1: Happy path workspace creation via REST API
//   TS-07-SMOKE-2: Workspace creation with team slug resolution (server-side path)
//   TS-07-SMOKE-3: Slug conflict rejection
//   TS-07-SMOKE-4: Admin token rejection
package integration_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/handler"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test infrastructure for spec 07 smoke tests
// ---------------------------------------------------------------------------

// spec07TestEnv holds the test infrastructure for spec 07 smoke tests.
// It includes the V2 handler wired exactly as server.go does, plus
// direct DB access for seeding and verification.
type spec07TestEnv struct {
	Echo  *echo.Echo
	Store store.Store
	DB    *sql.DB
}

// setupSpec07TestEnv creates a fully wired test environment matching the
// production server.go wiring for spec 07. This includes:
//   - Auth middleware on /api/v1 group
//   - WorkspaceV2Handler on POST /api/v1/workspaces (NOT admin-only)
//   - Legacy workspace routes (GET, archive, delete, members) as admin-only
//   - Full database schema with teams, team_members, and workspaces tables
func setupSpec07TestEnv(t *testing.T) *spec07TestEnv {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "spec07.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("setupSpec07TestEnv: open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("setupSpec07TestEnv: enable WAL: %v", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("setupSpec07TestEnv: enable FK: %v", err)
	}

	// Create all tables matching the production schema in db.go order.
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
		`CREATE TABLE IF NOT EXISTS teams (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			slug TEXT UNIQUE NOT NULL,
			url TEXT UNIQUE NOT NULL,
			status TEXT DEFAULT 'active',
			created_at TEXT,
			created_by TEXT REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS team_members (
			team_id TEXT REFERENCES teams(id),
			user_id TEXT REFERENCES users(id),
			role TEXT NOT NULL,
			created_at TEXT,
			granted_by TEXT REFERENCES users(id),
			PRIMARY KEY (user_id, team_id)
		)`,
		// api_keys — uses workspace_id because the store's CreateAPIKey
		// method has not yet been updated to use team_id (pre-existing
		// spec 06 rename gap). FK constraints are relaxed to match.
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			key_id TEXT UNIQUE,
			key_hash TEXT,
			user_id TEXT,
			workspace_id TEXT,
			role TEXT,
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
		// New workspaces table per spec 07-REQ-1.1 — with FK enforcement.
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
			t.Fatalf("setupSpec07TestEnv: schema: %v\nStatement: %s", err, stmt)
		}
	}

	s := store.NewStore(db)

	// Wire Echo exactly like server.go does for spec 07.
	e := echo.New()
	e.HTTPErrorHandler = handler.CustomHTTPErrorHandler
	e.Use(middleware.BodyLimit("1M"))

	// Protected routes with auth middleware.
	apiGroup := e.Group("/api/v1", auth.AuthMiddleware(s))

	// Spec 07 workspace V2 route — NOT admin-only; handler checks auth.
	workspaceV2Handler := handler.NewWorkspaceV2Handler(s)
	apiGroup.POST("/workspaces", workspaceV2Handler.CreateWorkspaceV2)

	// Legacy workspace routes (admin-only) — for list, archive, etc.
	workspaceHandler := handler.NewWorkspaceHandler(s)
	adminGroup := apiGroup.Group("", auth.RequireRole(auth.RoleAdmin))
	adminGroup.GET("/workspaces", workspaceHandler.ListWorkspaces)
	adminGroup.POST("/workspaces/:id/archive", workspaceHandler.ArchiveWorkspace)
	adminGroup.POST("/workspaces/:id/reactivate", workspaceHandler.ReactivateWorkspace)
	adminGroup.DELETE("/workspaces/:id", workspaceHandler.DeleteWorkspace)
	adminGroup.POST("/workspaces/:id/members", workspaceHandler.AddOrUpdateMember)
	adminGroup.GET("/workspaces/:id/members", workspaceHandler.ListMembers)

	return &spec07TestEnv{Echo: e, Store: s, DB: db}
}

// spec07WorkspaceResponse represents a workspace JSON response per spec 07.
type spec07WorkspaceResponse struct {
	ID        string  `json:"id"`
	Slug      string  `json:"slug"`
	GitURL    string  `json:"git_url"`
	Branch    *string `json:"branch"`
	OwnerID   string  `json:"owner_id"`
	TeamID    *string `json:"team_id"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
}

// seedSpec07User creates a user in the test database.
func seedSpec07User(t *testing.T, s store.Store, username string) *store.User {
	t.Helper()
	user, err := s.CreateUser(&store.User{
		Username:   username,
		Email:      username + "@test.com",
		Provider:   "local",
		ProviderID: username + "_pid",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("seedSpec07User(%q): %v", username, err)
	}
	return user
}

// seedSpec07APIKey creates an API key for the given user.
func seedSpec07APIKey(t *testing.T, s store.Store, userID, keyID, secret, role string) {
	t.Helper()
	_, err := s.CreateAPIKey(&store.APIKey{
		KeyID:   keyID,
		KeyHash: sha256HexString(secret),
		UserID:  userID,
		Role:    role,
		Label:   "test key " + keyID,
	})
	if err != nil {
		t.Fatalf("seedSpec07APIKey(%q): %v", keyID, err)
	}
}

// seedSpec07Team creates a team in the test database and returns its UUID.
func seedSpec07Team(t *testing.T, db *sql.DB, slug, createdBy string) string {
	t.Helper()
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO teams (id, name, slug, url, status, created_at, created_by)
		 VALUES (?, ?, ?, ?, 'active', ?, ?)`,
		id, "team-"+slug, slug, "https://"+slug+".example.com", now, createdBy,
	)
	if err != nil {
		t.Fatalf("seedSpec07Team(%q): %v", slug, err)
	}
	return id
}

// seedSpec07TeamMember adds a user as a member of a team.
func seedSpec07TeamMember(t *testing.T, db *sql.DB, userID, teamID, role string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO team_members (user_id, team_id, role, created_at)
		 VALUES (?, ?, ?, ?)`,
		userID, teamID, role, now,
	)
	if err != nil {
		t.Fatalf("seedSpec07TeamMember(user=%s, team=%s): %v", userID, teamID, err)
	}
}

// seedSpec07AdminToken creates an admin token in the store.
func seedSpec07AdminToken(t *testing.T, s store.Store, fullToken string) {
	t.Helper()
	_, err := s.CreateAdminToken(&store.AdminToken{
		TokenHash: sha256HexString(fullToken),
	})
	if err != nil {
		t.Fatalf("seedSpec07AdminToken: %v", err)
	}
}

// spec07APIKeyHeaders returns Authorization headers for an API key.
func spec07APIKeyHeaders(keyID, secret string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer af_" + keyID + "_" + secret,
	}
}

// ===========================================================================
// TS-07-SMOKE-1: Happy-path workspace creation via REST API
// Execution Path: 07-PATH-1
//
// Valid API key, slug, git_url, optional branch and team_id → HTTP 201 with
// complete workspace JSON; DB row exists with correct values.
// ===========================================================================

func TestSmoke_WorkspaceV2_HappyPath(t *testing.T) {
	env := setupSpec07TestEnv(t)

	// Seed a user and team; make user a team member.
	user := seedSpec07User(t, env.Store, "smoke-v2-user")
	seedSpec07APIKey(t, env.Store, user.ID, "smokekey1", "smokesecret1", "editor")
	teamID := seedSpec07Team(t, env.DB, "smoke-team", user.ID)
	seedSpec07TeamMember(t, env.DB, user.ID, teamID, "editor")

	// POST /api/v1/workspaces with valid slug, git_url, branch, and team_id.
	body := fmt.Sprintf(
		`{"slug":"smoke-api","git_url":"https://github.com/org/repo.git","branch":"main","team_id":"%s"}`,
		teamID,
	)
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		spec07APIKeyHeaders("smokekey1", "smokesecret1"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var ws spec07WorkspaceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil {
		t.Fatalf("failed to parse response JSON: %v\nBody: %s", err, rec.Body.String())
	}

	// Verify all response fields.
	if _, err := uuid.Parse(ws.ID); err != nil {
		t.Errorf("id %q is not a valid UUID: %v", ws.ID, err)
	}
	if ws.Slug != "smoke-api" {
		t.Errorf("slug = %q, want %q", ws.Slug, "smoke-api")
	}
	if ws.GitURL != "https://github.com/org/repo.git" {
		t.Errorf("git_url = %q, want %q", ws.GitURL, "https://github.com/org/repo.git")
	}
	if ws.Branch == nil || *ws.Branch != "main" {
		t.Errorf("branch = %v, want %q", ws.Branch, "main")
	}
	if ws.OwnerID != user.ID {
		t.Errorf("owner_id = %q, want %q", ws.OwnerID, user.ID)
	}
	if ws.TeamID == nil || *ws.TeamID != teamID {
		t.Errorf("team_id = %v, want %q", ws.TeamID, teamID)
	}
	if ws.Status != "active" {
		t.Errorf("status = %q, want %q", ws.Status, "active")
	}
	if ws.CreatedAt == "" {
		t.Error("created_at should not be empty")
	}

	// Verify DB row exists with matching values.
	var dbSlug, dbGitURL, dbOwnerID, dbStatus string
	var dbBranch, dbTeamID sql.NullString
	err := env.DB.QueryRow(
		"SELECT slug, git_url, branch, owner_id, team_id, status FROM workspaces WHERE id = ?",
		ws.ID,
	).Scan(&dbSlug, &dbGitURL, &dbBranch, &dbOwnerID, &dbTeamID, &dbStatus)
	if err != nil {
		t.Fatalf("DB query for workspace row failed: %v", err)
	}
	if dbSlug != "smoke-api" {
		t.Errorf("DB slug = %q, want %q", dbSlug, "smoke-api")
	}
	if dbStatus != "active" {
		t.Errorf("DB status = %q, want %q", dbStatus, "active")
	}
	if !dbTeamID.Valid || dbTeamID.String != teamID {
		t.Errorf("DB team_id = %v, want %q", dbTeamID, teamID)
	}
}

// ===========================================================================
// TS-07-SMOKE-2: Workspace creation with team association (server-side path)
// Execution Path: 07-PATH-2
//
// This tests the server-side portion of the CLI flow: POST /api/v1/workspaces
// with a resolved team_id. The CLI binary tests (internal/cli/) cover the
// full CLI → team slug resolution → POST flow.
// ===========================================================================

func TestSmoke_WorkspaceV2_WithTeamAssociation(t *testing.T) {
	env := setupSpec07TestEnv(t)

	user := seedSpec07User(t, env.Store, "cli-ws-user")
	seedSpec07APIKey(t, env.Store, user.ID, "clikey1", "clisecret1", "editor")
	teamID := seedSpec07Team(t, env.DB, "existing-team-slug", user.ID)
	seedSpec07TeamMember(t, env.DB, user.ID, teamID, "editor")

	// Simulate what the CLI does after resolving team slug to UUID:
	// POST /api/v1/workspaces with the resolved team_id.
	body := fmt.Sprintf(
		`{"slug":"cli-ws","git_url":"https://github.com/org/repo.git","team_id":"%s"}`,
		teamID,
	)
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		spec07APIKeyHeaders("clikey1", "clisecret1"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var ws spec07WorkspaceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	if ws.Slug != "cli-ws" {
		t.Errorf("slug = %q, want %q", ws.Slug, "cli-ws")
	}
	if ws.TeamID == nil || *ws.TeamID != teamID {
		t.Errorf("team_id = %v, want %q", ws.TeamID, teamID)
	}
	if ws.OwnerID != user.ID {
		t.Errorf("owner_id = %q, want %q", ws.OwnerID, user.ID)
	}

	// Verify the DB row has the correct team_id.
	var dbTeamID sql.NullString
	err := env.DB.QueryRow(
		"SELECT team_id FROM workspaces WHERE slug = ?", "cli-ws",
	).Scan(&dbTeamID)
	if err != nil {
		t.Fatalf("DB query failed: %v", err)
	}
	if !dbTeamID.Valid || dbTeamID.String != teamID {
		t.Errorf("DB team_id = %v, want %q", dbTeamID, teamID)
	}
}

// ===========================================================================
// TS-07-SMOKE-3: Slug conflict rejection
// Execution Path: 07-PATH-3
//
// POST with a duplicate slug → HTTP 409 with standard error envelope;
// no new row inserted; existing row unmodified.
// ===========================================================================

func TestSmoke_WorkspaceV2_SlugConflict(t *testing.T) {
	env := setupSpec07TestEnv(t)

	user := seedSpec07User(t, env.Store, "dup-smoke-user")
	seedSpec07APIKey(t, env.Store, user.ID, "dupkey1", "dupsecret1", "editor")

	// Pre-insert a workspace with the slug that will be duplicated.
	existingID := uuid.New().String()
	_, err := env.DB.Exec(
		`INSERT INTO workspaces (id, slug, git_url, owner_id, status, created_at)
		 VALUES (?, 'dup-ws', 'https://github.com/org/original.git', ?, 'active', datetime('now'))`,
		existingID, user.ID,
	)
	if err != nil {
		t.Fatalf("failed to pre-insert workspace: %v", err)
	}

	// Attempt to create a workspace with the same slug.
	body := `{"slug":"dup-ws","git_url":"https://github.com/org/different.git"}`
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		spec07APIKeyHeaders("dupkey1", "dupsecret1"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	var errResp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error JSON: %v\nBody: %s", err, rec.Body.String())
	}
	if errResp.Error.Code != "409" {
		t.Errorf("error.code = %q, want %q", errResp.Error.Code, "409")
	}
	if errResp.Error.Message != "workspace slug already exists" {
		t.Errorf("error.message = %q, want %q", errResp.Error.Message, "workspace slug already exists")
	}

	// Verify no new row was inserted.
	var count int
	err = env.DB.QueryRow("SELECT count(*) FROM workspaces WHERE slug = 'dup-ws'").Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("workspace count = %d, want 1 (original only)", count)
	}

	// Verify existing row is unchanged.
	var dbID string
	err = env.DB.QueryRow("SELECT id FROM workspaces WHERE slug = 'dup-ws'").Scan(&dbID)
	if err != nil {
		t.Fatalf("id query failed: %v", err)
	}
	if dbID != existingID {
		t.Errorf("existing workspace id = %q, want %q (unchanged)", dbID, existingID)
	}
}

// ===========================================================================
// TS-07-SMOKE-4: Admin token rejection
// Execution Path: 07-PATH-4
//
// POST with an admin token → HTTP 403 with "workspace creation requires user
// authentication"; workspace store CreateWorkspaceV2 is never invoked; no
// new row in workspaces table.
// ===========================================================================

func TestSmoke_WorkspaceV2_AdminTokenRejected(t *testing.T) {
	env := setupSpec07TestEnv(t)

	// Seed an admin token (no real user associated).
	adminToken := "af_admin_smoke07admin"
	seedSpec07AdminToken(t, env.Store, adminToken)

	// Count workspace rows before the request.
	var countBefore int
	if err := env.DB.QueryRow("SELECT count(*) FROM workspaces").Scan(&countBefore); err != nil {
		t.Fatalf("count query failed: %v", err)
	}

	// Attempt workspace creation with admin token.
	body := `{"slug":"admin-ws","git_url":"https://github.com/org/repo.git"}`
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		map[string]string{"Authorization": "Bearer " + adminToken})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var errResp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error JSON: %v\nBody: %s", err, rec.Body.String())
	}
	if errResp.Error.Code != "403" {
		t.Errorf("error.code = %q, want %q", errResp.Error.Code, "403")
	}
	if errResp.Error.Message != "workspace creation requires user authentication" {
		t.Errorf("error.message = %q, want %q",
			errResp.Error.Message, "workspace creation requires user authentication")
	}

	// Verify no workspace row was inserted.
	var countAfter int
	if err := env.DB.QueryRow("SELECT count(*) FROM workspaces").Scan(&countAfter); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if countAfter != countBefore {
		t.Errorf("workspace count changed from %d to %d; store should never be called",
			countBefore, countAfter)
	}
}
