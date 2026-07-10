package users_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/users"
)

// ---------------------------------------------------------------------------
// TS-02-20: Authenticated user can update their own full_name; null or empty
// string clears the field; updated_at bumped only when value changes.
// Requirement: 02-REQ-7.1
//
// NOTE: Reviewer finding (critical): users.full_name has NOT NULL DEFAULT ''.
// Storing NULL is impossible. The implementation should store empty string
// when null or "" is passed. Tests adapted accordingly — cleared full_name
// is "" not null. See docs/errata/02_user_management_divergences.md.
// ---------------------------------------------------------------------------

func TestUpdateUser_SelfUpdateFullName(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	t0 := "2025-01-01T00:00:00Z"
	insertTestUser(t, db, "user-uuid-2", "selfupdate", "self@example.com", "Old Name",
		"active", "github", "self-001", t0)

	e := setupEcho()
	e.PUT("/api/v1/users/:id", users.UpdateUserHandler(db), setAuthContext(userAuthContext("user-uuid-2")))

	t.Run("update_full_name", func(t *testing.T) {
		body := `{"full_name":"New Name"}`
		req := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-uuid-2", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if resp["full_name"] != "New Name" {
			t.Errorf("expected full_name 'New Name', got %v", resp["full_name"])
		}

		// updated_at should be bumped (> T0)
		updatedAt, ok := resp["updated_at"].(string)
		if !ok {
			t.Fatal("expected updated_at to be a string")
		}
		if updatedAt <= t0 {
			t.Errorf("expected updated_at > %s, got %s", t0, updatedAt)
		}

		// provider_id should be included in update response
		if _, ok := resp["provider_id"]; !ok {
			t.Error("expected provider_id in update response")
		}

		// team_memberships should NOT be included in update response
		if _, ok := resp["team_memberships"]; ok {
			t.Error("team_memberships should NOT be in PUT response")
		}
	})

	t.Run("clear_full_name_with_empty_string", func(t *testing.T) {
		// Per spec: null or empty string clears the field.
		// Per DDL constraint: full_name is NOT NULL DEFAULT '' — stored as "".
		body := `{"full_name":""}`
		req := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-uuid-2", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		// full_name should be cleared (empty string due to NOT NULL constraint).
		fullName := resp["full_name"]
		if fullName != "" && fullName != nil {
			t.Errorf("expected full_name to be empty or null, got %v", fullName)
		}
	})
}

// ---------------------------------------------------------------------------
// TS-02-21: Admin can update status on any user; updated_at bumped only
// when value changes.
// Requirement: 02-REQ-7.2
// ---------------------------------------------------------------------------

func TestUpdateUser_AdminUpdateStatus(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	t0 := "2025-01-01T00:00:00Z"
	insertTestUser(t, db, "user-uuid-3", "statususer", "status@example.com", "Status User",
		"active", "github", "status-001", t0)

	e := setupEcho()
	e.PUT("/api/v1/users/:id", users.UpdateUserHandler(db), setAuthContext(adminAuthContext()))

	body := `{"status":"blocked"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-uuid-3", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["status"] != "blocked" {
		t.Errorf("expected status 'blocked', got %v", resp["status"])
	}

	updatedAt, ok := resp["updated_at"].(string)
	if !ok {
		t.Fatal("expected updated_at to be a string")
	}
	if updatedAt <= t0 {
		t.Errorf("expected updated_at > %s, got %s", t0, updatedAt)
	}

	if _, ok := resp["provider_id"]; !ok {
		t.Error("expected provider_id in update response")
	}
	if _, ok := resp["team_memberships"]; ok {
		t.Error("team_memberships should NOT be in PUT response")
	}
}

// ---------------------------------------------------------------------------
// TS-02-22: PUT /api/v1/users/:id with no recognized fields or identical
// values returns HTTP 200 with unmodified user object; updated_at not bumped.
// Requirement: 02-REQ-7.3
// ---------------------------------------------------------------------------

func TestUpdateUser_NoOpWrite(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	t0 := "2025-01-01T00:00:00Z"
	insertTestUser(t, db, "user-uuid-4", "noop", "noop@example.com", "Same Name",
		"active", "github", "noop-001", t0)

	e := setupEcho()
	e.PUT("/api/v1/users/:id", users.UpdateUserHandler(db), setAuthContext(userAuthContext("user-uuid-4")))

	t.Run("identical_value_does_not_bump_updated_at", func(t *testing.T) {
		// Sleep briefly to ensure time difference is detectable if updated_at is incorrectly bumped.
		time.Sleep(10 * time.Millisecond)

		body := `{"full_name":"Same Name"}`
		req := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-uuid-4", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if resp["full_name"] != "Same Name" {
			t.Errorf("expected full_name 'Same Name', got %v", resp["full_name"])
		}
		if resp["updated_at"] != t0 {
			t.Errorf("expected updated_at unchanged (%s), got %v", t0, resp["updated_at"])
		}
	})

	t.Run("empty_body_does_not_bump_updated_at", func(t *testing.T) {
		body := `{}`
		req := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-uuid-4", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if resp["updated_at"] != t0 {
			t.Errorf("expected updated_at unchanged (%s), got %v", t0, resp["updated_at"])
		}
	})
}

// ---------------------------------------------------------------------------
// TS-02-23: Admin updating their own user record operates with admin-level
// permissions and can set status on themselves.
// Requirement: 02-REQ-7.4
// ---------------------------------------------------------------------------

func TestUpdateUser_AdminSelfUpdate(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "admin-uuid-1", "adminself", "admin@example.com", "Admin",
		"active", "github", "admin-001", "2025-01-01T00:00:00Z")

	// The admin auth context doesn't have a UserID set (per spec 01), but
	// we simulate an admin who is also a user by setting UserID.
	adminCtx := &users.AuthContext{
		CredentialType: users.CredentialTypeAdmin,
		UserID:         "admin-uuid-1",
		IsAdmin:        true,
	}

	e := setupEcho()
	e.PUT("/api/v1/users/:id", users.UpdateUserHandler(db), setAuthContext(adminCtx))

	body := `{"status":"blocked"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/users/admin-uuid-1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["status"] != "blocked" {
		t.Errorf("expected status 'blocked' on admin self-update, got %v", resp["status"])
	}
}

// ---------------------------------------------------------------------------
// TS-02-E20: Non-admin PUT /api/v1/users/:id on another user's record
// returns HTTP 403; no update performed.
// Requirement: 02-REQ-7.E1
// ---------------------------------------------------------------------------

func TestUpdateUser_EdgeCase_NonAdminOtherUser(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	// User A (caller) and User B (target).
	insertTestUser(t, db, "user-uuid-a", "usera", "a@example.com", "User A",
		"active", "github", "a-001", "2025-01-01T00:00:00Z")
	insertTestUser(t, db, "user-uuid-b", "userb", "b@example.com", "Original",
		"active", "github", "b-001", "2025-01-02T00:00:00Z")

	e := setupEcho()
	e.PUT("/api/v1/users/:id", users.UpdateUserHandler(db), setAuthContext(userAuthContext("user-uuid-a")))

	body := `{"full_name":"Hacked"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-uuid-b", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected HTTP 403, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify User B's full_name is unchanged.
	var fullName string
	err := db.QueryRow("SELECT full_name FROM users WHERE id = ?", "user-uuid-b").Scan(&fullName)
	if err != nil {
		t.Fatalf("failed to query user B: %v", err)
	}
	if fullName != "Original" {
		t.Errorf("expected User B full_name 'Original', got %q", fullName)
	}
}

// ---------------------------------------------------------------------------
// TS-02-E21: Non-admin PUT /api/v1/users/:id with status field returns
// HTTP 403 regardless of other fields.
// Requirement: 02-REQ-7.E2
// ---------------------------------------------------------------------------

func TestUpdateUser_EdgeCase_NonAdminStatusField(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-uuid-c", "userc", "c@example.com", "User C",
		"active", "github", "c-001", "2025-01-01T00:00:00Z")

	e := setupEcho()
	e.PUT("/api/v1/users/:id", users.UpdateUserHandler(db), setAuthContext(userAuthContext("user-uuid-c")))

	// Non-admin trying to set status field (even on own record) should be forbidden.
	body := `{"status":"blocked","full_name":"Test"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-uuid-c", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected HTTP 403 for non-admin setting status, got %d: %s",
			rec.Code, rec.Body.String())
	}

	// Verify no changes were made (neither full_name nor status).
	var fullName, status string
	err := db.QueryRow("SELECT full_name, status FROM users WHERE id = ?", "user-uuid-c").Scan(&fullName, &status)
	if err != nil {
		t.Fatalf("failed to query user C: %v", err)
	}
	if fullName != "User C" {
		t.Errorf("expected full_name unchanged ('User C'), got %q", fullName)
	}
	if status != "active" {
		t.Errorf("expected status unchanged ('active'), got %q", status)
	}
}

// ---------------------------------------------------------------------------
// TS-02-E22: PUT /api/v1/users/:id returns HTTP 404 with structured error
// body when user ID does not exist.
// Requirement: 02-REQ-7.E3
// ---------------------------------------------------------------------------

func TestUpdateUser_EdgeCase_NotFound(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	e := setupEcho()
	e.PUT("/api/v1/users/:id", users.UpdateUserHandler(db), setAuthContext(adminAuthContext()))

	body := `{"full_name":"Test"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/users/missing-user-uuid", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected HTTP 404, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify structured error body.
	var resp struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse error body: %v", err)
	}
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("expected error code 404, got %d", resp.Error.Code)
	}
}
