package users_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/users"
)

// ---------------------------------------------------------------------------
// Test Helpers (shared across user management test files)
// ---------------------------------------------------------------------------

// openTestDB opens an in-memory SQLite database with production pragmas.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory SQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatalf("failed to set %s: %v", pragma, err)
		}
	}
	return db
}

// initUsersTable creates the users table matching spec 01 DDL.
func initUsersTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id          TEXT PRIMARY KEY,
		username    TEXT NOT NULL UNIQUE,
		email       TEXT NOT NULL,
		full_name   TEXT NOT NULL DEFAULT '',
		status      TEXT NOT NULL DEFAULT 'active',
		provider    TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL,
		UNIQUE (provider, provider_id)
	)`)
	if err != nil {
		t.Fatalf("failed to create users table: %v", err)
	}
}

// initAPIKeysTable creates the api_keys table matching spec 01 DDL.
func initAPIKeysTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
		id              TEXT PRIMARY KEY,
		key_id          TEXT NOT NULL UNIQUE,
		secret_hash     TEXT NOT NULL,
		user_id         TEXT NOT NULL REFERENCES users(id),
		expires_at      TEXT,
		created_at      TEXT NOT NULL,
		revoked_at      TEXT,
		expires_in_days INTEGER
	)`)
	if err != nil {
		t.Fatalf("failed to create api_keys table: %v", err)
	}
}

// initTeamsTable creates the teams and team_members tables for join queries.
func initTeamsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS teams (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		slug       TEXT NOT NULL,
		url        TEXT,
		status     TEXT NOT NULL DEFAULT 'active',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("failed to create teams table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS team_members (
		team_id    TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
		user_id    TEXT NOT NULL REFERENCES users(id),
		created_at TEXT NOT NULL,
		PRIMARY KEY (team_id, user_id)
	)`)
	if err != nil {
		t.Fatalf("failed to create team_members table: %v", err)
	}
}

// initAllTables creates all tables needed for user management tests.
func initAllTables(t *testing.T, db *sql.DB) {
	t.Helper()
	initUsersTable(t, db)
	initAPIKeysTable(t, db)
	initTeamsTable(t, db)
}

// stubProviderRegistry is a simple ProviderRegistry for testing.
type stubProviderRegistry struct {
	providers map[string]bool
}

func newStubProviderRegistry(names ...string) *stubProviderRegistry {
	r := &stubProviderRegistry{providers: make(map[string]bool)}
	for _, n := range names {
		r.providers[n] = true
	}
	return r
}

func (r *stubProviderRegistry) IsRegistered(name string) bool {
	return r.providers[name]
}

// setupEcho creates an Echo instance with the custom error handler that
// returns the nested error envelope: {"error": {"code": N, "message": "..."}}.
func setupEcho() *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		he, ok := err.(*echo.HTTPError)
		if !ok {
			he = echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
		}
		msg, ok := he.Message.(string)
		if !ok {
			msg = "internal server error"
		}
		_ = c.JSON(he.Code, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    he.Code,
				"message": msg,
			},
		})
	}
	return e
}

// setAuthContext returns Echo middleware that injects the given AuthContext.
func setAuthContext(ac *users.AuthContext) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(string(users.AuthContextKey), ac)
			return next(c)
		}
	}
}

// adminAuthContext returns an AuthContext for an admin token.
func adminAuthContext() *users.AuthContext {
	return &users.AuthContext{
		CredentialType: users.CredentialTypeAdmin,
		IsAdmin:        true,
	}
}

// userAuthContext returns an AuthContext for a regular user API key.
func userAuthContext(userID string) *users.AuthContext {
	return &users.AuthContext{
		CredentialType: users.CredentialTypeAPIKey,
		UserID:         userID,
		IsAdmin:        false,
	}
}

// insertTestUser inserts a user row directly into the database for test setup.
func insertTestUser(t *testing.T, db *sql.DB, id, username, email, fullName, status, provider, providerID, createdAt string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO users (id, username, email, full_name, status, provider, provider_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, username, email, fullName, status, provider, providerID, createdAt, createdAt,
	)
	if err != nil {
		t.Fatalf("failed to insert test user %s: %v", username, err)
	}
}

// isUUIDv4 checks whether a string looks like a UUID v4.
func isUUIDv4(s string) bool {
	// UUID v4 format: xxxxxxxx-xxxx-4xxx-[89ab]xxx-xxxxxxxxxxxx
	if len(s) != 36 {
		return false
	}
	parts := strings.Split(s, "-")
	if len(parts) != 5 {
		return false
	}
	if len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		return false
	}
	// Version nibble must be 4.
	if len(parts[2]) > 0 && parts[2][0] != '4' {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// TS-02-13: Admin POST /api/v1/users creates a user with status='active',
// UUID v4 id, no API key, and returns HTTP 201 with user object excluding
// provider_id.
// Requirement: 02-REQ-4.1
// ---------------------------------------------------------------------------

func TestCreateUser_AdminCreatesUser(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)
	registry := newStubProviderRegistry("github")

	e := setupEcho()
	handler := users.CreateUserHandler(db, registry)
	e.POST("/api/v1/users", handler, setAuthContext(adminAuthContext()))

	body := `{"username":"adminuser","email":"admin@example.com","full_name":"Admin User","provider":"github","provider_id":"ext-001"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected HTTP 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// id must be UUID v4
	id, ok := resp["id"].(string)
	if !ok || !isUUIDv4(id) {
		t.Errorf("expected UUID v4 id, got %v", resp["id"])
	}

	// Required fields
	if resp["username"] != "adminuser" {
		t.Errorf("expected username 'adminuser', got %v", resp["username"])
	}
	if resp["email"] != "admin@example.com" {
		t.Errorf("expected email 'admin@example.com', got %v", resp["email"])
	}
	if resp["status"] != "active" {
		t.Errorf("expected status 'active', got %v", resp["status"])
	}
	if resp["provider"] != "github" {
		t.Errorf("expected provider 'github', got %v", resp["provider"])
	}

	// provider_id must NOT be in the response
	if _, exists := resp["provider_id"]; exists {
		t.Error("provider_id should be excluded from POST /api/v1/users response")
	}

	// created_at and updated_at must be present
	if resp["created_at"] == nil {
		t.Error("created_at should be set")
	}
	if resp["updated_at"] == nil {
		t.Error("updated_at should be set")
	}

	// No API keys should be created
	var keyCount int
	err := db.QueryRow("SELECT COUNT(*) FROM api_keys WHERE user_id = ?", id).Scan(&keyCount)
	if err != nil {
		t.Fatalf("failed to count api_keys: %v", err)
	}
	if keyCount != 0 {
		t.Errorf("expected 0 api_keys for new user, got %d", keyCount)
	}
}

// ---------------------------------------------------------------------------
// TS-02-14: Admin POST /api/v1/users accepts any non-empty provider string
// and logs a warning for unregistered providers but still creates the user.
// Requirement: 02-REQ-4.2
// ---------------------------------------------------------------------------

func TestCreateUser_UnregisteredProviderAccepted(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)
	registry := newStubProviderRegistry("github") // only github registered

	e := setupEcho()
	handler := users.CreateUserHandler(db, registry)
	e.POST("/api/v1/users", handler, setAuthContext(adminAuthContext()))

	body := `{"username":"provtest","email":"p@example.com","provider":"unknownprovider","provider_id":"ext-999"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected HTTP 201 for unregistered provider, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["provider"] != "unknownprovider" {
		t.Errorf("expected provider 'unknownprovider', got %v", resp["provider"])
	}

	// Verify user was created in DB
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", "provtest").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count users: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 user row for 'provtest', got %d", count)
	}
}

// ---------------------------------------------------------------------------
// TS-02-15: Admin POST /api/v1/users enforces username alphanumeric+hyphen,
// max 39 chars, and non-empty provider_id.
// Requirement: 02-REQ-4.3
// ---------------------------------------------------------------------------

func TestCreateUser_ValidationRules(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)
	registry := newStubProviderRegistry("github")

	e := setupEcho()
	handler := users.CreateUserHandler(db, registry)
	e.POST("/api/v1/users", handler, setAuthContext(adminAuthContext()))

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{
			name:     "invalid_username_special_chars",
			body:     `{"username":"bad_user!","email":"e@e.com","provider":"github","provider_id":"p1"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "empty_provider_id",
			body:     `{"username":"validuser","email":"e@e.com","provider":"github","provider_id":""}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "username_too_long",
			body:     `{"username":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","email":"e@e.com","provider":"github","provider_id":"p2"}`,
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(tt.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("expected HTTP %d, got %d: %s", tt.wantCode, rec.Code, rec.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-02-16: Admin POST /api/v1/users enforces case-insensitive username
// uniqueness and uniqueness of (provider, provider_id).
// Requirement: 02-REQ-4.4
// ---------------------------------------------------------------------------

func TestCreateUser_UniquenessConstraints(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)
	registry := newStubProviderRegistry("github", "gitlab")

	// Pre-insert a user: username='TestUser', provider='github', provider_id='ext-100'
	insertTestUser(t, db, "existing-uuid-1", "TestUser", "existing@example.com", "Existing",
		"active", "github", "ext-100", "2025-01-01T00:00:00Z")

	e := setupEcho()
	handler := users.CreateUserHandler(db, registry)
	e.POST("/api/v1/users", handler, setAuthContext(adminAuthContext()))

	t.Run("case_insensitive_username_conflict", func(t *testing.T) {
		body := `{"username":"testuser","email":"e@e.com","provider":"gitlab","provider_id":"ext-200"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusConflict {
			t.Errorf("expected HTTP 409 for case-insensitive username conflict, got %d: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("duplicate_provider_provider_id", func(t *testing.T) {
		body := `{"username":"differentuser","email":"e@e.com","provider":"github","provider_id":"ext-100"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusConflict {
			t.Errorf("expected HTTP 409 for duplicate (provider, provider_id), got %d: %s",
				rec.Code, rec.Body.String())
		}
	})
}

// ---------------------------------------------------------------------------
// TS-02-E14: Admin POST /api/v1/users returns HTTP 400 for invalid username
// or empty provider_id; no user record created.
// Requirement: 02-REQ-4.E1
// ---------------------------------------------------------------------------

func TestCreateUser_EdgeCase_InvalidInput(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)
	registry := newStubProviderRegistry("github")

	e := setupEcho()
	handler := users.CreateUserHandler(db, registry)
	e.POST("/api/v1/users", handler, setAuthContext(adminAuthContext()))

	t.Run("invalid_username_no_row_inserted", func(t *testing.T) {
		body := `{"username":"bad user!","email":"e@e.com","provider":"github","provider_id":"p1"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected HTTP 400, got %d", rec.Code)
		}

		var count int
		_ = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
		if count != 0 {
			t.Errorf("expected 0 users after invalid request, got %d", count)
		}
	})

	t.Run("empty_provider_id_no_row_inserted", func(t *testing.T) {
		body := `{"username":"validname","email":"e@e.com","provider":"github","provider_id":""}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected HTTP 400, got %d", rec.Code)
		}

		var count int
		_ = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
		if count != 0 {
			t.Errorf("expected 0 users after invalid request, got %d", count)
		}
	})
}

// ---------------------------------------------------------------------------
// TS-02-E15: Admin POST /api/v1/users returns HTTP 409 for case-insensitive
// username conflict or duplicate (provider, provider_id).
// Requirement: 02-REQ-4.E2
// ---------------------------------------------------------------------------

func TestCreateUser_EdgeCase_Conflicts(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)
	registry := newStubProviderRegistry("github", "gitlab")

	// Pre-insert: username='Bob', provider='github', provider_id='ext-200'
	insertTestUser(t, db, "existing-uuid-2", "Bob", "bob@example.com", "Bob",
		"active", "github", "ext-200", "2025-01-01T00:00:00Z")

	e := setupEcho()
	handler := users.CreateUserHandler(db, registry)
	e.POST("/api/v1/users", handler, setAuthContext(adminAuthContext()))

	userCountBefore := 0
	_ = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCountBefore)

	t.Run("username_case_conflict", func(t *testing.T) {
		body := `{"username":"bob","email":"e@e.com","provider":"gitlab","provider_id":"ext-300"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusConflict {
			t.Errorf("expected HTTP 409, got %d: %s", rec.Code, rec.Body.String())
		}

		var count int
		_ = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
		if count != userCountBefore {
			t.Errorf("expected user count unchanged (%d), got %d", userCountBefore, count)
		}
	})

	t.Run("provider_provider_id_conflict", func(t *testing.T) {
		body := `{"username":"Unique","email":"e@e.com","provider":"github","provider_id":"ext-200"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusConflict {
			t.Errorf("expected HTTP 409, got %d: %s", rec.Code, rec.Body.String())
		}

		var count int
		_ = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
		if count != userCountBefore {
			t.Errorf("expected user count unchanged (%d), got %d", userCountBefore, count)
		}
	})
}

// ---------------------------------------------------------------------------
// TS-02-E16: POST /api/v1/users returns HTTP 403 when called by a non-admin
// authenticated user.
// Requirement: 02-REQ-4.E3
// ---------------------------------------------------------------------------

func TestCreateUser_EdgeCase_NonAdminForbidden(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)
	registry := newStubProviderRegistry("github")

	// Insert a regular user who will try to call the admin endpoint.
	insertTestUser(t, db, "user-regular-1", "regularuser", "reg@example.com", "Regular",
		"active", "github", "reg-001", "2025-01-01T00:00:00Z")

	e := setupEcho()
	handler := users.CreateUserHandler(db, registry)
	e.POST("/api/v1/users", handler, setAuthContext(userAuthContext("user-regular-1")))

	body := `{"username":"newuser","email":"e@e.com","provider":"github","provider_id":"p1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected HTTP 403 for non-admin, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify no user was created
	var count int
	_ = db.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", "newuser").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 users named 'newuser' after forbidden request, got %d", count)
	}
}
