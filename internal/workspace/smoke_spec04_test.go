package workspace

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ========================================================================
// Smoke test helpers for spec 04 (personal_org)
// ========================================================================

// openSmokeDB creates an in-memory SQLite database with users, orgs, and
// org_members tables suitable for end-to-end smoke testing of the personal
// org hook. This combines apikit-style schema (users table) with the
// workspace schema.
func openSmokeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT NOT NULL PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL,
			full_name TEXT,
			role TEXT NOT NULL DEFAULT 'user',
			status TEXT NOT NULL DEFAULT 'active',
			provider TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS orgs (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			url TEXT,
			owner_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS org_members (
			org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (org_id, user_id)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema init: %v", err)
		}
	}

	// Initialize workspace schema.
	if err := initSchema(db); err != nil {
		t.Fatalf("init workspace schema: %v", err)
	}

	return db
}

// smokeInsertUser inserts a user row and returns the user ID, simulating
// the first half of what apikit's handleCallback / createUser does.
func smokeInsertUser(t *testing.T, tx *sql.Tx, userID, username, email string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.Exec(
		`INSERT INTO users (id, username, email, full_name, role, status, provider, provider_id, created_at, updated_at)
		 VALUES (?, ?, ?, '', 'user', 'active', 'github', ?, ?, ?)`,
		userID, username, email, username+"-pid", now, now,
	)
	if err != nil {
		t.Fatalf("smokeInsertUser(%q): %v", username, err)
	}
}

// ========================================================================
// TS-04-SMOKE-1: End-to-end smoke test: a new user completes OAuth login
// and a personal org is atomically created alongside the user row.
//
// Execution Path: 04-PATH-1
//
// This test simulates the OAuth callback new-user branch by:
// 1. Beginning a transaction (like handleCallback does)
// 2. Inserting a user row (like handleCallback does)
// 3. Calling CreatePersonalOrg (the real hub hook)
// 4. Committing the transaction
// 5. Verifying all rows exist atomically
//
// Real components: hub personal org CreatePersonalOrg hook, SQLite database
// (users, orgs, org_members tables)
// Mockable: OAuth provider (simulated by direct user INSERT)
// ========================================================================

func TestSmoke_OAuthNewUserPersonalOrg(t *testing.T) {
	db := openSmokeDB(t)

	// Simulate OAuth callback: begin tx, insert user, call hook, commit.
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	userID := "smoke-alice-001"
	smokeInsertUser(t, tx, userID, "smoketest-alice", "alice@smoke.test")

	// Call the real hook within the transaction.
	if err := CreatePersonalOrg(context.Background(), tx, userID, "smoketest-alice", "alice@smoke.test"); err != nil {
		tx.Rollback()
		t.Fatalf("CreatePersonalOrg: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Verify users table has exactly one row for 'smoketest-alice'.
	var userCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", "smoketest-alice").Scan(&userCount); err != nil {
		t.Fatalf("query users: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("user count = %d; want 1", userCount)
	}

	// Verify orgs table has exactly one row with owner_id = userID and
	// slug = 'smoketest-alice'.
	var orgCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM orgs WHERE owner_id = ?", userID).Scan(&orgCount); err != nil {
		t.Fatalf("query orgs: %v", err)
	}
	if orgCount != 1 {
		t.Fatalf("org count = %d; want 1", orgCount)
	}

	var orgID, orgSlug, orgName, orgOwnerID, orgStatus string
	err = db.QueryRow(
		"SELECT id, slug, name, owner_id, status FROM orgs WHERE owner_id = ?", userID,
	).Scan(&orgID, &orgSlug, &orgName, &orgOwnerID, &orgStatus)
	if err != nil {
		t.Fatalf("query org: %v", err)
	}
	if orgSlug != "smoketest-alice" {
		t.Errorf("org slug = %q; want %q", orgSlug, "smoketest-alice")
	}
	if orgName != "smoketest-alice" {
		t.Errorf("org name = %q; want %q", orgName, "smoketest-alice")
	}
	if orgOwnerID != userID {
		t.Errorf("org owner_id = %q; want %q", orgOwnerID, userID)
	}
	if orgStatus != "active" {
		t.Errorf("org status = %q; want %q", orgStatus, "active")
	}

	// Verify org_members table has one row linking user to the new org.
	var memberCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?",
		orgID, userID,
	).Scan(&memberCount); err != nil {
		t.Fatalf("query org_members: %v", err)
	}
	if memberCount != 1 {
		t.Errorf("org_members count = %d; want 1", memberCount)
	}

	// All three rows are in the database simultaneously (atomicity verified
	// by the fact that they were all inserted within a single committed tx).
}

// ========================================================================
// TS-04-SMOKE-2: End-to-end smoke test: admin creates a new user via
// POST /users and a personal org is created atomically.
//
// Execution Path: 04-PATH-2
//
// This test simulates the admin createUser handler by:
// 1. Beginning a transaction (like the handler does after spec 04)
// 2. Inserting a user row
// 3. Calling CreatePersonalOrg (the real hub hook)
// 4. Committing the transaction
// 5. Verifying HTTP 201 equivalent state (all rows exist)
//
// Real components: hub personal org CreatePersonalOrg hook, SQLite database
// Mockable: Admin authentication middleware (bypassed)
// ========================================================================

func TestSmoke_AdminCreateUserPersonalOrg(t *testing.T) {
	db := openSmokeDB(t)

	// Simulate admin createUser: begin tx, insert user, call hook, commit.
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	userID := "smoke-bob-001"
	smokeInsertUser(t, tx, userID, "smoketest-bob", "bob@smoke.test")

	// Call the real hook within the transaction.
	if err := CreatePersonalOrg(context.Background(), tx, userID, "smoketest-bob", "bob@smoke.test"); err != nil {
		tx.Rollback()
		t.Fatalf("CreatePersonalOrg: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Verify users table has one row for 'smoketest-bob'.
	var userCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", "smoketest-bob").Scan(&userCount); err != nil {
		t.Fatalf("query users: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("user count = %d; want 1", userCount)
	}

	// Verify orgs table has one row with owner_id = userID.
	var orgName, orgSlug, orgOwnerID, orgStatus string
	err = db.QueryRow(
		"SELECT name, slug, owner_id, status FROM orgs WHERE owner_id = ?", userID,
	).Scan(&orgName, &orgSlug, &orgOwnerID, &orgStatus)
	if err != nil {
		t.Fatalf("query org: %v", err)
	}
	if orgName != "smoketest-bob" {
		t.Errorf("org name = %q; want %q", orgName, "smoketest-bob")
	}
	if orgSlug != "smoketest-bob" {
		t.Errorf("org slug = %q; want %q", orgSlug, "smoketest-bob")
	}
	if orgOwnerID != userID {
		t.Errorf("org owner_id = %q; want %q", orgOwnerID, userID)
	}
	if orgStatus != "active" {
		t.Errorf("org status = %q; want %q", orgStatus, "active")
	}

	// Verify org_members row.
	var orgID string
	if err := db.QueryRow("SELECT id FROM orgs WHERE owner_id = ?", userID).Scan(&orgID); err != nil {
		t.Fatalf("query org id: %v", err)
	}
	var memberCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?",
		orgID, userID,
	).Scan(&memberCount); err != nil {
		t.Fatalf("query org_members: %v", err)
	}
	if memberCount != 1 {
		t.Errorf("org_members count = %d; want 1", memberCount)
	}
}

// ========================================================================
// TS-04-SMOKE-3: End-to-end smoke test: workspace creation without --org
// automatically associates the workspace with the user's personal org.
//
// Execution Path: 04-PATH-3
//
// Real components: hub handleCreateWorkspace handler, SQLite database
//   (orgs, workspaces tables)
// Mockable: Authentication middleware (inject test user with known
//   personal org id), Workspace git validation
// ========================================================================

func TestSmoke_WorkspaceAutoAssociatesPersonalOrg(t *testing.T) {
	env := newAutoOrgTestEnv(t)

	// Seed a personal org for the test user.
	env.seedOrgWithOwner(t, "smoke-org-001", "smokeuser", "smokeuser", "smoke-user-001")
	env.seedOrgMember(t, "smoke-org-001", "smoke-user-001")

	auth := userAuth("smoke-user-001")
	body := `{"slug":"smoke-ws","git_url":"https://github.com/smoke/test"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	// HTTP 201 expected.
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /workspaces status = %d; want %d; body = %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Verify response JSON contains org_id equal to the user's personal org id.
	ws := parseWorkspaceJSON(t, rec)
	if ws.OrgID == nil || *ws.OrgID != "smoke-org-001" {
		t.Errorf("response org_id = %v; want %q", ws.OrgID, "smoke-org-001")
	}

	// Verify workspace row in database has non-null org_id matching personal org.
	var dbOrgID *string
	err := env.db.QueryRow("SELECT org_id FROM workspaces WHERE slug = ?", "smoke-ws").Scan(&dbOrgID)
	if err != nil {
		t.Fatalf("query workspace: %v", err)
	}
	if dbOrgID == nil || *dbOrgID != "smoke-org-001" {
		t.Errorf("DB org_id = %v; want %q", dbOrgID, "smoke-org-001")
	}
}

// ========================================================================
// TS-04-SMOKE-4: Full end-to-end smoke test: new user logs in via OAuth,
// personal org is created, then creates a workspace which is auto-
// associated with the personal org.
//
// Execution Path: 04-PATH-4
//
// This test simulates the full flow:
// 1. OAuth callback creates user + personal org (via CreatePersonalOrg hook)
// 2. Workspace creation without org_id auto-associates with personal org
//
// Real components: hub personal org hook, hub handleCreateWorkspace,
//   SQLite database (users, orgs, org_members, workspaces tables)
// Mockable: OAuth provider token exchange (simulated by direct INSERT),
//   UUID generator (uses real UUIDs)
// ========================================================================

func TestSmoke_FullEndToEnd_OAuthThenWorkspace(t *testing.T) {
	db := openSmokeDB(t)

	// Step 1: Simulate OAuth callback — create user + personal org atomically.
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	userID := "smoke-e2e-001"
	smokeInsertUser(t, tx, userID, "smoketest-e2e", "e2e@smoke.test")

	if err := CreatePersonalOrg(context.Background(), tx, userID, "smoketest-e2e", "e2e@smoke.test"); err != nil {
		tx.Rollback()
		t.Fatalf("CreatePersonalOrg: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// After step 1: verify user, org (slug='smoketest-e2e'), and org_members exist.
	var orgID string
	err = db.QueryRow("SELECT id FROM orgs WHERE owner_id = ?", userID).Scan(&orgID)
	if err != nil {
		t.Fatalf("step 1: personal org not found: %v", err)
	}
	if orgID == "" {
		t.Fatal("step 1: org id is empty")
	}

	var orgSlug string
	if err := db.QueryRow("SELECT slug FROM orgs WHERE id = ?", orgID).Scan(&orgSlug); err != nil {
		t.Fatalf("step 1: query org slug: %v", err)
	}
	if orgSlug != "smoketest-e2e" {
		t.Errorf("step 1: org slug = %q; want %q", orgSlug, "smoketest-e2e")
	}

	var memberCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?",
		orgID, userID,
	).Scan(&memberCount); err != nil {
		t.Fatalf("step 1: member query: %v", err)
	}
	if memberCount != 1 {
		t.Errorf("step 1: org_members count = %d; want 1", memberCount)
	}

	// Step 2: Create workspace without org_id — handler auto-associates.
	e := newEchoWithRoutes(t, db)
	env := &testEnv{echo: e, db: db}

	auth := userAuth(userID)
	wsBody := `{"slug":"e2e-repo","git_url":"https://github.com/e2e/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", wsBody, auth)

	// HTTP 201 expected.
	if rec.Code != http.StatusCreated {
		t.Fatalf("step 2: workspace create status = %d; want %d; body = %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Verify workspace org_id = personal org id.
	ws := parseWorkspaceJSON(t, rec)
	if ws.OrgID == nil || *ws.OrgID != orgID {
		t.Errorf("step 2: workspace org_id = %v; want %q", ws.OrgID, orgID)
	}

	// Verify DB state: workspace row has org_id matching personal org.
	var dbWsOrgID *string
	err = db.QueryRow("SELECT org_id FROM workspaces WHERE slug = ?", "e2e-repo").Scan(&dbWsOrgID)
	if err != nil {
		t.Fatalf("step 2: query workspace: %v", err)
	}
	if dbWsOrgID == nil || *dbWsOrgID != orgID {
		t.Errorf("step 2: DB org_id = %v; want %q", dbWsOrgID, orgID)
	}

	// Final state: user, org, org_members, and workspace all exist and are linked.
	for _, table := range []struct {
		name  string
		query string
		args  []any
	}{
		{"users", "SELECT COUNT(*) FROM users WHERE id = ?", []any{userID}},
		{"orgs", "SELECT COUNT(*) FROM orgs WHERE id = ? AND owner_id = ?", []any{orgID, userID}},
		{"org_members", "SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?", []any{orgID, userID}},
		{"workspaces", "SELECT COUNT(*) FROM workspaces WHERE slug = 'e2e-repo' AND org_id = ?", []any{orgID}},
	} {
		var count int
		if err := db.QueryRow(table.query, table.args...).Scan(&count); err != nil {
			t.Fatalf("verify %s: %v", table.name, err)
		}
		if count != 1 {
			t.Errorf("verify %s: count = %d; want 1", table.name, count)
		}
	}
}

// ========================================================================
// TS-04-SMOKE-5: Smoke test for atomic rollback: when the hook returns an
// error (all 10 slug attempts collide), neither the user row nor any org
// row is persisted.
//
// Execution Path: 04-PATH-5
//
// Real components: hub personal org hook (collision resolution), SQLite
//   database (users, orgs tables)
// Mockable: OAuth provider token exchange (simulated)
// ========================================================================

func TestSmoke_SlugCollisionAtomicRollback(t *testing.T) {
	db := openSmokeDB(t)

	// Pre-populate orgs with slugs: 'collision', 'collision-1', ..., 'collision-10'.
	// This exhausts the base slug plus all 10 suffixed candidates.
	now := time.Now().UTC().Format(time.RFC3339)
	for i := 0; i <= 10; i++ {
		var slug string
		if i == 0 {
			slug = "collision"
		} else {
			slug = fmt.Sprintf("collision-%d", i)
		}
		name := fmt.Sprintf("pre-existing-%s", slug)
		_, err := db.Exec(
			`INSERT INTO orgs (id, name, slug, url, status, created_at, updated_at)
			 VALUES (?, ?, ?, '', 'active', ?, ?)`,
			fmt.Sprintf("pre-org-%s", slug), name, slug, now, now,
		)
		if err != nil {
			t.Fatalf("seed org slug %q: %v", slug, err)
		}
	}

	// Count pre-existing state.
	var preExistingOrgCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM orgs").Scan(&preExistingOrgCount); err != nil {
		t.Fatalf("count orgs: %v", err)
	}
	if preExistingOrgCount != 11 {
		t.Fatalf("pre-existing org count = %d; want 11", preExistingOrgCount)
	}

	// Simulate OAuth callback: begin tx, insert user, call hook.
	// The hook should fail because all slug attempts collide.
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	userID := "smoke-collision-001"
	smokeInsertUser(t, tx, userID, "collision", "collision@smoke.test")

	hookErr := CreatePersonalOrg(context.Background(), tx, userID, "collision", "collision@smoke.test")

	// The hook should return a non-nil error.
	if hookErr == nil {
		// If somehow the hook succeeded, we still need to clean up.
		tx.Commit()
		t.Fatal("CreatePersonalOrg should return error when all slug attempts collide")
	}

	// Rollback the transaction (what handleCallback does on hook error).
	tx.Rollback()

	// Verify no new user row was created (rolled back).
	var userCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", "collision").Scan(&userCount); err != nil {
		t.Fatalf("query user count: %v", err)
	}
	if userCount != 0 {
		t.Errorf("user count = %d; want 0 (transaction should be rolled back)", userCount)
	}

	// Verify no new org row was created for the attempted user.
	var orgCountAfter int
	if err := db.QueryRow("SELECT COUNT(*) FROM orgs").Scan(&orgCountAfter); err != nil {
		t.Fatalf("count orgs after: %v", err)
	}
	if orgCountAfter != preExistingOrgCount {
		t.Errorf("org count after = %d; want %d (pre-existing only, no new rows)",
			orgCountAfter, preExistingOrgCount)
	}

	// Verify no org_members row was created.
	var memberCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM org_members WHERE user_id = ?", userID).Scan(&memberCount); err != nil {
		t.Fatalf("query member count: %v", err)
	}
	if memberCount != 0 {
		t.Errorf("org_members count = %d; want 0 (transaction should be rolled back)", memberCount)
	}

	// Database is in the same state as before the callback was invoked.
}
