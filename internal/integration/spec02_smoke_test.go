package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/config"
	"github.com/agent-fox/af-hub/internal/handler"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// ============================================================================
// TS-02-SMOKE-1: OAuth login for new user — discover providers, exchange code,
// verify DB user record.
// Execution Path: 02-PATH-1
// ============================================================================

func TestSmoke_OAuthLoginNewUser(t *testing.T) {
	// Set up mock GitHub server returning valid token and user info.
	mockGitHub := setupMockGitHubServer(t,
		`{"access_token": "smoke_access_token", "token_type": "bearer"}`,
		`{"id": 90001, "login": "smokeuser1", "email": "smoke1@example.com", "name": "Smoke User 1"}`,
		http.StatusOK,
		http.StatusOK,
	)
	defer mockGitHub.Close()

	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "smoke_client_id",
			ClientSecret: "smoke_client_secret",
			TokenURL:     mockGitHub.URL + "/login/oauth/access_token",
			UserinfoURL:  mockGitHub.URL + "/api/user",
		},
	})

	// Step 1: GET /api/v1/auth/providers — discover available providers.
	provRec := doRequest(env.Echo, http.MethodGet, "/api/v1/auth/providers", "", nil)
	if provRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/auth/providers status = %d, want %d\nBody: %s",
			provRec.Code, http.StatusOK, provRec.Body.String())
	}

	var providers []providerListEntry
	parseJSON(t, provRec, &providers)

	if len(providers) == 0 {
		t.Fatal("providers list is empty")
	}

	foundGitHub := false
	for _, p := range providers {
		if p.Name == "github" {
			foundGitHub = true
			if p.AuthorizeURL == "" {
				t.Error("github provider authorize_url is empty")
			}
		}
		// Verify no secrets are exposed.
		raw, _ := json.Marshal(p)
		if strings.Contains(string(raw), "client_secret") {
			t.Error("provider response contains 'client_secret'")
		}
		if strings.Contains(string(raw), "token_url") {
			t.Error("provider response contains 'token_url'")
		}
	}
	if !foundGitHub {
		t.Error("github provider not found in providers list")
	}

	// Step 2: POST /api/v1/auth/callback — exchange code for user.
	callbackBody := `{"provider": "github", "code": "smoke_code_1", "redirect_uri": "http://localhost:9999/callback"}`
	cbRec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", callbackBody, nil)
	if cbRec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/auth/callback status = %d, want %d\nBody: %s",
			cbRec.Code, http.StatusOK, cbRec.Body.String())
	}

	var user userResponse
	parseJSON(t, cbRec, &user)

	// Verify user fields.
	if user.Username == "" {
		t.Error("user.username is empty")
	}
	if user.Email == "" {
		t.Error("user.email is empty")
	}
	if user.Provider != "github" {
		t.Errorf("user.provider = %q, want %q", user.Provider, "github")
	}
	if user.Status != "active" {
		t.Errorf("user.status = %q, want %q", user.Status, "active")
	}

	// Step 3: Verify DB record exists.
	dbUser, err := env.Store.GetUserByProviderID("github", user.ProviderID)
	if err != nil {
		t.Fatalf("user not found in database after OAuth login: %v", err)
	}
	if dbUser.Status != "active" {
		t.Errorf("db user status = %q, want 'active'", dbUser.Status)
	}

	// No secrets in response.
	cbBody := cbRec.Body.String()
	if strings.Contains(cbBody, "smoke_access_token") {
		t.Error("response leaks the OAuth access token")
	}
}

// ============================================================================
// TS-02-SMOKE-2: Admin creates workspace and assigns member.
// Execution Path: 02-PATH-2
// ============================================================================

func TestSmoke_AdminCreatesWorkspaceAndAssignsMember(t *testing.T) {
	env := setupFullTestEnv(t)

	adminToken := "af_admin_smoke2"
	seedAdminTokenFull(t, env.Store, adminToken)
	headers := adminHeaders(adminToken)

	// Create a user that will become a member.
	user, err := env.Store.CreateUser(&store.User{
		Username:   "smoke_member_user",
		Email:      "smokem@test.com",
		Provider:   "local",
		ProviderID: "smokem_pid",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create member user: %v", err)
	}

	// Step 1: POST /api/v1/workspaces — admin creates workspace.
	wsBody := `{"name": "Smoke Workspace", "slug": "smoke-ws", "url": "https://smoke.example.com"}`
	wsRec := doRequest(env.Echo, http.MethodPost, "/api/v1/workspaces", wsBody, headers)
	if wsRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d, want %d\nBody: %s",
			wsRec.Code, http.StatusCreated, wsRec.Body.String())
	}

	var ws workspaceResponse
	parseJSON(t, wsRec, &ws)

	if ws.ID == "" {
		t.Fatal("workspace.id is empty")
	}
	if ws.Name != "Smoke Workspace" {
		t.Errorf("workspace.name = %q, want %q", ws.Name, "Smoke Workspace")
	}
	if ws.Slug != "smoke-ws" {
		t.Errorf("workspace.slug = %q, want %q", ws.Slug, "smoke-ws")
	}

	// Verify workspace exists in DB.
	dbWs, err := env.Store.GetWorkspaceByID(ws.ID)
	if err != nil {
		t.Fatalf("workspace not in DB: %v", err)
	}
	if dbWs == nil {
		t.Fatal("workspace DB record is nil")
	}

	// Step 2: POST /api/v1/workspaces/:id/members — assign member.
	memberBody := fmt.Sprintf(`{"user_id": %q, "role": "editor"}`, user.ID)
	memberRec := doRequest(env.Echo, http.MethodPost,
		"/api/v1/workspaces/"+ws.ID+"/members", memberBody, headers)
	if memberRec.Code != http.StatusOK {
		t.Fatalf("POST /workspaces/:id/members status = %d, want %d\nBody: %s",
			memberRec.Code, http.StatusOK, memberRec.Body.String())
	}

	var membership membershipResponse
	parseJSON(t, memberRec, &membership)

	if membership.UserID != user.ID {
		t.Errorf("membership.user_id = %q, want %q", membership.UserID, user.ID)
	}
	if membership.Role != "editor" {
		t.Errorf("membership.role = %q, want %q", membership.Role, "editor")
	}
	if membership.WorkspaceID != ws.ID {
		t.Errorf("membership.workspace_id = %q, want %q", membership.WorkspaceID, ws.ID)
	}

	// Verify membership in DB.
	dbMember, err := env.Store.GetWorkspaceMember(user.ID, ws.ID)
	if err != nil {
		t.Fatalf("membership not in DB: %v", err)
	}
	if dbMember.Role != "editor" {
		t.Errorf("db membership.role = %q, want 'editor'", dbMember.Role)
	}
}

// ============================================================================
// TS-02-SMOKE-3: Editor creates API key and uses it for subsequent auth.
// Execution Path: 02-PATH-3
// ============================================================================

func TestSmoke_EditorCreatesAPIKeyAndUsesIt(t *testing.T) {
	env := setupFullTestEnv(t)

	// Create editor user, workspace, membership, and auth key.
	user, err := env.Store.CreateUser(&store.User{
		Username: "smoke_editor", Email: "smoke_editor@test.com",
		Provider: "local", ProviderID: "smoke_editor_pid", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create editor user: %v", err)
	}

	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "EditorSmoke WS", Slug: "editor-smoke-ws",
		URL: "https://editor-smoke.example.com", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	_, err = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID: user.ID, WorkspaceID: ws.ID, Role: "editor",
	})
	if err != nil {
		t.Fatalf("failed to add membership: %v", err)
	}

	// Create auth key for the editor.
	authSecret := "smoke_editor_auth_secret"
	authKeyID := "smoke_editor_authkey"
	_, err = env.Store.CreateAPIKey(&store.APIKey{
		KeyID: authKeyID, KeyHash: sha256HexString(authSecret),
		UserID: user.ID, WorkspaceID: ws.ID, Role: "editor",
		Label: "smoke editor auth",
	})
	if err != nil {
		t.Fatalf("failed to create auth key: %v", err)
	}

	authToken := fmt.Sprintf("af_%s_%s", authKeyID, authSecret)
	editorHeaders := map[string]string{"Authorization": "Bearer " + authToken}

	// Step 1: POST /api/v1/keys — create a new API key.
	createKeyBody := fmt.Sprintf(`{"workspace_id": %q, "label": "smoke-new-key", "expires": 30}`, ws.ID)
	createRec := doRequest(env.Echo, http.MethodPost, "/api/v1/keys", createKeyBody, editorHeaders)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/keys status = %d, want %d\nBody: %s",
			createRec.Code, http.StatusCreated, createRec.Body.String())
	}

	var createResp apiKeyCreateResponse
	parseJSON(t, createRec, &createResp)

	// Verify response format.
	if createResp.Key == "" {
		t.Fatal("create response missing plaintext key")
	}
	if !strings.HasPrefix(createResp.Key, "af_") {
		t.Errorf("key format invalid: %q, want 'af_' prefix", createResp.Key)
	}
	if createResp.KeyID == "" {
		t.Error("create response missing key_id")
	}
	if createResp.Role != "editor" {
		t.Errorf("create response role = %q, want 'editor'", createResp.Role)
	}

	// Verify expires_at is set for 30-day expiry.
	if createResp.ExpiresAt == nil {
		t.Error("expires_at is nil for 30-day key")
	}

	// Extract secret and verify DB hash.
	parts := strings.SplitN(createResp.Key, "_", 3)
	if len(parts) < 3 {
		t.Fatalf("key format invalid: %q", createResp.Key)
	}
	newSecret := parts[2]
	dbKey, err := env.Store.GetAPIKeyByKeyID(createResp.KeyID)
	if err != nil {
		t.Fatalf("new key not found in DB: %v", err)
	}
	expectedHash := sha256HexString(newSecret)
	if dbKey.KeyHash != expectedHash {
		t.Errorf("DB key_hash != sha256(secret)")
	}
	if dbKey.RevokedAt != nil {
		t.Error("new key revoked_at should be nil")
	}

	// Step 2: Use the new key to authenticate a subsequent request.
	newKeyHeaders := map[string]string{"Authorization": "Bearer " + createResp.Key}
	listRec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "", newKeyHeaders)
	if listRec.Code != http.StatusOK {
		t.Fatalf("subsequent auth with new key: status = %d, want %d\nBody: %s",
			listRec.Code, http.StatusOK, listRec.Body.String())
	}
}

// ============================================================================
// TS-02-SMOKE-4: Blocked user with valid key rejected at middleware with 403.
// Execution Path: 02-PATH-4
// ============================================================================

func TestSmoke_BlockedUserRejectedAtMiddleware(t *testing.T) {
	env := setupFullTestEnv(t)

	// Create a blocked user with a valid API key.
	user, err := env.Store.CreateUser(&store.User{
		Username: "smoke_blocked", Email: "smoke_blocked@test.com",
		Provider: "local", ProviderID: "smoke_blocked_pid", Status: "blocked",
	})
	if err != nil {
		t.Fatalf("failed to create blocked user: %v", err)
	}

	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "BlockedSmoke WS", Slug: "blocked-smoke-ws",
		URL: "https://blocked-smoke.example.com", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	_, err = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID: user.ID, WorkspaceID: ws.ID, Role: "editor",
	})
	if err != nil {
		t.Fatalf("failed to add membership: %v", err)
	}

	blockedSecret := "smoke_blocked_secret"
	blockedKeyID := "smoke_blocked_key"
	_, err = env.Store.CreateAPIKey(&store.APIKey{
		KeyID: blockedKeyID, KeyHash: sha256HexString(blockedSecret),
		UserID: user.ID, WorkspaceID: ws.ID, Role: "editor",
		Label: "blocked user key",
	})
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}

	blockedToken := fmt.Sprintf("af_%s_%s", blockedKeyID, blockedSecret)
	blockedHeaders := map[string]string{"Authorization": "Bearer " + blockedToken}

	// Attempt to access protected endpoint.
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "", blockedHeaders)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("blocked user on GET /api/v1/keys: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "403" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "403")
	}
	if errResp.Error.Message != "user is blocked" {
		t.Errorf("error message = %q, want %q", errResp.Error.Message, "user is blocked")
	}
}

// ============================================================================
// TS-02-SMOKE-5: Admin archives then deletes workspace with cascade.
// Execution Path: 02-PATH-5
// ============================================================================

func TestSmoke_AdminArchivesAndDeletesWorkspace(t *testing.T) {
	env := setupFullTestEnv(t)

	adminToken := "af_admin_smoke5"
	seedAdminTokenFull(t, env.Store, adminToken)
	headers := adminHeaders(adminToken)

	// Create workspace with members and API keys.
	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "CascadeSmoke WS", Slug: "cascade-smoke-ws",
		URL: "https://cascade-smoke.example.com", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	for i := range 2 {
		user, err := env.Store.CreateUser(&store.User{
			Username:   fmt.Sprintf("cascade_smoke_%d", i),
			Email:      fmt.Sprintf("cascade_smoke_%d@test.com", i),
			Provider:   "local",
			ProviderID: fmt.Sprintf("cascade_smoke_pid_%d", i),
			Status:     "active",
		})
		if err != nil {
			t.Fatalf("failed to create user %d: %v", i, err)
		}

		_, err = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
			UserID: user.ID, WorkspaceID: ws.ID, Role: "editor",
		})
		if err != nil {
			t.Fatalf("failed to create membership %d: %v", i, err)
		}

		_, err = env.Store.CreateAPIKey(&store.APIKey{
			KeyID: fmt.Sprintf("cascade_smoke_key_%d", i),
			KeyHash: sha256HexString(fmt.Sprintf("secret_%d", i)),
			UserID: user.ID, WorkspaceID: ws.ID, Role: "editor",
			Label: fmt.Sprintf("cascade key %d", i),
		})
		if err != nil {
			t.Fatalf("failed to create API key %d: %v", i, err)
		}
	}

	// Step 1: POST /api/v1/workspaces/:id/archive — archive the workspace.
	archiveRec := doRequest(env.Echo, http.MethodPost,
		"/api/v1/workspaces/"+ws.ID+"/archive", "", headers)
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("POST /workspaces/:id/archive status = %d, want %d\nBody: %s",
			archiveRec.Code, http.StatusOK, archiveRec.Body.String())
	}

	var archivedWs workspaceResponse
	parseJSON(t, archiveRec, &archivedWs)

	if archivedWs.Status != "archived" {
		t.Errorf("workspace status after archive = %q, want 'archived'", archivedWs.Status)
	}

	// Step 2: DELETE /api/v1/workspaces/:id — delete with cascade.
	deleteRec := doRequest(env.Echo, http.MethodDelete,
		"/api/v1/workspaces/"+ws.ID, "", headers)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("DELETE /workspaces/:id status = %d, want %d\nBody: %s",
			deleteRec.Code, http.StatusOK, deleteRec.Body.String())
	}

	var deleteResp struct {
		Message string `json:"message"`
	}
	parseJSON(t, deleteRec, &deleteResp)

	if deleteResp.Message != "workspace deleted" {
		t.Errorf("delete message = %q, want %q", deleteResp.Message, "workspace deleted")
	}

	// Verify workspace is gone.
	_, err = env.Store.GetWorkspaceByID(ws.ID)
	if err == nil {
		t.Error("workspace still exists after deletion")
	}

	// Verify memberships are gone.
	members, err := env.Store.ListWorkspaceMembers(ws.ID)
	if err == nil && len(members) > 0 {
		t.Errorf("memberships still exist after deletion: %d remain", len(members))
	}

	// Verify API keys are gone.
	keyCount, err := env.Store.CountAPIKeysByWorkspaceID(ws.ID)
	if err == nil && keyCount > 0 {
		t.Errorf("API keys still exist after deletion: %d remain", keyCount)
	}
}

// ============================================================================
// TS-02-SMOKE-6: Non-admin updates own full_name.
// Execution Path: 02-PATH-6
// ============================================================================

func TestSmoke_NonAdminUpdatesOwnFullName(t *testing.T) {
	env := setupFullTestEnv(t)

	// Create user with reader role.
	user, err := env.Store.CreateUser(&store.User{
		Username: "smoke_selfupdate", Email: "smoke_selfupdate@test.com",
		Provider: "local", ProviderID: "smoke_selfupdate_pid", Status: "active",
		FullName: "Old Name",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "SelfUpdate WS", Slug: "self-update-ws",
		URL: "https://selfupdate.example.com", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	_, err = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID: user.ID, WorkspaceID: ws.ID, Role: "reader",
	})
	if err != nil {
		t.Fatalf("failed to add membership: %v", err)
	}

	readerSecret := "smoke_reader_secret"
	readerKeyID := "smoke_reader_key"
	_, err = env.Store.CreateAPIKey(&store.APIKey{
		KeyID: readerKeyID, KeyHash: sha256HexString(readerSecret),
		UserID: user.ID, WorkspaceID: ws.ID, Role: "reader",
		Label: "reader key",
	})
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}

	readerToken := fmt.Sprintf("af_%s_%s", readerKeyID, readerSecret)
	readerHeaders := map[string]string{"Authorization": "Bearer " + readerToken}

	// PUT /api/v1/users/:id — update own full_name.
	updateBody := `{"full_name": "Smoke New Name"}`
	updateRec := doRequest(env.Echo, http.MethodPut,
		"/api/v1/users/"+user.ID, updateBody, readerHeaders)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("PUT /users/:id status = %d, want %d\nBody: %s",
			updateRec.Code, http.StatusOK, updateRec.Body.String())
	}

	var updatedUser userResponse
	parseJSON(t, updateRec, &updatedUser)

	if updatedUser.FullName != "Smoke New Name" {
		t.Errorf("full_name = %q, want %q", updatedUser.FullName, "Smoke New Name")
	}

	// Verify DB state.
	dbUser, err := env.Store.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("user not found in DB: %v", err)
	}
	if dbUser.FullName != "Smoke New Name" {
		t.Errorf("db full_name = %q, want %q", dbUser.FullName, "Smoke New Name")
	}

	// Status should remain unchanged.
	if dbUser.Status != "active" {
		t.Errorf("db status changed to %q after full_name update, want 'active'", dbUser.Status)
	}
}

// ============================================================================
// Additional smoke test: Verify that a full OAuth flow wired through the
// complete stack (middleware, RBAC, handlers) works end-to-end with a mock
// identity provider.
// ============================================================================

func TestSmoke_FullOAuthFlowWithMockProvider(t *testing.T) {
	// Set up mock GitHub server.
	mockGitHub := setupMockGitHubServer(t,
		`{"access_token": "full_smoke_token", "token_type": "bearer"}`,
		`{"id": 90099, "login": "fullsmokeuser", "email": "fullsmoke@example.com", "name": "Full Smoke"}`,
		http.StatusOK,
		http.StatusOK,
	)
	defer mockGitHub.Close()

	cfg := &config.AuthConfig{
		OAuth: []config.OAuthProviderConfig{
			{
				Provider:     "github",
				ClientID:     "smoke_id",
				ClientSecret: "smoke_secret",
				TokenURL:     mockGitHub.URL + "/login/oauth/access_token",
				UserinfoURL:  mockGitHub.URL + "/api/user",
			},
		},
		Timeout: 5,
	}

	registry := auth.NewRegistry(cfg)
	s := createTestStore(t)
	authHandler := handler.NewAuthHandler(registry, s)
	userHandler := handler.NewUserHandler(s)
	apiKeyHandler := handler.NewAPIKeyHandler(s)
	workspaceHandler := handler.NewWorkspaceHandler(s)

	e := echo.New()
	e.HTTPErrorHandler = handler.CustomHTTPErrorHandler

	// Public auth routes.
	authGroup := e.Group("/api/v1/auth")
	authGroup.GET("/providers", authHandler.ListProviders)
	authGroup.POST("/callback", authHandler.OAuthCallback)

	// Protected routes.
	apiGroup := e.Group("/api/v1", auth.AuthMiddleware(s))

	adminGroup := apiGroup.Group("", auth.RequireRole(auth.RoleAdmin))
	adminGroup.POST("/workspaces", workspaceHandler.CreateWorkspace)
	adminGroup.POST("/workspaces/:id/members", workspaceHandler.AddOrUpdateMember)

	editorGroup := apiGroup.Group("", auth.RequireRole(auth.RoleAdmin, auth.RoleEditor))
	editorGroup.POST("/keys", apiKeyHandler.CreateAPIKey)

	apiGroup.GET("/keys", apiKeyHandler.ListAPIKeys)
	apiGroup.PUT("/users/:id", userHandler.UpdateUser, auth.RequireAdminOrSelf())

	// Step 1: OAuth login.
	cbBody := `{"provider": "github", "code": "full_smoke_code", "redirect_uri": "http://localhost/cb"}`
	cbRec := doRequest(e, http.MethodPost, "/api/v1/auth/callback", cbBody, nil)
	if cbRec.Code != http.StatusOK {
		t.Fatalf("OAuth callback: status = %d, want %d\nBody: %s",
			cbRec.Code, http.StatusOK, cbRec.Body.String())
	}

	var createdUser userResponse
	parseJSON(t, cbRec, &createdUser)

	if createdUser.Status != "active" {
		t.Errorf("created user status = %q, want 'active'", createdUser.Status)
	}
	if createdUser.Provider != "github" {
		t.Errorf("created user provider = %q, want 'github'", createdUser.Provider)
	}

	// Step 2: Admin creates workspace and adds the OAuth user.
	adminToken := "af_admin_fullsmoke"
	seedAdminTokenFull(t, s, adminToken)
	aHeaders := adminHeaders(adminToken)

	wsBody := `{"name": "FullSmoke WS", "slug": "fullsmoke-ws", "url": "https://fullsmoke.example.com"}`
	wsRec := doRequest(e, http.MethodPost, "/api/v1/workspaces", wsBody, aHeaders)
	if wsRec.Code != http.StatusCreated {
		t.Fatalf("workspace creation: status = %d, want %d\nBody: %s",
			wsRec.Code, http.StatusCreated, wsRec.Body.String())
	}

	var ws workspaceResponse
	parseJSON(t, wsRec, &ws)

	memberBody := fmt.Sprintf(`{"user_id": %q, "role": "editor"}`, createdUser.ID)
	memberRec := doRequest(e, http.MethodPost,
		"/api/v1/workspaces/"+ws.ID+"/members", memberBody, aHeaders)
	if memberRec.Code != http.StatusOK {
		t.Fatalf("add member: status = %d, want %d\nBody: %s",
			memberRec.Code, http.StatusOK, memberRec.Body.String())
	}

	// Step 3: OAuth user creates an API key using their editor membership.
	// First create an auth key for the user.
	authSecret := "fullsmoke_editor_secret"
	_, err := s.CreateAPIKey(&store.APIKey{
		KeyID: "fullsmoke_editorkey", KeyHash: sha256HexString(authSecret),
		UserID: createdUser.ID, WorkspaceID: ws.ID, Role: "editor",
		Label: "fullsmoke editor auth",
	})
	if err != nil {
		t.Fatalf("failed to create auth key: %v", err)
	}

	editorToken := "af_fullsmoke_editorkey_" + authSecret
	editorHeaders := map[string]string{"Authorization": "Bearer " + editorToken}

	keyBody := fmt.Sprintf(`{"workspace_id": %q, "label": "fullsmoke-key", "expires": 0}`, ws.ID)
	keyRec := doRequest(e, http.MethodPost, "/api/v1/keys", keyBody, editorHeaders)
	if keyRec.Code != http.StatusCreated {
		t.Fatalf("create key: status = %d, want %d\nBody: %s",
			keyRec.Code, http.StatusCreated, keyRec.Body.String())
	}

	var keyResp apiKeyCreateResponse
	parseJSON(t, keyRec, &keyResp)

	if keyResp.Key == "" {
		t.Fatal("new key plaintext is empty")
	}

	// Step 4: Verify the new key works for authentication.
	newKeyHeaders := map[string]string{"Authorization": "Bearer " + keyResp.Key}
	listRec := doRequest(e, http.MethodGet, "/api/v1/keys", "", newKeyHeaders)
	if listRec.Code != http.StatusOK {
		t.Fatalf("auth with new key: status = %d, want %d\nBody: %s",
			listRec.Code, http.StatusOK, listRec.Body.String())
	}

	// Step 5: User updates their own full_name.
	updateBody := `{"full_name": "Full Smoke Updated"}`
	updateRec := doRequest(e, http.MethodPut,
		"/api/v1/users/"+createdUser.ID, updateBody, newKeyHeaders)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("self-update full_name: status = %d, want %d\nBody: %s",
			updateRec.Code, http.StatusOK, updateRec.Body.String())
	}
}

// ============================================================================
// Helper: sha256HexString is defined in middleware_test.go (same package).
// It computes SHA-256 of a string and returns hex encoding.
// We do NOT redefine it here — the existing definition in middleware_test.go
// is already available within the integration_test package.
// ============================================================================

// newTestServerFromMux is already defined in testutil_test.go.
// setupMockGitHubServer is already defined in testutil_test.go.
// doRequest is already defined in testutil_test.go.
// parseJSON is already defined in testutil_test.go.
// seedAdminTokenFull is already defined in testutil_test.go.
// adminHeaders is already defined in testutil_test.go.
// setupFullTestEnv is already defined in testutil_test.go.

