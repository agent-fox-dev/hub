package keys_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/keys"
)

// ---------------------------------------------------------------------------
// TS-02-27: POST /api/v1/keys/:key_id/refresh generates a new secret, stores
// SHA-256 hash, recalculates expires_at, returns key with plaintext secret
// and token.
// Requirement: 02-REQ-9.1
// ---------------------------------------------------------------------------

func TestRefreshKey_GeneratesNewSecret(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-uuid-6", "user6", "u6@e.com", "active", "github", "ext-6", "2025-01-01T00:00:00Z")

	// Create a key with 90-day expiry, created 10 days ago.
	createdAt := pastISO(10 * 24 * time.Hour)
	expiresAt := futureISO(80 * 24 * time.Hour) // 90 - 10 = 80 days from now
	oldSecret := "oldsecret-32-chars-padding-12345"
	insertTestAPIKey(t, db, "keyABC01", "user-uuid-6", oldSecret, createdAt, &expiresAt, nil)

	e := setupEcho()
	e.POST("/api/v1/keys/:key_id/refresh", keys.RefreshKeyHandler(db), setAuthContext(userAuthContext("user-uuid-6")))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys/keyABC01/refresh", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// key_id should be unchanged.
	if resp["key_id"] != "keyABC01" {
		t.Errorf("expected key_id 'keyABC01', got %v", resp["key_id"])
	}

	// New secret must be 32 alphanumeric chars.
	secret, ok := resp["secret"].(string)
	if !ok {
		t.Fatal("expected 'secret' field to be a string")
	}
	if len(secret) != 32 {
		t.Errorf("expected secret length 32, got %d", len(secret))
	}
	base62Re := regexp.MustCompile(`^[0-9A-Za-z]{32}$`)
	if !base62Re.MatchString(secret) {
		t.Errorf("secret should match ^[0-9A-Za-z]{32}$, got %q", secret)
	}

	// Token format: af_<key_id>_<secret>
	expectedToken := "af_keyABC01_" + secret
	if resp["token"] != expectedToken {
		t.Errorf("expected token %q, got %v", expectedToken, resp["token"])
	}

	// revoked_at should be null.
	if resp["revoked_at"] != nil {
		t.Errorf("expected revoked_at to be null, got %v", resp["revoked_at"])
	}

	// Verify DB: secret_hash should be updated to SHA-256 of new secret.
	var dbHash string
	err := db.QueryRow("SELECT secret_hash FROM api_keys WHERE key_id = ?", "keyABC01").Scan(&dbHash)
	if err != nil {
		t.Fatalf("failed to query secret_hash: %v", err)
	}
	expectedHash := sha256Hex(secret)
	if dbHash != expectedHash {
		t.Errorf("expected secret_hash=%s, got %s", expectedHash, dbHash)
	}

	// Old secret should no longer match.
	oldHash := sha256Hex(oldSecret)
	if dbHash == oldHash {
		t.Error("secret_hash should have changed from the old value")
	}

	// expires_at should be approximately 90 days from now (original duration reused).
	newExpiresAt, ok := resp["expires_at"].(string)
	if !ok {
		t.Fatal("expected expires_at field to be a string")
	}
	parsed, err := time.Parse(time.RFC3339, newExpiresAt)
	if err != nil {
		t.Fatalf("failed to parse expires_at: %v", err)
	}
	diffDays := time.Until(parsed).Hours() / 24
	if diffDays < 89.0 || diffDays > 91.0 {
		t.Errorf("expected expires_at approximately 90 days from now, got %.1f days", diffDays)
	}
}

// ---------------------------------------------------------------------------
// TS-02-28: Refreshing an expired (non-revoked) key is permitted; new
// expires_at is calculated using original expiry duration from now.
// Requirement: 02-REQ-9.2
// ---------------------------------------------------------------------------

func TestRefreshKey_ExpiredKeyPermitted(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-uuid-7", "user7", "u7@e.com", "active", "github", "ext-7", "2025-01-01T00:00:00Z")

	// Create a key with 30-day original expiry, now expired.
	createdAt := pastISO(60 * 24 * time.Hour)  // created 60 days ago
	expiredAt := pastISO(30 * 24 * time.Hour)   // expired 30 days ago (30-day original duration)
	insertTestAPIKey(t, db, "keyExpd1", "user-uuid-7", "secret-expired-32-chars-pad-123", createdAt, &expiredAt, nil)

	e := setupEcho()
	e.POST("/api/v1/keys/:key_id/refresh", keys.RefreshKeyHandler(db), setAuthContext(userAuthContext("user-uuid-7")))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys/keyExpd1/refresh", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 for expired key refresh, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["key_id"] != "keyExpd1" {
		t.Errorf("expected key_id 'keyExpd1', got %v", resp["key_id"])
	}

	// New secret should be present.
	secret, ok := resp["secret"].(string)
	if !ok || len(secret) != 32 {
		t.Fatalf("expected 32-char secret, got %v", resp["secret"])
	}

	// expires_at should be approximately 30 days from now (original duration reused).
	newExpiresAt, ok := resp["expires_at"].(string)
	if !ok {
		t.Fatal("expected expires_at to be a string")
	}
	parsed, err := time.Parse(time.RFC3339, newExpiresAt)
	if err != nil {
		t.Fatalf("failed to parse expires_at: %v", err)
	}
	diffDays := time.Until(parsed).Hours() / 24
	if diffDays < 29.0 || diffDays > 31.0 {
		t.Errorf("expected expires_at approximately 30 days from now, got %.1f days", diffDays)
	}
}

// ---------------------------------------------------------------------------
// TS-02-29: Admin can refresh a key belonging to a blocked user; user status
// does not prevent admin key management.
// Requirement: 02-REQ-9.3
// ---------------------------------------------------------------------------

func TestRefreshKey_AdminRefreshesBlockedUserKey(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-uuid-8", "user8", "u8@e.com", "blocked", "github", "ext-8", "2025-01-01T00:00:00Z")
	insertTestAPIKey(t, db, "keyBlk01", "user-uuid-8", "secret-blocked-32-chars-pad-123", "2025-01-01T00:00:00Z", nil, nil)

	e := setupEcho()
	e.POST("/api/v1/keys/:key_id/refresh", keys.RefreshKeyHandler(db), setAuthContext(adminAuthContext()))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys/keyBlk01/refresh", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 for admin refreshing blocked user key, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["key_id"] != "keyBlk01" {
		t.Errorf("expected key_id 'keyBlk01', got %v", resp["key_id"])
	}

	secret, ok := resp["secret"].(string)
	if !ok || len(secret) != 32 {
		t.Fatalf("expected 32-char secret, got %v", resp["secret"])
	}
}

// ---------------------------------------------------------------------------
// TS-02-30: Refresh endpoint does not allow caller to override expiry duration;
// original duration is always reused.
// Requirement: 02-REQ-9.4
// ---------------------------------------------------------------------------

func TestRefreshKey_IgnoresExpiresOverride(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-30day", "user30day", "u30@e.com", "active", "github", "ext-30", "2025-01-01T00:00:00Z")

	// 30-day key.
	createdAt := pastISO(5 * 24 * time.Hour)
	expiresAt := futureISO(25 * 24 * time.Hour) // 30 - 5 = 25 days remaining
	insertTestAPIKey(t, db, "key30day", "user-30day", "secret-30day-32-chars-pad-12345", createdAt, &expiresAt, nil)

	e := setupEcho()
	e.POST("/api/v1/keys/:key_id/refresh", keys.RefreshKeyHandler(db), setAuthContext(userAuthContext("user-30day")))

	// Attempt to override expires to 90 days — should be ignored.
	body := `{"expires":90}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys/key30day/refresh", strings.NewReader(body))
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

	// expires_at should be approximately 30 days from now (original duration), NOT 90 days.
	newExpiresAt, ok := resp["expires_at"].(string)
	if !ok {
		t.Fatal("expected expires_at to be a string")
	}
	parsed, err := time.Parse(time.RFC3339, newExpiresAt)
	if err != nil {
		t.Fatalf("failed to parse expires_at: %v", err)
	}
	diffDays := time.Until(parsed).Hours() / 24
	if diffDays < 29.0 || diffDays > 31.0 {
		t.Errorf("expected expires_at approximately 30 days from now (original duration), got %.1f days", diffDays)
	}
	// Make sure it's NOT approximately 90 days.
	if diffDays > 85.0 {
		t.Errorf("expires_at should NOT be approximately 90 days from now; caller override should be ignored, got %.1f days", diffDays)
	}
}

// ---------------------------------------------------------------------------
// TS-02-E24: POST /api/v1/keys/:key_id/refresh returns HTTP 404 for
// non-existent key_id or revoked key.
// Requirement: 02-REQ-9.E1
// ---------------------------------------------------------------------------

func TestRefreshKey_EdgeCase_NotFoundOrRevoked(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-ref-e", "userrefedge", "re@e.com", "active", "github", "ext-re", "2025-01-01T00:00:00Z")

	// Insert a revoked key.
	revokedTime := "2025-01-15T00:00:00Z"
	insertTestAPIKey(t, db, "keyRvkdR", "user-ref-e", "secret-revoked-32-chars-pad-123", "2025-01-01T00:00:00Z", nil, &revokedTime)

	e := setupEcho()
	e.POST("/api/v1/keys/:key_id/refresh", keys.RefreshKeyHandler(db), setAuthContext(adminAuthContext()))

	t.Run("nonexistent_key_id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/keys/keyNoExist/refresh", nil)
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
	})

	t.Run("revoked_key_treated_as_nonexistent", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/keys/keyRvkdR/refresh", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("expected HTTP 404 for revoked key, got %d: %s", rec.Code, rec.Body.String())
		}

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
	})
}

// ---------------------------------------------------------------------------
// TS-02-E25: Non-admin POST /api/v1/keys/:key_id/refresh for another user's
// key returns HTTP 403; no secret rotated.
// Requirement: 02-REQ-9.E2
// ---------------------------------------------------------------------------

func TestRefreshKey_EdgeCase_NonAdminOtherUserKey(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-caller", "usercaller", "caller@e.com", "active", "github", "ext-caller", "2025-01-01T00:00:00Z")
	insertTestUser(t, db, "user-owner", "userowner", "owner@e.com", "active", "github", "ext-owner", "2025-01-01T00:00:00Z")

	originalSecret := "secret-of-user-b-32-chars-pad-1"
	insertTestAPIKey(t, db, "keyOfUsB", "user-owner", originalSecret, "2025-01-01T00:00:00Z", nil, nil)

	e := setupEcho()
	// Caller is user-caller, but the key belongs to user-owner.
	e.POST("/api/v1/keys/:key_id/refresh", keys.RefreshKeyHandler(db), setAuthContext(userAuthContext("user-caller")))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys/keyOfUsB/refresh", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected HTTP 403, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify secret_hash is unchanged in DB.
	var dbHash string
	err := db.QueryRow("SELECT secret_hash FROM api_keys WHERE key_id = ?", "keyOfUsB").Scan(&dbHash)
	if err != nil {
		t.Fatalf("failed to query secret_hash: %v", err)
	}
	expectedHash := sha256Hex(originalSecret)
	if dbHash != expectedHash {
		t.Errorf("secret_hash should be unchanged; expected %s, got %s", expectedHash, dbHash)
	}
}
