package workspace

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	_ "modernc.org/sqlite"
)

// ========================================================================
// Spec 13 Task 1.1: New workspace schema fields
// (TS-13-1, TS-13-2, TS-13-3, 13-REQ-1.E1, 13-REQ-1.E2)
// Requirements: 13-REQ-1
// ========================================================================

// TS-13-1: Verifies that the workspaces table has all five new sync-related
// columns with correct types and defaults after DDL is applied.
// Requirement: 13-REQ-1.1
func TestSyncSchema_SyncColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	// Query table structure using PRAGMA table_info.
	rows, err := db.Query("PRAGMA table_info(workspaces)")
	if err != nil {
		t.Fatalf("PRAGMA table_info failed: %v", err)
	}
	defer rows.Close()

	type columnInfo struct {
		name      string
		colType   string
		notNull   bool
		dfltValue *string
	}

	columns := make(map[string]columnInfo)
	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			dfltValue  *string
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &primaryKey); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		columns[name] = columnInfo{
			name:      name,
			colType:   colType,
			notNull:   notNull == 1,
			dfltValue: dfltValue,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}

	// Verify sync_mode column: TEXT NOT NULL DEFAULT 'pull_only'
	t.Run("sync_mode", func(t *testing.T) {
		col, ok := columns["sync_mode"]
		if !ok {
			t.Fatal("column 'sync_mode' not found in workspaces table")
		}
		if col.colType != "TEXT" {
			t.Errorf("sync_mode type = %q; want TEXT", col.colType)
		}
		if !col.notNull {
			t.Error("sync_mode should be NOT NULL")
		}
		if col.dfltValue == nil || *col.dfltValue != "'pull_only'" {
			got := "<nil>"
			if col.dfltValue != nil {
				got = *col.dfltValue
			}
			t.Errorf("sync_mode default = %s; want 'pull_only'", got)
		}
	})

	// Verify sync_status column: TEXT NOT NULL DEFAULT 'idle'
	t.Run("sync_status", func(t *testing.T) {
		col, ok := columns["sync_status"]
		if !ok {
			t.Fatal("column 'sync_status' not found in workspaces table")
		}
		if col.colType != "TEXT" {
			t.Errorf("sync_status type = %q; want TEXT", col.colType)
		}
		if !col.notNull {
			t.Error("sync_status should be NOT NULL")
		}
		if col.dfltValue == nil || *col.dfltValue != "'idle'" {
			got := "<nil>"
			if col.dfltValue != nil {
				got = *col.dfltValue
			}
			t.Errorf("sync_status default = %s; want 'idle'", got)
		}
	})

	// Verify upstream_head_sha column: TEXT nullable
	t.Run("upstream_head_sha", func(t *testing.T) {
		col, ok := columns["upstream_head_sha"]
		if !ok {
			t.Fatal("column 'upstream_head_sha' not found in workspaces table")
		}
		if col.colType != "TEXT" {
			t.Errorf("upstream_head_sha type = %q; want TEXT", col.colType)
		}
		if col.notNull {
			t.Error("upstream_head_sha should be nullable (NOT NULL = false)")
		}
	})

	// Verify last_sync_at column: TEXT nullable
	t.Run("last_sync_at", func(t *testing.T) {
		col, ok := columns["last_sync_at"]
		if !ok {
			t.Fatal("column 'last_sync_at' not found in workspaces table")
		}
		if col.colType != "TEXT" {
			t.Errorf("last_sync_at type = %q; want TEXT", col.colType)
		}
		if col.notNull {
			t.Error("last_sync_at should be nullable (NOT NULL = false)")
		}
	})

	// Verify sync_error column: TEXT nullable
	t.Run("sync_error", func(t *testing.T) {
		col, ok := columns["sync_error"]
		if !ok {
			t.Fatal("column 'sync_error' not found in workspaces table")
		}
		if col.colType != "TEXT" {
			t.Errorf("sync_error type = %q; want TEXT", col.colType)
		}
		if col.notNull {
			t.Error("sync_error should be nullable (NOT NULL = false)")
		}
	})
}

// 13-REQ-1.E1: Verifies that the sync DDL migration is idempotent — calling
// initSchema twice succeeds without error and all sync columns are present.
func TestSyncSchema_DDLIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("first initSchema() returned error: %v", err)
	}
	if err := initSchema(db); err != nil {
		t.Fatalf("second initSchema() returned error: %v", err)
	}

	// Verify sync columns exist after the second (idempotent) call.
	syncCols := []string{"sync_mode", "sync_status", "upstream_head_sha", "last_sync_at", "sync_error"}
	rows, err := db.Query("PRAGMA table_info(workspaces)")
	if err != nil {
		t.Fatalf("PRAGMA table_info failed: %v", err)
	}
	defer rows.Close()

	found := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt *string
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}

	for _, col := range syncCols {
		if !found[col] {
			t.Errorf("column %q not found after idempotent initSchema", col)
		}
	}
}

// TS-13-2: Verifies that every workspace API response includes all five sync
// fields with correct nullability.
// Requirement: 13-REQ-1.2
func TestSyncSchema_ResponseIncludesSyncFields(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	// Create a workspace to use for response field checks.
	createBody := `{"slug":"sync-resp-ws","git_url":"https://github.com/example/repo.git"}`
	createRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", createBody, auth)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create returned %d, want %d; body: %s",
			createRec.Code, http.StatusCreated, createRec.Body.String())
	}

	// Helper: assert all five sync fields are present in a single workspace
	// JSON response object. sync_mode and sync_status must be non-null.
	assertSyncFields := func(t *testing.T, body []byte, endpoint string) {
		t.Helper()
		var resp map[string]any
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("%s: failed to decode response: %v", endpoint, err)
		}
		for _, field := range []string{"sync_mode", "sync_status", "upstream_head_sha", "last_sync_at", "sync_error"} {
			if _, ok := resp[field]; !ok {
				t.Errorf("%s: response missing %q field", endpoint, field)
			}
		}
		// Non-nullable fields must not be null.
		if resp["sync_mode"] == nil {
			t.Errorf("%s: sync_mode is null; want non-null", endpoint)
		}
		if resp["sync_status"] == nil {
			t.Errorf("%s: sync_status is null; want non-null", endpoint)
		}
	}

	// Helper: assert sync fields in list (array) response.
	assertSyncFieldsList := func(t *testing.T, body []byte, endpoint string) {
		t.Helper()
		var resp []map[string]any
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("%s: failed to decode response: %v", endpoint, err)
		}
		for i, item := range resp {
			for _, field := range []string{"sync_mode", "sync_status", "upstream_head_sha", "last_sync_at", "sync_error"} {
				if _, ok := item[field]; !ok {
					t.Errorf("%s[%d]: response missing %q field", endpoint, i, field)
				}
			}
		}
	}

	// GET /api/v1/workspaces/:slug
	t.Run("get", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/sync-resp-ws", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET returned %d, want %d", rec.Code, http.StatusOK)
		}
		assertSyncFields(t, rec.Body.Bytes(), "GET /api/v1/workspaces/:slug")
	})

	// GET /api/v1/workspaces (list)
	t.Run("list", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("list returned %d, want %d", rec.Code, http.StatusOK)
		}
		assertSyncFieldsList(t, rec.Body.Bytes(), "GET /api/v1/workspaces")
	})

	// PATCH /api/v1/workspaces/:slug
	t.Run("patch", func(t *testing.T) {
		body := `{"display_name":"Updated"}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/sync-resp-ws", body, auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH returned %d, want %d", rec.Code, http.StatusOK)
		}
		assertSyncFields(t, rec.Body.Bytes(), "PATCH /api/v1/workspaces/:slug")
	})

	// POST /api/v1/workspaces (create response already checked above)
	t.Run("create", func(t *testing.T) {
		body := `{"slug":"sync-resp-ws2","git_url":"https://github.com/example/repo2.git"}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create returned %d, want %d", rec.Code, http.StatusCreated)
		}
		assertSyncFields(t, rec.Body.Bytes(), "POST /api/v1/workspaces")
	})
}

// TS-13-3: Verifies that a newly created workspace without sync_mode specified
// defaults to sync_mode='pull_only', sync_status='idle', and null sync fields.
// Requirement: 13-REQ-1.3
func TestSyncSchema_CreateDefaultValues(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u1-id")

	body := `{"slug":"sync-defaults-ws","git_url":"https://github.com/example/repo.git"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create returned %d, want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// sync_mode should default to "pull_only".
	if val, ok := resp["sync_mode"]; !ok {
		t.Error("response missing 'sync_mode' field")
	} else if val != "pull_only" {
		t.Errorf("sync_mode = %v; want %q", val, "pull_only")
	}

	// sync_status should default to "idle".
	if val, ok := resp["sync_status"]; !ok {
		t.Error("response missing 'sync_status' field")
	} else if val != "idle" {
		t.Errorf("sync_status = %v; want %q", val, "idle")
	}

	// upstream_head_sha should be null.
	if val, ok := resp["upstream_head_sha"]; !ok {
		t.Error("response missing 'upstream_head_sha' field")
	} else if val != nil {
		t.Errorf("upstream_head_sha = %v; want nil", val)
	}

	// last_sync_at should be null.
	if val, ok := resp["last_sync_at"]; !ok {
		t.Error("response missing 'last_sync_at' field")
	} else if val != nil {
		t.Errorf("last_sync_at = %v; want nil", val)
	}

	// sync_error should be null.
	if val, ok := resp["sync_error"]; !ok {
		t.Error("response missing 'sync_error' field")
	} else if val != nil {
		t.Errorf("sync_error = %v; want nil", val)
	}
}

// 13-REQ-1.E2: Verifies that workspace API responses treat NULL sync_mode as
// 'pull_only' and NULL sync_status as 'idle'. This tests defensive NULL
// coalescing in the response serialization layer for pre-migration rows.
//
// The workspaces table uses NOT NULL DEFAULT for sync_mode and sync_status,
// so NULL values cannot occur via normal INSERT/UPDATE. To test the coalescing
// logic, this test creates a database with the old schema (pre-sync columns),
// then adds sync columns WITHOUT NOT NULL constraints to simulate a
// partially-applied or legacy migration where NULLs could appear.
func TestSyncSchema_NullSyncFieldsCoalesced(t *testing.T) {
	// Build a database that simulates the pre-migration state: the workspaces
	// table exists without sync columns, then we add them as nullable.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })

	// Create the old-style workspaces table (no sync columns).
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS workspaces (
		slug         TEXT PRIMARY KEY,
		git_url      TEXT NOT NULL,
		branch       TEXT,
		owner_id     TEXT NOT NULL,
		org_id       TEXT,
		status       TEXT NOT NULL DEFAULT 'active',
		display_name TEXT NOT NULL DEFAULT '',
		description  TEXT NOT NULL DEFAULT '',
		clone_status TEXT NOT NULL DEFAULT 'pending' CHECK(clone_status IN ('pending','cloning','ready','failed','archived')),
		head_sha     TEXT,
		clone_error  TEXT,
		created_at   TEXT NOT NULL,
		updated_at   TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("failed to create old workspaces table: %v", err)
	}

	// Add sync columns WITHOUT NOT NULL (simulating a legacy migration that
	// does not enforce NOT NULL — the scenario 13-REQ-1.E2 describes).
	legacyMigrations := []string{
		`ALTER TABLE workspaces ADD COLUMN sync_mode TEXT`,
		`ALTER TABLE workspaces ADD COLUMN sync_status TEXT`,
		`ALTER TABLE workspaces ADD COLUMN upstream_head_sha TEXT`,
		`ALTER TABLE workspaces ADD COLUMN last_sync_at TEXT`,
		`ALTER TABLE workspaces ADD COLUMN sync_error TEXT`,
		// Carry-patch columns (15-REQ-1) — added so the SELECT scan matches
		// the expected column count.
		`ALTER TABLE workspaces ADD COLUMN workspace_mode TEXT NOT NULL DEFAULT 'standard'`,
		`ALTER TABLE workspaces ADD COLUMN upstream_url TEXT`,
		`ALTER TABLE workspaces ADD COLUMN integration_branch TEXT`,
	}
	for _, ddl := range legacyMigrations {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("legacy migration failed: %v", err)
		}
	}

	// Create org tables needed by RegisterRoutes → handler lookups.
	orgSchemaSQL := []string{
		`CREATE TABLE IF NOT EXISTS orgs (
			id TEXT NOT NULL PRIMARY KEY, name TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE, url TEXT, owner_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS org_members (
			org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL, created_at TEXT NOT NULL,
			PRIMARY KEY (org_id, user_id)
		)`,
	}
	for _, stmt := range orgSchemaSQL {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create org schema: %v", err)
		}
	}

	// Insert a workspace row with NULL sync_mode and sync_status (pre-migration row).
	_, err = db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		"premigration-ws", "https://github.com/example/repo.git", "alice-id", "active", "ready",
	)
	if err != nil {
		t.Fatalf("failed to insert pre-migration workspace: %v", err)
	}

	// Verify sync fields are actually NULL in the database.
	var syncMode, syncStatus *string
	err = db.QueryRow(`SELECT sync_mode, sync_status FROM workspaces WHERE slug = ?`, "premigration-ws").Scan(&syncMode, &syncStatus)
	if err != nil {
		t.Fatalf("failed to read sync fields: %v", err)
	}
	if syncMode != nil {
		t.Fatalf("precondition: sync_mode should be NULL; got %q", *syncMode)
	}
	if syncStatus != nil {
		t.Fatalf("precondition: sync_status should be NULL; got %q", *syncStatus)
	}

	// Wire up a test server using this database.
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatalf("RegisterRoutes() returned error: %v", err)
	}

	auth := adminAuth()

	// GET the workspace and verify sync fields are coalesced to defaults.
	rec := (&testEnv{echo: e, db: db}).doRequest(t, http.MethodGet, "/api/v1/workspaces/premigration-ws", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET returned %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if val := resp["sync_mode"]; val != "pull_only" {
		t.Errorf("sync_mode = %v; want %q (coalesced from NULL)", val, "pull_only")
	}
	if val := resp["sync_status"]; val != "idle" {
		t.Errorf("sync_status = %v; want %q (coalesced from NULL)", val, "idle")
	}
}
