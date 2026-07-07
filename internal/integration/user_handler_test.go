package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/store"
)

// --- Constants for user handler tests ---

const testAdminToken = "af_admin_user_handler_test"

// --- TS-02-17: POST /api/v1/users creates user with status=active ---

// TS-02-17: Verify that POST /api/v1/users creates a new user with
// status=active and returns HTTP 201 with the user object.
func TestUserHandler_CreateUser_ReturnsCreated(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	body := `{
		"username": "newuser",
		"email": "newuser@example.com",
		"provider": "github",
		"provider_id": "gh_99999"
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/users", body,
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/users: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var user userResponse
	parseJSON(t, rec, &user)

	if user.Username != "newuser" {
		t.Errorf("username = %q, want %q", user.Username, "newuser")
	}
	if user.Email != "newuser@example.com" {
		t.Errorf("email = %q, want %q", user.Email, "newuser@example.com")
	}
	if user.Provider != "github" {
		t.Errorf("provider = %q, want %q", user.Provider, "github")
	}
	if user.ProviderID != "gh_99999" {
		t.Errorf("provider_id = %q, want %q", user.ProviderID, "gh_99999")
	}
	if user.Status != "active" {
		t.Errorf("status = %q, want %q", user.Status, "active")
	}
	if user.ID == "" {
		t.Error("id should be non-empty")
	}
}

// TS-02-17: Verify the user is persisted in the database after creation.
func TestUserHandler_CreateUser_PersistsInDB(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	body := `{
		"username": "persistuser",
		"email": "persist@example.com",
		"provider": "github",
		"provider_id": "gh_persist_001"
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/users", body,
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var user userResponse
	parseJSON(t, rec, &user)

	// Verify the user exists in the database.
	dbUser, err := env.Store.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID(%q) failed: %v", user.ID, err)
	}
	if dbUser.Username != "persistuser" {
		t.Errorf("DB username = %q, want %q", dbUser.Username, "persistuser")
	}
	if dbUser.Status != "active" {
		t.Errorf("DB status = %q, want %q", dbUser.Status, "active")
	}
}

// --- TS-02-18: GET /api/v1/users returns all users ---

// TS-02-18: Verify that GET /api/v1/users returns an array of all user
// records with HTTP 200.
func TestUserHandler_ListUsers_ReturnsAll(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	// Create two users directly in the store.
	_, err := env.Store.CreateUser(&store.User{
		Username:   "listuser1",
		Email:      "list1@example.com",
		Provider:   "github",
		ProviderID: "gh_list_001",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user 1: %v", err)
	}

	_, err = env.Store.CreateUser(&store.User{
		Username:   "listuser2",
		Email:      "list2@example.com",
		Provider:   "github",
		ProviderID: "gh_list_002",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user 2: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/users", "",
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/users: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var users []userResponse
	parseJSON(t, rec, &users)

	// Should have at least the 2 users we created.
	count, _ := env.Store.CountUsers()
	if len(users) != count {
		t.Errorf("len(users) = %d, want %d (matching DB count)", len(users), count)
	}
	if len(users) < 2 {
		t.Errorf("len(users) = %d, want at least 2", len(users))
	}
}

// TS-02-18: Verify the response is a JSON array.
func TestUserHandler_ListUsers_IsArray(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/users", "",
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Verify the body is a JSON array.
	var raw json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if raw[0] != '[' {
		t.Errorf("response body should be a JSON array, got: %c...", raw[0])
	}
}

// --- TS-02-19: GET /api/v1/users/:id returns user with memberships ---

// TS-02-19: Verify that GET /api/v1/users/:id returns the user object
// including workspace memberships and roles.
func TestUserHandler_GetUser_ReturnsMemberships(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	// Create a user.
	user, err := env.Store.CreateUser(&store.User{
		Username:   "membershipuser",
		Email:      "membership@example.com",
		Provider:   "github",
		ProviderID: "gh_membership_001",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create two workspaces and add memberships.
	ws1, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "Workspace One",
		Slug: "workspace-one",
		URL:  "https://ws1.example.com",
	})
	if err != nil {
		t.Fatalf("failed to create workspace 1: %v", err)
	}

	ws2, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "Workspace Two",
		Slug: "workspace-two",
		URL:  "https://ws2.example.com",
	})
	if err != nil {
		t.Fatalf("failed to create workspace 2: %v", err)
	}

	_, err = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID:      user.ID,
		WorkspaceID: ws1.ID,
		Role:        "editor",
	})
	if err != nil {
		t.Fatalf("failed to create membership 1: %v", err)
	}

	_, err = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID:      user.ID,
		WorkspaceID: ws2.ID,
		Role:        "reader",
	})
	if err != nil {
		t.Fatalf("failed to create membership 2: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodGet,
		fmt.Sprintf("/api/v1/users/%s", user.ID), "",
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/users/%s: status = %d, want %d\nBody: %s",
			user.ID, rec.Code, http.StatusOK, rec.Body.String())
	}

	var userWithMemberships userWithMembershipsResponse
	parseJSON(t, rec, &userWithMemberships)

	if userWithMemberships.ID != user.ID {
		t.Errorf("id = %q, want %q", userWithMemberships.ID, user.ID)
	}
	if len(userWithMemberships.Memberships) != 2 {
		t.Fatalf("len(memberships) = %d, want 2", len(userWithMemberships.Memberships))
	}

	// Verify memberships have workspace_id and role fields.
	for i, m := range userWithMemberships.Memberships {
		if m.WorkspaceID == "" {
			t.Errorf("membership[%d].workspace_id is empty", i)
		}
		if m.Role == "" {
			t.Errorf("membership[%d].role is empty", i)
		}
	}
}

// TS-02-19: Verify a user with no memberships has an empty memberships array.
func TestUserHandler_GetUser_NoMemberships(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	user, err := env.Store.CreateUser(&store.User{
		Username:   "nomemberuser",
		Email:      "nomember@example.com",
		Provider:   "github",
		ProviderID: "gh_nomember_001",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodGet,
		fmt.Sprintf("/api/v1/users/%s", user.ID), "",
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	// The response should either have an empty array or omit memberships.
	var result map[string]json.RawMessage
	parseJSON(t, rec, &result)

	if memberships, ok := result["memberships"]; ok {
		var mList []membershipResponse
		if err := json.Unmarshal(memberships, &mList); err == nil {
			if len(mList) != 0 {
				t.Errorf("expected empty memberships array, got %d entries", len(mList))
			}
		}
	}
}

// --- TS-02-20: PUT /api/v1/users/:id with admin updates full_name and status ---

// TS-02-20: Verify that PUT /api/v1/users/:id with admin credentials
// updates full_name and/or status and returns the updated user object.
func TestUserHandler_UpdateUser_AdminUpdatesBoth(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	user, err := env.Store.CreateUser(&store.User{
		Username:   "updateuser",
		Email:      "update@example.com",
		Provider:   "github",
		ProviderID: "gh_update_001",
		FullName:   "Old Name",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	body := `{"full_name": "Updated Name", "status": "blocked"}`

	rec := doRequest(env.Echo, http.MethodPut,
		fmt.Sprintf("/api/v1/users/%s", user.ID), body,
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/v1/users/%s: status = %d, want %d\nBody: %s",
			user.ID, rec.Code, http.StatusOK, rec.Body.String())
	}

	var updated userResponse
	parseJSON(t, rec, &updated)

	if updated.FullName != "Updated Name" {
		t.Errorf("full_name = %q, want %q", updated.FullName, "Updated Name")
	}
	if updated.Status != "blocked" {
		t.Errorf("status = %q, want %q", updated.Status, "blocked")
	}
}

// TS-02-20: Verify that admin can update only full_name without changing status.
func TestUserHandler_UpdateUser_AdminUpdatesOnlyFullName(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	user, err := env.Store.CreateUser(&store.User{
		Username:   "fnonly",
		Email:      "fnonly@example.com",
		Provider:   "github",
		ProviderID: "gh_fnonly_001",
		FullName:   "Original",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	body := `{"full_name": "Just Name Change"}`

	rec := doRequest(env.Echo, http.MethodPut,
		fmt.Sprintf("/api/v1/users/%s", user.ID), body,
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var updated userResponse
	parseJSON(t, rec, &updated)

	if updated.FullName != "Just Name Change" {
		t.Errorf("full_name = %q, want %q", updated.FullName, "Just Name Change")
	}
	if updated.Status != "active" {
		t.Errorf("status = %q, want %q (should remain unchanged)", updated.Status, "active")
	}
}

// TS-02-20: Verify the update is persisted in the database.
func TestUserHandler_UpdateUser_PersistsInDB(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	user, err := env.Store.CreateUser(&store.User{
		Username:   "dbpersist",
		Email:      "dbpersist@example.com",
		Provider:   "github",
		ProviderID: "gh_dbpersist_001",
		FullName:   "Before",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	body := `{"full_name": "After", "status": "blocked"}`

	rec := doRequest(env.Echo, http.MethodPut,
		fmt.Sprintf("/api/v1/users/%s", user.ID), body,
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Check the database directly.
	dbUser, err := env.Store.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}
	if dbUser.FullName != "After" {
		t.Errorf("DB full_name = %q, want %q", dbUser.FullName, "After")
	}
	if dbUser.Status != "blocked" {
		t.Errorf("DB status = %q, want %q", dbUser.Status, "blocked")
	}
}

// --- TS-02-E11: Duplicate username or (provider, provider_id) ---

// TS-02-E11: Verify that POST /api/v1/users with a duplicate username
// returns HTTP 409 and no user is created.
func TestUserHandler_CreateUser_DuplicateUsername_Returns409(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	// Create an existing user.
	_, err := env.Store.CreateUser(&store.User{
		Username:   "existing_user",
		Email:      "existing@example.com",
		Provider:   "github",
		ProviderID: "gh_111",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create existing user: %v", err)
	}

	countBefore, _ := env.Store.CountUsers()

	// Try to create another user with the same username.
	body := `{
		"username": "existing_user",
		"email": "other@example.com",
		"provider": "gitlab",
		"provider_id": "gl_999"
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/users", body,
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate username: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "409" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "409")
	}

	countAfter, _ := env.Store.CountUsers()
	if countAfter != countBefore {
		t.Errorf("user count changed from %d to %d; no user should be created",
			countBefore, countAfter)
	}
}

// TS-02-E11: Verify that POST /api/v1/users with a duplicate
// (provider, provider_id) pair returns HTTP 409.
func TestUserHandler_CreateUser_DuplicateProviderID_Returns409(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	// Create an existing user.
	_, err := env.Store.CreateUser(&store.User{
		Username:   "existingprovider",
		Email:      "existprov@example.com",
		Provider:   "github",
		ProviderID: "gh_111",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create existing user: %v", err)
	}

	countBefore, _ := env.Store.CountUsers()

	// Try to create another user with same (provider, provider_id).
	body := `{
		"username": "newuser2",
		"email": "other@example.com",
		"provider": "github",
		"provider_id": "gh_111"
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/users", body,
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate provider_id: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "409" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "409")
	}

	countAfter, _ := env.Store.CountUsers()
	if countAfter != countBefore {
		t.Errorf("user count changed from %d to %d; no user should be created",
			countBefore, countAfter)
	}
}

// --- TS-02-E12: Non-existent user ID returns HTTP 404 ---

// TS-02-E12: Verify that GET /api/v1/users/:id with a non-existent
// user ID returns HTTP 404.
func TestUserHandler_GetUser_NotFound_Returns404(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	rec := doRequest(env.Echo, http.MethodGet,
		"/api/v1/users/nonexistent_user_id", "",
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET non-existent user: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "404" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "404")
	}
	if errResp.Error.Message != "user not found" {
		t.Errorf("error message = %q, want %q",
			errResp.Error.Message, "user not found")
	}
}

// TS-02-E12: Verify that PUT /api/v1/users/:id with a non-existent
// user ID returns HTTP 404.
func TestUserHandler_UpdateUser_NotFound_Returns404(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	body := `{"full_name": "Test"}`

	rec := doRequest(env.Echo, http.MethodPut,
		"/api/v1/users/nonexistent_user_id", body,
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT non-existent user: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "404" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "404")
	}
}

// --- TS-02-E13: Missing required fields ---

// TS-02-E13: Verify that POST /api/v1/users with missing required fields
// returns HTTP 400.
func TestUserHandler_CreateUser_MissingFields_Returns400(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	// Only username provided; email, provider, provider_id are missing.
	body := `{"username": "incomplete_user"}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/users", body,
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing fields: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "400" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "400")
	}
	if !strings.Contains(errResp.Error.Message, "missing") {
		t.Errorf("error message = %q, want it to contain 'missing'",
			errResp.Error.Message)
	}
}

// TS-02-E13: Verify that an empty body returns HTTP 400.
func TestUserHandler_CreateUser_EmptyBody_Returns400(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/users", "{}",
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// TS-02-E13: Verify that missing only email returns HTTP 400.
func TestUserHandler_CreateUser_MissingEmail_Returns400(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminToken)

	body := `{
		"username": "noEmail",
		"provider": "github",
		"provider_id": "gh_noemail"
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/users", body,
		adminHeaders(testAdminToken))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing email: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
