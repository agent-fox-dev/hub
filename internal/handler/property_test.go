package handler_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/hub/internal/handler"
	mw "github.com/agent-fox-dev/hub/internal/middleware"
	"github.com/labstack/echo/v4"
	echoMw "github.com/labstack/echo/v4/middleware"
)

// ---------------------------------------------------------------------------
// Property Test TS-01-P7 — Error Envelope Consistency
// ---------------------------------------------------------------------------

// TestSpec01_PropErrorEnvelopeConsistency is a property test that triggers
// 10 different error conditions on /api/v1/* routes and verifies that every
// error response body always matches the envelope format:
//
//	{"error": {"code": <integer>, "message": "<string>"}}
//
// where code equals the HTTP status code, code is an integer, and message
// is a non-empty string.
//
// The 10 conditions tested:
//  1. 401 — missing auth header (requires real auth middleware)
//  2. 404 — route not found under /api/v1/
//  3. 413 — oversized body (requires real body-size limit middleware)
//  4. 500 — panic in handler (caught by Recover middleware)
//  5. 500 — plain Go error from handler
//  6. 422 — explicit *echo.HTTPError(422, "unprocessable entity")
//  7. 400 — explicit *echo.HTTPError(400, "bad request")
//  8. 403 — explicit *echo.HTTPError(403, "forbidden")
//  9. 409 — explicit *echo.HTTPError(409, "conflict")
// 10. 429 — explicit *echo.HTTPError(429, "too many requests")
//
// TS-01-P7, PROP: 01-PROP-7
// Validates: 01-REQ-12.1, 01-REQ-12.2, 01-REQ-12.3, 01-REQ-12.4
func TestSpec01_PropErrorEnvelopeConsistency(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = handler.CustomErrorHandler

	// Global middleware stack (spec 01 order).
	e.Use(echoMw.Recover())
	e.Use(mw.BodySizeLimitMiddleware("1M"))
	e.Use(mw.RequestLoggerMiddleware())

	// Auth group (no auth middleware) with catch-all for structural exclusion.
	authGroup := e.Group("/api/v1/auth")
	authGroup.Any("/*", func(c echo.Context) error {
		return echo.ErrNotFound
	})

	// Protected group (with auth middleware using full schema + seeded admin token).
	testDB, adminToken := setupAuthTestDB(t)
	protected := e.Group("/api/v1", mw.AuthMiddleware(testDB))

	// Register test handlers that produce specific errors.
	protected.GET("/panic-trigger", func(c echo.Context) error {
		panic("test panic for P7")
	})
	protected.GET("/plain-error", func(c echo.Context) error {
		return errors.New("some internal failure")
	})
	protected.GET("/http-422", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "unprocessable entity")
	})
	protected.GET("/http-400", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusBadRequest, "bad request")
	})
	protected.GET("/http-403", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusForbidden, "forbidden")
	})
	protected.GET("/http-409", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusConflict, "conflict")
	})
	protected.GET("/http-429", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusTooManyRequests, "too many requests")
	})
	protected.POST("/body-endpoint", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})

	// Health probes on root (for reference — not tested here).
	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Auth header for cases that need to pass through auth middleware
	// to reach the handler and trigger the intended error condition.
	authHeader := map[string]string{"Authorization": "Bearer " + adminToken}

	type errorCase struct {
		method         string
		path           string
		body           string
		headers        map[string]string
		expectedStatus int
		desc           string
	}

	cases := []errorCase{
		// 1. No auth header on protected route → 401
		{http.MethodGet, "/api/v1/test-nonexistent", "", nil, http.StatusUnauthorized, "missing_auth_401"},
		// 2. Route not found under /api/v1/ → 404 (with auth to pass middleware)
		{http.MethodGet, "/api/v1/does-not-exist-xyz", "", authHeader, http.StatusNotFound, "not_found_404"},
		// 3. Oversized body → 413 (body-size limit fires before auth)
		{http.MethodPost, "/api/v1/body-endpoint", strings.Repeat("X", 2*1024*1024), authHeader, http.StatusRequestEntityTooLarge, "body_too_large_413"},
		// 4. Panic in handler → 500 (with auth to reach handler)
		{http.MethodGet, "/api/v1/panic-trigger", "", authHeader, http.StatusInternalServerError, "panic_500"},
		// 5. Plain Go error → 500 (with auth to reach handler)
		{http.MethodGet, "/api/v1/plain-error", "", authHeader, http.StatusInternalServerError, "plain_error_500"},
		// 6. HTTPError 422 (with auth to reach handler)
		{http.MethodGet, "/api/v1/http-422", "", authHeader, http.StatusUnprocessableEntity, "http_error_422"},
		// 7. HTTPError 400 (with auth to reach handler)
		{http.MethodGet, "/api/v1/http-400", "", authHeader, http.StatusBadRequest, "http_error_400"},
		// 8. HTTPError 403 (with auth to reach handler)
		{http.MethodGet, "/api/v1/http-403", "", authHeader, http.StatusForbidden, "http_error_403"},
		// 9. HTTPError 409 (with auth to reach handler)
		{http.MethodGet, "/api/v1/http-409", "", authHeader, http.StatusConflict, "http_error_409"},
		// 10. HTTPError 429 (with auth to reach handler)
		{http.MethodGet, "/api/v1/http-429", "", authHeader, http.StatusTooManyRequests, "http_error_429"},
	}

	for i, tc := range cases {
		t.Run(fmt.Sprintf("case_%d_%s", i+1, tc.desc), func(t *testing.T) {
			var req *http.Request
			if tc.body != "" {
				req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/octet-stream")
			} else {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			}
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			// The HTTP status code must match the expected error status.
			if rec.Code != tc.expectedStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.expectedStatus)
			}

			// The response body must match the envelope format.
			var env struct {
				Error *struct {
					Code    any    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("failed to parse response body as JSON: %v; body = %q", err, rec.Body.String())
			}

			if env.Error == nil {
				t.Fatalf("response body missing 'error' key; body = %q", rec.Body.String())
			}

			// Code must be an integer matching the HTTP status.
			codeFloat, ok := env.Error.Code.(float64)
			if !ok {
				t.Fatalf("error.code is not a number: %v (type %T)", env.Error.Code, env.Error.Code)
			}
			if int(codeFloat) != rec.Code {
				t.Errorf("error.code = %d, want %d (must match HTTP status)", int(codeFloat), rec.Code)
			}

			// Message must be a non-empty string.
			if env.Error.Message == "" {
				t.Error("error.message must be a non-empty string")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Property Test TS-01-P8 — readyz Failure Counter Goroutine Safety
// ---------------------------------------------------------------------------

// TestSpec01_PropReadyzConcurrentGoroutineSafety is a property test that runs
// 50 concurrent GET /readyz requests in goroutines under various DB health
// states (healthy, degraded, recovering) and verifies:
//
//   - No data race on the failure counter (run with -race flag)
//   - Counter value after each successful probe is 0
//   - Counter value after each failed probe is >= 1
//   - Counter never becomes negative
//
// TS-01-P8, PROP: 01-PROP-8
// Validates: 01-REQ-4.3, 01-REQ-4.E1
func TestSpec01_PropReadyzConcurrentGoroutineSafety(t *testing.T) {
	healthyDB := setupTestDB(t)
	brokenDB := setupBrokenDB(t)

	healthyH := handler.ReadyzHandler(healthyDB)
	brokenH := handler.ReadyzHandler(brokenDB)

	if healthyH == nil {
		t.Fatal("ReadyzHandler(healthyDB) returned nil handler")
	}
	if brokenH == nil {
		t.Fatal("ReadyzHandler(brokenDB) returned nil handler")
	}

	e := echo.New()

	// Phase 1: Fire 50 concurrent requests against healthy DB.
	handler.ResetReadyzFailureCounter()

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			if err := healthyH(c); err != nil {
				// Not fatal — just log.
				t.Logf("healthyH error: %v", err)
			}
		})
	}
	wg.Wait()

	// After all healthy probes, counter must be 0.
	counter := handler.GetReadyzFailureCounter()
	if counter != 0 {
		t.Errorf("counter after 50 healthy probes = %d, want 0", counter)
	}

	// Phase 2: Fire 50 concurrent requests against broken DB.
	handler.ResetReadyzFailureCounter()

	for range 50 {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			if err := brokenH(c); err != nil {
				t.Logf("brokenH error: %v", err)
			}
		})
	}
	wg.Wait()

	// After all broken probes, counter must be >= 1 (not negative).
	counter = handler.GetReadyzFailureCounter()
	if counter < 1 {
		t.Errorf("counter after 50 broken probes = %d, want >= 1", counter)
	}
	if counter < 0 {
		t.Errorf("counter became negative: %d", counter)
	}

	// Phase 3: Mixed — fire 50 concurrent recovery probes while counter > 0.
	for range 50 {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			if err := healthyH(c); err != nil {
				t.Logf("recovery probe error: %v", err)
			}
		})
	}
	wg.Wait()

	// After recovery, counter must be 0.
	counter = handler.GetReadyzFailureCounter()
	if counter != 0 {
		t.Errorf("counter after recovery phase = %d, want 0", counter)
	}
	if counter < 0 {
		t.Errorf("counter became negative during recovery: %d", counter)
	}
}
