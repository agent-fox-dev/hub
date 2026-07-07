package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/labstack/echo/v4"
)

// rbacErrorResponse is the standard error envelope for RBAC test assertions.
type rbacErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// setAuthContext sets authentication context values on an Echo context,
// simulating what AuthMiddleware would populate on a successful token
// validation.
func setAuthContext(c echo.Context, userID, role, workspaceID, authMethod, userStatus string) {
	c.Set(auth.ContextKeyUserID, userID)
	c.Set(auth.ContextKeyRole, role)
	c.Set(auth.ContextKeyWorkspaceID, workspaceID)
	c.Set(auth.ContextKeyAuthMethod, authMethod)
	c.Set(auth.ContextKeyUserStatus, userStatus)
}

// makeRBACRequest creates an Echo context with pre-populated auth context
// values and calls the RBAC middleware chain. Returns the response recorder.
func makeRBACRequest(
	t *testing.T,
	method, path string,
	body string,
	userID, role, workspaceID, authMethod, userStatus string,
	middleware echo.MiddlewareFunc,
) *httptest.ResponseRecorder {
	t.Helper()

	e := echo.New()
	rec := httptest.NewRecorder()

	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	c := e.NewContext(req, rec)

	// Set auth context values as if auth middleware has already validated.
	setAuthContext(c, userID, role, workspaceID, authMethod, userStatus)

	// Create the handler chain: middleware → handler.
	handler := middleware(func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Execute the middleware chain.
	if err := handler(c); err != nil {
		// Use Echo's default error handler to write the error response.
		e.DefaultHTTPErrorHandler(err, c)
	}

	return rec
}

// makeRBACRequestWithParams creates an Echo context with pre-populated auth
// context values and URL path parameters.
func makeRBACRequestWithParams(
	t *testing.T,
	method, path string,
	body string,
	userID, role, workspaceID, authMethod, userStatus string,
	params map[string]string,
	middleware echo.MiddlewareFunc,
) *httptest.ResponseRecorder {
	t.Helper()

	e := echo.New()
	rec := httptest.NewRecorder()

	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	c := e.NewContext(req, rec)
	setAuthContext(c, userID, role, workspaceID, authMethod, userStatus)

	// Set path parameters.
	names := make([]string, 0, len(params))
	values := make([]string, 0, len(params))
	for k, v := range params {
		names = append(names, k)
		values = append(values, v)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)

	handler := middleware(func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	if err := handler(c); err != nil {
		e.DefaultHTTPErrorHandler(err, c)
	}

	return rec
}

// parseRBACJSON parses the response body as JSON into the given target.
func parseRBACJSON(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("failed to parse JSON response: %v\nBody: %s",
			err, rec.Body.String())
	}
}

// --- TS-02-12: Role definitions and permission enforcement ---

// TS-02-12: Verify that the three role constants exist with the correct
// string values.
func TestRBAC_RoleConstants_Exist(t *testing.T) {
	if auth.RoleAdmin != "admin" {
		t.Errorf("RoleAdmin = %q, want %q", auth.RoleAdmin, "admin")
	}
	if auth.RoleEditor != "editor" {
		t.Errorf("RoleEditor = %q, want %q", auth.RoleEditor, "editor")
	}
	if auth.RoleReader != "reader" {
		t.Errorf("RoleReader = %q, want %q", auth.RoleReader, "reader")
	}
}

// TS-02-12: Verify that admin role is permitted on workspace creation.
func TestRBAC_AdminCanCreateWorkspaces(t *testing.T) {
	rbac := auth.RequireRole(auth.RoleAdmin)

	rec := makeRBACRequest(t,
		http.MethodPost, "/api/v1/workspaces", "",
		"admin_user_1", auth.RoleAdmin, "", auth.AuthMethodAdmin, "active",
		rbac,
	)

	if rec.Code != http.StatusOK {
		t.Errorf("admin on POST /api/v1/workspaces: status = %d, want %d",
			rec.Code, http.StatusOK)
	}
}

// TS-02-12: Verify that editor is permitted on key creation endpoint.
func TestRBAC_EditorCanCreateKeys(t *testing.T) {
	rbac := auth.RequireRole(auth.RoleAdmin, auth.RoleEditor)

	rec := makeRBACRequest(t,
		http.MethodPost, "/api/v1/keys", "",
		"editor_user_1", auth.RoleEditor, "ws001", auth.AuthMethodAPIKey, "active",
		rbac,
	)

	if rec.Code != http.StatusOK {
		t.Errorf("editor on POST /api/v1/keys: status = %d, want %d",
			rec.Code, http.StatusOK)
	}
}

// TS-02-12: Verify that reader is rejected on key creation endpoint.
func TestRBAC_ReaderCannotCreateKeys(t *testing.T) {
	rbac := auth.RequireRole(auth.RoleAdmin, auth.RoleEditor)

	rec := makeRBACRequest(t,
		http.MethodPost, "/api/v1/keys", "",
		"reader_user_1", auth.RoleReader, "ws001", auth.AuthMethodAPIKey, "active",
		rbac,
	)

	if rec.Code != http.StatusForbidden {
		t.Errorf("reader on POST /api/v1/keys: status = %d, want %d",
			rec.Code, http.StatusForbidden)
	}
}

// --- TS-02-13: Non-admin on admin-only endpoint ---

// TS-02-13: Verify that a non-admin user attempting an admin-only endpoint
// receives HTTP 403 before the handler executes.
func TestRBAC_NonAdmin_AdminOnlyEndpoint_Returns403(t *testing.T) {
	rbac := auth.RequireRole(auth.RoleAdmin)

	// Reader attempting admin-only endpoint.
	rec := makeRBACRequest(t,
		http.MethodPost, "/api/v1/workspaces", `{"name":"x","slug":"x-slug","url":"https://x.com"}`,
		"reader_user_2", auth.RoleReader, "ws001", auth.AuthMethodAPIKey, "active",
		rbac,
	)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("reader on admin-only endpoint: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var errResp rbacErrorResponse
	parseRBACJSON(t, rec, &errResp)

	if errResp.Error.Code != "403" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "403")
	}
	if errResp.Error.Message != "insufficient permissions" {
		t.Errorf("error message = %q, want %q",
			errResp.Error.Message, "insufficient permissions")
	}
}

// TS-02-13: Verify that the handler is never invoked for non-admin user.
func TestRBAC_NonAdmin_HandlerNotInvoked(t *testing.T) {
	handlerCalled := false
	rbac := auth.RequireRole(auth.RoleAdmin)

	e := echo.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", nil)
	c := e.NewContext(req, rec)
	setAuthContext(c, "editor_user", auth.RoleEditor, "ws001", auth.AuthMethodAPIKey, "active")

	h := rbac(func(c echo.Context) error {
		handlerCalled = true
		return c.JSON(http.StatusOK, nil)
	})

	if err := h(c); err != nil {
		e.DefaultHTTPErrorHandler(err, c)
	}

	if handlerCalled {
		t.Error("handler was invoked for non-admin user on admin-only endpoint")
	}
}

// TS-02-13: Verify editor is also rejected on admin-only endpoint.
func TestRBAC_Editor_AdminOnlyEndpoint_Returns403(t *testing.T) {
	rbac := auth.RequireRole(auth.RoleAdmin)

	rec := makeRBACRequest(t,
		http.MethodPost, "/api/v1/workspaces", "",
		"editor_user_2", auth.RoleEditor, "ws001", auth.AuthMethodAPIKey, "active",
		rbac,
	)

	if rec.Code != http.StatusForbidden {
		t.Errorf("editor on admin-only endpoint: status = %d, want %d",
			rec.Code, http.StatusForbidden)
	}
}

// --- TS-02-14: Non-admin self-update tests ---

// TS-02-14: Verify that a non-admin user can update their own full_name.
func TestRBAC_NonAdmin_CanUpdateOwnFullName(t *testing.T) {
	rbac := auth.RequireAdminOrSelf()

	rec := makeRBACRequestWithParams(t,
		http.MethodPut, "/api/v1/users/user003", `{"full_name":"New Name"}`,
		"user003", auth.RoleReader, "ws001", auth.AuthMethodAPIKey, "active",
		map[string]string{"id": "user003"},
		rbac,
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("non-admin updating own full_name: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TS-02-14: Verify that a non-admin user is rejected when attempting to
// change their own status.
func TestRBAC_NonAdmin_CannotChangeOwnStatus(t *testing.T) {
	rbac := auth.RequireAdminOrSelf()

	rec := makeRBACRequestWithParams(t,
		http.MethodPut, "/api/v1/users/user003", `{"status":"blocked"}`,
		"user003", auth.RoleReader, "ws001", auth.AuthMethodAPIKey, "active",
		map[string]string{"id": "user003"},
		rbac,
	)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin changing own status: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var errResp rbacErrorResponse
	parseRBACJSON(t, rec, &errResp)

	if errResp.Error.Code != "403" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "403")
	}
}

// TS-02-14: Verify non-admin cannot update a different user's record.
func TestRBAC_NonAdmin_CannotUpdateOtherUser(t *testing.T) {
	rbac := auth.RequireAdminOrSelf()

	rec := makeRBACRequestWithParams(t,
		http.MethodPut, "/api/v1/users/other_user", `{"full_name":"Hacked"}`,
		"user003", auth.RoleReader, "ws001", auth.AuthMethodAPIKey, "active",
		map[string]string{"id": "other_user"},
		rbac,
	)

	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin updating other user: status = %d, want %d",
			rec.Code, http.StatusForbidden)
	}
}

// --- TS-02-15: Admin bypasses workspace scoping ---

// TS-02-15: Verify that admin-authenticated requests can access all endpoints
// across all workspaces regardless of workspace scoping.
func TestRBAC_Admin_BypassesWorkspaceScoping(t *testing.T) {
	endpoints := []struct {
		name       string
		method     string
		path       string
		middleware echo.MiddlewareFunc
	}{
		{
			"list users",
			http.MethodGet, "/api/v1/users",
			auth.RequireRole(auth.RoleAdmin),
		},
		{
			"list workspaces",
			http.MethodGet, "/api/v1/workspaces",
			auth.RequireRole(auth.RoleAdmin),
		},
		{
			"list keys",
			http.MethodGet, "/api/v1/keys",
			auth.RequireRole(auth.RoleAdmin, auth.RoleEditor, auth.RoleReader),
		},
	}

	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			rec := makeRBACRequest(t,
				ep.method, ep.path, "",
				"admin_global", auth.RoleAdmin, "", auth.AuthMethodAdmin, "active",
				ep.middleware,
			)

			if rec.Code != http.StatusOK {
				t.Errorf("admin on %s %s: status = %d, want %d",
					ep.method, ep.path, rec.Code, http.StatusOK)
			}
		})
	}
}

// TS-02-15: Verify admin can access workspace-scoped endpoints even without
// workspace_id in context.
func TestRBAC_Admin_AccessesWithoutWorkspaceScope(t *testing.T) {
	rbac := auth.RequireRole(auth.RoleAdmin, auth.RoleEditor)

	// Admin with empty workspace_id should still be permitted.
	rec := makeRBACRequest(t,
		http.MethodPost, "/api/v1/keys", "",
		"admin_no_ws", auth.RoleAdmin, "", auth.AuthMethodAdmin, "active",
		rbac,
	)

	if rec.Code != http.StatusOK {
		t.Errorf("admin without workspace_id: status = %d, want %d",
			rec.Code, http.StatusOK)
	}
}

// --- TS-02-16: Editor vs reader on write endpoints ---

// TS-02-16: Verify that editor API keys are permitted on write endpoints
// (e.g. POST /api/v1/keys).
func TestRBAC_Editor_PermittedOnWriteEndpoint(t *testing.T) {
	rbac := auth.RequireRole(auth.RoleAdmin, auth.RoleEditor)

	rec := makeRBACRequest(t,
		http.MethodPost, "/api/v1/keys",
		`{"workspace_id":"ws001","label":"testkey","expires":30}`,
		"editor_ws001", auth.RoleEditor, "ws001", auth.AuthMethodAPIKey, "active",
		rbac,
	)

	if rec.Code != http.StatusOK {
		t.Errorf("editor on POST /api/v1/keys: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TS-02-16: Verify that reader API keys are rejected on write endpoints
// with HTTP 403.
func TestRBAC_Reader_RejectedOnWriteEndpoint(t *testing.T) {
	rbac := auth.RequireRole(auth.RoleAdmin, auth.RoleEditor)

	rec := makeRBACRequest(t,
		http.MethodPost, "/api/v1/keys",
		`{"workspace_id":"ws001","label":"testkey","expires":30}`,
		"reader_ws001", auth.RoleReader, "ws001", auth.AuthMethodAPIKey, "active",
		rbac,
	)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("reader on POST /api/v1/keys: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var errResp rbacErrorResponse
	parseRBACJSON(t, rec, &errResp)

	if errResp.Error.Code != "403" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "403")
	}
}

// TS-02-16: Verify reader is also rejected on DELETE key endpoint.
func TestRBAC_Reader_RejectedOnDeleteKey(t *testing.T) {
	rbac := auth.RequireRole(auth.RoleAdmin, auth.RoleEditor)

	rec := makeRBACRequestWithParams(t,
		http.MethodDelete, "/api/v1/keys/key123", "",
		"reader_ws001", auth.RoleReader, "ws001", auth.AuthMethodAPIKey, "active",
		map[string]string{"key_id": "key123"},
		rbac,
	)

	if rec.Code != http.StatusForbidden {
		t.Errorf("reader on DELETE /api/v1/keys/:key_id: status = %d, want %d",
			rec.Code, http.StatusForbidden)
	}
}

// TS-02-16: Verify editor is permitted on DELETE key endpoint.
func TestRBAC_Editor_PermittedOnDeleteKey(t *testing.T) {
	rbac := auth.RequireRole(auth.RoleAdmin, auth.RoleEditor)

	rec := makeRBACRequestWithParams(t,
		http.MethodDelete, "/api/v1/keys/key123", "",
		"editor_ws001", auth.RoleEditor, "ws001", auth.AuthMethodAPIKey, "active",
		map[string]string{"key_id": "key123"},
		rbac,
	)

	if rec.Code != http.StatusOK {
		t.Errorf("editor on DELETE /api/v1/keys/:key_id: status = %d, want %d",
			rec.Code, http.StatusOK)
	}
}

// --- TS-02-E10: Unknown role value ---

// TS-02-E10: Verify that an unknown role value in the request context is
// treated as an authorization failure with HTTP 403.
func TestRBAC_UnknownRole_Returns403(t *testing.T) {
	rbac := auth.RequireRole(auth.RoleAdmin, auth.RoleEditor)

	rec := makeRBACRequest(t,
		http.MethodPost, "/api/v1/workspaces", "",
		"unknown_role_user", "superuser", "", auth.AuthMethodAPIKey, "active",
		rbac,
	)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("unknown role 'superuser': status = %d, want %d\nBody: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var errResp rbacErrorResponse
	parseRBACJSON(t, rec, &errResp)

	if errResp.Error.Code != "403" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "403")
	}
	if !strings.Contains(errResp.Error.Message, "unknown role") {
		t.Errorf("error message = %q, want it to contain 'unknown role'",
			errResp.Error.Message)
	}
}

// TS-02-E10: Verify that an empty role value is also treated as unknown.
func TestRBAC_EmptyRole_Returns403(t *testing.T) {
	rbac := auth.RequireRole(auth.RoleAdmin, auth.RoleEditor)

	rec := makeRBACRequest(t,
		http.MethodPost, "/api/v1/workspaces", "",
		"empty_role_user", "", "", auth.AuthMethodAPIKey, "active",
		rbac,
	)

	if rec.Code != http.StatusForbidden {
		t.Errorf("empty role: status = %d, want %d",
			rec.Code, http.StatusForbidden)
	}
}

// TS-02-E10: Verify that nil role context value is treated as unknown.
func TestRBAC_NilRole_Returns403(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil)
	c := e.NewContext(req, rec)

	// Do not set ContextKeyRole — it will be nil.
	c.Set(auth.ContextKeyUserID, "user_no_role")
	c.Set(auth.ContextKeyAuthMethod, auth.AuthMethodAPIKey)
	c.Set(auth.ContextKeyUserStatus, "active")

	rbac := auth.RequireRole(auth.RoleAdmin)
	h := rbac(func(c echo.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	if err := h(c); err != nil {
		e.DefaultHTTPErrorHandler(err, c)
	}

	if rec.Code != http.StatusForbidden {
		t.Errorf("nil role: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
