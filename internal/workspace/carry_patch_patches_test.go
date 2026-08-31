package workspace

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ========================================================================
// Spec 15 Task 3.1: Patches table schema
// (TS-15-21, TS-15-22)
// Requirements: 15-REQ-7
// ========================================================================

// TS-15-21: Hub startup creates the patches table with all required columns
// and constraints when the table does not yet exist.
// Requirement: 15-REQ-7.1
func TestCarryPatch_PatchesTableSchema_AllColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()
	// Limit to one connection so all PRAGMAs see the same in-memory database.
	db.SetMaxOpenConns(1)

	// Run the schema initializer.
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	// Verify the patches table exists by querying PRAGMA table_info.
	rows, err := db.Query("PRAGMA table_info(patches)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(patches) failed: %v", err)
	}
	defer rows.Close()

	type columnInfo struct {
		name    string
		colType string
		notNull bool
		dflt    *string
		pk      bool
	}
	columns := make(map[string]columnInfo)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt *string
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		columns[name] = columnInfo{
			name:    name,
			colType: ctype,
			notNull: notnull == 1,
			dflt:    dflt,
			pk:      pk == 1,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}

	if len(columns) == 0 {
		t.Fatal("patches table does not exist or has no columns")
	}

	// Required columns per 15-REQ-7.1.
	requiredColumns := []string{
		"id", "workspace_slug", "branch_name", "position",
		"status", "conflict_files", "upstream_pr_url", "description",
		"added_at", "updated_at",
	}
	for _, col := range requiredColumns {
		if _, ok := columns[col]; !ok {
			t.Errorf("patches table missing required column %q", col)
		}
	}

	// Verify id is TEXT PRIMARY KEY.
	if col, ok := columns["id"]; ok {
		if !col.pk {
			t.Error("id column should be PRIMARY KEY")
		}
	}

	// Verify workspace_slug is TEXT NOT NULL.
	if col, ok := columns["workspace_slug"]; ok {
		if !col.notNull {
			t.Error("workspace_slug should be NOT NULL")
		}
	}

	// Verify branch_name is TEXT NOT NULL.
	if col, ok := columns["branch_name"]; ok {
		if !col.notNull {
			t.Error("branch_name should be NOT NULL")
		}
	}

	// Verify position is INTEGER NOT NULL.
	if col, ok := columns["position"]; ok {
		if !col.notNull {
			t.Error("position should be NOT NULL")
		}
	}

	// Verify status is TEXT NOT NULL DEFAULT 'active'.
	if col, ok := columns["status"]; ok {
		if !col.notNull {
			t.Error("status should be NOT NULL")
		}
		if col.dflt == nil || *col.dflt != "'active'" {
			got := "<nil>"
			if col.dflt != nil {
				got = *col.dflt
			}
			t.Errorf("status default = %s; want 'active'", got)
		}
	}

	// Verify added_at is TEXT NOT NULL.
	if col, ok := columns["added_at"]; ok {
		if !col.notNull {
			t.Error("added_at should be NOT NULL")
		}
	}

	// Verify updated_at is TEXT NOT NULL.
	if col, ok := columns["updated_at"]; ok {
		if !col.notNull {
			t.Error("updated_at should be NOT NULL")
		}
	}

	// Verify UNIQUE constraints via PRAGMA index_list.
	// Collect index names first, then close the rows before querying each
	// index's columns to avoid nested queries on a single-connection pool.
	idxRows, err := db.Query("PRAGMA index_list(patches)")
	if err != nil {
		t.Fatalf("PRAGMA index_list(patches) failed: %v", err)
	}

	type indexInfo struct {
		name    string
		unique  bool
		columns []string
	}
	var indexes []indexInfo
	for idxRows.Next() {
		var seq int
		var name, origin string
		var unique, partial int
		if err := idxRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index_list failed: %v", err)
		}
		indexes = append(indexes, indexInfo{name: name, unique: unique == 1})
	}
	idxRows.Close()

	// Now query columns for each index.
	for i, idx := range indexes {
		colRows, err := db.Query(fmt.Sprintf("PRAGMA index_info(%s)", idx.name))
		if err != nil {
			t.Fatalf("PRAGMA index_info(%s) failed: %v", idx.name, err)
		}
		for colRows.Next() {
			var seqno, cid int
			var colName string
			if err := colRows.Scan(&seqno, &cid, &colName); err != nil {
				t.Fatalf("scan index_info failed: %v", err)
			}
			indexes[i].columns = append(indexes[i].columns, colName)
		}
		colRows.Close()
	}

	// Check for UNIQUE(workspace_slug, branch_name).
	foundBranchUnique := false
	for _, idx := range indexes {
		if idx.unique && len(idx.columns) == 2 &&
			containsAll(idx.columns, "workspace_slug", "branch_name") {
			foundBranchUnique = true
			break
		}
	}
	if !foundBranchUnique {
		t.Error("patches table missing UNIQUE constraint on (workspace_slug, branch_name)")
	}

	// Check for UNIQUE(workspace_slug, position).
	foundPositionUnique := false
	for _, idx := range indexes {
		if idx.unique && len(idx.columns) == 2 &&
			containsAll(idx.columns, "workspace_slug", "position") {
			foundPositionUnique = true
			break
		}
	}
	if !foundPositionUnique {
		t.Error("patches table missing UNIQUE constraint on (workspace_slug, position)")
	}
}

// containsAll returns true if slice contains all specified items.
func containsAll(slice []string, items ...string) bool {
	set := make(map[string]bool, len(slice))
	for _, s := range slice {
		set[s] = true
	}
	for _, item := range items {
		if !set[item] {
			return false
		}
	}
	return true
}

// TS-15-21 (status default): Inserting a row without specifying status
// defaults to 'active'.
// Requirement: 15-REQ-7.1
func TestCarryPatch_PatchesTableSchema_StatusDefault(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, added_at, updated_at)
		 VALUES ('test-id-1', 'ws-1', 'feature/foo', 1, ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("INSERT without status failed: %v", err)
	}

	var status string
	err = db.QueryRow("SELECT status FROM patches WHERE id = ?", "test-id-1").Scan(&status)
	if err != nil {
		t.Fatalf("SELECT status failed: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q; want %q (default)", status, "active")
	}
}

// TS-15-22: Inserting two patches with the same (workspace_slug, branch_name)
// pair is rejected by the database unique constraint.
// Requirement: 15-REQ-7.2
func TestCarryPatch_PatchesTableSchema_DuplicateBranchNameRejected(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// First insert should succeed.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-1', 'cp-ws', 'feature/foo', 1, 'active', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("first INSERT failed: %v", err)
	}

	// Second insert with same (workspace_slug, branch_name) should fail.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-2', 'cp-ws', 'feature/foo', 2, 'active', ?, ?)`,
		now, now,
	)
	if err == nil {
		t.Fatal("second INSERT with duplicate (workspace_slug, branch_name) should fail; got nil error")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") &&
		!strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Errorf("expected UNIQUE constraint error; got: %v", err)
	}
}

// TS-15-22 (position uniqueness): Inserting two patches with the same
// (workspace_slug, position) is rejected by the database unique constraint.
// Requirement: 15-REQ-7.2
func TestCarryPatch_PatchesTableSchema_DuplicatePositionRejected(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// First insert at position 1.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-1', 'cp-ws', 'feature/a', 1, 'active', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("first INSERT failed: %v", err)
	}

	// Second insert at same position 1 for same workspace should fail.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-2', 'cp-ws', 'feature/b', 1, 'active', ?, ?)`,
		now, now,
	)
	if err == nil {
		t.Fatal("second INSERT with duplicate (workspace_slug, position) should fail; got nil error")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") &&
		!strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Errorf("expected UNIQUE constraint error; got: %v", err)
	}
}

// Verify same branch_name across different workspaces is allowed.
// Requirement: 15-REQ-7.2 (uniqueness is per-workspace, not global)
func TestCarryPatch_PatchesTableSchema_SameBranchDifferentWorkspaces(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Same branch name in different workspaces should succeed.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-1', 'ws-a', 'feature/foo', 1, 'active', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("first INSERT failed: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-2', 'ws-b', 'feature/foo', 1, 'active', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Errorf("same branch_name in different workspace should succeed; got: %v", err)
	}
}

// ========================================================================
// Spec 15 Task 3.2: Add patch operation
// (TS-15-23, TS-15-24, TS-15-25, TS-15-26, TS-15-27, TS-15-28)
// Requirements: 15-REQ-8
// ========================================================================

// newPatchTestEnv creates a test environment with carry_patch workspace support.
// It ensures the carry_patch columns exist and seeds a carry_patch workspace
// with the given slug and integration branch.
func newPatchTestEnv(t *testing.T, slug, integrationBranch string) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	ensureCarryPatchColumns(t, env.db)
	seedCarryPatchWorkspaceRaw(t, env.db, slug,
		"https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git",
		integrationBranch)
	return env
}

// seedPatchRaw inserts a patch row directly into the database.
func seedPatchRaw(t *testing.T, db *sql.DB, id, workspaceSlug, branchName string, position int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', ?, ?)`,
		id, workspaceSlug, branchName, position, now, now,
	)
	if err != nil {
		t.Fatalf("seedPatchRaw(%q, %q, %q, %d) failed: %v", id, workspaceSlug, branchName, position, err)
	}
}

// TS-15-23: Adding a patch with only branch_name (no position) appends it
// at the end with position = max+1, status='active', and returns HTTP 201
// with all patch fields.
// Requirement: 15-REQ-8.1
func TestCarryPatch_AddPatch_AppendNoPosition(t *testing.T) {
	slug := "cp-add-append"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed two existing patches at positions 1 and 2.
	seedPatchRaw(t, env.db, "existing-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "existing-2", slug, "feature/b", 2)

	body := `{"branch_name": "feature/new-patch"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify position = 3 (max+1).
	if pos, ok := resp["position"]; !ok {
		t.Error("response missing 'position' field")
	} else if pos != float64(3) {
		t.Errorf("position = %v; want 3", pos)
	}

	// Verify status = 'active'.
	if status, ok := resp["status"]; !ok {
		t.Error("response missing 'status' field")
	} else if status != "active" {
		t.Errorf("status = %v; want %q", status, "active")
	}

	// Verify id is non-empty.
	if id, ok := resp["id"]; !ok {
		t.Error("response missing 'id' field")
	} else if id == "" {
		t.Error("id is empty; want non-empty UUID")
	}

	// Verify branch_name.
	if bn, ok := resp["branch_name"]; !ok {
		t.Error("response missing 'branch_name' field")
	} else if bn != "feature/new-patch" {
		t.Errorf("branch_name = %v; want %q", bn, "feature/new-patch")
	}

	// Verify added_at is RFC 3339.
	if at, ok := resp["added_at"].(string); ok {
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			t.Errorf("added_at %q is not valid RFC 3339: %v", at, err)
		}
	} else {
		t.Error("response missing or invalid 'added_at' field")
	}

	// Verify updated_at is RFC 3339.
	if ut, ok := resp["updated_at"].(string); ok {
		if _, err := time.Parse(time.RFC3339, ut); err != nil {
			t.Errorf("updated_at %q is not valid RFC 3339: %v", ut, err)
		}
	} else {
		t.Error("response missing or invalid 'updated_at' field")
	}
}

// TS-15-24: Adding a patch with an explicit position inserts it at that
// position and shifts existing patches at that position or higher down by one.
// Requirement: 15-REQ-8.2
func TestCarryPatch_AddPatch_ExplicitPositionShifts(t *testing.T) {
	slug := "cp-add-shift"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed two existing patches.
	seedPatchRaw(t, env.db, "patch-a", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "patch-b", slug, "feature/b", 2)

	body := `{"branch_name": "feature/inserted", "position": 2}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// New patch should be at position 2.
	if pos, ok := resp["position"]; !ok {
		t.Error("response missing 'position' field")
	} else if pos != float64(2) {
		t.Errorf("new patch position = %v; want 2", pos)
	}

	// Verify database state: feature/a=1, feature/inserted=2, feature/b=3.
	rows, err := env.db.Query(
		`SELECT branch_name, position FROM patches WHERE workspace_slug = ? ORDER BY position`,
		slug,
	)
	if err != nil {
		t.Fatalf("query patches failed: %v", err)
	}
	defer rows.Close()

	expected := []struct {
		branch   string
		position int
	}{
		{"feature/a", 1},
		{"feature/inserted", 2},
		{"feature/b", 3},
	}

	i := 0
	for rows.Next() {
		var branch string
		var position int
		if err := rows.Scan(&branch, &position); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		if i >= len(expected) {
			t.Fatalf("more rows than expected")
		}
		if branch != expected[i].branch {
			t.Errorf("row[%d] branch = %q; want %q", i, branch, expected[i].branch)
		}
		if position != expected[i].position {
			t.Errorf("row[%d] position = %d; want %d", i, position, expected[i].position)
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("got %d rows; want %d", i, len(expected))
	}
}

// TS-15-25: Attempting to add a patch to a workspace with
// workspace_mode='standard' returns HTTP 400 with a JSON error body.
// Requirement: 15-REQ-8.3
func TestCarryPatch_AddPatch_StandardWorkspaceRejected(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("user-1")

	// Seed a standard workspace (no carry_patch setup).
	env.seedWorkspace(t, &Workspace{
		Slug:    "std-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "active",
	})

	body := `{"branch_name": "feature/foo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/std-ws/patches", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// Verify error body.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if errObj, ok := resp["error"]; ok {
			if errMap, ok := errObj.(map[string]any); ok {
				if _, hasMsg := errMap["message"]; !hasMsg {
					t.Error("error response missing 'message' field")
				}
			}
		}
	}
}

// TS-15-26: Adding a patch with a branch_name that already exists in the
// patch list for the workspace returns HTTP 409 with a JSON error body.
// Requirement: 15-REQ-8.4
func TestCarryPatch_AddPatch_DuplicateBranchName(t *testing.T) {
	slug := "cp-add-dup"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed an existing patch.
	seedPatchRaw(t, env.db, "existing-patch", slug, "feature/existing", 1)

	body := `{"branch_name": "feature/existing"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusConflict {
		t.Errorf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if errObj, ok := resp["error"]; ok {
			if errMap, ok := errObj.(map[string]any); ok {
				if _, hasMsg := errMap["message"]; !hasMsg {
					t.Error("error response missing 'message' field")
				}
			}
		}
	}
}

// TS-15-27: Adding a patch whose branch_name equals the workspace's
// integration_branch returns HTTP 400 with a JSON error body.
// Requirement: 15-REQ-8.5
func TestCarryPatch_AddPatch_IntegrationBranchRejected(t *testing.T) {
	slug := "cp-add-intbranch"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	body := `{"branch_name": "deploy"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if errObj, ok := resp["error"]; ok {
			if errMap, ok := errObj.(map[string]any); ok {
				if _, hasMsg := errMap["message"]; !hasMsg {
					t.Error("error response missing 'message' field")
				}
			}
		}
	}
}

// TS-15-28: Adding a patch with an absent or empty branch_name returns
// HTTP 400 with a JSON error body.
// Requirement: 15-REQ-8.6
func TestCarryPatch_AddPatch_EmptyBranchName(t *testing.T) {
	slug := "cp-add-empty"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	t.Run("empty_branch_name", func(t *testing.T) {
		body := `{"branch_name": ""}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST with empty branch_name: status = %d; want %d; body: %s",
				rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})

	t.Run("absent_branch_name", func(t *testing.T) {
		body := `{}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST without branch_name: status = %d; want %d; body: %s",
				rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})
}

// When no branch-check hook is registered, non-existent branches are accepted
// (backwards-compatible behavior before branch validation was added).
func TestCarryPatch_AddPatch_NonExistentBranchAccepted_NoHook(t *testing.T) {
	slug := "cp-add-noexist"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Ensure no hook is registered.
	RegisterBranchCheckHook(nil)
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	body := `{"branch_name": "feature/does-not-exist-in-git"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Errorf("POST with non-existent branch (no hook): status = %d; want %d (accepted); body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// TS-NS-1: POST /workspaces/:slug/patches returns HTTP 400 when the given
// branch_name does not exist in the workspace git repository.
func TestCarryPatch_AddPatch_NonExistentBranchRejected(t *testing.T) {
	slug := "cp-add-noexist-400"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Register a hook that always fails (simulates branch not found).
	RegisterBranchCheckHook(func(_, _ string) error {
		return fmt.Errorf("unknown revision")
	})
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	body := `{"branch_name": "feature/does-not-exist-in-git"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST with non-existent branch: status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// Verify error message mentions branch.
	var errResp errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if !strings.Contains(errResp.Error.Message, "branch does not exist") {
		t.Errorf("error message = %q; want message containing 'branch does not exist'",
			errResp.Error.Message)
	}

	// Verify no patch was created in the database.
	patches, err := listPatches(env.db, slug)
	if err != nil {
		t.Fatalf("listPatches: %v", err)
	}
	if len(patches) != 0 {
		t.Errorf("expected 0 patches after failed add; got %d", len(patches))
	}
}

// TS-NS-2: POST /workspaces/:slug/patches with skip_branch_check: true
// succeeds even when the branch does not exist in the repository.
func TestCarryPatch_AddPatch_SkipBranchCheck(t *testing.T) {
	slug := "cp-add-skipcheck"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Register a hook that always fails (simulates branch not found).
	RegisterBranchCheckHook(func(_, _ string) error {
		return fmt.Errorf("unknown revision")
	})
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	body := `{"branch_name": "feature/will-be-pushed-later", "skip_branch_check": true}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Errorf("POST with skip_branch_check=true: status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Verify patch was created.
	patches, err := listPatches(env.db, slug)
	if err != nil {
		t.Fatalf("listPatches: %v", err)
	}
	if len(patches) != 1 {
		t.Errorf("expected 1 patch after add with skip_branch_check; got %d", len(patches))
	}
}

// When the branch-check hook is registered and the branch exists,
// the patch is created successfully.
func TestCarryPatch_AddPatch_ExistingBranchAccepted(t *testing.T) {
	slug := "cp-add-exists-ok"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Register a hook that always succeeds (simulates branch found).
	RegisterBranchCheckHook(func(_, _ string) error {
		return nil
	})
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	body := `{"branch_name": "feature/valid-branch"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST with existing branch: status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// 15-REQ-8.E2: Position greater than (max+1) is clamped to (max+1).
// Requirement: 15-REQ-8.E2
func TestCarryPatch_AddPatch_PositionClampedToMax(t *testing.T) {
	slug := "cp-add-clamp"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed one existing patch at position 1.
	seedPatchRaw(t, env.db, "clamp-1", slug, "feature/a", 1)

	// Request position 100 — should be clamped to 2.
	body := `{"branch_name": "feature/clamped", "position": 100}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if pos, ok := resp["position"]; !ok {
		t.Error("response missing 'position' field")
	} else if pos != float64(2) {
		t.Errorf("position = %v; want 2 (clamped from 100)", pos)
	}
}

// 15-REQ-8.E3: Position less than 1 returns HTTP 400.
// Requirement: 15-REQ-8.E3
func TestCarryPatch_AddPatch_PositionLessThanOneRejected(t *testing.T) {
	slug := "cp-add-negpos"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	for _, pos := range []string{"0", "-1"} {
		t.Run("position_"+pos, func(t *testing.T) {
			body := `{"branch_name": "feature/neg", "position": ` + pos + `}`
			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST with position=%s: status = %d; want %d; body: %s",
					pos, rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

// 15-REQ-8.E4: A caller without patches:write scope is rejected with HTTP 403.
// Requirement: 15-REQ-8.E4
func TestCarryPatch_AddPatch_PATWithoutWriteScope(t *testing.T) {
	slug := "cp-add-noscope"
	env := newPatchTestEnv(t, slug, "deploy")

	// PAT with only patches:read scope.
	auth := patAuth("user-1", "patches:read")

	body := `{"branch_name": "feature/forbidden"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST with patches:read scope: status = %d; want %d; body: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// 15-REQ-8.E6: Workspace does not exist returns HTTP 400.
// Requirement: 15-REQ-8.E6
func TestCarryPatch_AddPatch_WorkspaceNotFound(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("user-1")

	body := `{"branch_name": "feature/foo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/nonexistent-ws/patches", body, auth)

	// Spec says HTTP 400 for non-existent workspace via WriteAPIError.
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST to non-existent workspace: status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// Verify error envelope format (WriteAPIError).
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// ========================================================================
// Spec 15 Task 3.3: List patches operation
// (TS-15-29, TS-15-30)
// Requirements: 15-REQ-9
// ========================================================================

// TS-15-29: GET /api/v1/workspaces/:slug/patches returns HTTP 200 with a
// JSON array of patches ordered by position ascending.
// Requirement: 15-REQ-9.1
func TestCarryPatch_ListPatches_OrderedByPosition(t *testing.T) {
	slug := "cp-list-ordered"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed three patches in non-sequential insert order.
	seedPatchRaw(t, env.db, "list-3", slug, "feature/c", 3)
	seedPatchRaw(t, env.db, "list-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "list-2", slug, "feature/b", 2)

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var patches []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &patches); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(patches) != 3 {
		t.Fatalf("got %d patches; want 3", len(patches))
	}

	// Verify order: position 1, 2, 3.
	for i, expectedPos := range []float64{1, 2, 3} {
		if pos, ok := patches[i]["position"]; !ok {
			t.Errorf("patch[%d] missing 'position' field", i)
		} else if pos != expectedPos {
			t.Errorf("patch[%d] position = %v; want %v", i, pos, expectedPos)
		}
	}

	// Verify patch fields are present.
	requiredFields := []string{"id", "branch_name", "position", "status", "added_at", "updated_at"}
	for i, p := range patches {
		for _, field := range requiredFields {
			if _, ok := p[field]; !ok {
				t.Errorf("patch[%d] missing field %q", i, field)
			}
		}
	}
}

// TS-15-29 (empty): GET returns an empty array when no patches exist.
// Requirement: 15-REQ-9.1
func TestCarryPatch_ListPatches_EmptyArray(t *testing.T) {
	slug := "cp-list-empty"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var patches []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &patches); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(patches) != 0 {
		t.Errorf("got %d patches; want 0 (empty array)", len(patches))
	}
}

// TS-15-30: A caller without patches:read scope attempting to list patches
// receives HTTP 403 with a JSON error body.
// Requirement: 15-REQ-9.2
func TestCarryPatch_ListPatches_PATWithoutReadScope(t *testing.T) {
	slug := "cp-list-noscope"
	env := newPatchTestEnv(t, slug, "deploy")

	// PAT with no patches:read scope.
	auth := patAuth("user-1")

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("GET without patches:read scope: status = %d; want %d; body: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// 15-REQ-9.E1: GET /api/v1/workspaces/:slug/patches for a non-existent
// workspace returns HTTP 404 via WriteAPIError.
// Requirement: 15-REQ-9.E1
func TestCarryPatch_ListPatches_WorkspaceNotFound(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("user-1")

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/no-such-ws/patches", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET for non-existent workspace: status = %d; want %d; body: %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	// Verify error envelope format (WriteAPIError).
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// 15-REQ-9.E2: GET /api/v1/workspaces/:slug/patches on a standard workspace
// returns HTTP 200 with an empty array.
// Requirement: 15-REQ-9.E2
func TestCarryPatch_ListPatches_StandardWorkspaceReturns400(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("user-1")

	env.seedWorkspace(t, &Workspace{
		Slug:    "std-list-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "active",
	})

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/std-list-ws/patches", "", auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// seedPatchRawWithConflictFiles inserts a patch row with conflict_files set.
func seedPatchRawWithConflictFiles(t *testing.T, db *sql.DB, id, workspaceSlug, branchName string, position int, status string, conflictFiles []string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	var cfJSON *string
	if len(conflictFiles) > 0 {
		b, err := json.Marshal(conflictFiles)
		if err != nil {
			t.Fatalf("seedPatchRawWithConflictFiles: marshal: %v", err)
		}
		s := string(b)
		cfJSON = &s
	}
	_, err := db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, conflict_files, added_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, workspaceSlug, branchName, position, status, cfJSON, now, now,
	)
	if err != nil {
		t.Fatalf("seedPatchRawWithConflictFiles(%q) failed: %v", id, err)
	}
}

// TS-NS-4: GET /workspaces/:slug/patches response includes conflict_files for
// each patch when present.
// Requirement: NS-REQ-4
func TestCarryPatch_ListPatches_IncludesConflictFiles(t *testing.T) {
	slug := "cp-list-cf"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed a conflicting patch with conflict_files.
	seedPatchRawWithConflictFiles(t, env.db, "cf-1", slug, "feature/conflict", 1, "conflict", []string{"pkg/api.go", "pkg/handler.go"})
	// Seed a clean patch without conflict_files.
	seedPatchRaw(t, env.db, "cf-2", slug, "feature/clean", 2)

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var patches []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &patches); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(patches) != 2 {
		t.Fatalf("got %d patches; want 2", len(patches))
	}

	// First patch (conflict) should have conflict_files.
	cf, ok := patches[0]["conflict_files"]
	if !ok {
		t.Fatal("patch[0] (conflict) missing conflict_files field")
	}
	cfSlice, ok := cf.([]any)
	if !ok {
		t.Fatalf("patch[0] conflict_files is not an array: %T", cf)
	}
	if len(cfSlice) != 2 {
		t.Fatalf("patch[0] conflict_files has %d entries; want 2", len(cfSlice))
	}
	expectedFiles := []string{"pkg/api.go", "pkg/handler.go"}
	for i, expected := range expectedFiles {
		if got, _ := cfSlice[i].(string); got != expected {
			t.Errorf("patch[0] conflict_files[%d] = %q; want %q", i, got, expected)
		}
	}

	// Second patch (clean) should not have conflict_files (omitted or absent).
	if cf2, ok := patches[1]["conflict_files"]; ok {
		t.Errorf("patch[1] (clean) should not have conflict_files, got %v", cf2)
	}
}

// TS-NS-3: SQLPatchStore.ListPatches and UpdatePatchStatus execute without
// error on a fresh database with conflict_files.
// Requirement: NS-REQ-3
func TestPatchStore_ConflictFilesRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	// Insert a workspace.
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, created_at, updated_at)
		 VALUES (?, ?, ?, 'active', 'ready', 'carry_patch', ?, ?)`,
		"ws-cf-rt", "https://github.com/org/repo", "user-1", now, now,
	)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	// Insert a patch.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', ?, ?)`,
		"p1", "ws-cf-rt", "feature/test", 1, now, now,
	)
	if err != nil {
		t.Fatalf("insert patch: %v", err)
	}

	// Update conflict_files via raw SQL (simulating UpdatePatchStatus).
	conflictJSON := `["pkg/api.go","pkg/handler.go"]`
	_, err = db.Exec(
		`UPDATE patches SET status = ?, conflict_files = ?, updated_at = ? WHERE id = ?`,
		"conflict", conflictJSON, now, "p1",
	)
	if err != nil {
		t.Fatalf("update conflict_files: %v", err)
	}

	// Read back via listPatches.
	patches, err := listPatches(db, "ws-cf-rt")
	if err != nil {
		t.Fatalf("listPatches returned error: %v", err)
	}
	if len(patches) != 1 {
		t.Fatalf("got %d patches; want 1", len(patches))
	}

	p := patches[0]
	if p.Status != "conflict" {
		t.Errorf("patch status = %q; want %q", p.Status, "conflict")
	}
	if len(p.ConflictFiles) != 2 {
		t.Fatalf("patch ConflictFiles has %d entries; want 2", len(p.ConflictFiles))
	}
	if p.ConflictFiles[0] != "pkg/api.go" {
		t.Errorf("ConflictFiles[0] = %q; want %q", p.ConflictFiles[0], "pkg/api.go")
	}
	if p.ConflictFiles[1] != "pkg/handler.go" {
		t.Errorf("ConflictFiles[1] = %q; want %q", p.ConflictFiles[1], "pkg/handler.go")
	}
}

// TestPatchStore_ConflictFilesNullHandling verifies that patches without
// conflict_files return nil/empty ConflictFiles slice.
func TestPatchStore_ConflictFilesNullHandling(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	// Insert a workspace and patch without conflict_files.
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, created_at, updated_at)
		 VALUES (?, ?, ?, 'active', 'ready', 'carry_patch', ?, ?)`,
		"ws-cf-null", "https://github.com/org/repo", "user-1", now, now,
	)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', ?, ?)`,
		"p1", "ws-cf-null", "feature/test", 1, now, now,
	)
	if err != nil {
		t.Fatalf("insert patch: %v", err)
	}

	patches, err := listPatches(db, "ws-cf-null")
	if err != nil {
		t.Fatalf("listPatches returned error: %v", err)
	}
	if len(patches) != 1 {
		t.Fatalf("got %d patches; want 1", len(patches))
	}

	if len(patches[0].ConflictFiles) != 0 {
		t.Errorf("ConflictFiles should be empty for non-conflict patch, got %v", patches[0].ConflictFiles)
	}
}

// ========================================================================
// Issue #13: Idempotent and batch patch-add operations
// ========================================================================

// TS-NS-1 (Issue #13): POST /patches with if_not_exists:true and an existing
// branch returns HTTP 200 with the existing record.
// Requirement: NS-REQ-1
func TestCarryPatch_AddPatch_IfNotExists_ReturnsExisting(t *testing.T) {
	slug := "cp-idempotent"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed an existing patch.
	seedPatchRaw(t, env.db, "existing-id", slug, "feature/x", 1)

	// Retrieve the seeded patch to get its full state.
	var origAddedAt, origUpdatedAt string
	err := env.db.QueryRow(
		`SELECT added_at, updated_at FROM patches WHERE id = ?`, "existing-id",
	).Scan(&origAddedAt, &origUpdatedAt)
	if err != nil {
		t.Fatalf("failed to retrieve original patch: %v", err)
	}

	body := `{"branch_name": "feature/x", "if_not_exists": true}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST with if_not_exists=true: status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify the returned record is the existing one.
	if resp["id"] != "existing-id" {
		t.Errorf("id = %v; want %q", resp["id"], "existing-id")
	}
	if resp["position"] != float64(1) {
		t.Errorf("position = %v; want 1", resp["position"])
	}
	if resp["status"] != "active" {
		t.Errorf("status = %v; want %q", resp["status"], "active")
	}
	if resp["added_at"] != origAddedAt {
		t.Errorf("added_at = %v; want %q (original)", resp["added_at"], origAddedAt)
	}

	// Verify only one row exists for this branch.
	var count int
	err = env.db.QueryRow(
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = ? AND branch_name = ?`,
		slug, "feature/x",
	).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 patch for feature/x; got %d", count)
	}
}

// TS-NS-2 (Issue #13): POST /patches without if_not_exists (or false) and an
// existing branch still returns HTTP 409. No behaviour regression.
// Requirement: NS-REQ-2
func TestCarryPatch_AddPatch_DuplicateWithoutIfNotExists_Returns409(t *testing.T) {
	slug := "cp-no-idempotent"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	seedPatchRaw(t, env.db, "existing-dup", slug, "feature/y", 1)

	t.Run("absent_if_not_exists", func(t *testing.T) {
		body := `{"branch_name": "feature/y"}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		if rec.Code != http.StatusConflict {
			t.Errorf("POST without if_not_exists: status = %d; want %d; body: %s",
				rec.Code, http.StatusConflict, rec.Body.String())
		}
	})

	t.Run("if_not_exists_false", func(t *testing.T) {
		body := `{"branch_name": "feature/y", "if_not_exists": false}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		if rec.Code != http.StatusConflict {
			t.Errorf("POST with if_not_exists=false: status = %d; want %d; body: %s",
				rec.Code, http.StatusConflict, rec.Body.String())
		}
	})
}

// TS-NS-1 (Issue #13): POST /patches with if_not_exists:true and a NEW branch
// still creates the patch (201).
func TestCarryPatch_AddPatch_IfNotExists_NewBranch_Creates(t *testing.T) {
	slug := "cp-idempotent-new"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	body := `{"branch_name": "feature/brand-new", "if_not_exists": true}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST with if_not_exists=true (new branch): status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["branch_name"] != "feature/brand-new" {
		t.Errorf("branch_name = %v; want %q", resp["branch_name"], "feature/brand-new")
	}
}

// TS-NS-3 (Issue #13): Batch add with array body inserts all patches atomically
// with sequential positions.
// Requirement: NS-REQ-3
func TestCarryPatch_AddPatch_BatchInsert(t *testing.T) {
	slug := "cp-batch"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	body := `[{"branch_name":"a"},{"branch_name":"b"},{"branch_name":"c"}]`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST batch: status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp) != 3 {
		t.Fatalf("response has %d patches; want 3", len(resp))
	}

	// Verify positions are 1, 2, 3.
	for i, expected := range []float64{1, 2, 3} {
		if resp[i]["position"] != expected {
			t.Errorf("patch[%d] position = %v; want %v", i, resp[i]["position"], expected)
		}
	}

	// Verify database has exactly 3 rows.
	var count int
	err := env.db.QueryRow(
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, slug,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 patches in database; got %d", count)
	}
}

// TS-NS-3 (Issue #13): Batch add with a duplicate branch rolls back entirely.
// Requirement: NS-REQ-3
func TestCarryPatch_AddPatch_BatchRollbackOnDuplicate(t *testing.T) {
	slug := "cp-batch-dup"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed an existing patch.
	seedPatchRaw(t, env.db, "pre-existing", slug, "feature/dup", 1)

	// Batch includes existing branch — should fail.
	body := `[{"branch_name":"feature/new1"},{"branch_name":"feature/dup"},{"branch_name":"feature/new2"}]`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusConflict {
		t.Errorf("POST batch with duplicate: status = %d; want %d; body: %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	// Verify no new patches were inserted (only the pre-existing one remains).
	var count int
	err := env.db.QueryRow(
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, slug,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 patch (pre-existing only) after failed batch; got %d", count)
	}
}

// TS-NS-3 (Issue #13): Batch add with duplicate branch_name within the batch
// is rejected.
func TestCarryPatch_AddPatch_BatchDuplicateWithinBatch(t *testing.T) {
	slug := "cp-batch-indup"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	body := `[{"branch_name":"feature/same"},{"branch_name":"feature/same"}]`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusConflict {
		t.Errorf("POST batch with internal duplicate: status = %d; want %d; body: %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

// TS-NS-4 (Issue #13): Batch add with explicit position shifts existing patches.
// Requirement: NS-REQ-4
func TestCarryPatch_AddPatch_BatchWithExplicitPosition(t *testing.T) {
	slug := "cp-batch-pos"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed two existing patches at positions 1 and 2.
	seedPatchRaw(t, env.db, "orig-1", slug, "feature/orig1", 1)
	seedPatchRaw(t, env.db, "orig-2", slug, "feature/orig2", 2)

	// Insert: x at position 1 (shifts existing), y appended.
	body := `[{"branch_name":"x","position":1},{"branch_name":"y"}]`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST batch with position: status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Verify database state: should have 4 patches with contiguous positions.
	rows, err := env.db.Query(
		`SELECT branch_name, position FROM patches WHERE workspace_slug = ? ORDER BY position`,
		slug,
	)
	if err != nil {
		t.Fatalf("query patches failed: %v", err)
	}
	defer rows.Close()

	type branchPos struct {
		branch   string
		position int
	}
	var got []branchPos
	for rows.Next() {
		var bp branchPos
		if err := rows.Scan(&bp.branch, &bp.position); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		got = append(got, bp)
	}

	if len(got) != 4 {
		t.Fatalf("got %d patches; want 4", len(got))
	}

	// Verify positions are contiguous 1-based.
	for i, bp := range got {
		if bp.position != i+1 {
			t.Errorf("patch %q: position = %d; want %d", bp.branch, bp.position, i+1)
		}
	}

	// Verify x is at position 1.
	if got[0].branch != "x" {
		t.Errorf("patch at position 1 = %q; want %q", got[0].branch, "x")
	}
}

// TS-NS-4 (Issue #13): Batch add appends after existing when no position specified.
func TestCarryPatch_AddPatch_BatchAppendAfterExisting(t *testing.T) {
	slug := "cp-batch-append"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed 2 existing patches.
	seedPatchRaw(t, env.db, "orig-a", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "orig-b", slug, "feature/b", 2)

	body := `[{"branch_name":"c"},{"branch_name":"d"}]`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST batch append: status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// New patches should be at positions 3 and 4.
	if len(resp) != 2 {
		t.Fatalf("response has %d patches; want 2", len(resp))
	}
	if resp[0]["position"] != float64(3) {
		t.Errorf("patch c position = %v; want 3", resp[0]["position"])
	}
	if resp[1]["position"] != float64(4) {
		t.Errorf("patch d position = %v; want 4", resp[1]["position"])
	}
}

// Batch add with empty array returns 400.
func TestCarryPatch_AddPatch_BatchEmptyArray(t *testing.T) {
	slug := "cp-batch-empty"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	body := `[]`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST empty batch: status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
