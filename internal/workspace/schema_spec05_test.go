package workspace

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	_ "modernc.org/sqlite"
)

// ========================================================================
// Spec 05 Task 1.3: New workspace fields — schema, response, constraint
// (TS-05-7, TS-05-8, TS-05-9)
// ========================================================================

// TS-05-7: The workspaces SQLite table includes status (TEXT NOT NULL
// DEFAULT 'pending'), head_sha (TEXT nullable), and clone_error (TEXT nullable).
// Requirement: 05-REQ-3.1
func TestWorkspaceSchema_Spec05_CloneColumns(t *testing.T) {
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

	// Verify status column: TEXT NOT NULL DEFAULT 'pending'
	t.Run("status", func(t *testing.T) {
		col, ok := columns["status"]
		if !ok {
			t.Fatal("column 'status' not found in workspaces table")
		}
		if col.colType != "TEXT" {
			t.Errorf("status type = %q; want TEXT", col.colType)
		}
		if !col.notNull {
			t.Error("status should be NOT NULL")
		}
		if col.dfltValue == nil || *col.dfltValue != "'pending'" {
			got := "<nil>"
			if col.dfltValue != nil {
				got = *col.dfltValue
			}
			t.Errorf("status default = %s; want 'pending'", got)
		}
	})
	for _, gone := range []string{"clone_status", "sync_status", "sync_mode"} {
		if _, ok := columns[gone]; ok {
			t.Errorf("column %q still present in workspaces table; want it folded into status", gone)
		}
	}

	// Verify head_sha column: TEXT nullable
	t.Run("head_sha", func(t *testing.T) {
		col, ok := columns["head_sha"]
		if !ok {
			t.Fatal("column 'head_sha' not found in workspaces table")
		}
		if col.colType != "TEXT" {
			t.Errorf("head_sha type = %q; want TEXT", col.colType)
		}
		if col.notNull {
			t.Error("head_sha should be nullable (NOT NULL = false)")
		}
	})

	// Verify clone_error column: TEXT nullable
	t.Run("clone_error", func(t *testing.T) {
		col, ok := columns["clone_error"]
		if !ok {
			t.Fatal("column 'clone_error' not found in workspaces table")
		}
		if col.colType != "TEXT" {
			t.Errorf("clone_error type = %q; want TEXT", col.colType)
		}
		if col.notNull {
			t.Error("clone_error should be nullable (NOT NULL = false)")
		}
	})
}

// TS-05-8: All workspace API endpoints include status, head_sha, and
// clone_error fields in their JSON responses.
// Requirement: 05-REQ-3.2
func TestWorkspaceResponse_Spec05_IncludesCloneFields(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("user-spec05")

	// assertCloneFields checks that a response body contains status,
	// head_sha, and clone_error at the top level.
	assertCloneFields := func(t *testing.T, body []byte, endpoint string) {
		t.Helper()
		var resp map[string]any
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("%s: failed to decode response as JSON object: %v", endpoint, err)
		}

		if _, ok := resp["status"]; !ok {
			t.Errorf("%s: response missing 'status' field", endpoint)
		}
		if _, ok := resp["clone_status"]; ok {
			t.Errorf("%s: response still has 'clone_status' field", endpoint)
		}
		// head_sha must be present (value may be null).
		if _, ok := resp["head_sha"]; !ok {
			t.Errorf("%s: response missing 'head_sha' field", endpoint)
		}
		// clone_error must be present (value may be null).
		if _, ok := resp["clone_error"]; !ok {
			t.Errorf("%s: response missing 'clone_error' field", endpoint)
		}
	}

	// assertCloneFieldsList checks that each element in a JSON array response
	// contains status, head_sha, and clone_error.
	assertCloneFieldsList := func(t *testing.T, body []byte, endpoint string) {
		t.Helper()
		var resp []map[string]any
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("%s: failed to decode response as JSON array: %v", endpoint, err)
		}
		for i, item := range resp {
			if _, ok := item["status"]; !ok {
				t.Errorf("%s[%d]: response item missing 'status' field", endpoint, i)
			}
			if _, ok := item["head_sha"]; !ok {
				t.Errorf("%s[%d]: response item missing 'head_sha' field", endpoint, i)
			}
			if _, ok := item["clone_error"]; !ok {
				t.Errorf("%s[%d]: response item missing 'clone_error' field", endpoint, i)
			}
		}
	}

	// --- POST /api/v1/workspaces (create) ---
	t.Run("create", func(t *testing.T) {
		body := `{"slug":"ws-clone-test","git_url":"https://github.com/org/repo"}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create returned %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
		}
		assertCloneFields(t, rec.Body.Bytes(), "POST /api/v1/workspaces")
	})

	// --- GET /api/v1/workspaces (list) ---
	t.Run("list", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("list returned %d, want %d", rec.Code, http.StatusOK)
		}
		assertCloneFieldsList(t, rec.Body.Bytes(), "GET /api/v1/workspaces")
	})

	// --- GET /api/v1/workspaces/:slug (get) ---
	t.Run("get", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-clone-test", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("get returned %d, want %d", rec.Code, http.StatusOK)
		}
		assertCloneFields(t, rec.Body.Bytes(), "GET /api/v1/workspaces/:slug")
	})

	// --- PATCH /api/v1/workspaces/:slug (update) ---
	t.Run("update", func(t *testing.T) {
		body := `{"display_name":"Updated Name"}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/ws-clone-test", body, auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("update returned %d, want %d", rec.Code, http.StatusOK)
		}
		assertCloneFields(t, rec.Body.Bytes(), "PATCH /api/v1/workspaces/:slug")
	})

	// --- POST /api/v1/workspaces/:slug/archive ---
	t.Run("archive", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-clone-test/archive", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("archive returned %d, want %d", rec.Code, http.StatusOK)
		}
		assertCloneFields(t, rec.Body.Bytes(), "POST /api/v1/workspaces/:slug/archive")
	})

	// --- POST /api/v1/workspaces/:slug/reactivate ---
	t.Run("reactivate", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-clone-test/reactivate", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("reactivate returned %d, want %d", rec.Code, http.StatusOK)
		}
		assertCloneFields(t, rec.Body.Bytes(), "POST /api/v1/workspaces/:slug/reactivate")
	})
}

// TS-05-9: The workspaces table enforces that status is one of the five
// valid values: pending, cloning, ready, failed, archived.
// Requirement: 05-REQ-3.3
func TestWorkspaceSchema_Spec05_StatusConstraint(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	// Valid status values should insert without error.
	validStatuses := []string{"pending", "cloning", "ready", "failed", "archived"}
	for i, status := range validStatuses {
		t.Run("valid_"+status, func(t *testing.T) {
			_, err := db.Exec(
				`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description, created_at, updated_at)
				 VALUES (?, 'https://github.com/org/repo', 'user-1', ?, '', '', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
				"ws-status-"+validStatuses[i], status,
			)
			if err != nil {
				t.Errorf("INSERT with status=%q failed: %v", status, err)
			}
		})
	}

	// Invalid status values should be rejected by the CHECK constraint;
	// 'active' is the old value of the column before the clone state was
	// folded into it.
	for _, invalid := range []string{"unknown_state", "active"} {
		t.Run("invalid_"+invalid, func(t *testing.T) {
			_, err := db.Exec(
				`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description, created_at, updated_at)
				 VALUES (?, 'https://github.com/org/repo', 'user-1', ?, '', '', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
				"ws-invalid-"+invalid, invalid,
			)
			if err == nil {
				t.Errorf("INSERT with status=%q should be rejected by CHECK constraint", invalid)
			}
		})
	}
}

// 05-REQ-3.E1: When a workspace is first created, status defaults to
// "pending", head_sha to NULL, and clone_error to NULL.
func TestWorkspaceResponse_Spec05_CreateDefaults(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("user-defaults")

	body := `{"slug":"ws-defaults","git_url":"https://github.com/org/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create returned %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// status should be "pending" on creation.
	if cs, ok := resp["status"]; !ok {
		t.Error("response missing 'status' field")
	} else if cs != "pending" {
		t.Errorf("status = %v, want %q", cs, "pending")
	}

	// head_sha should be null on creation.
	if hs, ok := resp["head_sha"]; !ok {
		t.Error("response missing 'head_sha' field")
	} else if hs != nil {
		t.Errorf("head_sha = %v, want nil", hs)
	}

	// clone_error should be null on creation.
	if ce, ok := resp["clone_error"]; !ok {
		t.Error("response missing 'clone_error' field")
	} else if ce != nil {
		t.Errorf("clone_error = %v, want nil", ce)
	}
}
