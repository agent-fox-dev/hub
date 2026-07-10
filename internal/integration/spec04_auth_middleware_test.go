package integration

import (
	"net/http"
	"testing"
	"time"
)

// ==========================================================================
// Task group 2.2: Auth middleware workspace token validation tests
// TS-04-51, TS-04-52, TS-04-53, TS-04-54
// ==========================================================================

// TestSpec04_TS51_ExpiredAndRevokedTokensRejectedWith401 verifies that expired
// or revoked workspace tokens are rejected with HTTP 401 by the auth middleware
// before the handler runs.
// Requirement: 04-REQ-12.3
func TestSpec04_TS51_ExpiredAndRevokedTokensRejectedWith401(t *testing.T) {
	env := setupStandardEnv(t)

	wsID := createTestWorkspace(t, env.DB, "ws-auth-chk", "auth-check-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	// Expired token (expires_at in the past).
	expiredToken := createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-expired", TokenID: "expird02",
		Secret: "abcdefghABCDEFGH0123456789abcdef",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: strPtr(pastISO(24 * time.Hour)),
		CreatedAt: pastISO(72 * time.Hour), RevokedAt: nil,
	})

	// Revoked token (revoked_at is set).
	revokedAt := pastISO(12 * time.Hour)
	revokedToken := createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-revoked", TokenID: "revokd02",
		Secret: "ZYXWVUTSzyxwvuts9876543210zyxwvu",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: nil,
		CreatedAt: pastISO(48 * time.Hour), RevokedAt: &revokedAt,
	})

	t.Run("expired token", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet,
			"/api/v1/workspaces/auth-check-ws", nil,
			bearer(expiredToken))
		assertStatus(t, rec, http.StatusUnauthorized)
	})

	t.Run("revoked token", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet,
			"/api/v1/workspaces/auth-check-ws", nil,
			bearer(revokedToken))
		assertStatus(t, rec, http.StatusUnauthorized)
	})
}

// TestSpec04_TS52_BlockedUserTokenRejected verifies that a workspace token
// belonging to a blocked user is rejected by the auth middleware.
// Requirement: 04-REQ-12.4
//
// CRITICAL REVIEWER FINDING: Spec 04 says 401, but spec 01 owns the auth
// middleware and rejects blocked users with 403. We assert 403 per the actual
// middleware behavior. See docs/errata/04_blocked_user_status_code.md.
func TestSpec04_TS52_BlockedUserTokenRejected(t *testing.T) {
	env := setupStandardEnv(t)

	// Create a blocked user and a workspace owned by another user.
	createTestBlockedUser(t, env.DB, "blocked-user", "blockeduser")

	wsID := createTestWorkspace(t, env.DB, "ws-blocked", "blocked-user-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	// Create a valid, non-expired, non-revoked workspace token for the blocked user.
	blockedUserToken := createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-blocked", TokenID: "blkdusr1",
		Secret: "abcdefghABCDEFGH0123456789abcdef",
		WorkspaceID: wsID, UserID: "blocked-user",
		Label: nil, ExpiresAt: strPtr(futureISO(30 * 24 * time.Hour)),
		CreatedAt: nowISO(), RevokedAt: nil,
	})

	rec := doRequest(env.E, http.MethodGet,
		"/api/v1/workspaces/blocked-user-ws", nil,
		bearer(blockedUserToken))

	// Spec 01 auth middleware rejects blocked users with 403, not 401.
	assertStatus(t, rec, http.StatusForbidden)
}

// TestSpec04_TS53_AccessControlMatrix verifies the complete access control
// matrix is enforced across all endpoint and caller combinations.
// Requirement: 04-REQ-13.1
func TestSpec04_TS53_AccessControlMatrix(t *testing.T) {
	env := setupStandardEnv(t)

	wsID := createTestWorkspace(t, env.DB, "ws-acl", "acl-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	wtToken := createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-acl", TokenID: "acltoken",
		Secret: "abcdefghABCDEFGH0123456789abcdef",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: strPtr(futureISO(30 * 24 * time.Hour)),
		CreatedAt: nowISO(), RevokedAt: nil,
	})

	// --- POST /api/v1/workspaces ---
	t.Run("admin cannot create workspace", func(t *testing.T) {
		body := map[string]interface{}{
			"slug":    "admin-ws-attempt",
			"git_url": "https://github.com/org/repo.git",
		}
		rec := doRequest(env.E, http.MethodPost, "/api/v1/workspaces", body,
			bearer(env.AdminToken))
		assertStatus(t, rec, http.StatusForbidden)
	})

	// --- GET /api/v1/workspaces ---
	t.Run("admin can list all workspaces", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet, "/api/v1/workspaces", nil,
			bearer(env.AdminToken))
		assertStatus(t, rec, http.StatusOK)
	})

	t.Run("workspace token cannot list workspaces", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet, "/api/v1/workspaces", nil,
			bearer(wtToken))
		assertStatus(t, rec, http.StatusForbidden)
	})

	// --- GET /api/v1/workspaces/:slug ---
	t.Run("workspace token can get own workspace", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet, "/api/v1/workspaces/acl-ws", nil,
			bearer(wtToken))
		assertStatus(t, rec, http.StatusOK)
	})

	// --- POST /api/v1/workspaces/:slug/tokens ---
	t.Run("workspace token cannot create tokens", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/acl-ws/tokens", map[string]interface{}{},
			bearer(wtToken))
		assertStatus(t, rec, http.StatusForbidden)
	})

	// --- GET /api/v1/workspaces/:slug/tokens ---
	t.Run("workspace token cannot list tokens", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet,
			"/api/v1/workspaces/acl-ws/tokens", nil,
			bearer(wtToken))
		assertStatus(t, rec, http.StatusForbidden)
	})

	// --- DELETE /api/v1/workspaces/:slug/tokens/:token_id ---
	t.Run("workspace token cannot revoke tokens", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodDelete,
			"/api/v1/workspaces/acl-ws/tokens/acltoken", nil,
			bearer(wtToken))
		assertStatus(t, rec, http.StatusForbidden)
	})

	// --- Non-owner user checks ---
	t.Run("non-owner cannot get others workspace", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet,
			"/api/v1/workspaces/acl-ws", nil,
			bearer(env.NonOwnerAPIKey))
		assertStatus(t, rec, http.StatusForbidden)
	})

	t.Run("non-owner cannot create tokens on others workspace", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/acl-ws/tokens", map[string]interface{}{},
			bearer(env.NonOwnerAPIKey))
		assertStatus(t, rec, http.StatusForbidden)
	})

	t.Run("non-owner cannot list tokens on others workspace", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet,
			"/api/v1/workspaces/acl-ws/tokens", nil,
			bearer(env.NonOwnerAPIKey))
		assertStatus(t, rec, http.StatusForbidden)
	})
}

// TestSpec04_TS54_WorkspaceIDMismatchReturns403 verifies that the handler
// compares workspace_id from context against the slug-resolved workspace id
// and returns HTTP 403 on mismatch.
// Requirement: 04-REQ-13.2
func TestSpec04_TS54_WorkspaceIDMismatchReturns403(t *testing.T) {
	env := setupStandardEnv(t)

	wsOneID := createTestWorkspace(t, env.DB, "ws-one-id", "ws-one",
		"https://github.com/org/repo.git", env.OwnerUserID)
	createTestWorkspace(t, env.DB, "ws-two-id", "ws-two",
		"https://github.com/org/repo.git", env.OwnerUserID)

	// Token scoped to ws-one.
	wtToken := createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-ws-one", TokenID: "wsone001",
		Secret: "abcdefghABCDEFGH0123456789abcdef",
		WorkspaceID: wsOneID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: strPtr(futureISO(30 * 24 * time.Hour)),
		CreatedAt: nowISO(), RevokedAt: nil,
	})

	// Try to access ws-two using ws-one's token.
	rec := doRequest(env.E, http.MethodGet,
		"/api/v1/workspaces/ws-two", nil,
		bearer(wtToken))

	assertStatus(t, rec, http.StatusForbidden)
	assertErrorEnvelope(t, rec, 403)
}
