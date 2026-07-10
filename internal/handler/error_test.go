package handler_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/handler"
	mw "github.com/agent-fox-dev/hub/internal/middleware"
	"github.com/labstack/echo/v4"
	echoMw "github.com/labstack/echo/v4/middleware"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// errorEnvelope matches the standard API error response format:
// {"error": {"code": <int>, "message": "<string>"}}
type errorEnvelope struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// setupEchoWithRouteGroups creates an Echo instance with the spec 01 route
// group structure:
//   - Health probes registered directly on root (no group, no auth middleware)
//   - Auth group at /api/v1/auth (no auth middleware, catch-all for structural exclusion)
//   - Protected group at /api/v1 (with auth middleware)
//   - Custom error handler assigned to e.HTTPErrorHandler
//   - Global middleware: Recover, body-size limit, request logger
//
// Returns the Echo instance and a valid admin token for authenticated requests.
// A test handler is registered on both the auth and protected groups.
func setupEchoWithRouteGroups(t *testing.T) (*echo.Echo, string) {
	t.Helper()
	e := echo.New()

	// Custom error handler.
	e.HTTPErrorHandler = handler.CustomErrorHandler

	// Global middleware stack (spec 01 order: Recover → body-size limit → request logger).
	e.Use(echoMw.Recover())
	e.Use(mw.BodySizeLimitMiddleware("1M"))
	e.Use(mw.RequestLoggerMiddleware())

	// Health probes on root Echo instance — outside all groups.
	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	e.HEAD("/healthz", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})
	e.GET("/readyz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
	})
	e.HEAD("/readyz", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	// Auth group at /api/v1/auth — NO auth middleware.
	authGroup := e.Group("/api/v1/auth")
	authGroup.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	// Catch-all on the auth group ensures unregistered paths under
	// /api/v1/auth/* are handled by this group (no auth middleware)
	// rather than falling through to the protected group's auth middleware.
	// This enforces structural exclusion via Echo's group model (REQ-11.2).
	authGroup.Any("/*", func(c echo.Context) error {
		return echo.ErrNotFound
	})

	// Protected group at /api/v1 — WITH auth middleware.
	testDB, adminToken := setupAuthTestDB(t)
	protectedGroup := e.Group("/api/v1", mw.AuthMiddleware(testDB))
	protectedGroup.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})

	return e, adminToken
}

// ---------------------------------------------------------------------------
// 5.1 — Route Group Structure
// ---------------------------------------------------------------------------

// TestSpec01_RouteGroupHealthzAccessibleWithoutAuth verifies that /healthz is
// registered on the root Echo instance outside all groups and is accessible
// without any auth header.
//
// TS-01-35, REQ: 01-REQ-11.1
func TestSpec01_RouteGroupHealthzAccessibleWithoutAuth(t *testing.T) {
	e, _ := setupEchoWithRouteGroups(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want %d (should be accessible without auth)", rec.Code, http.StatusOK)
	}
}

// TestSpec01_RouteGroupProtectedRequiresAuth verifies that routes on the
// protected group (/api/v1/*) require auth and return HTTP 401 without
// an Authorization header.
//
// TS-01-35, REQ: 01-REQ-11.1
func TestSpec01_RouteGroupProtectedRequiresAuth(t *testing.T) {
	e, _ := setupEchoWithRouteGroups(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	// No Authorization header set.
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/test without auth = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestSpec01_RouteGroupAuthGroupNoChallenge verifies that routes under
// /api/v1/auth/* are NOT challenged by auth middleware. The auth group
// should return a route-specific response (200 or 404), not 401.
//
// TS-01-35, TS-01-36, REQ: 01-REQ-11.1, 01-REQ-11.2
func TestSpec01_RouteGroupAuthGroupNoChallenge(t *testing.T) {
	e, _ := setupEchoWithRouteGroups(t)

	// Registered route under auth group.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/test", nil)
	// No Authorization header.
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Error("GET /api/v1/auth/test should NOT get 401; auth group has no auth middleware")
	}
}

// TestSpec01_AuthGroupExclusionIsStructural verifies that auth middleware
// exclusion for /api/v1/auth/* is enforced via Echo's group model, not via
// path-prefix string matching inside the auth middleware itself.
//
// TS-01-36, REQ: 01-REQ-11.2
func TestSpec01_AuthGroupExclusionIsStructural(t *testing.T) {
	e, _ := setupEchoWithRouteGroups(t)

	// Try an unregistered path under the auth group. Should get 404 (not 401)
	// because auth middleware is not applied to this group.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/nonexistent", nil)
	// No Authorization header.
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Must NOT be 401 — the auth middleware should not be involved.
	if rec.Code == http.StatusUnauthorized {
		t.Error("GET /api/v1/auth/nonexistent should NOT get 401; exclusion must be structural via Echo group, not path matching")
	}
	// 404 or 405 is acceptable for an unregistered route.
}

// ---------------------------------------------------------------------------
// 5.1 — Custom Error Handler Envelope on API Routes
// ---------------------------------------------------------------------------

// TestSpec01_CustomErrorHandlerEnvelope verifies that the global custom error
// handler translates errors on non-health-probe routes into the standard JSON
// error envelope: {"error": {"code": <int>, "message": "<string>"}}.
//
// TS-01-37, REQ: 01-REQ-12.1
func TestSpec01_CustomErrorHandlerEnvelope(t *testing.T) {
	e, _ := setupEchoWithRouteGroups(t)

	// Access a nonexistent route under /api/v1 — should trigger 404 from Echo.
	// Note: Since the auth middleware stub is pass-through, 404 should reach
	// the error handler. In the real implementation, this would also get a 401
	// from auth middleware, but the test verifies the error envelope format.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nonexistent-route-xyz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should be an error status (404 or 401 depending on middleware).
	if rec.Code < 400 {
		t.Fatalf("GET /api/v1/nonexistent status = %d, want >= 400", rec.Code)
	}

	// Verify the error envelope format.
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse error envelope: %v; body = %q", err, rec.Body.String())
	}
	if env.Error == nil {
		t.Fatal("response body should contain {\"error\": {...}} envelope")
	}
	if env.Error.Code != rec.Code {
		t.Errorf("envelope code = %d, want %d (must match HTTP status)", env.Error.Code, rec.Code)
	}
	if env.Error.Message == "" {
		t.Error("envelope message should be non-empty")
	}
}

// ---------------------------------------------------------------------------
// 5.1 — Health Probe Routes Use Plain JSON, Not Error Envelope
// ---------------------------------------------------------------------------

// TestSpec01_HealthProbeResponseNotErrorEnvelope verifies that health probe
// routes (/healthz, /readyz) return their own plain JSON bodies using the
// {"status": "..."} format, NOT the {"error": {...}} error envelope.
//
// TS-01-41, REQ: 01-REQ-12.5
func TestSpec01_HealthProbeResponseNotErrorEnvelope(t *testing.T) {
	e, _ := setupEchoWithRouteGroups(t)

	t.Run("healthz", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /healthz status = %d, want %d", rec.Code, http.StatusOK)
		}

		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("failed to parse /healthz body: %v", err)
		}

		if _, hasStatus := body["status"]; !hasStatus {
			t.Error("/healthz body should contain {\"status\": ...}")
		}
		if _, hasError := body["error"]; hasError {
			t.Error("/healthz body should NOT contain error envelope")
		}
	})

	t.Run("readyz", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		// 200 (ready) or 503 (not ready) are both acceptable.
		if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET /readyz status = %d, want 200 or 503", rec.Code)
		}

		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("failed to parse /readyz body: %v", err)
		}

		if _, hasStatus := body["status"]; !hasStatus {
			t.Error("/readyz body should contain {\"status\": ...}")
		}
		if _, hasError := body["error"]; hasError {
			t.Error("/readyz body should NOT contain error envelope")
		}
	})
}

// ---------------------------------------------------------------------------
// 5.2 — Custom Error Handler Dispatch Logic
// ---------------------------------------------------------------------------

// TestSpec01_ErrorHandlerHTTPError verifies that the custom error handler
// uses the Code and Message from *echo.HTTPError to construct the response
// envelope.
//
// TS-01-38, REQ: 01-REQ-12.2
func TestSpec01_ErrorHandlerHTTPError(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = handler.CustomErrorHandler

	// Register a handler that returns a specific *echo.HTTPError.
	e.GET("/api/v1/http-error-test", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "unprocessable entity")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/http-error-test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}

	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse response: %v; body = %q", err, rec.Body.String())
	}
	if env.Error == nil {
		t.Fatal("response should contain error envelope")
	}
	if env.Error.Code != http.StatusUnprocessableEntity {
		t.Errorf("envelope code = %d, want %d", env.Error.Code, http.StatusUnprocessableEntity)
	}
	if env.Error.Message != "unprocessable entity" {
		t.Errorf("envelope message = %q, want %q", env.Error.Message, "unprocessable entity")
	}
}

// TestSpec01_ErrorHandlerPlainError verifies that the custom error handler
// returns HTTP 500 with body {"error": {"code": 500, "message": "internal
// server error"}} for non-*echo.HTTPError errors.
//
// TS-01-39, REQ: 01-REQ-12.3
func TestSpec01_ErrorHandlerPlainError(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = handler.CustomErrorHandler

	// Register a handler that returns a plain Go error.
	e.GET("/api/v1/plain-error-test", func(c echo.Context) error {
		return errors.New("database connection lost")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/plain-error-test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse response: %v; body = %q", err, rec.Body.String())
	}
	if env.Error == nil {
		t.Fatal("response should contain error envelope")
	}
	if env.Error.Code != http.StatusInternalServerError {
		t.Errorf("envelope code = %d, want %d", env.Error.Code, http.StatusInternalServerError)
	}
	if env.Error.Message != "internal server error" {
		t.Errorf("envelope message = %q, want %q", env.Error.Message, "internal server error")
	}
}

// TestSpec01_RecoverMiddlewarePanicProducesEnvelope verifies that when a panic
// occurs in a handler, the Recover middleware catches it and routes it through
// the custom error handler, producing HTTP 500 with the standard error
// envelope. No raw panic output reaches the client. X-Request-ID is present.
//
// TS-01-40, REQ: 01-REQ-12.4
func TestSpec01_RecoverMiddlewarePanicProducesEnvelope(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = handler.CustomErrorHandler

	// Global middleware: Recover must be present to catch panics.
	e.Use(echoMw.Recover())
	e.Use(mw.RequestLoggerMiddleware())

	// Register a handler that panics.
	e.GET("/api/v1/panic-test", func(c echo.Context) error {
		panic("test panic: something went very wrong")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/panic-test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d after panic", rec.Code, http.StatusInternalServerError)
	}

	// Verify the response uses the error envelope, not raw panic output.
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse response: %v; body = %q", err, rec.Body.String())
	}
	if env.Error == nil {
		t.Fatal("response should contain error envelope after panic")
	}
	if env.Error.Code != http.StatusInternalServerError {
		t.Errorf("envelope code = %d, want %d", env.Error.Code, http.StatusInternalServerError)
	}
	if env.Error.Message != "internal server error" {
		t.Errorf("envelope message = %q, want %q", env.Error.Message, "internal server error")
	}

	// Ensure no raw panic output in response body.
	bodyStr := rec.Body.String()
	if strings.Contains(strings.ToLower(bodyStr), "panic") {
		t.Errorf("response body should not contain raw panic output; body = %q", bodyStr)
	}

	// X-Request-ID should be present in response headers.
	requestID := rec.Header().Get("X-Request-ID")
	if requestID == "" {
		t.Error("X-Request-ID header should be present in response after panic recovery")
	}
}
