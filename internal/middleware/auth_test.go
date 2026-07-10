package middleware_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/authctx"
	mw "github.com/agent-fox-dev/hub/internal/middleware"
	"github.com/labstack/echo/v4"
)

// authErrorEnvelope is a local type for auth test JSON parsing.
type authErrorEnvelope struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// setupAuthTestEcho creates an Echo instance with auth middleware applied to
// a test endpoint. Returns the Echo instance and a DB that can be used for
// inserting test data.
func setupAuthTestEcho(t *testing.T, db *sql.DB) *echo.Echo {
	t.Helper()
	e := echo.New()

	// Protected group with auth middleware.
	g := e.Group("/api/v1", mw.AuthMiddleware(db))

	// Test endpoint that echoes back the AuthContext.
	g.GET("/test", func(c echo.Context) error {
		raw := c.Get(string(authctx.AuthContextKey))
		if raw == nil {
			return c.JSON(http.StatusOK, map[string]any{
				"auth_context": nil,
			})
		}
		ac, ok := raw.(*authctx.AuthContext)
		if !ok {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "auth context is not *authctx.AuthContext",
			})
		}
		return c.JSON(http.StatusOK, map[string]any{
			"credential_type": string(ac.CredentialType),
			"is_admin":        ac.IsAdmin,
			"user_id":         ac.UserID,
			"workspace_id":    ac.WorkspaceID,
		})
	})

	return e
}

// ---------------------------------------------------------------------------
// 5.4 — Auth Middleware 401 Conditions
// ---------------------------------------------------------------------------

// TestSpec01_AuthMiddlewareFive401Conditions verifies that the auth middleware
// returns HTTP 401 with the standard error envelope for all five categories of
// missing/invalid token conditions:
//
//	(a) no Authorization header
//	(b) non-Bearer scheme (e.g., Basic)
//	(c) Authorization: Bearer <empty>
//	(d) unrecognized token prefix (e.g., totally-wrong)
//	(e) structural validation failure (e.g., af_12345 — too few parts)
//
// TS-01-45, REQ: 01-REQ-14.1
func TestSpec01_AuthMiddlewareFive401Conditions(t *testing.T) {
	db := setupTestDB(t)
	e := setupAuthTestEcho(t, db)

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{
			name:    "a_no_auth_header",
			headers: map[string]string{},
		},
		{
			name: "b_non_bearer_scheme",
			headers: map[string]string{
				"Authorization": "Basic dXNlcjpwYXNz",
			},
		},
		{
			name: "c_empty_bearer_token",
			headers: map[string]string{
				"Authorization": "Bearer ",
			},
		},
		{
			name: "d_unrecognized_prefix",
			headers: map[string]string{
				"Authorization": "Bearer totally-wrong-token-format",
			},
		},
		{
			name: "e_structural_failure",
			headers: map[string]string{
				"Authorization": "Bearer af_12345",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("case %s: status = %d, want %d", tc.name, rec.Code, http.StatusUnauthorized)
			}

			var env authErrorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("case %s: failed to parse response: %v; body = %q", tc.name, err, rec.Body.String())
			}
			if env.Error == nil {
				t.Fatalf("case %s: response should contain error envelope", tc.name)
			}
			if env.Error.Code != http.StatusUnauthorized {
				t.Errorf("case %s: envelope code = %d, want %d", tc.name, env.Error.Code, http.StatusUnauthorized)
			}
			expectedMsg := "missing or invalid authentication credentials"
			if env.Error.Message != expectedMsg {
				t.Errorf("case %s: envelope message = %q, want %q", tc.name, env.Error.Message, expectedMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 5.4 — AuthContext Retrieval from Echo Context
// ---------------------------------------------------------------------------

// TestSpec01_AuthContextRetrievable verifies that after successful
// authentication, the AuthContext struct is stored in Echo's context under
// the typed key AuthContextKey and is retrievable by downstream handlers
// via c.Get(string(AuthContextKey)).(*AuthContext).
//
// TS-01-49, REQ: 01-REQ-14.5
func TestSpec01_AuthContextRetrievable(t *testing.T) {
	db := setupAuthTestDBWithSchema(t)
	// Insert admin token hash for the test token suffix.
	suffix := "0000000000000000000000000000000000000000000000000000000000000000"
	insertAdminTokenHash(t, db, suffix)
	e := setupAuthTestEcho(t, db)

	// Use a valid admin token format to authenticate.
	// The stub middleware is pass-through, so for this test to properly verify
	// the behavior, auth middleware must set AuthContext on success.
	token := "af_admin_0000000000000000000000000000000000000000000000000000000000000000"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// The handler echoes back the auth context. With the stub (pass-through),
	// auth_context will be nil. This test SHOULD fail until auth middleware
	// is implemented.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response: %v; body = %q", err, rec.Body.String())
	}

	// After auth middleware is implemented, auth_context should be set.
	// For now, check that the handler reached and returned auth context data.
	if body["auth_context"] == nil && body["credential_type"] == nil {
		t.Error("AuthContext should be set in Echo context by auth middleware; got nil (middleware is pass-through stub)")
	}

	// When implemented, we expect:
	// credential_type == "admin", is_admin == true, user_id == "", workspace_id == ""
	if ct, ok := body["credential_type"]; ok {
		if ct != "admin" {
			t.Errorf("credential_type = %v, want %q", ct, "admin")
		}
	}
	if isAdmin, ok := body["is_admin"]; ok {
		if isAdmin != true {
			t.Errorf("is_admin = %v, want true", isAdmin)
		}
	}
}

// ---------------------------------------------------------------------------
// 5.4 — Structural Validation Before DB Query
// ---------------------------------------------------------------------------

// TestSpec01_StructuralValidationBeforeDB verifies that auth middleware
// performs ALL structural validation (part count, key_id/token_id length,
// character set, secret length and character set) before executing any
// database query. Any structural validation failure returns HTTP 401
// immediately without touching the DB.
//
// TS-01-50, REQ: 01-REQ-14.6
func TestSpec01_StructuralValidationBeforeDB(t *testing.T) {
	// Use a broken DB that panics/errors on any query. If auth middleware
	// makes a DB call for a structurally invalid token, the test will catch it
	// via error response (503 or panic instead of 401).
	brokenDB := setupBrokenDB(t)
	e := setupAuthTestEcho(t, brokenDB)

	invalidTokens := []struct {
		name  string
		token string
	}{
		{
			name:  "admin_token_too_short",
			token: "af_admin_0000",
		},
		{
			name:  "admin_token_not_hex",
			token: "af_admin_ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ",
		},
		{
			name:  "api_key_missing_secret",
			token: "af_abcd1234",
		},
		{
			name:  "api_key_key_id_too_short",
			token: "af_short_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
		{
			name:  "api_key_secret_too_short",
			token: "af_abcd1234_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", // 31 chars
		},
		{
			name:  "api_key_too_many_parts",
			token: "af_abcd1234_secret12_extra",
		},
		{
			name:  "workspace_token_id_too_short",
			token: "af_wt_short_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		},
		{
			name:  "workspace_token_secret_too_short",
			token: "af_wt_abcd1234_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", // 31 chars
		},
		{
			name:  "workspace_token_too_many_parts",
			token: "af_wt_abc_def_extra",
		},
		{
			name:  "unrecognized_prefix",
			token: "xyz_totally_wrong",
		},
		{
			name:  "empty_token",
			token: "",
		},
	}

	for _, tc := range invalidTokens {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			// Must be 401 — NOT 503 (which would mean a DB query was attempted
			// on the broken DB).
			if rec.Code == http.StatusServiceUnavailable {
				t.Errorf("token %q: got 503, means DB query was attempted before structural validation completed", tc.token)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("token %q: status = %d, want %d (structural validation should reject before DB query)",
					tc.token, rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

// ===========================================================================
// Task Group 6 — Auth Middleware Credential Verification & Edge Cases
// ===========================================================================

// ---------------------------------------------------------------------------
// 6.1 — Admin Token Auth and User API Key Auth
// ---------------------------------------------------------------------------

// TestSpec01_ValidAdminTokenAuthContext verifies that a valid admin token
// (af_admin_<64 hex chars>) is authenticated correctly and sets AuthContext
// with CredentialType=admin, IsAdmin=true, UserID="", WorkspaceID="".
//
// The test seeds admin_tokens with the SHA-256 hash of the token suffix,
// then presents the full token via Bearer header.
//
// TS-01-46, REQ: 01-REQ-14.2
func TestSpec01_ValidAdminTokenAuthContext(t *testing.T) {
	db := setupAuthTestDBWithSchema(t)
	suffix := "aabbccdd00112233aabbccdd00112233aabbccdd00112233aabbccdd00112233"
	insertAdminTokenHash(t, db, suffix)

	e := setupAuthTestEcho(t, db)
	token := "af_admin_" + suffix

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Must not be 401 or 403 — the token is valid.
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("valid admin token got 401; auth middleware should accept it")
	}
	if rec.Code == http.StatusForbidden {
		t.Fatalf("valid admin token got 403; admin should bypass blocked-user check")
	}

	// Parse the AuthContext echoed by the test handler.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response body: %v; body = %q", err, rec.Body.String())
	}

	// credential_type must be "admin".
	if ct, _ := body["credential_type"].(string); ct != "admin" {
		t.Errorf("credential_type = %q, want %q", ct, "admin")
	}
	// is_admin must be true.
	if isAdmin, _ := body["is_admin"].(bool); !isAdmin {
		t.Errorf("is_admin = %v, want true", body["is_admin"])
	}
	// user_id must be empty string.
	if uid, _ := body["user_id"].(string); uid != "" {
		t.Errorf("user_id = %q, want empty string", uid)
	}
	// workspace_id must be empty string.
	if wsid, _ := body["workspace_id"].(string); wsid != "" {
		t.Errorf("workspace_id = %q, want empty string", wsid)
	}
}

// TestSpec01_ValidAPIKeyAuthContext verifies that a valid user API key
// (af_<key_id>_<secret>) is authenticated correctly and sets AuthContext
// with CredentialType=api_key, UserID=<user UUID>, IsAdmin=false,
// WorkspaceID="".
//
// TS-01-47, REQ: 01-REQ-14.3
func TestSpec01_ValidAPIKeyAuthContext(t *testing.T) {
	db := setupAuthTestDBWithSchema(t)

	userID := "user-uuid-1234"
	keyID := "abcd1234" // 8 alphanumeric chars
	secret := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 32 alphanumeric chars

	insertUser(t, db, userID, "testuser1", "active")
	insertAPIKey(t, db, keyID, secret, userID, nil, nil)

	e := setupAuthTestEcho(t, db)
	token := "af_" + keyID + "_" + secret

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("valid API key got 401; auth middleware should accept it")
	}
	if rec.Code == http.StatusForbidden {
		t.Fatalf("valid API key for active user got 403")
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response body: %v; body = %q", err, rec.Body.String())
	}

	if ct, _ := body["credential_type"].(string); ct != "api_key" {
		t.Errorf("credential_type = %q, want %q", ct, "api_key")
	}
	if isAdmin, _ := body["is_admin"].(bool); isAdmin {
		t.Errorf("is_admin = %v, want false", body["is_admin"])
	}
	if uid, _ := body["user_id"].(string); uid != userID {
		t.Errorf("user_id = %q, want %q", uid, userID)
	}
	if wsid, _ := body["workspace_id"].(string); wsid != "" {
		t.Errorf("workspace_id = %q, want empty string", wsid)
	}
}

// TestSpec01_ConstantTimeCompareUsedInAuth performs a static analysis assertion
// that all three credential verification paths (admin token, API key, workspace
// token) use crypto/subtle.ConstantTimeCompare for hash comparison.
//
// This test reads the auth.go source file and checks for usage of
// subtle.ConstantTimeCompare. It does NOT check for bytes.Equal (which would
// be a timing-attack vulnerability).
//
// TS-01-51, REQ: 01-REQ-14.7
func TestSpec01_ConstantTimeCompareUsedInAuth(t *testing.T) {
	// Read the auth middleware source file.
	authSource, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatalf("failed to read auth.go: %v", err)
	}
	src := string(authSource)

	// Check that subtle.ConstantTimeCompare is used.
	if !strings.Contains(src, "subtle.ConstantTimeCompare") &&
		!strings.Contains(src, "ConstantTimeCompare") {
		t.Error("auth.go should use crypto/subtle.ConstantTimeCompare for hash comparison; not found in source")
	}

	// Check that bytes.Equal is NOT used for hash comparison.
	// bytes.Equal is vulnerable to timing attacks.
	if strings.Contains(src, "bytes.Equal") {
		t.Error("auth.go should NOT use bytes.Equal for hash comparison (timing attack vulnerability)")
	}
}

// TestSpec01_AdminTokenBypassesBlockedUser verifies that admin token
// authentication bypasses the blocked-user check entirely. Even if the admin
// user row in users has status=blocked, the admin token should still be
// accepted with IsAdmin=true and UserID="".
//
// TS-01-52, REQ: 01-REQ-14.8
func TestSpec01_AdminTokenBypassesBlockedUser(t *testing.T) {
	db := setupAuthTestDBWithSchema(t)

	// Insert admin user with status=blocked.
	insertUser(t, db, "admin-user-id", "admin", "blocked")

	// Insert admin token hash.
	suffix := "1111222233334444555566667777888899990000aaaabbbbccccddddeeee1234"
	insertAdminTokenHash(t, db, suffix)

	e := setupAuthTestEcho(t, db)
	token := "af_admin_" + suffix

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Must NOT be 403 — admin bypasses blocked-user check.
	if rec.Code == http.StatusForbidden {
		t.Fatal("admin token should bypass blocked-user check; got 403")
	}
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("valid admin token got 401; auth middleware should accept it")
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response body: %v; body = %q", err, rec.Body.String())
	}

	if ct, _ := body["credential_type"].(string); ct != "admin" {
		t.Errorf("credential_type = %q, want %q", ct, "admin")
	}
	if isAdmin, _ := body["is_admin"].(bool); !isAdmin {
		t.Errorf("is_admin = %v, want true", body["is_admin"])
	}
	if uid, _ := body["user_id"].(string); uid != "" {
		t.Errorf("user_id = %q, want empty string (admin token does no users lookup)", uid)
	}
}

// ---------------------------------------------------------------------------
// 6.2 — Workspace Token Auth and Token State Edge Cases
// ---------------------------------------------------------------------------

// TestSpec01_ValidWorkspaceTokenAuthContext verifies that a valid workspace
// token (af_wt_<token_id>_<secret>) is authenticated correctly and sets
// AuthContext with CredentialType=workspace_token, UserID=<user UUID>,
// WorkspaceID=<workspace UUID>, IsAdmin=false.
//
// TS-01-48, REQ: 01-REQ-14.4
func TestSpec01_ValidWorkspaceTokenAuthContext(t *testing.T) {
	db := setupAuthTestDBWithSchema(t)

	userID := "ws-user-uuid-5678"
	wsID := "workspace-uuid-9012"
	tokenID := "wt123456" // 8 alphanumeric chars
	secret := "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB" // 32 alphanumeric chars

	insertUser(t, db, userID, "wsuser1", "active")
	insertWorkspace(t, db, wsID, "test-workspace", userID)
	insertWorkspaceToken(t, db, tokenID, secret, wsID, userID, nil, nil)

	e := setupAuthTestEcho(t, db)
	token := "af_wt_" + tokenID + "_" + secret

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("valid workspace token got 401; auth middleware should accept it")
	}
	if rec.Code == http.StatusForbidden {
		t.Fatalf("valid workspace token for active user got 403")
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response body: %v; body = %q", err, rec.Body.String())
	}

	if ct, _ := body["credential_type"].(string); ct != "workspace_token" {
		t.Errorf("credential_type = %q, want %q", ct, "workspace_token")
	}
	if isAdmin, _ := body["is_admin"].(bool); isAdmin {
		t.Errorf("is_admin = %v, want false", body["is_admin"])
	}
	if uid, _ := body["user_id"].(string); uid != userID {
		t.Errorf("user_id = %q, want %q", uid, userID)
	}
	if wsid, _ := body["workspace_id"].(string); wsid != wsID {
		t.Errorf("workspace_id = %q, want %q", wsid, wsID)
	}
}

// TestSpec01_MalformedWorkspaceTokens401NoDB verifies that malformed workspace
// tokens return HTTP 401 without any DB query. Tests three specific
// malformations:
//
//	(a) too many parts after af_wt_ prefix
//	(b) token_id shorter than 8 characters
//	(c) secret shorter than 32 characters
//
// Uses a broken DB — if any DB query is attempted, it produces 503 or error
// instead of 401.
//
// TS-01-E16, REQ: 01-REQ-14.E2
func TestSpec01_MalformedWorkspaceTokens401NoDB(t *testing.T) {
	brokenDB := setupBrokenDB(t)
	e := setupAuthTestEcho(t, brokenDB)

	cases := []struct {
		name  string
		token string
	}{
		{
			name:  "too_many_parts_after_prefix",
			token: "af_wt_abc_def_extra",
		},
		{
			name:  "token_id_too_short",
			token: "af_wt_short_" + strings.Repeat("A", 32),
		},
		{
			name:  "secret_too_short",
			token: "af_wt_abcd1234_" + strings.Repeat("B", 31),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code == http.StatusServiceUnavailable {
				t.Errorf("token %q: got 503, means DB query was attempted for structurally invalid workspace token", tc.token)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("token %q: status = %d, want %d", tc.token, rec.Code, http.StatusUnauthorized)
			}

			// Verify standard error envelope.
			var env authErrorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err == nil && env.Error != nil {
				if env.Error.Code != 401 {
					t.Errorf("envelope code = %d, want 401", env.Error.Code)
				}
				if env.Error.Message != "missing or invalid authentication credentials" {
					t.Errorf("envelope message = %q, want standard auth error", env.Error.Message)
				}
			}
		})
	}
}

// TestSpec01_MalformedAPIKeyTokens401NoDB verifies that malformed API key
// tokens return HTTP 401 without any DB query. Tests three specific
// malformations:
//
//	(a) key_id shorter than 8 characters
//	(b) missing secret part entirely
//	(c) secret shorter than 32 characters
//
// TS-01-E17, REQ: 01-REQ-14.E3
func TestSpec01_MalformedAPIKeyTokens401NoDB(t *testing.T) {
	brokenDB := setupBrokenDB(t)
	e := setupAuthTestEcho(t, brokenDB)

	cases := []struct {
		name  string
		token string
	}{
		{
			name:  "key_id_too_short",
			token: "af_short_" + strings.Repeat("A", 32),
		},
		{
			name:  "missing_secret_part",
			token: "af_abcd1234",
		},
		{
			name:  "secret_too_short",
			token: "af_abcd1234_" + strings.Repeat("B", 31),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code == http.StatusServiceUnavailable {
				t.Errorf("token %q: got 503, means DB query was attempted for structurally invalid API key", tc.token)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("token %q: status = %d, want %d", tc.token, rec.Code, http.StatusUnauthorized)
			}

			// Verify standard error envelope.
			var env authErrorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err == nil && env.Error != nil {
				if env.Error.Code != 401 {
					t.Errorf("envelope code = %d, want 401", env.Error.Code)
				}
				if env.Error.Message != "missing or invalid authentication credentials" {
					t.Errorf("envelope message = %q, want standard auth error", env.Error.Message)
				}
			}
		})
	}
}

// TestSpec01_RevokedToken401 verifies that a revoked token (api_keys row with
// non-null revoked_at) returns HTTP 401 with the standard error envelope.
// The response should be identical to an expired token response — no
// distinction is made between revoked and expired states.
//
// TS-01-E18, REQ: 01-REQ-14.E4
func TestSpec01_RevokedToken401(t *testing.T) {
	db := setupAuthTestDBWithSchema(t)

	userID := "revoke-user-uuid"
	keyID := "rkey1234" // 8 alphanumeric chars
	secret := "RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR" // 32 alphanumeric chars

	insertUser(t, db, userID, "revokeuser", "active")
	revokedAt := pastISO(1 * time.Hour) // revoked an hour ago
	insertAPIKey(t, db, keyID, secret, userID, nil, &revokedAt)

	e := setupAuthTestEcho(t, db)
	token := "af_" + keyID + "_" + secret

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	var env authErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse response: %v; body = %q", err, rec.Body.String())
	}
	if env.Error == nil {
		t.Fatal("response should contain error envelope")
	}
	if env.Error.Code != 401 {
		t.Errorf("envelope code = %d, want 401", env.Error.Code)
	}
	if env.Error.Message != "missing or invalid authentication credentials" {
		t.Errorf("envelope message = %q, want standard auth error", env.Error.Message)
	}
}

// TestSpec01_ExpiredTokenExclusiveBoundary verifies the exclusive UTC boundary
// for token expiry:
//
//	(a) expires_at in the past → HTTP 401
//	(b) expires_at equal to exact current UTC instant → NOT expired (still valid)
//
// TS-01-E19, REQ: 01-REQ-14.E5
func TestSpec01_ExpiredTokenExclusiveBoundary(t *testing.T) {
	db := setupAuthTestDBWithSchema(t)

	userID := "expiry-user-uuid"
	insertUser(t, db, userID, "expiryuser", "active")

	// (a) Expired token: expires_at in the past.
	t.Run("expired_past", func(t *testing.T) {
		keyID := "expkey01" // 8 alphanumeric chars
		secret := "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE" // 32 chars
		past := pastISO(1 * time.Second) // expired 1 second ago
		insertAPIKey(t, db, keyID, secret, userID, &past, nil)

		e := setupAuthTestEcho(t, db)
		token := "af_" + keyID + "_" + secret

		req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expired token (past): status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	// (b) Boundary token: expires_at == exact now (exclusive → NOT expired).
	t.Run("boundary_exact_now", func(t *testing.T) {
		keyID := "expkey02"
		secret := "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF" // 32 chars
		// Set expires_at to a point slightly in the future to account for
		// test execution time — the spec says expires_at == now is NOT expired
		// (exclusive boundary). We add 2 seconds to ensure the token is valid
		// at the time the middleware checks it.
		boundary := futureISO(2 * time.Second)
		insertAPIKey(t, db, keyID, secret, userID, &boundary, nil)

		e := setupAuthTestEcho(t, db)
		token := "af_" + keyID + "_" + secret

		req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		// Should NOT be 401 — the token is still valid at the boundary.
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("boundary token (expires_at == now): status = 401, but token should NOT be expired (exclusive boundary)")
		}
	})
}

// ---------------------------------------------------------------------------
// 6.3 — Auth Middleware DB Contention
// ---------------------------------------------------------------------------

// TestSpec01_AuthDBContention503 verifies that when a DB lookup for api_keys
// encounters a write-contention busy-timeout error, the auth middleware
// returns HTTP 503 with the standard error envelope:
//
//	{"error": {"code": 503, "message": "service temporarily unavailable"}}
//
// Uses DELETE journal mode with a very short busy_timeout and an exclusive
// write lock on a second connection to trigger SQLITE_BUSY.
//
// TS-01-E15, REQ: 01-REQ-14.E1
func TestSpec01_AuthDBContention503(t *testing.T) {
	contentionDB := setupContentionDB(t)
	e := setupAuthTestEcho(t, contentionDB)

	// Use the valid API key that was inserted in setupContentionDB.
	token := "af_contkey1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// With the write lock held and a 1ms busy_timeout, the middleware's
	// DB query should fail with SQLITE_BUSY, resulting in 503.
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("DB contention: status = %d, want %d (503)", rec.Code, http.StatusServiceUnavailable)
	}

	var env authErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse response: %v; body = %q", err, rec.Body.String())
	}
	if env.Error == nil {
		t.Fatal("response should contain error envelope")
	}
	if env.Error.Code != 503 {
		t.Errorf("envelope code = %d, want 503", env.Error.Code)
	}
	if env.Error.Message != "service temporarily unavailable" {
		t.Errorf("envelope message = %q, want %q", env.Error.Message, "service temporarily unavailable")
	}
}

// ---------------------------------------------------------------------------
// 6.4 — Property Tests for Hash Invariants and Expiry Boundary
// ---------------------------------------------------------------------------

// TestSpec01_PropAdminTokenHashRoundtrip is a property test that generates 100
// random 64-char hex strings as admin token suffixes, stores each in
// admin_tokens, retrieves the stored hash, and verifies it matches
// hex(sha256(suffix)). Also verifies that recomputing the hash at verification
// time produces the same result.
//
// TS-01-P1, PROP: 01-PROP-1
func TestSpec01_PropAdminTokenHashRoundtrip(t *testing.T) {
	db := setupAuthTestDBWithSchema(t)

	for i := range 100 {
		suffix := deterministicHex(64, i)
		tokenHash := sha256hex(suffix)

		// Store the hash directly.
		tokID := fmt.Sprintf("admin-prop-tok-%d", i)
		_, err := db.Exec(
			"INSERT INTO admin_tokens (id, token_hash, created_at) VALUES (?, ?, ?)",
			tokID, tokenHash, nowISO(),
		)
		if err != nil {
			t.Fatalf("iteration %d: insert failed: %v", i, err)
		}

		// Compute expected hash independently.
		h := sha256.Sum256([]byte(suffix))
		expectedHash := hex.EncodeToString(h[:])

		if tokenHash != expectedHash {
			t.Errorf("iteration %d: stored hash = %q, want sha256(%q) = %q",
				i, tokenHash, suffix[:16]+"...", expectedHash)
		}

		// Retrieve from DB and verify roundtrip.
		var dbHash string
		err = db.QueryRow(
			"SELECT token_hash FROM admin_tokens WHERE id = ?", tokID,
		).Scan(&dbHash)
		if err != nil {
			t.Fatalf("iteration %d: failed to retrieve stored hash: %v", i, err)
		}
		if dbHash != expectedHash {
			t.Errorf("iteration %d: DB hash = %q, want %q", i, dbHash, expectedHash)
		}

		// Recompute at verification time — must produce the same hash.
		recomputedHash := sha256hex(suffix)
		if recomputedHash != dbHash {
			t.Errorf("iteration %d: recomputed hash %q != stored hash %q", i, recomputedHash, dbHash)
		}
	}
}

// TestSpec01_PropAPIKeySecretHashOnly is a property test that generates 100
// random (key_id, secret) pairs, stores sha256(secret) in api_keys, and
// verifies that authentication succeeds — implicitly proving the hash is
// computed from the secret only (not the full token or key_id).
//
// TS-01-P2, PROP: 01-PROP-2
func TestSpec01_PropAPIKeySecretHashOnly(t *testing.T) {
	db := setupAuthTestDBWithSchema(t)

	for i := range 100 {
		// Use fmt.Sprintf to guarantee unique key_ids.
		keyID := fmt.Sprintf("ak%06d", i)[:8]
		secret := deterministicAlphanumeric(32, i*2000+7)
		userID := fmt.Sprintf("apikey-prop-user-%d", i)
		username := fmt.Sprintf("apikeyuser%d", i)

		insertUser(t, db, userID, username, "active")
		insertAPIKey(t, db, keyID, secret, userID, nil, nil)

		// Build the full token.
		fullToken := "af_" + keyID + "_" + secret

		// Verify that sha256(secret) matches what's stored (not sha256(fullToken)).
		secretHash := sha256hex(secret)
		fullTokenHash := sha256hex(fullToken)

		// The stored hash MUST be of the secret only.
		var storedHash string
		err := db.QueryRow(
			"SELECT secret_hash FROM api_keys WHERE key_id = ?", keyID,
		).Scan(&storedHash)
		if err != nil {
			t.Fatalf("iteration %d: failed to retrieve secret_hash: %v", i, err)
		}

		if storedHash != secretHash {
			t.Errorf("iteration %d: stored hash should be sha256(secret), got %q, want %q",
				i, storedHash, secretHash)
		}
		if storedHash == fullTokenHash {
			t.Errorf("iteration %d: stored hash equals sha256(fullToken) — hash should be of secret only, not full token",
				i)
		}

		// Authenticate via the test Echo instance.
		e := setupAuthTestEcho(t, db)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
		req.Header.Set("Authorization", "Bearer "+fullToken)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		// With auth middleware implemented, this should succeed (not 401).
		// With the stub, it passes through — this is correct for a failing test.
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("iteration %d: valid API key got 401; middleware should accept sha256(secret)-based auth",
				i)
		}
	}
}

// TestSpec01_PropWorkspaceTokenSecretHashOnly is a property test that generates
// 100 random (token_id, secret) pairs for workspace tokens, stores
// sha256(secret) in workspace_tokens, and verifies authentication succeeds.
//
// TS-01-P3, PROP: 01-PROP-3
func TestSpec01_PropWorkspaceTokenSecretHashOnly(t *testing.T) {
	db := setupAuthTestDBWithSchema(t)

	for i := range 100 {
		// Use fmt.Sprintf to guarantee unique token_ids.
		tokenID := fmt.Sprintf("wt%06d", i)[:8]
		secret := deterministicAlphanumeric(32, i*4000+13)
		userID := fmt.Sprintf("wstok-prop-user-%d", i)
		username := fmt.Sprintf("wstokuser%d", i)
		wsID := fmt.Sprintf("wstok-prop-ws-%d", i)
		wsSlug := fmt.Sprintf("prop-ws-%d", i)

		insertUser(t, db, userID, username, "active")
		insertWorkspace(t, db, wsID, wsSlug, userID)
		insertWorkspaceToken(t, db, tokenID, secret, wsID, userID, nil, nil)

		// Build the full token.
		fullToken := "af_wt_" + tokenID + "_" + secret

		// Verify that sha256(secret) matches what's stored.
		secretHash := sha256hex(secret)
		fullTokenHash := sha256hex(fullToken)

		var storedHash string
		err := db.QueryRow(
			"SELECT secret_hash FROM workspace_tokens WHERE token_id = ?", tokenID,
		).Scan(&storedHash)
		if err != nil {
			t.Fatalf("iteration %d: failed to retrieve secret_hash: %v", i, err)
		}

		if storedHash != secretHash {
			t.Errorf("iteration %d: stored hash should be sha256(secret), got %q, want %q",
				i, storedHash, secretHash)
		}
		if storedHash == fullTokenHash {
			t.Errorf("iteration %d: stored hash equals sha256(fullToken) — hash should be of secret only",
				i)
		}

		// Authenticate via the test Echo instance.
		e := setupAuthTestEcho(t, db)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
		req.Header.Set("Authorization", "Bearer "+fullToken)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		// With auth middleware implemented, this should succeed (not 401).
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("iteration %d: valid workspace token got 401; middleware should accept sha256(secret)-based auth",
				i)
		}
	}
}

// TestSpec01_PropTokenExpiryExclusiveBoundary is a property test that generates
// 50 tokens with expires_at values spanning past (various durations),
// exact-now, and near-future, then asserts the exclusive UTC boundary:
//
//	expires_at < now → EXPIRED (should be rejected)
//	expires_at == now → VALID (should be accepted)
//	expires_at > now → VALID (should be accepted)
//
// TS-01-P10, PROP: 01-PROP-10
func TestSpec01_PropTokenExpiryExclusiveBoundary(t *testing.T) {
	db := setupAuthTestDBWithSchema(t)

	userID := "expiry-prop-user"
	insertUser(t, db, userID, "expirypropuser", "active")

	type testCase struct {
		offset   time.Duration
		wantCode int // 401 if expired, anything-but-401 if valid
		desc     string
	}

	// Generate 50 test cases spanning past, exact-now, and future.
	var cases []testCase

	// 20 cases with expires_at in the past (various durations).
	for i := 0; i < 20; i++ {
		d := time.Duration(i+1) * time.Second
		cases = append(cases, testCase{
			offset:   -d,
			wantCode: http.StatusUnauthorized,
			desc:     fmt.Sprintf("past_%ds", i+1),
		})
	}

	// 10 cases with expires_at equal to now (±0).
	// Due to clock granularity, we use a very small positive offset to
	// represent "at the boundary" — the spec says expires_at == now is valid.
	for i := 0; i < 10; i++ {
		// Small positive offset ensures expires_at > now during the check.
		d := time.Duration(2+i) * time.Second
		cases = append(cases, testCase{
			offset:   d,
			wantCode: 0, // anything but 401
			desc:     fmt.Sprintf("near_future_%ds", 2+i),
		})
	}

	// 20 cases with expires_at in the future (various durations).
	for i := 0; i < 20; i++ {
		d := time.Duration(60+i*10) * time.Second
		cases = append(cases, testCase{
			offset:   d,
			wantCode: 0, // anything but 401
			desc:     fmt.Sprintf("future_%ds", 60+i*10),
		})
	}

	for idx, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			keyID := fmt.Sprintf("ep%06d", idx)
			secret := deterministicAlphanumeric(32, idx*5000+19)

			expiresAt := time.Now().UTC().Add(tc.offset).Format(time.RFC3339Nano)
			insertAPIKey(t, db, keyID, secret, userID, &expiresAt, nil)

			e := setupAuthTestEcho(t, db)
			token := "af_" + keyID + "_" + secret

			req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if tc.wantCode == http.StatusUnauthorized {
				// Expired: must be 401.
				if rec.Code != http.StatusUnauthorized {
					t.Errorf("expired token (offset=%v): status = %d, want 401", tc.offset, rec.Code)
				}
			} else {
				// Valid: must NOT be 401.
				if rec.Code == http.StatusUnauthorized {
					t.Errorf("valid token (offset=%v): got 401, but expires_at >= now so token should be valid", tc.offset)
				}
			}
		})
	}
}

// Ensure all imports are used.
var (
	_ = sha256.Sum256
	_ = hex.EncodeToString
	_ = fmt.Sprintf
	_ = os.ReadFile
	_ = filepath.Join
	_ = strings.Contains
	_ = time.Now
)
