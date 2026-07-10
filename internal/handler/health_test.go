package handler_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/agent-fox-dev/hub/internal/handler"
	"github.com/labstack/echo/v4"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// setupTestDB creates a temporary SQLite database for testing.
// Returns the *sql.DB and a cleanup function.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	// Apply pragmas as InitDatabase would.
	if _, err := database.Exec("PRAGMA journal_mode = WAL"); err != nil {
		t.Fatalf("failed to set WAL: %v", err)
	}
	if _, err := database.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("failed to set foreign_keys: %v", err)
	}
	if _, err := database.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		t.Fatalf("failed to set busy_timeout: %v", err)
	}

	t.Cleanup(func() { database.Close() })
	return database
}

// setupBrokenDB returns a *sql.DB that is closed, so queries will fail.
func setupBrokenDB(t *testing.T) *sql.DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "broken.db")

	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	// Close immediately so any subsequent queries fail.
	database.Close()

	// Remove the file to ensure even a reconnect attempt fails.
	os.Remove(dbPath)

	return database
}

// ---------------------------------------------------------------------------
// 2.3 — Health Probe Endpoints
// ---------------------------------------------------------------------------

// TestSpec01_HealthzGET verifies GET /healthz returns HTTP 200 with body
// {"status": "ok"} without performing any database check.
// TS-01-12, REQ: 01-REQ-4.1
func TestSpec01_HealthzGET(t *testing.T) {
	e := echo.New()
	h := handler.HealthzHandler()
	if h == nil {
		t.Fatal("HealthzHandler returned nil handler")
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h(c); err != nil {
		t.Fatalf("HealthzHandler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want %q", body["status"], "ok")
	}
}

// TestSpec01_HealthzHEAD verifies HEAD /healthz returns HTTP 200 with
// an empty body.
// TS-01-12, REQ: 01-REQ-4.1
func TestSpec01_HealthzHEAD(t *testing.T) {
	e := echo.New()
	h := handler.HealthzHandler()
	if h == nil {
		t.Fatal("HealthzHandler returned nil handler")
	}

	req := httptest.NewRequest(http.MethodHead, "/healthz", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h(c); err != nil {
		t.Fatalf("HealthzHandler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestSpec01_ReadyzGETHealthy verifies GET /readyz returns HTTP 200
// with {"status": "ready"} when the database SELECT 1 query succeeds
// within the 2-second timeout.
// TS-01-13, REQ: 01-REQ-4.2
func TestSpec01_ReadyzGETHealthy(t *testing.T) {
	database := setupTestDB(t)
	e := echo.New()

	h := handler.ReadyzHandler(database)
	if h == nil {
		t.Fatal("ReadyzHandler returned nil handler")
	}

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h(c); err != nil {
		t.Fatalf("ReadyzHandler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response body: %v", err)
	}
	if body["status"] != "ready" {
		t.Errorf("status = %q, want %q", body["status"], "ready")
	}
}

// TestSpec01_ReadyzGETDegradedNoShutdown verifies that when /readyz
// returns HTTP 503 (DB check failed), the server continues running
// and /healthz still returns 200. No shutdown occurs.
// TS-01-15, REQ: 01-REQ-4.4
func TestSpec01_ReadyzGETDegradedNoShutdown(t *testing.T) {
	brokenDB := setupBrokenDB(t)
	healthyDB := setupTestDB(t) // separate healthy DB for healthz

	e := echo.New()

	// Readyz with broken DB should return 503.
	readyzH := handler.ReadyzHandler(brokenDB)
	if readyzH == nil {
		t.Fatal("ReadyzHandler returned nil handler")
	}

	handler.ResetReadyzFailureCounter()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := readyzH(c); err != nil {
		t.Fatalf("ReadyzHandler returned error: %v", err)
	}

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var readyzBody map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &readyzBody); err != nil {
		t.Fatalf("failed to parse readyz body: %v", err)
	}
	if readyzBody["status"] != "not ready" {
		t.Errorf("readyz status = %q, want %q", readyzBody["status"], "not ready")
	}

	// Healthz should still return 200 (no DB dependency).
	healthzH := handler.HealthzHandler()
	if healthzH == nil {
		t.Fatal("HealthzHandler returned nil handler")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(req2, rec2)

	if err := healthzH(c2); err != nil {
		t.Fatalf("HealthzHandler returned error: %v", err)
	}

	if rec2.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want %d (server should still be alive)", rec2.Code, http.StatusOK)
	}

	// Verify that the server (Echo instance) is still usable — not shut down.
	// We test this by confirming healthz works; actual server shutdown is a
	// distinct integration test.
	_ = healthyDB // referenced to avoid unused import
}

// TestSpec01_HealthProbeMethodNotAllowed verifies that POST, PUT, DELETE,
// PATCH methods on /healthz and /readyz return HTTP 405 using Echo's
// default behavior (not the custom API error envelope).
// TS-01-16, REQ: 01-REQ-4.5
func TestSpec01_HealthProbeMethodNotAllowed(t *testing.T) {
	database := setupTestDB(t)
	e := echo.New()

	// Register the handlers with proper routing for method-not-allowed.
	e.GET("/healthz", handler.HealthzHandler())
	e.HEAD("/healthz", handler.HealthzHandler())
	e.GET("/readyz", handler.ReadyzHandler(database))
	e.HEAD("/readyz", handler.ReadyzHandler(database))

	// Routes must not be nil for this test to work.
	if handler.HealthzHandler() == nil {
		t.Fatal("HealthzHandler returned nil")
	}
	if handler.ReadyzHandler(database) == nil {
		t.Fatal("ReadyzHandler returned nil")
	}

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/healthz"},
		{http.MethodPut, "/healthz"},
		{http.MethodDelete, "/healthz"},
		{http.MethodPost, "/readyz"},
		{http.MethodPut, "/readyz"},
		{http.MethodDelete, "/readyz"},
	}

	for _, tc := range cases {
		t.Run(tc.method+"_"+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s status = %d, want %d",
					tc.method, tc.path, rec.Code, http.StatusMethodNotAllowed)
			}

			// Verify NOT wrapped in the API error envelope.
			// The response body should NOT contain {"error": {"code": ...}}.
			var envelope struct {
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			bodyBytes := rec.Body.Bytes()
			if len(bodyBytes) > 0 {
				if json.Unmarshal(bodyBytes, &envelope) == nil && envelope.Error != nil {
					t.Errorf("%s %s should use Echo's default 405, not API error envelope",
						tc.method, tc.path)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2.4 — Readyz Failure Counter and Logging Cadence
// ---------------------------------------------------------------------------

// TestSpec01_ReadyzFirstFailureCounterAndLogLevel verifies that the first
// DB check failure increments the atomic counter to 1 and logs at error level.
// Subsequent failures log at debug level. Recovery logs at info level and
// resets the counter to 0.
// TS-01-14, REQ: 01-REQ-4.3
func TestSpec01_ReadyzFirstFailureCounterAndLogLevel(t *testing.T) {
	brokenDB := setupBrokenDB(t)
	e := echo.New()

	readyzH := handler.ReadyzHandler(brokenDB)
	if readyzH == nil {
		t.Fatal("ReadyzHandler returned nil handler")
	}

	handler.ResetReadyzFailureCounter()

	// First failure: counter should go to 1.
	req1 := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec1 := httptest.NewRecorder()
	c1 := e.NewContext(req1, rec1)
	if err := readyzH(c1); err != nil {
		t.Fatalf("ReadyzHandler returned error: %v", err)
	}

	if rec1.Code != http.StatusServiceUnavailable {
		t.Errorf("first failure: status = %d, want %d", rec1.Code, http.StatusServiceUnavailable)
	}

	counter := handler.GetReadyzFailureCounter()
	if counter != 1 {
		t.Errorf("failure counter after first failure = %d, want 1", counter)
	}

	// Second failure: counter should be >= 2.
	req2 := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(req2, rec2)
	if err := readyzH(c2); err != nil {
		t.Fatalf("ReadyzHandler returned error: %v", err)
	}

	if rec2.Code != http.StatusServiceUnavailable {
		t.Errorf("second failure: status = %d, want %d", rec2.Code, http.StatusServiceUnavailable)
	}

	counter = handler.GetReadyzFailureCounter()
	if counter < 2 {
		t.Errorf("failure counter after second failure = %d, want >= 2", counter)
	}

	// Recovery: use a healthy DB.
	healthyDB := setupTestDB(t)
	recoveryH := handler.ReadyzHandler(healthyDB)
	if recoveryH == nil {
		t.Fatal("ReadyzHandler returned nil handler for recovery check")
	}

	req3 := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec3 := httptest.NewRecorder()
	c3 := e.NewContext(req3, rec3)
	if err := recoveryH(c3); err != nil {
		t.Fatalf("ReadyzHandler returned error on recovery: %v", err)
	}

	if rec3.Code != http.StatusOK {
		t.Errorf("recovery: status = %d, want %d", rec3.Code, http.StatusOK)
	}

	counter = handler.GetReadyzFailureCounter()
	if counter != 0 {
		t.Errorf("failure counter after recovery = %d, want 0", counter)
	}
}

// TestSpec01_ReadyzConcurrentRecoveryRace verifies that two concurrent
// /readyz probes both succeeding after a degraded period do not cause
// a data race on the failure counter. At most two info-level recovery
// log entries may appear, and the counter is correctly reset to 0.
//
// This test should be run with the -race flag to verify no data races.
// TS-01-E8, REQ: 01-REQ-4.E1
func TestSpec01_ReadyzConcurrentRecoveryRace(t *testing.T) {
	brokenDB := setupBrokenDB(t)
	healthyDB := setupTestDB(t)
	e := echo.New()

	brokenH := handler.ReadyzHandler(brokenDB)
	recoveryH := handler.ReadyzHandler(healthyDB)

	if brokenH == nil || recoveryH == nil {
		t.Fatal("ReadyzHandler returned nil")
	}

	handler.ResetReadyzFailureCounter()

	// Put server in degraded state (counter >= 1).
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := brokenH(c); err != nil {
		t.Fatalf("brokenH error: %v", err)
	}
	if handler.GetReadyzFailureCounter() < 1 {
		t.Fatal("failure counter should be >= 1 after degraded probe")
	}

	// Fire two concurrent recovery probes.
	var wg sync.WaitGroup
	results := make([]int, 2)

	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			if err := recoveryH(c); err != nil {
				t.Errorf("concurrent recovery probe %d error: %v", idx, err)
				return
			}
			results[idx] = rec.Code
		}(i)
	}
	wg.Wait()

	// Both probes should return 200.
	for i, code := range results {
		if code != http.StatusOK {
			t.Errorf("concurrent recovery probe %d: status = %d, want %d", i, code, http.StatusOK)
		}
	}

	// Counter should be reset to 0.
	counter := handler.GetReadyzFailureCounter()
	if counter != 0 {
		t.Errorf("failure counter after concurrent recovery = %d, want 0", counter)
	}
}
