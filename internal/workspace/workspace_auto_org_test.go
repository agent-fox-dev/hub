package workspace

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test helpers for workspace auto-org tests
// ---------------------------------------------------------------------------

// newAutoOrgTestEnv creates a test environment whose orgs table includes the
// owner_id column (added by spec 04). The standard openTestDB creates orgs
// WITHOUT owner_id; this variant includes it so that personal org lookup
// tests can seed orgs with owner_id set.
func newAutoOrgTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := openAutoOrgTestDB(t)
	e := newEchoWithRoutes(t, db)
	return &testEnv{echo: e, db: db}
}

// openAutoOrgTestDB opens an in-memory SQLite database with the workspaces
// table, the orgs table (with owner_id column), and the org_members table.
func openAutoOrgTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })

	// Initialize the workspaces table schema.
	if err := initSchema(db); err != nil {
		t.Fatalf("failed to initialize schema: %v", err)
	}

	// Create orgs table WITH owner_id column (spec 04 addition).
	orgSchemaSQL := []string{
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
	for _, stmt := range orgSchemaSQL {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create org schema: %v", err)
		}
	}

	return db
}

// newEchoWithRoutes creates an echo server with workspace routes and test
// auth middleware. Extracted so both newTestEnv and newAutoOrgTestEnv can
// share the same route wiring logic.
func newEchoWithRoutes(t *testing.T, db *sql.DB) *echo.Echo {
	t.Helper()
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatalf("RegisterRoutes() returned error: %v", err)
	}
	return e
}

// seedOrgWithOwner inserts an organization with owner_id set.
func (env *testEnv) seedOrgWithOwner(t *testing.T, orgID, name, slug, ownerID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := env.db.Exec(
		`INSERT INTO orgs (id, name, slug, owner_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', ?, ?)`,
		orgID, name, slug, ownerID, now, now,
	)
	if err != nil {
		t.Fatalf("seedOrgWithOwner(%q, owner=%q) returned error: %v", orgID, ownerID, err)
	}
}

// ---------------------------------------------------------------------------
// TS-04-29: Verify that workspace creation without org_id auto-assigns the
// user's personal org id as the workspace org_id.
// Requirement: 04-REQ-8.1
// ---------------------------------------------------------------------------
func TestWorkspaceAutoOrg_NoOrgIDUsesPersonalOrg(t *testing.T) {
	env := newAutoOrgTestEnv(t)

	// Seed personal org for user 'mark' with owner_id set.
	env.seedOrgWithOwner(t, "org-mark-001", "mark", "mark", "user-mark-001")
	env.seedOrgMember(t, "org-mark-001", "user-mark-001")

	auth := userAuth("user-mark-001")
	body := `{"slug":"my-repo","git_url":"https://github.com/mark/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	// Assert HTTP 201 response.
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d; body = %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Assert response includes org_id matching user's personal org.
	ws := parseWorkspaceJSON(t, rec)
	if ws.OrgID == nil || *ws.OrgID != "org-mark-001" {
		t.Errorf("response org_id = %v; want %q", ws.OrgID, "org-mark-001")
	}

	// Assert database row has org_id set.
	var dbOrgID *string
	err := env.db.QueryRow("SELECT org_id FROM workspaces WHERE slug = ?", "my-repo").Scan(&dbOrgID)
	if err != nil {
		t.Fatalf("querying workspace org_id from DB: %v", err)
	}
	if dbOrgID == nil || *dbOrgID != "org-mark-001" {
		t.Errorf("DB org_id = %v; want %q", dbOrgID, "org-mark-001")
	}
}

// ---------------------------------------------------------------------------
// TS-04-30: Verify that workspace creation with explicit org_id uses existing
// checkOrgMembership validation unchanged.
// Requirement: 04-REQ-8.2
// ---------------------------------------------------------------------------
func TestWorkspaceAutoOrg_ExplicitOrgIDUsesCheckOrgMembership(t *testing.T) {
	env := newAutoOrgTestEnv(t)

	// Seed a team org and add user 'nina' as a member.
	env.seedOrgWithOwner(t, "org-team-001", "Team Org", "team-org", "")
	env.seedOrgMember(t, "org-team-001", "user-nina-001")

	// Also seed a personal org for nina (to verify it is NOT used).
	env.seedOrgWithOwner(t, "org-nina-personal", "nina", "nina", "user-nina-001")
	env.seedOrgMember(t, "org-nina-personal", "user-nina-001")

	auth := userAuth("user-nina-001")
	body := `{"slug":"team-repo","git_url":"https://github.com/nina/repo","org_id":"org-team-001"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	// Assert HTTP 201 response.
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d; body = %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Assert response includes the explicit org_id, not the personal org.
	ws := parseWorkspaceJSON(t, rec)
	if ws.OrgID == nil || *ws.OrgID != "org-team-001" {
		t.Errorf("response org_id = %v; want %q (explicit org, not personal)", ws.OrgID, "org-team-001")
	}
}

// ---------------------------------------------------------------------------
// TS-04-31: Verify that workspace creation without org_id returns HTTP 400
// with the prescribed message when the user has no personal org.
// Requirement: 04-REQ-8.3
// ---------------------------------------------------------------------------
func TestWorkspaceAutoOrg_NoPersonalOrgReturns400(t *testing.T) {
	env := newAutoOrgTestEnv(t)

	// No orgs seeded for user 'orphan' — they have no personal org.

	auth := userAuth("user-orphan-001")
	body := `{"slug":"orphan-ws","git_url":"https://github.com/orphan/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	// Assert HTTP 400 response.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// Assert error message matches the prescribed text.
	resp := parseErrorEnvelope(t, rec)
	expectedMsg := "user has no personal organization; contact an administrator"
	if resp.Error.Message != expectedMsg {
		t.Errorf("error message = %q; want %q", resp.Error.Message, expectedMsg)
	}
}

// ---------------------------------------------------------------------------
// TS-04-E14: Verify that a database error during personal org lookup in
// workspace creation returns HTTP 500 without inserting a workspace.
// Requirement: 04-REQ-8.E1
// ---------------------------------------------------------------------------
func TestWorkspaceAutoOrg_DBErrorOnPersonalOrgLookup(t *testing.T) {
	env := newAutoOrgTestEnv(t)

	// Drop the orgs table to simulate a database error when querying for
	// the personal org. The handler should return 500.
	if _, err := env.db.Exec("DROP TABLE orgs"); err != nil {
		t.Fatalf("failed to drop orgs table: %v", err)
	}

	auth := userAuth("user-dberr")
	body := `{"slug":"dberr-ws","git_url":"https://github.com/dberr/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	// Assert HTTP 500 response.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d; body = %s",
			rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	// Assert no workspace row was inserted.
	var count int
	err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "dberr-ws").Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("workspace count = %d; want 0 (no workspace should be inserted on DB error)", count)
	}
}

// ---------------------------------------------------------------------------
// TS-04-E15: Verify that when multiple personal org rows exist for a user
// (data inconsistency), the first result is used and a warning is logged,
// but the request succeeds.
// Requirement: 04-REQ-8.E2
// ---------------------------------------------------------------------------
func TestWorkspaceAutoOrg_MultiplePersonalOrgsUsesFirst(t *testing.T) {
	env := newAutoOrgTestEnv(t)

	// Seed two org rows both with owner_id='user-multi-org' (data inconsistency).
	// The handler should use the first result and log a warning.
	env.seedOrgWithOwner(t, "org-multi-001", "Multi Org 1", "multi-org-one", "user-multi-org")
	env.seedOrgMember(t, "org-multi-001", "user-multi-org")

	// Second org for same user. The name column has a UNIQUE constraint, so
	// use a different name.
	env.seedOrgWithOwner(t, "org-multi-002", "Multi Org 2", "multi-org-two", "user-multi-org")
	env.seedOrgMember(t, "org-multi-002", "user-multi-org")

	auth := userAuth("user-multi-org")
	body := `{"slug":"multi-org-ws","git_url":"https://github.com/multi/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	// Assert HTTP 201 response — request should succeed despite data inconsistency.
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d; body = %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Assert org_id is populated with one of the personal orgs' ids.
	ws := parseWorkspaceJSON(t, rec)
	if ws.OrgID == nil || *ws.OrgID == "" {
		t.Fatal("response org_id should be populated; got nil or empty")
	}
	validOrgIDs := map[string]bool{"org-multi-001": true, "org-multi-002": true}
	if !validOrgIDs[*ws.OrgID] {
		t.Errorf("response org_id = %q; want one of org-multi-001 or org-multi-002", *ws.OrgID)
	}

	// Note: we cannot easily instrument the logger in this test to verify
	// the warning was logged, since the test uses the standard testEnv pattern.
	// The implementation should log a warning using logrus or similar when
	// the personal org query returns more than one row. The warning is
	// verified by code review rather than assertion. The critical property
	// is that the request succeeds — the warning is advisory.
}

// ---------------------------------------------------------------------------
// TS-04-P3 (partial): Verify that any successful workspace creation (HTTP 201)
// results in non-null org_id in the persisted row.
// Property: 04-PROP-3
// Validates: 04-REQ-8.1, 04-REQ-8.3
// ---------------------------------------------------------------------------
func TestWorkspaceAutoOrg_PropertyEveryCreatedWorkspaceHasOrgID(t *testing.T) {
	// This property test generates workspace creation requests with and
	// without org_id for users with and without personal orgs. It verifies:
	//  - HTTP 201 → persisted workspace has non-null org_id
	//  - HTTP != 201 → no workspace row exists for that slug

	type testCase struct {
		name        string
		userID      string
		orgID       string // explicit org_id in request (empty = omitted)
		hasPersonal bool   // whether user has a personal org
		wantStatus  int
	}

	cases := []testCase{
		// --- Users WITH a personal org ---
		{
			name:        "with personal org, no explicit org_id",
			userID:      "user-prop3-a",
			orgID:       "",
			hasPersonal: true,
			wantStatus:  http.StatusCreated,
		},
		{
			name:        "with personal org, explicit org_id matching personal",
			userID:      "user-prop3-b",
			orgID:       "org-prop3-b",
			hasPersonal: true,
			wantStatus:  http.StatusCreated,
		},
		// --- Users WITHOUT a personal org ---
		{
			name:        "no personal org, no explicit org_id",
			userID:      "user-prop3-c",
			orgID:       "",
			hasPersonal: false,
			wantStatus:  http.StatusBadRequest,
		},
		// --- Nonexistent explicit org_id ---
		{
			name:        "no personal org, nonexistent explicit org_id",
			userID:      "user-prop3-d",
			orgID:       "org-does-not-exist-999",
			hasPersonal: false,
			wantStatus:  http.StatusBadRequest, // membership check fails
		},
		// --- Second user with personal org, no explicit org_id ---
		{
			name:        "another user with personal org, no explicit org_id",
			userID:      "user-prop3-e",
			orgID:       "",
			hasPersonal: true,
			wantStatus:  http.StatusCreated,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newAutoOrgTestEnv(t)
			slug := fmt.Sprintf("prop3-ws-%d", i)

			if tc.hasPersonal {
				orgID := "org-prop3-" + string(rune('a'+i))
				env.seedOrgWithOwner(t, orgID, "Org "+tc.userID, "org-"+tc.userID, tc.userID)
				env.seedOrgMember(t, orgID, tc.userID)
			}

			// Build request body.
			bodyParts := []string{
				`"slug":"` + slug + `"`,
				`"git_url":"https://github.com/test/repo"`,
			}
			if tc.orgID != "" {
				bodyParts = append(bodyParts, `"org_id":"`+tc.orgID+`"`)
			}
			body := "{" + strings.Join(bodyParts, ",") + "}"

			auth := userAuth(tc.userID)
			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d; want %d; body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if rec.Code == http.StatusCreated {
				// Invariant: persisted workspace has non-null org_id.
				var dbOrgID *string
				err := env.db.QueryRow("SELECT org_id FROM workspaces WHERE slug = ?", slug).Scan(&dbOrgID)
				if err != nil {
					t.Fatalf("querying workspace org_id: %v", err)
				}
				if dbOrgID == nil || *dbOrgID == "" {
					t.Error("persisted workspace org_id is null or empty; want non-null for every HTTP 201 response")
				}
			} else {
				// Invariant: no workspace row exists.
				var count int
				err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", slug).Scan(&count)
				if err != nil {
					t.Fatalf("count query failed: %v", err)
				}
				if count != 0 {
					t.Errorf("workspace count = %d; want 0 when status != 201", count)
				}
			}
		})
	}
}
