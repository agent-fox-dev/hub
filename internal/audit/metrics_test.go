package audit

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// metricValue reads the value of a gauge or counter.
func metricValue(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	var pb dto.Metric
	if err := m.Write(&pb); err != nil {
		t.Fatal(err)
	}
	if pb.Gauge != nil {
		return pb.Gauge.GetValue()
	}
	return pb.Counter.GetValue()
}

// scrape serves the metrics handler and returns the exposition text.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

// Every afhub_* series is listed from the start, with the histogram buckets
// of the request duration, on a registry of its own without the Go runtime
// metrics.
func TestMetrics_AllSeriesListedOnStartup(t *testing.T) {
	text := scrape(t, NewMetrics())
	for _, name := range []string{"afhub_http_requests_total", "afhub_http_request_duration_seconds_bucket", "afhub_agent_sessions_active",
		"afhub_agent_tokens_total", "afhub_audit_events_total", "afhub_sse_connections", "afhub_retention_errors_total",
		"afhub_retention_last_run_timestamp_seconds", "afhub_audit_table_rows", "afhub_sandboxes_active",
		`afhub_sandbox_creates_total{result="created"}`, `afhub_sandbox_creates_total{result="failed"}`, "afhub_sandbox_lease_seconds_bucket"} {
		if !strings.Contains(text, name) {
			t.Errorf("%s not listed", name)
		}
	}
	if strings.Contains(text, "afhub_jobqueue_depth{") {
		t.Error("job queue depth pre-initialised; its owner initialises it")
	}
	if strings.Contains(text, "go_goroutines") || strings.Contains(text, "process_") {
		t.Error("Go runtime metrics listed on the custom registry")
	}
	for _, bucket := range []string{`le="0.005"`, `le="10"`, `le="+Inf"`} {
		if !strings.Contains(text, "afhub_http_request_duration_seconds_bucket{method=\"\",path=\"\","+bucket) {
			t.Errorf("duration bucket %s missing", bucket)
		}
	}
}

// The middleware counts requests by method, route template and status and
// observes their duration by method and route template.
func TestPrometheusMiddleware_RecordsRouteTemplate(t *testing.T) {
	m := NewMetrics()
	e := echo.New()
	e.Use(m.PrometheusMiddleware())
	e.GET("/api/v1/sessions/:id", func(c echo.Context) error { return c.String(http.StatusOK, c.Param("id")) })
	for _, id := range []string{"a", "b"} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+id, nil))
	}
	if got := counter(t, m.HTTPRequestsTotal, "GET", "/api/v1/sessions/:id", "200"); got != 2 {
		t.Errorf("requests of the route template: %v; want 2", got)
	}
	text := scrape(t, m)
	if strings.Contains(text, "/api/v1/sessions/a") {
		t.Error("the resolved path was used as a label")
	}
	if !strings.Contains(text, `afhub_http_request_duration_seconds_count{method="GET",path="/api/v1/sessions/:id"} 2`) {
		t.Error("duration not observed per route template")
	}
}

// SetMetrics wires the process-wide sink the SSE manager and the audit
// event recorders use; without it recording is a no-op, and a non-positive
// count records nothing.
func TestSetMetrics_WiresSSEGaugeAndAuditCounter(t *testing.T) {
	m := NewMetrics()
	SetMetrics(m)
	t.Cleanup(func() { SetMetrics(nil) })
	mgr := NewSSEManager(5)
	a, _ := mgr.Register(sseFilters{})
	if got := metricValue(t, m.SSEConnections); got != 1 {
		t.Errorf("sse connections %v after register; want 1", got)
	}
	mgr.Unregister(a.id)
	if got := metricValue(t, m.SSEConnections); got != 0 {
		t.Errorf("sse connections %v after unregister; want 0", got)
	}
	recordAuditEvents("agent", "x", 3)
	recordAuditEvents("agent", "x", 0)
	recordAuditEvents("agent", "x", -1)
	if got := counter(t, m.AuditEventsTotal, "agent", "x"); got != 3 {
		t.Errorf("audit events %v; want 3", got)
	}
	SetMetrics(nil)
	recordAuditEvents("agent", "x", 1)
	recordSSEConnections(9)
	if got := counter(t, m.AuditEventsTotal, "agent", "x"); got != 3 {
		t.Errorf("audit events %v after SetMetrics(nil); want 3", got)
	}
	env := newBareEnv(t)
	SetMetrics(nil)
	env.must(t, http.StatusCreated, http.MethodPost, runPath+"/events", `{"event_type":"session.start"}`, admin)
	if got := counter(t, env.metrics.AuditEventsTotal, "agent", "session.start"); got != 0 {
		t.Errorf("ingestion counted %v without a sink", got)
	}
}
