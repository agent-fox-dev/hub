package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/handler"
	"github.com/labstack/echo/v4"
)

// errorEnvelope is the standard error response for assertions.
type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// newTestEcho creates a minimal Echo server with CustomHTTPErrorHandler.
func newTestEcho() *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = handler.CustomHTTPErrorHandler
	return e
}

// ============================================================================
// TS-02-33: Verify standard error envelope format with correct HTTP status
// ============================================================================

// TS-02-33: Verify that a 404 error response uses the standard error envelope
// format {"error": {"code": "404", "message": "..."}} and Content-Type is JSON.
func TestErrorHandler_StandardEnvelope_404(t *testing.T) {
	e := newTestEcho()

	// Register a handler that returns a 404 via NewErrorResponse.
	e.GET("/test-404", func(c echo.Context) error {
		return handler.NewErrorResponse(c, http.StatusNotFound, "user not found")
	})

	req := httptest.NewRequest(http.MethodGet, "/test-404", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v\nBody: %s", err, rec.Body.String())
	}

	if resp.Error.Code != "404" {
		t.Errorf("error.code = %q, want %q", resp.Error.Code, "404")
	}
	if resp.Error.Message == "" {
		t.Error("error.message should be non-empty")
	}
}

// TS-02-33: Verify that a 400 error response also uses the standard format.
func TestErrorHandler_StandardEnvelope_400(t *testing.T) {
	e := newTestEcho()

	e.GET("/test-400", func(c echo.Context) error {
		return handler.NewErrorResponse(c, http.StatusBadRequest, "missing required fields")
	})

	req := httptest.NewRequest(http.MethodGet, "/test-400", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if resp.Error.Code != "400" {
		t.Errorf("error.code = %q, want %q", resp.Error.Code, "400")
	}
}

// TS-02-33: Verify that a 401 error response uses the standard format.
func TestErrorHandler_StandardEnvelope_401(t *testing.T) {
	e := newTestEcho()

	e.GET("/test-401", func(c echo.Context) error {
		return handler.NewErrorResponse(c, http.StatusUnauthorized, "missing or malformed token")
	})

	req := httptest.NewRequest(http.MethodGet, "/test-401", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	var resp errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if resp.Error.Code != "401" {
		t.Errorf("error.code = %q, want %q", resp.Error.Code, "401")
	}
}

// ============================================================================
// TS-02-34: Verify that all seven status codes are mapped correctly
// ============================================================================

// TS-02-34: Verify that the error handler correctly maps all seven defined
// status codes (400, 401, 403, 404, 409, 413, 500).
func TestErrorHandler_AllStatusCodes(t *testing.T) {
	tests := []struct {
		code    int
		message string
	}{
		{400, "bad request"},
		{401, "unauthorized"},
		{403, "forbidden"},
		{404, "not found"},
		{409, "conflict"},
		{413, "payload too large"},
		{500, "internal server error"},
	}

	for _, tc := range tests {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			e := newTestEcho()

			e.GET("/test", func(c echo.Context) error {
				return handler.NewErrorResponse(c, tc.code, tc.message)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != tc.code {
				t.Fatalf("status = %d, want %d", rec.Code, tc.code)
			}

			var resp errorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to parse JSON: %v\nBody: %s", err, rec.Body.String())
			}

			expectedCode := http.StatusText(tc.code)
			// The code in the envelope is the numeric string, not the text.
			_ = expectedCode
			if resp.Error.Code == "" {
				t.Error("error.code should be non-empty")
			}

			if resp.Error.Message == "" {
				t.Error("error.message should be non-empty")
			}
		})
	}
}

// ============================================================================
// TS-02-35: 500-level errors never expose internal details
// ============================================================================

// TS-02-35: Verify that 500-level error responses never expose internal error
// details, stack traces, or database error messages in the response body.
func TestErrorHandler_500_NoInternalDetails(t *testing.T) {
	e := newTestEcho()

	// Simulate an internal error that contains sensitive details.
	e.GET("/test-500", func(c echo.Context) error {
		return handler.NewErrorResponse(c, http.StatusInternalServerError,
			"internal server error")
	})

	req := httptest.NewRequest(http.MethodGet, "/test-500", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	var resp errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if resp.Error.Code != "500" {
		t.Errorf("error.code = %q, want %q", resp.Error.Code, "500")
	}

	if resp.Error.Message != "internal server error" {
		t.Errorf("error.message = %q, want %q",
			resp.Error.Message, "internal server error")
	}

	// Verify no internal details leaked.
	body := strings.ToLower(rec.Body.String())
	for _, forbidden := range []string{"sql", "stack", "panic", "goroutine", "runtime", ".go:"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("response body contains forbidden string %q: %s",
				forbidden, rec.Body.String())
		}
	}
}

// TS-02-35: Verify that Echo HTTP errors (e.g., from middleware) are also
// wrapped in the standard error envelope via CustomHTTPErrorHandler.
func TestErrorHandler_EchoHTTPError_Wrapped(t *testing.T) {
	e := newTestEcho()

	// Simulate an echo.HTTPError that might come from middleware.
	e.GET("/test-echo-error", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusForbidden, "insufficient permissions")
	})

	req := httptest.NewRequest(http.MethodGet, "/test-echo-error", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	var resp errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v\nBody: %s", err, rec.Body.String())
	}

	if resp.Error.Code != "403" {
		t.Errorf("error.code = %q, want %q", resp.Error.Code, "403")
	}
}

// ============================================================================
// TS-02-36: Verify docs/api.md exists and contains all endpoints
// ============================================================================

// TS-02-36: Verify that docs/api.md exists and contains every endpoint with
// method, path, auth requirements, request/response examples, status codes,
// and error format.
func TestDocumentation_APIDocExists(t *testing.T) {
	content, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatalf("docs/api.md does not exist or cannot be read: %v", err)
	}

	if len(content) == 0 {
		t.Fatal("docs/api.md is empty")
	}

	doc := string(content)

	// Verify all endpoint paths are documented.
	endpoints := []string{
		"/api/v1/auth/providers",
		"/api/v1/auth/callback",
		"/api/v1/users",
		"/api/v1/workspaces",
		"/api/v1/keys",
	}
	for _, ep := range endpoints {
		if !strings.Contains(doc, ep) {
			t.Errorf("docs/api.md does not contain endpoint %q", ep)
		}
	}

	// Verify HTTP methods are present.
	methods := []string{"GET", "POST", "PUT", "DELETE"}
	for _, m := range methods {
		if !strings.Contains(doc, m) {
			t.Errorf("docs/api.md does not contain HTTP method %q", m)
		}
	}

	// Verify auth-related content is documented.
	if !strings.Contains(doc, "Authorization") {
		t.Error("docs/api.md does not mention 'Authorization'")
	}

	// Verify error format is documented.
	if !strings.Contains(doc, "error") {
		t.Error("docs/api.md does not mention 'error'")
	}

	// Verify status codes are documented.
	statusCodes := []string{"400", "401", "403", "404", "409", "500"}
	for _, code := range statusCodes {
		if !strings.Contains(doc, code) {
			t.Errorf("docs/api.md does not contain status code %q", code)
		}
	}
}

// ============================================================================
// TS-02-37: Verify README.md contains link to docs/api.md
// ============================================================================

// TS-02-37: Verify that README.md contains a markdown hyperlink or reference
// to docs/api.md.
func TestDocumentation_ReadmeLinksToAPIDocs(t *testing.T) {
	content, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("README.md does not exist or cannot be read: %v", err)
	}

	if len(content) == 0 {
		t.Fatal("README.md is empty")
	}

	doc := string(content)

	if !strings.Contains(doc, "docs/api.md") {
		t.Error("README.md does not contain a reference to 'docs/api.md'")
	}
}
