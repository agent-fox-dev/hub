package integration

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ==========================================================================
// Task group 2.4: Smoke tests
// TS-04-SMOKE-1 through TS-04-SMOKE-5
// ==========================================================================

// TestSpec04_SMOKE1_CreateWorkspaceEndToEnd smoke-tests the end-to-end workspace
// creation flow using a user API key, validating the full path from auth
// middleware through DB insert to HTTP 201 response.
// Execution Path: 04-PATH-1
func TestSpec04_SMOKE1_CreateWorkspaceEndToEnd(t *testing.T) {
	env := setupStandardEnv(t)

	body := map[string]interface{}{
		"slug":    "smoke-workspace",
		"git_url": "https://github.com/org/smoke-repo.git",
		"branch":  "main",
	}
	rec := doRequest(env.E, http.MethodPost, "/api/v1/workspaces", body,
		bearer(env.OwnerAPIKey))

	assertStatus(t, rec, http.StatusCreated)

	respBody := parseJSONMap(t, rec)
	assertWorkspaceSchema(t, respBody)

	// Verify specific field values.
	if slug, _ := respBody["slug"].(string); slug != "smoke-workspace" {
		t.Errorf("slug = %q, want %q", slug, "smoke-workspace")
	}
	if ownerID, _ := respBody["owner_user_id"].(string); ownerID != env.OwnerUserID {
		t.Errorf("owner_user_id = %q, want %q", ownerID, env.OwnerUserID)
	}
	createdAt, _ := respBody["created_at"].(string)
	updatedAt, _ := respBody["updated_at"].(string)
	if createdAt != updatedAt {
		t.Errorf("created_at (%q) != updated_at (%q)", createdAt, updatedAt)
	}

	// Verify DB record exists.
	wsID, _ := respBody["id"].(string)
	var dbSlug string
	err := env.DB.QueryRow(`SELECT slug FROM workspaces WHERE id = ?`, wsID).Scan(&dbSlug)
	if err != nil {
		t.Fatalf("workspace not found in DB: %v", err)
	}
	if dbSlug != "smoke-workspace" {
		t.Errorf("DB slug = %q, want %q", dbSlug, "smoke-workspace")
	}
}

// TestSpec04_SMOKE2_CreateTokenEndToEnd smoke-tests the workspace token creation
// flow, verifying crypto/rand generation, SHA-256 hashing, DB insertion, and
// plaintext token returned exactly once.
// Execution Path: 04-PATH-2
func TestSpec04_SMOKE2_CreateTokenEndToEnd(t *testing.T) {
	env := setupStandardEnv(t)

	createTestWorkspace(t, env.DB, "ws-smoke2", "smoke-token-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	tokenRegex := regexp.MustCompile(`^af_wt_[0-9A-Za-z]{8}_[0-9A-Za-z]{32}$`)

	t.Run("default expires (30 days)", func(t *testing.T) {
		body := map[string]interface{}{
			"label": "smoke-label",
		}
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/smoke-token-ws/tokens", body,
			bearer(env.OwnerAPIKey))

		assertStatus(t, rec, http.StatusCreated)

		respBody := parseJSONMap(t, rec)

		// Verify token format.
		tokenStr, ok := respBody["token"].(string)
		if !ok {
			t.Fatal("response missing 'token' field")
		}
		if !tokenRegex.MatchString(tokenStr) {
			t.Errorf("token %q does not match expected format", tokenStr)
		}

		// Verify token_id is 8 base62 chars.
		tokenID, ok := respBody["token_id"].(string)
		if !ok {
			t.Fatal("response missing 'token_id' field")
		}
		if len(tokenID) != 8 || !isBase62(tokenID) {
			t.Errorf("token_id = %q, want 8 base62 chars", tokenID)
		}

		// Verify label.
		label, _ := respBody["label"].(string)
		if label != "smoke-label" {
			t.Errorf("label = %q, want %q", label, "smoke-label")
		}

		// Verify expires_at is approximately created_at + 30 days.
		expiresAt, ok := respBody["expires_at"].(string)
		if !ok {
			t.Fatal("expires_at missing")
		}
		if !isISO8601(expiresAt) {
			t.Errorf("expires_at = %q is not ISO 8601", expiresAt)
		}

		// Verify created_at.
		if !isISO8601(respBody["created_at"].(string)) {
			t.Error("created_at is not ISO 8601")
		}

		// Verify DB: secret_hash matches SHA-256 of plaintext secret.
		parts := strings.SplitN(tokenStr, "_", 4)
		secret := parts[3]
		var dbSecretHash string
		err := env.DB.QueryRow(`SELECT secret_hash FROM workspace_tokens WHERE token_id = ?`,
			tokenID).Scan(&dbSecretHash)
		if err != nil {
			t.Fatalf("failed to query token: %v", err)
		}
		expectedHash := sha256Hex(secret)
		if dbSecretHash != expectedHash {
			t.Errorf("DB secret_hash = %q, want %q", dbSecretHash, expectedHash)
		}
		if dbSecretHash == secret {
			t.Error("plaintext secret stored in DB!")
		}
	})

	t.Run("expires=0 produces null expires_at", func(t *testing.T) {
		body := map[string]interface{}{
			"expires": 0,
		}
		rec := doRequest(env.E, http.MethodPost,
			"/api/v1/workspaces/smoke-token-ws/tokens", body,
			bearer(env.OwnerAPIKey))

		assertStatus(t, rec, http.StatusCreated)

		respBody := parseJSONMap(t, rec)
		if respBody["expires_at"] != nil {
			t.Errorf("expires_at should be null when expires=0, got %v", respBody["expires_at"])
		}
	})
}

// TestSpec04_SMOKE3_DelegatedWorkspaceAccess smoke-tests delegated workspace
// access using a workspace token, verifying middleware validates token, attaches
// workspace_id to context, handler checks scope, and returns full workspace object.
// Execution Path: 04-PATH-3
func TestSpec04_SMOKE3_DelegatedWorkspaceAccess(t *testing.T) {
	env := setupStandardEnv(t)

	wsID := createTestWorkspace(t, env.DB, "ws-smoke3", "smoke-delegated-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)
	createTestWorkspace(t, env.DB, "ws-smoke3-other", "smoke-other-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)

	// Valid, non-expired, non-revoked token scoped to smoke-delegated-ws.
	validToken := createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-smoke3", TokenID: "smk3tok1",
		Secret:      "abcdefghABCDEFGH0123456789abcdef",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: strPtr("smoke-token"), ExpiresAt: strPtr(futureISO(30 * 24 * time.Hour)),
		CreatedAt: nowISO(), RevokedAt: nil,
	})

	t.Run("valid token returns full workspace object", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet,
			"/api/v1/workspaces/smoke-delegated-ws", nil,
			bearer(validToken))
		assertStatus(t, rec, http.StatusOK)

		respBody := parseJSONMap(t, rec)
		assertWorkspaceSchema(t, respBody)

		if slug, _ := respBody["slug"].(string); slug != "smoke-delegated-ws" {
			t.Errorf("slug = %q, want %q", slug, "smoke-delegated-ws")
		}
	})

	t.Run("token for different workspace returns 403", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet,
			"/api/v1/workspaces/smoke-other-ws", nil,
			bearer(validToken))
		assertStatus(t, rec, http.StatusForbidden)
	})

	t.Run("expired token returns 401", func(t *testing.T) {
		expiredToken := createTestWorkspaceToken(t, env.DB, testTokenRecord{
			ID: "tok-smoke3-exp", TokenID: "smk3exp1",
			Secret:      "ZYXWVUTSzyxwvuts9876543210zyxwvu",
			WorkspaceID: wsID, UserID: env.OwnerUserID,
			Label: nil, ExpiresAt: strPtr(pastISO(24 * time.Hour)),
			CreatedAt: pastISO(72 * time.Hour), RevokedAt: nil,
		})
		rec := doRequest(env.E, http.MethodGet,
			"/api/v1/workspaces/smoke-delegated-ws", nil,
			bearer(expiredToken))
		assertStatus(t, rec, http.StatusUnauthorized)
	})

	t.Run("revoked token returns 401", func(t *testing.T) {
		revokedAt := pastISO(6 * time.Hour)
		revokedToken := createTestWorkspaceToken(t, env.DB, testTokenRecord{
			ID: "tok-smoke3-rev", TokenID: "smk3rev1",
			Secret:      "12345678901234567890123456789012",
			WorkspaceID: wsID, UserID: env.OwnerUserID,
			Label: nil, ExpiresAt: nil,
			CreatedAt: pastISO(48 * time.Hour), RevokedAt: &revokedAt,
		})
		rec := doRequest(env.E, http.MethodGet,
			"/api/v1/workspaces/smoke-delegated-ws", nil,
			bearer(revokedToken))
		assertStatus(t, rec, http.StatusUnauthorized)
	})
}

// TestSpec04_SMOKE4_TokenRevocationFlow smoke-tests the workspace token
// revocation flow, verifying auth, authorization, DB update, idempotency,
// and information hiding for cross-workspace token IDs.
// Execution Path: 04-PATH-4
func TestSpec04_SMOKE4_TokenRevocationFlow(t *testing.T) {
	env := setupStandardEnv(t)

	wsID := createTestWorkspace(t, env.DB, "ws-smoke4", "smoke-revoke-ws",
		"https://github.com/org/repo.git", env.OwnerUserID)
	otherWSID := createTestWorkspace(t, env.DB, "ws-smoke4-other", "smoke-revoke-other",
		"https://github.com/org/repo.git", env.OwnerUserID)

	createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-smoke4", TokenID: "smk4tok1",
		Secret:      "abcdefghABCDEFGH0123456789abcdef",
		WorkspaceID: wsID, UserID: env.OwnerUserID,
		Label: strPtr("to-revoke"), ExpiresAt: nil,
		CreatedAt: nowISO(), RevokedAt: nil,
	})

	// Token belonging to the other workspace (for info hiding test).
	createTestWorkspaceToken(t, env.DB, testTokenRecord{
		ID: "tok-smoke4-other", TokenID: "smk4oth1",
		Secret:      "ZYXWVUTSzyxwvuts9876543210zyxwvu",
		WorkspaceID: otherWSID, UserID: env.OwnerUserID,
		Label: nil, ExpiresAt: nil,
		CreatedAt: nowISO(), RevokedAt: nil,
	})

	t.Run("first revocation returns 204", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodDelete,
			"/api/v1/workspaces/smoke-revoke-ws/tokens/smk4tok1", nil,
			bearer(env.OwnerAPIKey))
		assertStatus(t, rec, http.StatusNoContent)
		assertEmptyBody(t, rec)
	})

	t.Run("DB has revoked_at set", func(t *testing.T) {
		var revokedAt *string
		err := env.DB.QueryRow(`SELECT revoked_at FROM workspace_tokens WHERE token_id = ?`,
			"smk4tok1").Scan(&revokedAt)
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if revokedAt == nil {
			t.Error("revoked_at should be set after revocation")
		}
	})

	t.Run("second revocation is idempotent", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodDelete,
			"/api/v1/workspaces/smoke-revoke-ws/tokens/smk4tok1", nil,
			bearer(env.OwnerAPIKey))
		assertStatus(t, rec, http.StatusNoContent)
		assertEmptyBody(t, rec)
	})

	t.Run("cross-workspace token_id returns 404", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodDelete,
			"/api/v1/workspaces/smoke-revoke-ws/tokens/smk4oth1", nil,
			bearer(env.OwnerAPIKey))
		assertStatus(t, rec, http.StatusNotFound)
	})

	t.Run("non-existent slug returns 404", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodDelete,
			"/api/v1/workspaces/nonexistent-ws/tokens/smk4tok1", nil,
			bearer(env.AdminToken))
		assertStatus(t, rec, http.StatusNotFound)
	})

	t.Run("non-owner returns 403", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodDelete,
			"/api/v1/workspaces/smoke-revoke-ws/tokens/smk4tok1", nil,
			bearer(env.NonOwnerAPIKey))
		assertStatus(t, rec, http.StatusForbidden)
	})
}

// TestSpec04_SMOKE5_AdminListsAllWorkspaces smoke-tests admin listing all
// workspaces, verifying the admin credential is accepted and all workspace
// records are returned ordered correctly.
// Execution Path: 04-PATH-5
func TestSpec04_SMOKE5_AdminListsAllWorkspaces(t *testing.T) {
	env := setupStandardEnv(t)

	// Create workspaces owned by different users.
	createTestWorkspace(t, env.DB, "ws-smoke5-a", "smoke5-ws-a",
		"https://github.com/org/repo.git", env.OwnerUserID)
	createTestWorkspace(t, env.DB, "ws-smoke5-b", "smoke5-ws-b",
		"https://github.com/org/repo.git", env.NonOwnerUserID)
	createTestWorkspace(t, env.DB, "ws-smoke5-c", "smoke5-ws-c",
		"https://github.com/org/repo.git", env.OwnerUserID)

	t.Run("admin sees all workspaces ordered correctly", func(t *testing.T) {
		rec := doRequest(env.E, http.MethodGet, "/api/v1/workspaces", nil,
			bearer(env.AdminToken))
		assertStatus(t, rec, http.StatusOK)

		workspaces := parseJSONArray(t, rec)
		if len(workspaces) < 3 {
			t.Fatalf("expected at least 3 workspaces, got %d", len(workspaces))
		}

		// Verify all have complete schema.
		for _, ws := range workspaces {
			assertWorkspaceSchema(t, ws)
		}

		// Verify ordering: created_at ASC, id ASC.
		for i := 0; i < len(workspaces)-1; i++ {
			ca1, _ := workspaces[i]["created_at"].(string)
			ca2, _ := workspaces[i+1]["created_at"].(string)
			if ca1 > ca2 {
				t.Errorf("workspace[%d].created_at (%s) > workspace[%d].created_at (%s)",
					i, ca1, i+1, ca2)
			}
		}
	})

	t.Run("workspace token cannot list workspaces", func(t *testing.T) {
		wsID := "ws-smoke5-a"
		wtToken := createTestWorkspaceToken(t, env.DB, testTokenRecord{
			ID: "tok-smoke5", TokenID: "smk5tok1",
			Secret:      "abcdefghABCDEFGH0123456789abcdef",
			WorkspaceID: wsID, UserID: env.OwnerUserID,
			Label: nil, ExpiresAt: strPtr(futureISO(30 * 24 * time.Hour)),
			CreatedAt: nowISO(), RevokedAt: nil,
		})
		rec := doRequest(env.E, http.MethodGet, "/api/v1/workspaces", nil,
			bearer(wtToken))
		assertStatus(t, rec, http.StatusForbidden)
	})

	t.Run("empty DB returns empty array", func(t *testing.T) {
		// Create a fresh environment with no workspaces.
		freshDB := openTestDB(t)
		initTestSchema(t, freshDB)
		createTestUser(t, freshDB, "fresh-user", "freshuser")
		freshAdmin := createTestAdminToken(t, freshDB)
		freshE := setupTestServer(t, freshDB)

		rec := doRequest(freshE, http.MethodGet, "/api/v1/workspaces", nil,
			bearer(freshAdmin))
		assertStatus(t, rec, http.StatusOK)

		workspaces := parseJSONArray(t, rec)
		if len(workspaces) != 0 {
			t.Errorf("expected empty array, got %d workspaces", len(workspaces))
		}
	})
}
