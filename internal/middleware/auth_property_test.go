package middleware_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// Property Test TS-01-P4 — Admin Token Bypasses User Status Check
// ---------------------------------------------------------------------------

// TestSpec01_PropAdminTokenBypassUserStatusCheck is a property test that
// generates 50 random valid admin tokens with varied users table states
// (empty, admin row present+active, admin row blocked, admin row absent)
// and verifies for each that:
//
//   - AuthContext.IsAdmin is always true
//   - AuthContext.UserID is always empty string
//   - AuthContext.WorkspaceID is always empty string
//   - No users table lookup is performed (no HTTP 403 from blocked user)
//   - Response code is never 403
//
// TS-01-P4, PROP: 01-PROP-4
// Validates: 01-REQ-14.8, 01-REQ-14.2
func TestSpec01_PropAdminTokenBypassUserStatusCheck(t *testing.T) {
	db := setupAuthTestDBWithSchema(t)

	type usersState struct {
		name  string
		setup func()
	}

	states := []usersState{
		{
			name: "empty_users",
			setup: func() {
				// Delete all related rows first to avoid FK violations.
				db.Exec("DELETE FROM workspace_tokens")
				db.Exec("DELETE FROM api_keys")
				db.Exec("DELETE FROM team_members")
				db.Exec("DELETE FROM workspaces")
				db.Exec("DELETE FROM users")
			},
		},
		{
			name: "admin_active",
			setup: func() {
				db.Exec("DELETE FROM workspace_tokens")
				db.Exec("DELETE FROM api_keys")
				db.Exec("DELETE FROM team_members")
				db.Exec("DELETE FROM workspaces")
				db.Exec("DELETE FROM users")
				insertUser(t, db, "admin-user-active", "admin_active", "active")
			},
		},
		{
			name: "admin_blocked",
			setup: func() {
				db.Exec("DELETE FROM workspace_tokens")
				db.Exec("DELETE FROM api_keys")
				db.Exec("DELETE FROM team_members")
				db.Exec("DELETE FROM workspaces")
				db.Exec("DELETE FROM users")
				insertUser(t, db, "admin-user-blocked", "admin_blocked", "blocked")
			},
		},
		{
			name: "admin_absent_other_present",
			setup: func() {
				db.Exec("DELETE FROM workspace_tokens")
				db.Exec("DELETE FROM api_keys")
				db.Exec("DELETE FROM team_members")
				db.Exec("DELETE FROM workspaces")
				db.Exec("DELETE FROM users")
				// No admin user — only another unrelated user exists.
				insertUser(t, db, "other-user-id", "otheruser", "active")
			},
		},
	}

	for i := range 50 {
		suffix := deterministicHex(64, i*100+42)
		token := "af_admin_" + suffix

		// Cycle through users table states.
		state := states[i%len(states)]

		t.Run(fmt.Sprintf("iter_%d_%s", i, state.name), func(t *testing.T) {
			// Set up users table state for this iteration.
			state.setup()

			// Clean admin_tokens and insert hash for this iteration's token.
			db.Exec("DELETE FROM admin_tokens")
			insertAdminTokenHash(t, db, suffix)

			e := setupAuthTestEcho(t, db)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			// Must NOT be 403 (admin bypasses blocked-user check).
			if rec.Code == http.StatusForbidden {
				t.Errorf("iteration %d (%s): admin token got 403; admin auth must bypass blocked-user check",
					i, state.name)
			}

			// Must NOT be 401 (token is valid).
			if rec.Code == http.StatusUnauthorized {
				t.Errorf("iteration %d (%s): admin token got 401; valid admin token should be accepted",
					i, state.name)
			}

			// Parse the response body to verify AuthContext fields.
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("iteration %d: failed to parse response body: %v", i, err)
			}

			// Check that AuthContext was set (not nil).
			if body["auth_context"] == nil && body["is_admin"] == nil {
				t.Errorf("iteration %d (%s): AuthContext not set; expected IsAdmin=true but got no auth context",
					i, state.name)
				return
			}

			// AuthContext.IsAdmin must be true.
			if isAdmin, ok := body["is_admin"].(bool); !ok || !isAdmin {
				t.Errorf("iteration %d (%s): IsAdmin = %v, want true",
					i, state.name, body["is_admin"])
			}

			// AuthContext.UserID must be empty string.
			if userID, ok := body["user_id"].(string); !ok || userID != "" {
				t.Errorf("iteration %d (%s): UserID = %v, want empty string",
					i, state.name, body["user_id"])
			}

			// AuthContext.WorkspaceID must be empty string.
			if wsID, ok := body["workspace_id"].(string); !ok || wsID != "" {
				t.Errorf("iteration %d (%s): WorkspaceID = %v, want empty string",
					i, state.name, body["workspace_id"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Property Test TS-01-P5 — Structural Validation Precedes DB Query
// ---------------------------------------------------------------------------

// TestSpec01_PropStructuralValidationBeforeDB is a property test that generates
// 200 random token strings — a mix of valid-prefix-invalid-structure tokens and
// completely invalid tokens — and verifies that no database query is executed
// for any structurally invalid token.
//
// Detection mechanism: uses a broken (closed) DB. If the middleware attempts a
// DB query, it will get an error and return 503 (not 401). So if the response
// is 401, we know no DB query was attempted. If the response is 503, the
// middleware failed to reject the token structurally before hitting the DB.
//
// TS-01-P5, PROP: 01-PROP-5
// Validates: 01-REQ-14.6, 01-REQ-14.E2, 01-REQ-14.E3
func TestSpec01_PropStructuralValidationBeforeDB(t *testing.T) {
	brokenDB := setupBrokenDB(t)

	// Generate 200 structurally invalid tokens.
	tokens := make([]struct {
		token string
		desc  string
	}, 0, 200)

	// Category 1: completely random strings (no recognized prefix).
	for i := range 40 {
		tok := deterministicAlphanumeric(10+i%20, i*31+1)
		tokens = append(tokens, struct {
			token string
			desc  string
		}{tok, fmt.Sprintf("random_%d", i)})
	}

	// Category 2: af_admin_ prefix with wrong suffix length.
	for i := range 30 {
		// Too short (< 64 hex chars).
		suffix := deterministicHex(10+i%50, i*37+2)
		tokens = append(tokens, struct {
			token string
			desc  string
		}{"af_admin_" + suffix, fmt.Sprintf("admin_short_suffix_%d", i)})
	}

	// Category 3: af_admin_ prefix with non-hex characters in suffix.
	for i := range 20 {
		suffix := deterministicAlphanumeric(64, i*41+3) // alphanumeric, not hex-only
		tokens = append(tokens, struct {
			token string
			desc  string
		}{"af_admin_" + suffix, fmt.Sprintf("admin_nonhex_suffix_%d", i)})
	}

	// Category 4: af_ prefix (API key) with wrong part count.
	for i := range 20 {
		// Only 2 parts instead of 3.
		tokens = append(tokens, struct {
			token string
			desc  string
		}{"af_" + deterministicAlphanumeric(8, i*43+4), fmt.Sprintf("apikey_2parts_%d", i)})
	}

	// Category 5: af_ prefix with key_id wrong length.
	for i := range 20 {
		keyID := deterministicAlphanumeric(5, i*47+5) // 5 chars, need 8
		secret := deterministicAlphanumeric(32, i*53+6)
		tokens = append(tokens, struct {
			token string
			desc  string
		}{"af_" + keyID + "_" + secret, fmt.Sprintf("apikey_short_keyid_%d", i)})
	}

	// Category 6: af_ prefix with secret wrong length.
	for i := range 20 {
		keyID := deterministicAlphanumeric(8, i*59+7)
		secret := deterministicAlphanumeric(20, i*61+8) // 20 chars, need 32
		tokens = append(tokens, struct {
			token string
			desc  string
		}{"af_" + keyID + "_" + secret, fmt.Sprintf("apikey_short_secret_%d", i)})
	}

	// Category 7: af_wt_ prefix with wrong part count.
	for i := range 20 {
		// Too many parts.
		tokens = append(tokens, struct {
			token string
			desc  string
		}{"af_wt_abc_def_extra", fmt.Sprintf("wt_extra_parts_%d", i)})
	}

	// Category 8: af_wt_ prefix with token_id wrong length.
	for i := range 15 {
		tokenID := deterministicAlphanumeric(5, i*67+9) // 5 chars, need 8
		secret := deterministicAlphanumeric(32, i*71+10)
		tokens = append(tokens, struct {
			token string
			desc  string
		}{"af_wt_" + tokenID + "_" + secret, fmt.Sprintf("wt_short_tokenid_%d", i)})
	}

	// Category 9: af_wt_ prefix with secret wrong length.
	for i := range 15 {
		tokenID := deterministicAlphanumeric(8, i*73+11)
		secret := deterministicAlphanumeric(20, i*79+12) // 20 chars, need 32
		tokens = append(tokens, struct {
			token string
			desc  string
		}{"af_wt_" + tokenID + "_" + secret, fmt.Sprintf("wt_short_secret_%d", i)})
	}

	if len(tokens) < 200 {
		t.Fatalf("expected at least 200 test tokens, got %d", len(tokens))
	}

	e := setupAuthTestEcho(t, brokenDB)

	for _, tc := range tokens {
		t.Run(tc.desc, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			// Structurally invalid tokens MUST be rejected with 401 before
			// any DB query. If the middleware attempted a DB query with the
			// broken DB, we would see 503 instead.
			if rec.Code == http.StatusServiceUnavailable {
				t.Errorf("token %q: got 503 (DB query attempted); structural validation should have rejected with 401 before any DB I/O",
					tc.token)
			}

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("token %q: status = %d, want 401 for structurally invalid token",
					tc.token, rec.Code)
			}
		})
	}
}
