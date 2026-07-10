package middleware_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	db := setupTestDB(t)
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
