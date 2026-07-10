package middleware_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	mw "github.com/agent-fox-dev/hub/internal/middleware"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// uuidV4Pattern matches a standard UUID v4 string.
var uuidV4Pattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

// isValidUUIDv4 checks whether a string is a valid UUID v4 (lowercase hex).
func isValidUUIDv4(s string) bool {
	return uuidV4Pattern.MatchString(strings.ToLower(s))
}

// setupLogCapture redirects logrus output to a buffer for test assertions.
// Returns the buffer and a cleanup function that restores the original output.
func setupLogCapture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer

	origFormatter := logrus.StandardLogger().Formatter
	origOutput := logrus.StandardLogger().Out
	origLevel := logrus.GetLevel()

	logrus.SetFormatter(&logrus.JSONFormatter{})
	logrus.SetOutput(&buf)
	logrus.SetLevel(logrus.TraceLevel) // Capture all levels.

	t.Cleanup(func() {
		logrus.SetFormatter(origFormatter)
		logrus.SetOutput(origOutput)
		logrus.SetLevel(origLevel)
	})

	return &buf
}

// parseLogEntries splits a buffer's content into individual JSON log entries.
func parseLogEntries(buf *bytes.Buffer) []map[string]any {
	var entries []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err == nil {
			entries = append(entries, entry)
		}
	}
	return entries
}

// ---------------------------------------------------------------------------
// 3.1 — Request Logger Middleware Log Entry Fields
// ---------------------------------------------------------------------------

// TestSpec01_RequestLoggerFields verifies that the request logger middleware
// emits a structured log entry for every completed HTTP request containing:
//   - method (string)
//   - path (string)
//   - status (integer)
//   - duration_ms (float > 0)
//   - request_id (non-empty UUID string)
//
// TS-01-18, REQ: 01-REQ-5.2
func TestSpec01_RequestLoggerFields(t *testing.T) {
	logBuf := setupLogCapture(t)

	e := echo.New()
	e.Use(mw.RequestLoggerMiddleware())

	// Register a simple 200-OK handler.
	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	entries := parseLogEntries(logBuf)
	if len(entries) == 0 {
		t.Fatal("request logger middleware emitted no log entry; expected one entry per request")
	}

	// Find the request log entry (look for one with "method" field).
	var found map[string]any
	for _, e := range entries {
		if _, ok := e["method"]; ok {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatal("no log entry with 'method' field found; request logger should emit method, path, status, duration_ms, request_id")
	}

	// Verify method.
	if method, ok := found["method"].(string); !ok || method != "GET" {
		t.Errorf("method = %v, want %q", found["method"], "GET")
	}

	// Verify path.
	if path, ok := found["path"].(string); !ok || path != "/healthz" {
		t.Errorf("path = %v, want %q", found["path"], "/healthz")
	}

	// Verify status is a number == 200.
	if status, ok := found["status"].(float64); !ok || int(status) != 200 {
		t.Errorf("status = %v (type %T), want integer 200", found["status"], found["status"])
	}

	// Verify duration_ms is a positive float.
	if durationMs, ok := found["duration_ms"].(float64); !ok || durationMs <= 0 {
		t.Errorf("duration_ms = %v (type %T), want positive float", found["duration_ms"], found["duration_ms"])
	}

	// Verify request_id is a non-empty string (should be UUID).
	if reqID, ok := found["request_id"].(string); !ok || reqID == "" {
		t.Errorf("request_id = %v (type %T), want non-empty UUID string", found["request_id"], found["request_id"])
	}
}

// TestSpec01_RequestLoggerFieldsOnError verifies that the request logger
// middleware also emits a log entry for error responses (e.g., 404).
// TS-01-18, REQ: 01-REQ-5.2
func TestSpec01_RequestLoggerFieldsOnError(t *testing.T) {
	logBuf := setupLogCapture(t)

	e := echo.New()
	e.Use(mw.RequestLoggerMiddleware())

	// No handler registered for /missing — will produce 404 from Echo.
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	entries := parseLogEntries(logBuf)
	var found map[string]any
	for _, e := range entries {
		if _, ok := e["method"]; ok {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatal("request logger should emit a log entry even for error responses")
	}

	// Status should reflect the error code (404 or similar).
	if status, ok := found["status"].(float64); !ok || int(status) < 400 {
		t.Errorf("status = %v, want >= 400 for error response", found["status"])
	}

	// request_id should still be present.
	if reqID, ok := found["request_id"].(string); !ok || reqID == "" {
		t.Error("request_id should be present in log entry for error responses")
	}
}

// ---------------------------------------------------------------------------
// 3.2 — Request ID Generation and Propagation
// ---------------------------------------------------------------------------

// TestSpec01_RequestIDValidHeaderPropagated verifies that a valid X-Request-ID
// header value is propagated as-is to the Echo context and the response header.
// TS-01-20, REQ: 01-REQ-6.1
func TestSpec01_RequestIDValidHeaderPropagated(t *testing.T) {
	e := echo.New()
	e.Use(mw.RequestLoggerMiddleware())

	clientRequestID := "my-valid-request-123"

	var contextRequestID string
	e.GET("/test", func(c echo.Context) error {
		// Capture the request ID from context.
		if id, ok := c.Get(mw.RequestIDContextKey).(string); ok {
			contextRequestID = id
		}
		return c.JSON(http.StatusOK, nil)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Request-ID", clientRequestID)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Response header should carry the same value.
	respID := rec.Header().Get("X-Request-ID")
	if respID != clientRequestID {
		t.Errorf("X-Request-ID response header = %q, want %q", respID, clientRequestID)
	}

	// Context should carry the same value.
	if contextRequestID != clientRequestID {
		t.Errorf("context request_id = %q, want %q", contextRequestID, clientRequestID)
	}
}

// TestSpec01_RequestIDGeneratedWhenAbsent verifies that when no X-Request-ID
// header is present, a UUID v4 is generated and used as the request_id.
// TS-01-21, REQ: 01-REQ-6.2
func TestSpec01_RequestIDGeneratedWhenAbsent(t *testing.T) {
	e := echo.New()
	e.Use(mw.RequestLoggerMiddleware())

	var contextRequestID string
	e.GET("/test", func(c echo.Context) error {
		if id, ok := c.Get(mw.RequestIDContextKey).(string); ok {
			contextRequestID = id
		}
		return c.JSON(http.StatusOK, nil)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	// No X-Request-ID header set.
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Response header should have a UUID v4.
	respID := rec.Header().Get("X-Request-ID")
	if respID == "" {
		t.Fatal("X-Request-ID response header should be set when absent from request")
	}
	if !isValidUUIDv4(respID) {
		t.Errorf("X-Request-ID response header = %q, want valid UUID v4", respID)
	}

	// Context should carry the same UUID.
	if contextRequestID == "" {
		t.Error("context request_id should be set")
	}
	if contextRequestID != respID {
		t.Errorf("context request_id = %q, response X-Request-ID = %q; should match", contextRequestID, respID)
	}
}

// TestSpec01_RequestIDAlwaysPresentInResponse verifies that the X-Request-ID
// response header is always present on every response, regardless of whether
// the ID was propagated from the client or generated.
// TS-01-22, REQ: 01-REQ-6.3
func TestSpec01_RequestIDAlwaysPresentInResponse(t *testing.T) {
	e := echo.New()
	e.Use(mw.RequestLoggerMiddleware())

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	t.Run("with_valid_X-Request-ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Request-ID", "valid-id-from-client")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		respID := rec.Header().Get("X-Request-ID")
		if respID == "" {
			t.Error("X-Request-ID response header should be present")
		}
		if respID != "valid-id-from-client" {
			t.Errorf("X-Request-ID = %q, want %q", respID, "valid-id-from-client")
		}
	})

	t.Run("without_X-Request-ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		respID := rec.Header().Get("X-Request-ID")
		if respID == "" {
			t.Error("X-Request-ID response header should be present")
		}
		if !isValidUUIDv4(respID) {
			t.Errorf("X-Request-ID = %q, want valid UUID v4", respID)
		}
	})

	t.Run("with_invalid_X-Request-ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Request-ID", "\x01invalid")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		respID := rec.Header().Get("X-Request-ID")
		if respID == "" {
			t.Error("X-Request-ID response header should be present")
		}
		if !isValidUUIDv4(respID) {
			t.Errorf("X-Request-ID = %q, want valid UUID v4 (invalid input should be discarded)", respID)
		}
	})
}

// TestSpec01_RequestIDInvalidDiscarded verifies that invalid X-Request-ID
// header values are silently discarded and replaced with a fresh UUID v4.
// No error is returned to the client.
//
// Invalid cases tested:
//   - (a) empty string
//   - (b) non-printable bytes (0x01)
//   - (c) string exceeding 128 characters
//
// TS-01-E9, REQ: 01-REQ-6.E1
func TestSpec01_RequestIDInvalidDiscarded(t *testing.T) {
	e := echo.New()
	e.Use(mw.RequestLoggerMiddleware())

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	invalidIDs := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"non_printable", "\x01\x02"},
		{"exceeds_128_chars", strings.Repeat("a", 129)},
	}

	for _, tc := range invalidIDs {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tc.value != "" {
				req.Header.Set("X-Request-ID", tc.value)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			// No error returned — should be 200.
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want %d (no error for invalid X-Request-ID)", rec.Code, http.StatusOK)
			}

			// X-Request-ID should be set to a fresh UUID v4.
			respID := rec.Header().Get("X-Request-ID")
			if respID == "" {
				t.Fatal("X-Request-ID response header should be set")
			}
			if respID == tc.value {
				t.Errorf("X-Request-ID = %q, should have been discarded and replaced with UUID v4", respID)
			}
			if !isValidUUIDv4(respID) {
				t.Errorf("X-Request-ID = %q, want valid UUID v4", respID)
			}
		})
	}
}

// TestSpec01_RequestIDValidationBoundary verifies the exact boundary of
// X-Request-ID validation:
//   - 128 characters: valid (accepted)
//   - 129 characters: invalid (discarded)
//   - printable ASCII (0x20-0x7E): valid
//   - bytes outside that range: invalid
//
// TS-01-E9, REQ: 01-REQ-6.E1
func TestSpec01_RequestIDValidationBoundary(t *testing.T) {
	e := echo.New()
	e.Use(mw.RequestLoggerMiddleware())

	var capturedID string
	e.GET("/test", func(c echo.Context) error {
		if id, ok := c.Get(mw.RequestIDContextKey).(string); ok {
			capturedID = id
		}
		return c.JSON(http.StatusOK, nil)
	})

	t.Run("exactly_128_chars_valid", func(t *testing.T) {
		capturedID = ""
		validID := strings.Repeat("x", 128)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Request-ID", validID)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		respID := rec.Header().Get("X-Request-ID")
		if respID != validID {
			t.Errorf("128-char ID should be accepted; got X-Request-ID = %q", respID)
		}
		if capturedID != validID {
			t.Errorf("128-char ID should be in context; got %q", capturedID)
		}
	})

	t.Run("129_chars_invalid", func(t *testing.T) {
		capturedID = ""
		invalidID := strings.Repeat("x", 129)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Request-ID", invalidID)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		respID := rec.Header().Get("X-Request-ID")
		if respID == invalidID {
			t.Error("129-char ID should be discarded")
		}
		if !isValidUUIDv4(respID) {
			t.Errorf("129-char ID should be replaced with UUID v4; got %q", respID)
		}
	})

	t.Run("printable_ascii_space_tilde", func(t *testing.T) {
		capturedID = ""
		// Space (0x20) and tilde (0x7E) are valid boundaries.
		validID := " request-id~"
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Request-ID", validID)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		respID := rec.Header().Get("X-Request-ID")
		if respID != validID {
			t.Errorf("printable ASCII ID should be accepted; got X-Request-ID = %q", respID)
		}
	})

	t.Run("non_ascii_byte_0x7F", func(t *testing.T) {
		capturedID = ""
		invalidID := "request\x7Fid"
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Request-ID", invalidID)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		respID := rec.Header().Get("X-Request-ID")
		if respID == invalidID {
			t.Error("ID with 0x7F byte should be discarded")
		}
		if !isValidUUIDv4(respID) {
			t.Errorf("ID with 0x7F byte should be replaced with UUID v4; got %q", respID)
		}
	})
}

// TestSpec01_RequestIDLoggedInEntry verifies that the request logger
// middleware includes the request_id in its structured log output, and
// the logged request_id matches the X-Request-ID response header value.
// TS-01-20/21, REQ: 01-REQ-6.1, 01-REQ-6.2
func TestSpec01_RequestIDLoggedInEntry(t *testing.T) {
	logBuf := setupLogCapture(t)

	e := echo.New()
	e.Use(mw.RequestLoggerMiddleware())

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	clientID := "test-request-id-for-log"
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Request-ID", clientID)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Find the log entry with request_id.
	entries := parseLogEntries(logBuf)
	var found map[string]any
	for _, e := range entries {
		if rid, ok := e["request_id"].(string); ok && rid == clientID {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatalf("no log entry found with request_id=%q; entries=%v", clientID, entries)
	}

	// Verify the response header matches.
	respID := rec.Header().Get("X-Request-ID")
	if respID != clientID {
		t.Errorf("X-Request-ID response = %q, want %q", respID, clientID)
	}
}
