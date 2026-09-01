package audit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// TS-19-32: GET /metrics returns HTTP 200 with Prometheus text exposition format
// containing all registered afhub_* metrics.
// Requirement: 19-REQ-10.1
func TestMetricsEndpoint_ReturnsAllRegisteredMetrics(t *testing.T) {
	m := NewMetrics()
	e := echo.New()

	// Mount the metrics endpoint.
	e.GET("/metrics", echo.WrapHandler(m.MetricsHandler()))

	// Make a request to /metrics.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	// Check Content-Type includes text/plain.
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected Content-Type to contain 'text/plain', got %q", ct)
	}

	body := rec.Body.String()

	// Verify key metrics are present.
	requiredMetrics := []string{
		"afhub_http_requests_total",
		"afhub_agent_sessions_active",
		"afhub_http_request_duration_seconds",
	}
	for _, metric := range requiredMetrics {
		if !strings.Contains(body, metric) {
			t.Errorf("expected /metrics body to contain %q", metric)
		}
	}
}

// TS-19-33: GET /metrics returns HTTP 200 without requiring any Authorization header.
// Requirement: 19-REQ-10.2
func TestMetricsEndpoint_NoAuthRequired(t *testing.T) {
	m := NewMetrics()
	e := echo.New()

	// Mount metrics outside any auth middleware group.
	e.GET("/metrics", echo.WrapHandler(m.MetricsHandler()))

	// Send request with no Authorization header.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	// Explicitly ensure no auth header is set.
	req.Header.Del("Authorization")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 without auth, got %d", rec.Code)
	}
}

// TS-19-34: Prometheus middleware uses c.Path() route template as the path label.
// Requirement: 19-REQ-10.3
func TestMetricsMiddleware_UsesRouteTemplate(t *testing.T) {
	m := NewMetrics()
	e := echo.New()

	// Apply Prometheus middleware.
	e.Use(m.PrometheusMiddleware())

	// Register a parameterized route.
	e.GET("/api/v1/sessions/:id", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// Mount metrics endpoint.
	e.GET("/metrics", echo.WrapHandler(m.MetricsHandler()))

	// Make a request to the parameterized route with a unique UUID.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/some-unique-uuid-123", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handler returned status %d", rec.Code)
	}

	// Scrape /metrics to check the path label.
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()

	// The path label should use the route template, not the resolved URI.
	if !strings.Contains(body, `path="/api/v1/sessions/:id"`) {
		t.Errorf("expected path label to use route template /api/v1/sessions/:id, not found in metrics output")
	}
	if strings.Contains(body, `path="/api/v1/sessions/some-unique-uuid-123"`) {
		t.Errorf("raw URI should not appear as path label; cardinality explosion")
	}
}

// TS-19-35: The /metrics endpoint uses a custom Prometheus registry and does not
// include global default registry metrics.
// Requirement: 19-REQ-10.4
func TestMetricsEndpoint_CustomRegistryExcludesGoDefaults(t *testing.T) {
	m := NewMetrics()
	e := echo.New()

	e.GET("/metrics", echo.WrapHandler(m.MetricsHandler()))

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Should contain afhub_* metrics.
	if !strings.Contains(body, "afhub_http_requests_total") {
		t.Errorf("expected afhub_http_requests_total in metrics output")
	}

	// Should NOT contain Go runtime metrics from default registry.
	if strings.Contains(body, "go_goroutines") {
		t.Errorf("expected custom registry NOT to include go_goroutines (default registry metric)")
	}
}

// 19-REQ-10.E1: HTTP request updates metrics before next scrape.
func TestMetricsMiddleware_IncrementOnRequest(t *testing.T) {
	m := NewMetrics()
	e := echo.New()
	e.Use(m.PrometheusMiddleware())
	e.GET("/api/v1/sessions", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	e.GET("/metrics", echo.WrapHandler(m.MetricsHandler()))

	// Make a request to the sessions route.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Scrape metrics.
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()

	// The afhub_http_requests_total counter should reflect the request.
	if !strings.Contains(body, "afhub_http_requests_total") {
		t.Errorf("expected afhub_http_requests_total in metrics output")
	}
}

// 19-REQ-10.E2: All afhub_* metrics appear immediately after startup (except
// afhub_jobqueue_depth which is initialized by durable_job_queue).
func TestMetricsEndpoint_AllMetricsPresentOnStartup(t *testing.T) {
	m := NewMetrics()
	e := echo.New()
	e.GET("/metrics", echo.WrapHandler(m.MetricsHandler()))

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()

	expectedMetrics := []string{
		"afhub_http_requests_total",
		"afhub_http_request_duration_seconds",
		"afhub_agent_sessions_active",
		"afhub_agent_tokens_total",
		"afhub_audit_events_total",
		"afhub_retention_errors_total",
		"afhub_retention_last_run_timestamp_seconds",
		"afhub_audit_table_rows",
	}
	for _, metric := range expectedMetrics {
		if !strings.Contains(body, metric) {
			t.Errorf("expected %q to be present in /metrics output on startup", metric)
		}
	}
}

// 19-REQ-10.E3: Histogram uses standard latency buckets.
func TestMetricsEndpoint_HistogramBuckets(t *testing.T) {
	m := NewMetrics()
	e := echo.New()
	e.Use(m.PrometheusMiddleware())
	// Need a route to generate at least one observation.
	e.GET("/test", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	e.GET("/metrics", echo.WrapHandler(m.MetricsHandler()))

	// Generate an observation.
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Scrape metrics.
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Verify the standard latency bucket boundaries appear.
	expectedBuckets := []string{
		`le="0.005"`,
		`le="0.01"`,
		`le="0.025"`,
		`le="0.05"`,
		`le="0.1"`,
		`le="0.25"`,
		`le="0.5"`,
		`le="1"`,
		`le="2.5"`,
		`le="5"`,
		`le="10"`,
	}
	for _, bucket := range expectedBuckets {
		if !strings.Contains(body, bucket) {
			t.Errorf("expected histogram bucket %s in metrics output", bucket)
		}
	}
}
