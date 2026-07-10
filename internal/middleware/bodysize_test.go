package middleware_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/handler"
	mw "github.com/agent-fox-dev/hub/internal/middleware"
	"github.com/labstack/echo/v4"
	echoMw "github.com/labstack/echo/v4/middleware"
)

// errorEnvelope matches the standard API error response format:
// {"error": {"code": <int>, "message": "<string>"}}
type errorEnvelope struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// setupEchoWithFullMiddleware creates an Echo instance with the spec 01
// middleware stack in the correct order and route groups for testing
// middleware execution order.
func setupEchoWithFullMiddleware(t *testing.T) *echo.Echo {
	t.Helper()
	e := echo.New()

	// Custom error handler for translating errors to envelope format.
	e.HTTPErrorHandler = handler.CustomErrorHandler

	// Global middleware stack (spec 01 order: Recover → body-size limit → request logger).
	e.Use(echoMw.Recover())
	e.Use(mw.BodySizeLimitMiddleware("1M"))
	e.Use(mw.RequestLoggerMiddleware())

	// Health probes on root Echo instance.
	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	e.HEAD("/healthz", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	// Protected group at /api/v1 — WITH auth middleware.
	testDB := setupTestDB(t)
	protectedGroup := e.Group("/api/v1", mw.AuthMiddleware(testDB))
	protectedGroup.POST("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	protectedGroup.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})

	return e
}

// ---------------------------------------------------------------------------
// 5.3 — Middleware Execution Order: Body-Size Limit Before Auth
// ---------------------------------------------------------------------------

// TestSpec01_MiddlewareOrder413BeforeAuth verifies that the body-size limit
// middleware fires BEFORE auth middleware: a POST request with a >1MB body
// and a valid auth token returns HTTP 413 (not 401 or handler response).
// The 413 should appear in the request log.
//
// TS-01-42, REQ: 01-REQ-13.1
func TestSpec01_MiddlewareOrder413BeforeAuth(t *testing.T) {
	e := setupEchoWithFullMiddleware(t)

	// Generate a body exceeding 1 MB (2 MB).
	bigBody := make([]byte, 2*1024*1024)
	for i := range bigBody {
		bigBody[i] = 'A'
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", bytes.NewReader(bigBody))
	req.Header.Set("Content-Type", "application/octet-stream")
	// Include a valid-looking auth token (though it doesn't matter;
	// body-size should fire before auth).
	req.Header.Set("Authorization", "Bearer af_admin_0000000000000000000000000000000000000000000000000000000000000000")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Body-size limit should produce 413 before auth processes the token.
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("POST /api/v1/test with 2MB body status = %d, want %d (body-size limit should fire before auth)",
			rec.Code, http.StatusRequestEntityTooLarge)
	}
}

// ---------------------------------------------------------------------------
// 5.3 — Body-Size Limit Returns 413 with Error Envelope
// ---------------------------------------------------------------------------

// TestSpec01_BodySizeLimit413WithEnvelope verifies that a POST request with
// a body exceeding 1 MB returns HTTP 413 with the standard JSON error
// envelope: {"error": {"code": 413, "message": "..."}}.
//
// TS-01-43, REQ: 01-REQ-13.2
func TestSpec01_BodySizeLimit413WithEnvelope(t *testing.T) {
	e := setupEchoWithFullMiddleware(t)

	// 1.1 MB body.
	body := make([]byte, 1100*1024)
	for i := range body {
		body[i] = 'B'
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer af_admin_0000000000000000000000000000000000000000000000000000000000000000")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}

	// Verify Content-Type is application/json.
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// Verify the error envelope format.
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse error envelope: %v; body = %q", err, rec.Body.String())
	}
	if env.Error == nil {
		t.Fatal("response should contain error envelope {\"error\": {...}}")
	}
	if env.Error.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("envelope code = %d, want %d", env.Error.Code, http.StatusRequestEntityTooLarge)
	}
	if env.Error.Message == "" {
		t.Error("envelope message should be non-empty")
	}
}

// ---------------------------------------------------------------------------
// 5.3 — Body-Size Limit Is No-Op for Bodyless Requests
// ---------------------------------------------------------------------------

// TestSpec01_BodySizeLimitNoOpForBodyless verifies that the body-size limit
// middleware does not issue HTTP 413 for GET and HEAD requests (which have
// no body). Requests proceed normally.
//
// TS-01-44, REQ: 01-REQ-13.3
func TestSpec01_BodySizeLimitNoOpForBodyless(t *testing.T) {
	e := setupEchoWithFullMiddleware(t)

	t.Run("GET", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code == http.StatusRequestEntityTooLarge {
			t.Error("GET /healthz should NOT get 413 — body-size limit is no-op for bodyless requests")
		}
		if rec.Code != http.StatusOK {
			t.Errorf("GET /healthz status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("HEAD", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/healthz", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code == http.StatusRequestEntityTooLarge {
			t.Error("HEAD /healthz should NOT get 413 — body-size limit is no-op for bodyless requests")
		}
		if rec.Code != http.StatusOK {
			t.Errorf("HEAD /healthz status = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}
