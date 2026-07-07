package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// TS-01-32: Verify that the request logger middleware emits a structured JSON
// log entry with method, path, status code, and duration after each HTTP request.
func TestRequestLoggerMiddleware_EmitsStructuredLog(t *testing.T) {
	// Capture logrus output.
	var buf bytes.Buffer
	logrus.SetOutput(&buf)
	logrus.SetFormatter(&logrus.JSONFormatter{})
	logrus.SetLevel(logrus.InfoLevel)
	t.Cleanup(func() {
		logrus.SetOutput(nil)
		logrus.SetFormatter(&logrus.TextFormatter{})
	})

	e := echo.New()

	// Register the middleware.
	e.Use(RequestLoggerMiddleware())

	// Register a simple handler.
	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Verify the response worked.
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Parse the log output.
	output := buf.String()
	if output == "" {
		t.Fatal("expected log output from middleware, got empty string")
	}

	var entry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &entry); err != nil {
		t.Fatalf("log output is not valid JSON: %v\nOutput: %s", err, output)
	}

	// Verify required fields.
	if method, ok := entry["method"]; !ok {
		t.Error("log entry should contain 'method' field")
	} else if method != "GET" {
		t.Errorf("expected method 'GET', got %v", method)
	}

	if path, ok := entry["path"]; !ok {
		t.Error("log entry should contain 'path' field")
	} else if path != "/healthz" {
		t.Errorf("expected path '/healthz', got %v", path)
	}

	if status, ok := entry["status"]; !ok {
		t.Error("log entry should contain 'status' field")
	} else {
		// JSON numbers are float64 by default.
		statusFloat, isFloat := status.(float64)
		if !isFloat {
			t.Errorf("expected status as number, got %T", status)
		} else if int(statusFloat) != 200 {
			t.Errorf("expected status 200, got %v", status)
		}
	}

	if duration, ok := entry["duration"]; !ok {
		t.Error("log entry should contain 'duration' field")
	} else {
		// Duration should be numeric (milliseconds).
		if _, isFloat := duration.(float64); !isFloat {
			t.Errorf("expected duration as numeric, got %T", duration)
		}
	}
}

// TS-01-32 continued: Verify that middleware logs for non-200 responses too.
func TestRequestLoggerMiddleware_Logs404(t *testing.T) {
	var buf bytes.Buffer
	logrus.SetOutput(&buf)
	logrus.SetFormatter(&logrus.JSONFormatter{})
	logrus.SetLevel(logrus.InfoLevel)
	t.Cleanup(func() {
		logrus.SetOutput(nil)
		logrus.SetFormatter(&logrus.TextFormatter{})
	})

	e := echo.New()
	e.Use(RequestLoggerMiddleware())

	// Request a non-existent route.
	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	output := buf.String()
	if output == "" {
		t.Fatal("expected log output from middleware for 404, got empty string")
	}

	var entry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &entry); err != nil {
		t.Fatalf("log output is not valid JSON: %v\nOutput: %s", err, output)
	}

	if status, ok := entry["status"]; ok {
		statusFloat, isFloat := status.(float64)
		if isFloat && int(statusFloat) != 404 && int(statusFloat) != 405 {
			t.Errorf("expected status 404 or 405 for nonexistent route, got %v", status)
		}
	}
}
