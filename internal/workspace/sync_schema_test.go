package workspace

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	_ "modernc.org/sqlite"
)

// ========================================================================
// Spec 13 Task 1.1: New workspace schema fields
// (TS-13-1, TS-13-2, TS-13-3, 13-REQ-1.E1)
// Requirements: 13-REQ-1
// ========================================================================

// TS-13-1: Verifies that the workspaces table has the three sync-related
// columns (upstream_head_sha, last_sync_at, sync_error) with correct types
// and nullability after DDL is applied. A sync in progress is not persisted
// and the sync mode is the sync_enabled setting, so there are no sync_status
// or sync_mode columns.
// Requirement: 13-REQ-1.1
func TestSyncSchema_SyncColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
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

	for _, gone := range []string{"sync_mode", "sync_status", "clone_status"} {
		if _, ok := columns[gone]; ok {
			t.Errorf("column %q still present in workspaces table; want it folded away", gone)
		}
	}

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
// InitSchema twice succeeds without error and all sync columns are present.
func TestSyncSchema_DDLIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("first InitSchema() returned error: %v", err)
	}
	if err := InitSchema(db); err != nil {
		t.Fatalf("second InitSchema() returned error: %v", err)
	}

	// Verify sync columns exist after the second (idempotent) call.
	syncCols := []string{"upstream_head_sha", "last_sync_at", "sync_error"}
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
			t.Errorf("column %q not found after idempotent InitSchema", col)
		}
	}
}

// TS-13-2: Verifies that every workspace API response includes the three
// sync fields and the sync_enabled setting, and none of the folded keys.
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

	syncFields := []string{"upstream_head_sha", "last_sync_at", "sync_error"}
	goneFields := []string{"clone_status", "sync_mode", "sync_status"}

	// Helper: assert the sync fields are present in a single workspace JSON
	// response object, the folded keys are absent, and settings.sync_enabled
	// is a boolean.
	assertSyncFields := func(t *testing.T, body []byte, endpoint string) {
		t.Helper()
		var resp map[string]any
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("%s: failed to decode response: %v", endpoint, err)
		}
		for _, field := range syncFields {
			if _, ok := resp[field]; !ok {
				t.Errorf("%s: response missing %q field", endpoint, field)
			}
		}
		for _, field := range goneFields {
			if _, ok := resp[field]; ok {
				t.Errorf("%s: response still has %q field", endpoint, field)
			}
		}
		settings, _ := resp["settings"].(map[string]any)
		if _, ok := settings["sync_enabled"].(bool); !ok {
			t.Errorf("%s: settings.sync_enabled = %v; want a boolean", endpoint, settings["sync_enabled"])
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
			for _, field := range syncFields {
				if _, ok := item[field]; !ok {
					t.Errorf("%s[%d]: response missing %q field", endpoint, i, field)
				}
			}
			for _, field := range goneFields {
				if _, ok := item[field]; ok {
					t.Errorf("%s[%d]: response still has %q field", endpoint, i, field)
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

// TS-13-3: Verifies that a newly created workspace without settings defaults
// to settings.sync_enabled=true and null sync fields.
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

	// settings.sync_enabled should default to true.
	settings, _ := resp["settings"].(map[string]any)
	if val, ok := settings["sync_enabled"]; !ok {
		t.Error("response missing 'settings.sync_enabled' field")
	} else if val != true {
		t.Errorf("settings.sync_enabled = %v; want true", val)
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
