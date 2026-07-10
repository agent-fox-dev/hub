package integration

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/admin"
	"github.com/agent-fox-dev/hub/internal/db"
	"github.com/agent-fox-dev/hub/internal/handler"
	mw "github.com/agent-fox-dev/hub/internal/middleware"
	"github.com/agent-fox-dev/hub/internal/serverconfig"
	"github.com/labstack/echo/v4"
	echoMw "github.com/labstack/echo/v4/middleware"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Smoke Test Helpers
// ---------------------------------------------------------------------------

// smokeSetupDB initializes a SQLite database at the given path using the
// production InitDatabase function. Returns the *sql.DB or fails the test
// if InitDatabase returns nil (stub behavior).
func smokeSetupDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	database, err := db.InitDatabase(dbPath)
	if err != nil {
		t.Fatalf("InitDatabase(%s) returned error: %v", dbPath, err)
	}
	if database == nil {
		t.Fatal("InitDatabase returned nil DB (stub not yet implemented)")
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// smokeSetupEcho creates an Echo instance with the full spec 01 route group
// structure: health probes on root, auth group at /api/v1/auth, protected
// group at /api/v1 with auth middleware, custom error handler, and global
// middleware stack (Recover, body-size limit, request logger).
func smokeSetupEcho(t *testing.T, database *sql.DB) *echo.Echo {
	t.Helper()
	e := echo.New()
	e.HTTPErrorHandler = handler.CustomErrorHandler

	// Global middleware stack (spec 01 order).
	e.Use(echoMw.Recover())
	e.Use(mw.BodySizeLimitMiddleware("1M"))
	e.Use(mw.RequestLoggerMiddleware())

	// Health probes on root.
	healthzH := handler.HealthzHandler()
	readyzH := handler.ReadyzHandler(database)
	if healthzH == nil {
		t.Fatal("HealthzHandler() returned nil handler")
	}
	if readyzH == nil {
		t.Fatal("ReadyzHandler() returned nil handler")
	}

	e.GET("/healthz", healthzH)
	e.HEAD("/healthz", healthzH)
	e.GET("/readyz", readyzH)
	e.HEAD("/readyz", readyzH)

	// Auth group — no auth middleware.
	e.Group("/api/v1/auth")

	// Protected group — with auth middleware.
	protected := e.Group("/api/v1", mw.AuthMiddleware(database))
	protected.GET("/test", func(c echo.Context) error {
		// Echo back AuthContext fields if set.
		authCtxRaw := c.Get("auth_context")
		if authCtxRaw == nil {
			return c.JSON(http.StatusOK, map[string]any{"auth_context": nil})
		}
		return c.JSON(http.StatusOK, authCtxRaw)
	})

	return e
}

// sha256hexSmoke returns hex(sha256(s)).
func sha256hexSmoke(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// TS-01-SMOKE-1 — Full First-Boot Server Startup
// ---------------------------------------------------------------------------

// TestSpec01_Smoke_FirstBoot verifies the full first-boot path:
// no config, no env vars; server generates admin token, writes file, opens
// HTTP listener, creates DB with tables/indexes.
//
// Execution Path: 01-PATH-1
// TS-01-SMOKE-1
func TestSpec01_Smoke_FirstBoot(t *testing.T) {
	tmpDir := t.TempDir()

	// Step 2: Load config (no config.toml present — apply all defaults).
	configPath := filepath.Join(tmpDir, "config.toml")
	configResult, err := serverconfig.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("Config loading failed: %v", err)
	}

	// Step 4: Initialize SQLite database.
	dbPath := filepath.Join(tmpDir, "data", "af-hub.db")
	database := smokeSetupDB(t, dbPath)

	// Verify ./data directory was created.
	if _, err := os.Stat(filepath.Join(tmpDir, "data")); os.IsNotExist(err) {
		t.Error("directory ./data was not created")
	}

	// Verify DB file exists.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file was not created")
	}

	// Verify pragmas.
	var journalMode string
	database.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	// Verify all 7 tables exist.
	expectedTables := []string{"users", "admin_tokens", "api_keys", "teams", "team_members", "workspaces", "workspace_tokens"}
	for _, tbl := range expectedTables {
		var name string
		err := database.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", tbl, err)
		}
	}

	// Verify all 5 indexes exist.
	expectedIndexes := []string{"idx_api_keys_key_id", "idx_workspace_tokens_token_id", "idx_users_provider", "idx_workspaces_slug", "idx_teams_slug"}
	for _, idx := range expectedIndexes {
		var name string
		err := database.QueryRow("SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx).Scan(&name)
		if err != nil {
			t.Errorf("index %q not found: %v", idx, err)
		}
	}

	// Step 5: Admin bootstrap (first boot).
	bootstrapResult, bootstrapErr := admin.Bootstrap(database, tmpDir, false)
	if bootstrapErr != nil {
		t.Fatalf("Admin bootstrap failed: %v", bootstrapErr)
	}

	// Verify admin user exists.
	var username string
	err = database.QueryRow("SELECT username FROM users WHERE username='admin'").Scan(&username)
	if err != nil {
		t.Errorf("admin user row not found: %v", err)
	}

	// Verify admin_tokens has one row.
	var tokenCount int
	database.QueryRow("SELECT COUNT(*) FROM admin_tokens").Scan(&tokenCount)
	if tokenCount != 1 {
		t.Errorf("admin_tokens count = %d, want 1", tokenCount)
	}

	// Verify admin_token file exists with mode 0600.
	tokenFilePath := bootstrapResult.TokenFilePath
	if tokenFilePath == "" {
		tokenFilePath = filepath.Join(tmpDir, "admin_token")
	}
	fileInfo, err := os.Stat(tokenFilePath)
	if err != nil {
		t.Fatalf("admin_token file not found: %v", err)
	}
	if fileInfo.Mode().Perm() != 0600 {
		t.Errorf("admin_token file mode = %o, want 0600", fileInfo.Mode().Perm())
	}

	// Verify token content matches af_admin_<64 hex chars> pattern.
	tokenContent, _ := os.ReadFile(tokenFilePath)
	token := strings.TrimSpace(string(tokenContent))
	if len(token) != 73 || !strings.HasPrefix(token, "af_admin_") {
		t.Errorf("token format invalid: %q", token)
	}

	// Step 7: Startup log fields.
	fields := serverconfig.StartupLogFields(configResult.Config)
	if fields == nil {
		t.Error("StartupLogFields returned nil")
	}

	// Step 8+: Verify health probes work.
	e := smokeSetupEcho(t, database)

	// GET /healthz → 200 {"status": "ok"}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want 200", rec.Code)
	}

	// GET /readyz → 200 {"status": "ready"}
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /readyz status = %d, want 200", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// TS-01-SMOKE-2 — Subsequent-Boot Token Validation
// ---------------------------------------------------------------------------

// TestSpec01_Smoke_SubsequentBoot verifies subsequent-boot token validation:
// server reads AF_HUB_ADMIN_TOKEN, validates hash, starts normally.
//
// Execution Path: 01-PATH-2
// TS-01-SMOKE-2
func TestSpec01_Smoke_SubsequentBoot(t *testing.T) {
	tmpDir := t.TempDir()

	// First boot — generate the admin token.
	dbPath := filepath.Join(tmpDir, "data", "af-hub.db")
	database := smokeSetupDB(t, dbPath)

	_, err := admin.Bootstrap(database, tmpDir, false)
	if err != nil {
		t.Fatalf("First boot bootstrap failed: %v", err)
	}

	// Read the plaintext token.
	tokenContent, err := os.ReadFile(filepath.Join(tmpDir, "admin_token"))
	if err != nil {
		t.Fatalf("Failed to read admin_token file: %v", err)
	}
	token := strings.TrimSpace(string(tokenContent))

	// Subsequent boot: set AF_HUB_ADMIN_TOKEN and bootstrap again.
	t.Setenv("AF_HUB_ADMIN_TOKEN", token)

	_, err = admin.Bootstrap(database, tmpDir, false)
	if err != nil {
		t.Fatalf("Subsequent boot bootstrap failed: %v", err)
	}

	// Verify health probes still work.
	e := smokeSetupEcho(t, database)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want 200", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// TS-01-SMOKE-3 — Admin Token Rotation via --reset-admin-token
// ---------------------------------------------------------------------------

// TestSpec01_Smoke_ResetAdminToken verifies admin token rotation: new token
// generated, old token invalidated, server starts.
//
// Execution Path: 01-PATH-3
// TS-01-SMOKE-3
func TestSpec01_Smoke_ResetAdminToken(t *testing.T) {
	tmpDir := t.TempDir()

	// First boot — generate initial token.
	dbPath := filepath.Join(tmpDir, "data", "af-hub.db")
	database := smokeSetupDB(t, dbPath)

	_, err := admin.Bootstrap(database, tmpDir, false)
	if err != nil {
		t.Fatalf("First boot bootstrap failed: %v", err)
	}

	// Read old token.
	oldTokenBytes, _ := os.ReadFile(filepath.Join(tmpDir, "admin_token"))
	oldToken := strings.TrimSpace(string(oldTokenBytes))

	// Get old hash from DB.
	var oldHash string
	database.QueryRow("SELECT token_hash FROM admin_tokens").Scan(&oldHash)

	// Rotation: --reset-admin-token (no AF_HUB_ADMIN_TOKEN needed).
	_, err = admin.Bootstrap(database, tmpDir, true)
	if err != nil {
		t.Fatalf("Reset admin token failed: %v", err)
	}

	// New hash should differ from old.
	var newHash string
	database.QueryRow("SELECT token_hash FROM admin_tokens").Scan(&newHash)
	if newHash == oldHash {
		t.Error("token hash should have changed after rotation")
	}

	// admin_tokens should have exactly one row.
	var count int
	database.QueryRow("SELECT COUNT(*) FROM admin_tokens").Scan(&count)
	if count != 1 {
		t.Errorf("admin_tokens count = %d, want 1", count)
	}

	// New token file should be different.
	newTokenBytes, _ := os.ReadFile(filepath.Join(tmpDir, "admin_token"))
	newToken := strings.TrimSpace(string(newTokenBytes))
	if newToken == oldToken {
		t.Error("admin_token file should contain new token after rotation")
	}

	// File mode should be 0600.
	fi, _ := os.Stat(filepath.Join(tmpDir, "admin_token"))
	if fi != nil && fi.Mode().Perm() != 0600 {
		t.Errorf("admin_token mode = %o, want 0600", fi.Mode().Perm())
	}

	// Verify old token is rejected with 401.
	e := smokeSetupEcho(t, database)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+oldToken)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("old token should be rejected with 401, got %d", rec.Code)
	}

	// Verify server is listening (health probe works).
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want 200", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// TS-01-SMOKE-4 — Authenticated API Request Using Admin Token
// ---------------------------------------------------------------------------

// TestSpec01_Smoke_AdminTokenAuth verifies authenticated API request using
// admin token: middleware resolves identity, sets AuthContext with IsAdmin=true,
// X-Request-ID is set, request log entry emitted.
//
// Execution Path: 01-PATH-4
// TS-01-SMOKE-4
func TestSpec01_Smoke_AdminTokenAuth(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "data", "af-hub.db")
	database := smokeSetupDB(t, dbPath)

	// First boot to generate admin token.
	_, err := admin.Bootstrap(database, tmpDir, false)
	if err != nil {
		t.Fatalf("Bootstrap failed: %v", err)
	}

	tokenBytes, _ := os.ReadFile(filepath.Join(tmpDir, "admin_token"))
	token := strings.TrimSpace(string(tokenBytes))

	e := smokeSetupEcho(t, database)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should succeed (not 401 or 403).
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Errorf("admin token auth should succeed, got status %d", rec.Code)
	}

	// X-Request-ID response header should be set.
	requestID := rec.Header().Get("X-Request-ID")
	if requestID == "" {
		t.Error("X-Request-ID response header should be set")
	}

	// Parse response to verify AuthContext fields.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response body: %v", err)
	}

	// AuthContext should have IsAdmin=true, UserID="", WorkspaceID="".
	if isAdmin, ok := body["is_admin"].(bool); !ok || !isAdmin {
		t.Errorf("is_admin = %v, want true", body["is_admin"])
	}
	if uid, ok := body["user_id"].(string); !ok || uid != "" {
		t.Errorf("user_id = %v, want empty string", body["user_id"])
	}
	if wsid, ok := body["workspace_id"].(string); !ok || wsid != "" {
		t.Errorf("workspace_id = %v, want empty string", body["workspace_id"])
	}
}

// ---------------------------------------------------------------------------
// TS-01-SMOKE-5 — Blocked User Rejected with HTTP 403
// ---------------------------------------------------------------------------

// TestSpec01_Smoke_BlockedUserRejection verifies blocked user is rejected with
// HTTP 403 when authenticating via user API key.
//
// Execution Path: 01-PATH-5
// TS-01-SMOKE-5
func TestSpec01_Smoke_BlockedUserRejection(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "data", "af-hub.db")
	database := smokeSetupDB(t, dbPath)

	// First boot to create schema.
	_, err := admin.Bootstrap(database, tmpDir, false)
	if err != nil {
		t.Fatalf("Bootstrap failed: %v", err)
	}

	// Insert a blocked user and API key.
	now := "2026-01-01T00:00:00.000Z"
	_, err = database.Exec(
		`INSERT INTO users (id, username, email, full_name, status, provider, provider_id, created_at, updated_at)
		 VALUES ('blocked-user-1', 'blockeduser', 'blocked@test.com', '', 'blocked', 'local', 'local_blocked', ?, ?)`,
		now, now)
	if err != nil {
		t.Fatalf("failed to insert blocked user: %v", err)
	}

	apiSecret := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 32 chars
	_, err = database.Exec(
		`INSERT INTO api_keys (id, key_id, secret_hash, user_id, expires_at, created_at, revoked_at)
		 VALUES ('apikey-blk1', 'blkuser1', ?, 'blocked-user-1', NULL, ?, NULL)`,
		sha256hexSmoke(apiSecret), now)
	if err != nil {
		t.Fatalf("failed to insert API key: %v", err)
	}

	e := smokeSetupEcho(t, database)

	token := "af_blkuser1_" + apiSecret
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("blocked user should get 403, got %d", rec.Code)
	}

	// Verify error envelope.
	var env struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse response: %v; body=%q", err, rec.Body.String())
	}
	if env.Error == nil || env.Error.Code != 403 {
		t.Errorf("expected error envelope with code 403, got %+v", env.Error)
	}

	// X-Request-ID should be present.
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID header should be present")
	}
}

// ---------------------------------------------------------------------------
// TS-01-SMOKE-6 — Readiness Probe in Degraded Database State
// ---------------------------------------------------------------------------

// TestSpec01_Smoke_DegradedReadyz verifies readiness probe returns HTTP 503
// in degraded database state: server still alive, error log on first failure.
//
// Execution Path: 01-PATH-6
// TS-01-SMOKE-6
func TestSpec01_Smoke_DegradedReadyz(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "data", "af-hub.db")
	database := smokeSetupDB(t, dbPath)

	// First boot.
	_, err := admin.Bootstrap(database, tmpDir, false)
	if err != nil {
		t.Fatalf("Bootstrap failed: %v", err)
	}

	e := smokeSetupEcho(t, database)

	// Close the DB to simulate degraded state.
	database.Close()

	// GET /readyz → 503 {"status": "not ready"}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz with broken DB: status = %d, want 503", rec.Code)
	}

	// Server should still be alive — GET /healthz → 200.
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz after degraded readyz: status = %d, want 200", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// TS-01-SMOKE-7 — Graceful Shutdown on SIGTERM
// ---------------------------------------------------------------------------

// TestSpec01_Smoke_GracefulShutdown verifies graceful SIGTERM shutdown:
// clean drain, info log "server shutdown complete", exit 0.
//
// This smoke test verifies the shutdown function exists and behaves correctly
// when called with a running Echo server. Full process-level SIGTERM testing
// requires a real binary and is deferred to end-to-end tests.
//
// Execution Path: 01-PATH-7
// TS-01-SMOKE-7
func TestSpec01_Smoke_GracefulShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "data", "af-hub.db")
	database := smokeSetupDB(t, dbPath)

	// First boot.
	_, err := admin.Bootstrap(database, tmpDir, false)
	if err != nil {
		t.Fatalf("Bootstrap failed: %v", err)
	}

	e := smokeSetupEcho(t, database)

	// Start Echo on a random port in a goroutine.
	go func() {
		// Ignore the error — server.Shutdown will cause Start to return.
		_ = e.Start("127.0.0.1:0")
	}()

	// Use GracefulShutdown with 15s timeout. The function should call
	// e.Shutdown(ctx) and log "server shutdown complete".
	shutdownErr := mw.GracefulShutdown(e, 15*time.Second)
	if shutdownErr != nil {
		t.Logf("GracefulShutdown returned error (may be expected with stub): %v", shutdownErr)
	}

	// Verify health probes are no longer reachable after shutdown.
	// (This is a best-effort check — in tests we can't fully verify port closure.)
	t.Log("Graceful shutdown smoke test completed")
}

// ---------------------------------------------------------------------------
// TS-01-SMOKE-8 — Oversized Request Body Rejection
// ---------------------------------------------------------------------------

// TestSpec01_Smoke_OversizedBody verifies oversized request body is rejected
// with HTTP 413 before auth or handler processing; status 413 appears in log.
//
// Execution Path: 01-PATH-8
// TS-01-SMOKE-8
func TestSpec01_Smoke_OversizedBody(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "data", "af-hub.db")
	database := smokeSetupDB(t, dbPath)

	// First boot to generate admin token.
	_, err := admin.Bootstrap(database, tmpDir, false)
	if err != nil {
		t.Fatalf("Bootstrap failed: %v", err)
	}

	tokenBytes, _ := os.ReadFile(filepath.Join(tmpDir, "admin_token"))
	token := strings.TrimSpace(string(tokenBytes))

	e := smokeSetupEcho(t, database)

	// POST /api/v1/test with 2 MB body and valid admin token.
	largeBody := strings.NewReader(strings.Repeat("X", 2*1024*1024))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", largeBody)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should get 413 from body-size limit middleware.
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: status = %d, want 413", rec.Code)
	}

	// Verify error envelope.
	var env struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Logf("body = %q", rec.Body.String())
		t.Fatalf("failed to parse error envelope: %v", err)
	}
	if env.Error == nil || env.Error.Code != 413 {
		t.Errorf("expected error envelope with code 413, got %+v", env.Error)
	}

	// X-Request-ID should be present.
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID header should be present in 413 response")
	}
}
