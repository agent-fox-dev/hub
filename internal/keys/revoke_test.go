package keys_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-fox-dev/hub/internal/keys"
)

// ---------------------------------------------------------------------------
// TS-02-31: DELETE /api/v1/keys/:key_id sets revoked_at on first revocation
// and returns HTTP 204 with no body.
// Requirement: 02-REQ-10.1
// ---------------------------------------------------------------------------

func TestRevokeKey_SetsRevokedAt(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-uuid-9", "user9", "u9@e.com", "active", "github", "ext-9", "2025-01-01T00:00:00Z")
	insertTestAPIKey(t, db, "keyToRvk", "user-uuid-9", "secret-torevoke-32-chars-pad-12", "2025-01-01T00:00:00Z", nil, nil)

	e := setupEcho()
	e.DELETE("/api/v1/keys/:key_id", keys.RevokeKeyHandler(db), setAuthContext(userAuthContext("user-uuid-9")))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/keys/keyToRvk", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected HTTP 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Response body should be empty.
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for HTTP 204, got %q", rec.Body.String())
	}

	// Verify DB: revoked_at should be set.
	var revokedAt *string
	err := db.QueryRow("SELECT revoked_at FROM api_keys WHERE key_id = ?", "keyToRvk").Scan(&revokedAt)
	if err != nil {
		t.Fatalf("failed to query revoked_at: %v", err)
	}
	if revokedAt == nil {
		t.Error("expected revoked_at to be set (non-null) after revocation")
	}
}

// ---------------------------------------------------------------------------
// TS-02-32: DELETE /api/v1/keys/:key_id is idempotent; revoking an
// already-revoked key returns HTTP 204 without updating revoked_at again.
// Requirement: 02-REQ-10.2
// ---------------------------------------------------------------------------

func TestRevokeKey_Idempotent(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-idem", "useridem", "idem@e.com", "active", "github", "ext-idem", "2025-01-01T00:00:00Z")

	// Insert a key that is already revoked.
	revokedTime := "2025-01-15T12:00:00Z"
	insertTestAPIKey(t, db, "keyAlRvk", "user-idem", "secret-already-revoked-32-pad-1", "2025-01-01T00:00:00Z", nil, &revokedTime)

	e := setupEcho()
	e.DELETE("/api/v1/keys/:key_id", keys.RevokeKeyHandler(db), setAuthContext(adminAuthContext()))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/keys/keyAlRvk", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected HTTP 204 for idempotent revocation, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify revoked_at is unchanged (still the original timestamp).
	var dbRevokedAt string
	err := db.QueryRow("SELECT revoked_at FROM api_keys WHERE key_id = ?", "keyAlRvk").Scan(&dbRevokedAt)
	if err != nil {
		t.Fatalf("failed to query revoked_at: %v", err)
	}
	if dbRevokedAt != revokedTime {
		t.Errorf("expected revoked_at unchanged (%s), got %s", revokedTime, dbRevokedAt)
	}
}

// ---------------------------------------------------------------------------
// TS-02-33: Admin can revoke a key belonging to a blocked user; user status
// does not prevent admin key revocation.
// Requirement: 02-REQ-10.3
// ---------------------------------------------------------------------------

func TestRevokeKey_AdminRevokesBlockedUserKey(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-blk-r", "userblkr", "blkr@e.com", "blocked", "github", "ext-blk-r", "2025-01-01T00:00:00Z")
	insertTestAPIKey(t, db, "keyBlkRv", "user-blk-r", "secret-blocked-revoke-32-pad-12", "2025-01-01T00:00:00Z", nil, nil)

	e := setupEcho()
	e.DELETE("/api/v1/keys/:key_id", keys.RevokeKeyHandler(db), setAuthContext(adminAuthContext()))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/keys/keyBlkRv", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected HTTP 204 for admin revoking blocked user key, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify revoked_at is set.
	var revokedAt *string
	err := db.QueryRow("SELECT revoked_at FROM api_keys WHERE key_id = ?", "keyBlkRv").Scan(&revokedAt)
	if err != nil {
		t.Fatalf("failed to query revoked_at: %v", err)
	}
	if revokedAt == nil {
		t.Error("expected revoked_at to be set (non-null) after admin revocation")
	}
}

// ---------------------------------------------------------------------------
// TS-02-E26: DELETE /api/v1/keys/:key_id returns HTTP 404 when key_id does
// not exist in api_keys table.
// Requirement: 02-REQ-10.E1
// ---------------------------------------------------------------------------

func TestRevokeKey_EdgeCase_NotFound(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	e := setupEcho()
	e.DELETE("/api/v1/keys/:key_id", keys.RevokeKeyHandler(db), setAuthContext(adminAuthContext()))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/keys/keyNoExst", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected HTTP 404 for non-existent key, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify structured error body.
	var errResp struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error body: %v", err)
	}
	if errResp.Error.Code != http.StatusNotFound {
		t.Errorf("expected error code 404, got %d", errResp.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// TS-02-E27: Non-admin DELETE /api/v1/keys/:key_id for another user's key
// returns HTTP 403; revoked_at not set.
// Requirement: 02-REQ-10.E2
// ---------------------------------------------------------------------------

func TestRevokeKey_EdgeCase_NonAdminOtherUserKey(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-rvk-a", "userrvka", "rvka@e.com", "active", "github", "ext-rvk-a", "2025-01-01T00:00:00Z")
	insertTestUser(t, db, "user-rvk-c", "userrvkc", "rvkc@e.com", "active", "github", "ext-rvk-c", "2025-01-01T00:00:00Z")

	// Key belongs to user-rvk-c.
	insertTestAPIKey(t, db, "keyOfUsC", "user-rvk-c", "secret-of-user-c-32-chars-pad-1", "2025-01-01T00:00:00Z", nil, nil)

	e := setupEcho()
	// Caller is user-rvk-a, but key belongs to user-rvk-c.
	e.DELETE("/api/v1/keys/:key_id", keys.RevokeKeyHandler(db), setAuthContext(userAuthContext("user-rvk-a")))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/keys/keyOfUsC", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected HTTP 403, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify revoked_at remains null in DB.
	var revokedAt *string
	err := db.QueryRow("SELECT revoked_at FROM api_keys WHERE key_id = ?", "keyOfUsC").Scan(&revokedAt)
	if err != nil {
		t.Fatalf("failed to query revoked_at: %v", err)
	}
	if revokedAt != nil {
		t.Errorf("expected revoked_at to remain null, got %v", *revokedAt)
	}
}
