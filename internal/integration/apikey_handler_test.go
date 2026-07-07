package integration_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox/af-hub/internal/store"
)

// --- Constants for API key handler tests ---

const testAdminTokenKey = "af_admin_apikey_handler_test"

// --- Helpers for API key tests ---

// seedEditorUser creates a user, workspace, membership, and an API key
// for that user in the workspace with role=editor. Returns the user,
// workspace, and the plaintext API key token string.
func seedEditorUser(t *testing.T, s store.Store, suffix string) (
	user *store.User, ws *store.Workspace, token string,
) {
	t.Helper()

	user, err := s.CreateUser(&store.User{
		Username:   "editor_" + suffix,
		Email:      "editor_" + suffix + "@example.com",
		Provider:   "github",
		ProviderID: "gh_editor_" + suffix,
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	ws, err = s.CreateWorkspace(&store.Workspace{
		Name:   "WS " + suffix,
		Slug:   "ws-" + suffix,
		URL:    "https://ws-" + suffix + ".example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	_, err = s.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID:      user.ID,
		WorkspaceID: ws.ID,
		Role:        "editor",
	})
	if err != nil {
		t.Fatalf("failed to create membership: %v", err)
	}

	keyID := "editorkey_" + suffix
	secret := "editorsecret_" + suffix
	_, err = s.CreateAPIKey(&store.APIKey{
		KeyID:       keyID,
		KeyHash:     sha256HexString(secret),
		UserID:      user.ID,
		WorkspaceID: ws.ID,
		Role:        "editor",
		Label:       "editor key " + suffix,
	})
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}

	token = fmt.Sprintf("af_%s_%s", keyID, secret)
	return user, ws, token
}

// seedReaderUser creates a user, membership, and API key with role=reader.
func seedReaderUser(t *testing.T, s store.Store, ws *store.Workspace, suffix string) (
	user *store.User, token string,
) {
	t.Helper()

	user, err := s.CreateUser(&store.User{
		Username:   "reader_" + suffix,
		Email:      "reader_" + suffix + "@example.com",
		Provider:   "github",
		ProviderID: "gh_reader_" + suffix,
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	_, err = s.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID:      user.ID,
		WorkspaceID: ws.ID,
		Role:        "reader",
	})
	if err != nil {
		t.Fatalf("failed to create membership: %v", err)
	}

	keyID := "readerkey_" + suffix
	secret := "readersecret_" + suffix
	_, err = s.CreateAPIKey(&store.APIKey{
		KeyID:       keyID,
		KeyHash:     sha256HexString(secret),
		UserID:      user.ID,
		WorkspaceID: ws.ID,
		Role:        "reader",
		Label:       "reader key " + suffix,
	})
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}

	token = fmt.Sprintf("af_%s_%s", keyID, secret)
	return user, token
}

// apiKeyTokenHeaders builds an Authorization header from a token string.
func apiKeyTokenHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
	}
}

// ============================================================================
// TS-02-28: POST /api/v1/keys creates API key with correct format
// ============================================================================

// TS-02-28: Verify that POST /api/v1/keys creates an API key in
// af_<key_id>_<secret> format, stores key_hash, sets expires_at,
// and returns the plaintext key exactly once.
func TestAPIKeyHandler_CreateKey_ReturnsCreated(t *testing.T) {
	env := setupFullTestEnv(t)
	_, ws, token := seedEditorUser(t, env.Store, "create001")

	body := fmt.Sprintf(`{
		"workspace_id": %q,
		"label": "mykey",
		"expires": 30
	}`, ws.ID)

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/keys", body,
		apiKeyTokenHeaders(token))

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/keys: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp apiKeyCreateResponse
	parseJSON(t, rec, &resp)

	// Key format: af_<key_id>_<secret>
	keyPattern := regexp.MustCompile(`^af_[a-zA-Z0-9_]+_[a-zA-Z0-9_]+$`)
	if !keyPattern.MatchString(resp.Key) {
		t.Errorf("key = %q, want format 'af_<key_id>_<secret>'", resp.Key)
	}

	if resp.KeyID == "" {
		t.Error("key_id should be non-empty")
	}

	if resp.Role != "editor" {
		t.Errorf("role = %q, want %q", resp.Role, "editor")
	}

	// expires_at should be approximately 30 days from now.
	if resp.ExpiresAt == nil {
		t.Error("expires_at should be set for expires=30")
	}
}

// TS-02-28: Verify that the key_hash stored in the database is the SHA-256
// of the secret portion, not the plaintext.
func TestAPIKeyHandler_CreateKey_StoresHash(t *testing.T) {
	env := setupFullTestEnv(t)
	_, ws, token := seedEditorUser(t, env.Store, "create002")

	body := fmt.Sprintf(`{
		"workspace_id": %q,
		"label": "hashtest",
		"expires": 0
	}`, ws.ID)

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/keys", body,
		apiKeyTokenHeaders(token))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp apiKeyCreateResponse
	parseJSON(t, rec, &resp)

	// Verify the database has a hash, not the plaintext secret.
	dbKey, err := env.Store.GetAPIKeyByKeyID(resp.KeyID)
	if err != nil {
		t.Fatalf("GetAPIKeyByKeyID(%q) failed: %v", resp.KeyID, err)
	}

	// Extract secret from the key string: af_<key_id>_<secret>.
	prefix := "af_" + resp.KeyID + "_"
	if !strings.HasPrefix(resp.Key, prefix) {
		t.Fatalf("key %q does not start with expected prefix %q", resp.Key, prefix)
	}
	secret := resp.Key[len(prefix):]

	expectedHash := sha256HexString(secret)
	if dbKey.KeyHash != expectedHash {
		t.Errorf("stored key_hash does not match sha256(secret)\nstored: %q\nexpected: %q",
			dbKey.KeyHash, expectedHash)
	}
}

// TS-02-28: Verify that the plaintext key is NOT retrievable via GET.
func TestAPIKeyHandler_CreateKey_PlaintextNotInList(t *testing.T) {
	env := setupFullTestEnv(t)
	_, ws, token := seedEditorUser(t, env.Store, "create003")

	body := fmt.Sprintf(`{
		"workspace_id": %q,
		"label": "noplaintext",
		"expires": 0
	}`, ws.ID)

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/keys", body,
		apiKeyTokenHeaders(token))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	var createResp apiKeyCreateResponse
	parseJSON(t, rec, &createResp)

	// Now list keys — plaintext should not be in any response.
	listRec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "",
		apiKeyTokenHeaders(token))

	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/keys: status = %d, want %d",
			listRec.Code, http.StatusOK)
	}

	var keys []apiKeyResponse
	parseJSON(t, listRec, &keys)

	for _, k := range keys {
		if k.Key != "" {
			t.Errorf("plaintext key should not be in list response, got: %q", k.Key)
		}
	}
}

// TS-02-28: Verify that expires=0 results in nil expires_at.
func TestAPIKeyHandler_CreateKey_ExpiresZero_NoExpiry(t *testing.T) {
	env := setupFullTestEnv(t)
	_, ws, token := seedEditorUser(t, env.Store, "create004")

	body := fmt.Sprintf(`{
		"workspace_id": %q,
		"label": "noexpiry",
		"expires": 0
	}`, ws.ID)

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/keys", body,
		apiKeyTokenHeaders(token))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	var resp apiKeyCreateResponse
	parseJSON(t, rec, &resp)

	if resp.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want nil for expires=0", *resp.ExpiresAt)
	}
}

// ============================================================================
// TS-02-29: GET /api/v1/keys with admin token returns all keys
// ============================================================================

// TS-02-29: Verify that GET /api/v1/keys with admin token returns all API
// keys across all users including expired ones, with no plaintext secrets.
func TestAPIKeyHandler_ListKeys_Admin_ReturnsAll(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenKey)

	// Create users and keys.
	user1, err := env.Store.CreateUser(&store.User{
		Username:   "listadmin1",
		Email:      "listadmin1@example.com",
		Provider:   "github",
		ProviderID: "gh_listadmin_001",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user1: %v", err)
	}

	user2, err := env.Store.CreateUser(&store.User{
		Username:   "listadmin2",
		Email:      "listadmin2@example.com",
		Provider:   "github",
		ProviderID: "gh_listadmin_002",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user2: %v", err)
	}

	// Create 2 regular keys and 1 expired key.
	_, _ = env.Store.CreateAPIKey(&store.APIKey{
		KeyID: "adminlist_key1", KeyHash: sha256HexString("s1"),
		UserID: user1.ID, WorkspaceID: "ws1", Role: "editor", Label: "key1",
	})
	_, _ = env.Store.CreateAPIKey(&store.APIKey{
		KeyID: "adminlist_key2", KeyHash: sha256HexString("s2"),
		UserID: user2.ID, WorkspaceID: "ws1", Role: "reader", Label: "key2",
	})

	expiredTime := time.Now().Add(-24 * time.Hour)
	_, _ = env.Store.CreateAPIKey(&store.APIKey{
		KeyID: "adminlist_key3", KeyHash: sha256HexString("s3"),
		UserID: user1.ID, WorkspaceID: "ws1", Role: "editor",
		Label: "expired key", ExpiresAt: &expiredTime,
	})

	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "",
		adminHeaders(testAdminTokenKey))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/keys: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var keys []apiKeyResponse
	parseJSON(t, rec, &keys)

	if len(keys) < 3 {
		t.Errorf("expected at least 3 keys (including expired), got %d", len(keys))
	}

	// No plaintext secrets should be in the response.
	for _, k := range keys {
		if k.Key != "" {
			t.Errorf("key %q should not have plaintext secret in list", k.KeyID)
		}
	}
}

// ============================================================================
// TS-02-30: GET /api/v1/keys with API key returns only user's own keys
// ============================================================================

// TS-02-30: Verify that GET /api/v1/keys with an API key token returns
// only the authenticated user's own keys, not other users'.
func TestAPIKeyHandler_ListKeys_APIKey_ReturnsOwnOnly(t *testing.T) {
	env := setupFullTestEnv(t)

	user1, ws, token1 := seedEditorUser(t, env.Store, "listuser001")

	// Create a second user with a different key in the same workspace.
	user2, err := env.Store.CreateUser(&store.User{
		Username: "listother002", Email: "other002@example.com",
		Provider: "github", ProviderID: "gh_other_002", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create user2: %v", err)
	}
	_, _ = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID: user2.ID, WorkspaceID: ws.ID, Role: "editor",
	})
	_, _ = env.Store.CreateAPIKey(&store.APIKey{
		KeyID: "otherkey002", KeyHash: sha256HexString("othersecret002"),
		UserID: user2.ID, WorkspaceID: ws.ID, Role: "editor", Label: "other key",
	})

	// Also create a second key for user1.
	_, _ = env.Store.CreateAPIKey(&store.APIKey{
		KeyID: "secondkey001", KeyHash: sha256HexString("secondsecret001"),
		UserID: user1.ID, WorkspaceID: ws.ID, Role: "editor", Label: "second key",
	})

	// List keys using user1's token — should see user1's keys only.
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "",
		apiKeyTokenHeaders(token1))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var keys []apiKeyResponse
	parseJSON(t, rec, &keys)

	// user1 should have 2 keys: the seed key + secondkey001.
	if len(keys) != 2 {
		t.Errorf("len(keys) = %d, want 2 (user's own keys only)", len(keys))
	}

	for _, k := range keys {
		if k.UserID != user1.ID {
			t.Errorf("key %q belongs to user %q, want %q",
				k.KeyID, k.UserID, user1.ID)
		}
	}
}

// ============================================================================
// TS-02-31: POST /api/v1/keys/:key_id/refresh generates new secret
// ============================================================================

// TS-02-31: Verify that POST /api/v1/keys/:key_id/refresh generates a new
// secret, updates key_hash, and returns the new plaintext key once.
func TestAPIKeyHandler_RefreshKey_ReturnsNewSecret(t *testing.T) {
	env := setupFullTestEnv(t)
	user, ws, token := seedEditorUser(t, env.Store, "refresh001")

	// Create an additional key to refresh.
	origSecret := "orig_refresh_secret"
	_, err := env.Store.CreateAPIKey(&store.APIKey{
		KeyID: "refresh_key_001", KeyHash: sha256HexString(origSecret),
		UserID: user.ID, WorkspaceID: ws.ID, Role: "editor", Label: "refresh me",
	})
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodPost,
		"/api/v1/keys/refresh_key_001/refresh", "",
		apiKeyTokenHeaders(token))

	if rec.Code != http.StatusOK {
		t.Fatalf("POST refresh: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp apiKeyCreateResponse
	parseJSON(t, rec, &resp)

	if resp.KeyID != "refresh_key_001" {
		t.Errorf("key_id = %q, want %q", resp.KeyID, "refresh_key_001")
	}

	// The new key should contain the key_id.
	if !strings.Contains(resp.Key, "refresh_key_001") {
		t.Errorf("new key %q should contain the key_id 'refresh_key_001'", resp.Key)
	}

	// The old secret should no longer match the stored hash.
	dbKey, err := env.Store.GetAPIKeyByKeyID("refresh_key_001")
	if err != nil {
		t.Fatalf("GetAPIKeyByKeyID failed: %v", err)
	}

	oldHash := sha256HexString(origSecret)
	if dbKey.KeyHash == oldHash {
		t.Error("key_hash should have changed after refresh")
	}
}

// ============================================================================
// TS-02-32: DELETE /api/v1/keys/:key_id sets revoked_at
// ============================================================================

// TS-02-32: Verify that DELETE /api/v1/keys/:key_id sets revoked_at on the
// key and returns HTTP 200 with {"message": "key revoked"}.
func TestAPIKeyHandler_RevokeKey_Returns200(t *testing.T) {
	env := setupFullTestEnv(t)
	user, ws, token := seedEditorUser(t, env.Store, "revoke001")

	// Create a key to revoke.
	_, err := env.Store.CreateAPIKey(&store.APIKey{
		KeyID: "revoke_key_001", KeyHash: sha256HexString("revokesecret"),
		UserID: user.ID, WorkspaceID: ws.ID, Role: "editor", Label: "revoke me",
	})
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodDelete,
		"/api/v1/keys/revoke_key_001", "",
		apiKeyTokenHeaders(token))

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE key: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]string
	parseJSON(t, rec, &resp)

	if resp["message"] != "key revoked" {
		t.Errorf("message = %q, want %q", resp["message"], "key revoked")
	}

	// Verify revoked_at is set in the database.
	dbKey, err := env.Store.GetAPIKeyByKeyID("revoke_key_001")
	if err != nil {
		t.Fatalf("GetAPIKeyByKeyID failed: %v", err)
	}
	if dbKey.RevokedAt == nil {
		t.Error("revoked_at should be non-nil after revocation")
	}
}

// ============================================================================
// TS-02-E22: POST /api/v1/keys for non-member workspace returns HTTP 403
// ============================================================================

// TS-02-E22: Verify that POST /api/v1/keys for a workspace where the user
// has no membership returns HTTP 403.
func TestAPIKeyEdge_CreateKey_NotMember_Returns403(t *testing.T) {
	env := setupFullTestEnv(t)
	_, _, token := seedEditorUser(t, env.Store, "notmember001")

	// Create a different workspace the user is NOT a member of.
	otherWS, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "Other WS", Slug: "other-ws",
		URL: "https://other-ws.example.com", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create other workspace: %v", err)
	}

	body := fmt.Sprintf(`{
		"workspace_id": %q,
		"label": "mykey",
		"expires": 30
	}`, otherWS.ID)

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/keys", body,
		apiKeyTokenHeaders(token))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("not member: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "403" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "403")
	}
	if !strings.Contains(errResp.Error.Message, "not a member") {
		t.Errorf("error message = %q, want it to contain 'not a member'",
			errResp.Error.Message)
	}
}

// ============================================================================
// TS-02-E23: POST /api/v1/keys with invalid expires returns HTTP 400
// ============================================================================

// TS-02-E23: Verify that POST /api/v1/keys with an invalid expires value
// (not 0, 30, 60, or 90) returns HTTP 400.
func TestAPIKeyEdge_CreateKey_InvalidExpires_Returns400(t *testing.T) {
	env := setupFullTestEnv(t)
	_, ws, token := seedEditorUser(t, env.Store, "invalidexp001")

	invalidExpires := []int{15, -1, 365}

	for _, exp := range invalidExpires {
		t.Run(fmt.Sprintf("expires=%d", exp), func(t *testing.T) {
			body := fmt.Sprintf(`{
				"workspace_id": %q,
				"label": "k",
				"expires": %d
			}`, ws.ID, exp)

			rec := doRequest(env.Echo, http.MethodPost, "/api/v1/keys", body,
				apiKeyTokenHeaders(token))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expires=%d: status = %d, want %d\nBody: %s",
					exp, rec.Code, http.StatusBadRequest, rec.Body.String())
			}

			var errResp errorResponse
			parseJSON(t, rec, &errResp)

			if errResp.Error.Code != "400" {
				t.Errorf("error code = %q, want %q", errResp.Error.Code, "400")
			}
			if !strings.Contains(errResp.Error.Message, "0, 30, 60, or 90") {
				t.Errorf("error message = %q, want it to contain '0, 30, 60, or 90'",
					errResp.Error.Message)
			}
		})
	}
}

// ============================================================================
// TS-02-E24: POST /api/v1/keys referencing non-existent workspace_id → 404
// ============================================================================

// TS-02-E24: Verify that POST /api/v1/keys referencing a non-existent
// workspace_id returns HTTP 404.
func TestAPIKeyEdge_CreateKey_NonExistentWorkspace_Returns404(t *testing.T) {
	env := setupFullTestEnv(t)
	_, _, token := seedEditorUser(t, env.Store, "noworkspace001")

	body := `{
		"workspace_id": "ws_does_not_exist",
		"label": "mykey",
		"expires": 30
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/keys", body,
		apiKeyTokenHeaders(token))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-existent workspace: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "404" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "404")
	}
	if errResp.Error.Message != "workspace not found" {
		t.Errorf("error message = %q, want %q",
			errResp.Error.Message, "workspace not found")
	}
}

// ============================================================================
// TS-02-E25: Refresh or revoke on non-existent/other-user key → 404
// ============================================================================

// TS-02-E25: Verify that POST /api/v1/keys/:key_id/refresh on a
// non-existent key returns HTTP 404.
func TestAPIKeyEdge_RefreshKey_NonExistent_Returns404(t *testing.T) {
	env := setupFullTestEnv(t)
	_, _, token := seedEditorUser(t, env.Store, "nokey_refresh001")

	rec := doRequest(env.Echo, http.MethodPost,
		"/api/v1/keys/key_nonexistent/refresh", "",
		apiKeyTokenHeaders(token))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("refresh non-existent: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "404" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "404")
	}
	if !strings.Contains(errResp.Error.Message, "not found") {
		t.Errorf("error message = %q, want it to contain 'not found'",
			errResp.Error.Message)
	}
}

// TS-02-E25: Verify that DELETE /api/v1/keys/:key_id on a key belonging
// to another user returns HTTP 404.
func TestAPIKeyEdge_RevokeKey_OtherUser_Returns404(t *testing.T) {
	env := setupFullTestEnv(t)
	_, ws, token1 := seedEditorUser(t, env.Store, "otheruser_revoke001")

	// Create a second user with their own key.
	user2, err := env.Store.CreateUser(&store.User{
		Username: "otherowner", Email: "otherowner@example.com",
		Provider: "github", ProviderID: "gh_otherowner", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create user2: %v", err)
	}
	_, _ = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID: user2.ID, WorkspaceID: ws.ID, Role: "editor",
	})
	_, _ = env.Store.CreateAPIKey(&store.APIKey{
		KeyID: "key_owned_by_user2", KeyHash: sha256HexString("u2secret"),
		UserID: user2.ID, WorkspaceID: ws.ID, Role: "editor", Label: "u2 key",
	})

	// User1 tries to revoke user2's key — should get 404.
	rec := doRequest(env.Echo, http.MethodDelete,
		"/api/v1/keys/key_owned_by_user2", "",
		apiKeyTokenHeaders(token1))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoke other user's key: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "404" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "404")
	}
}

// TS-02-E25: Verify that POST /api/v1/keys/:key_id/refresh on a key
// belonging to another user returns HTTP 404.
func TestAPIKeyEdge_RefreshKey_OtherUser_Returns404(t *testing.T) {
	env := setupFullTestEnv(t)
	_, ws, token1 := seedEditorUser(t, env.Store, "otheruser_refresh001")

	user2, err := env.Store.CreateUser(&store.User{
		Username: "refreshother", Email: "refreshother@example.com",
		Provider: "github", ProviderID: "gh_refreshother", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create user2: %v", err)
	}
	_, _ = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID: user2.ID, WorkspaceID: ws.ID, Role: "editor",
	})
	_, _ = env.Store.CreateAPIKey(&store.APIKey{
		KeyID: "key_refresh_other", KeyHash: sha256HexString("refreshothersecret"),
		UserID: user2.ID, WorkspaceID: ws.ID, Role: "editor", Label: "other refresh",
	})

	rec := doRequest(env.Echo, http.MethodPost,
		"/api/v1/keys/key_refresh_other/refresh", "",
		apiKeyTokenHeaders(token1))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("refresh other user's key: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// ============================================================================
// TS-02-E26: Reader-role user attempting create or revoke → 403
// ============================================================================

// TS-02-E26: Verify that a reader-role user attempting to create an API key
// via POST /api/v1/keys receives HTTP 403.
func TestAPIKeyEdge_CreateKey_ReaderRole_Returns403(t *testing.T) {
	env := setupFullTestEnv(t)
	_, ws, _ := seedEditorUser(t, env.Store, "readerrole001")

	_, readerToken := seedReaderUser(t, env.Store, ws, "readerrole001")

	body := fmt.Sprintf(`{
		"workspace_id": %q,
		"label": "newkey",
		"expires": 30
	}`, ws.ID)

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/keys", body,
		apiKeyTokenHeaders(readerToken))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("reader create: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "403" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "403")
	}
}

// TS-02-E26: Verify that a reader-role user attempting to revoke an API key
// via DELETE /api/v1/keys/:key_id receives HTTP 403.
func TestAPIKeyEdge_RevokeKey_ReaderRole_Returns403(t *testing.T) {
	env := setupFullTestEnv(t)
	_, ws, _ := seedEditorUser(t, env.Store, "readerrevoke001")

	readerUser, readerToken := seedReaderUser(t, env.Store, ws, "readerrevoke001")

	// Create a key owned by the reader (for attempted revocation).
	_, _ = env.Store.CreateAPIKey(&store.APIKey{
		KeyID: "key_reader_own", KeyHash: sha256HexString("readersecown"),
		UserID: readerUser.ID, WorkspaceID: ws.ID, Role: "reader",
		Label: "reader own key",
	})

	rec := doRequest(env.Echo, http.MethodDelete,
		"/api/v1/keys/key_reader_own", "",
		apiKeyTokenHeaders(readerToken))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("reader revoke: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "403" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "403")
	}
}
