package audit

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the custom Prometheus registry and all afhub_* metric
// collectors. All metrics use the custom registry (not the global default)
// to avoid conflicts with other registered metrics.
type Metrics struct {
	Registry *prometheus.Registry

	// HTTP metrics
	HTTPRequestsTotal   *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec

	// Session metrics
	AgentSessionsActive *prometheus.GaugeVec

	// Token metrics
	AgentTokensTotal *prometheus.CounterVec

	// Audit event metrics
	AuditEventsTotal *prometheus.CounterVec

	// SSE metrics
	SSEConnections prometheus.Gauge

	// Job queue metrics (defined here, owned by durable_job_queue)
	JobQueueDepth *prometheus.GaugeVec

	// Retention metrics
	RetentionErrorsTotal     *prometheus.CounterVec
	RetentionLastRunTimestamp prometheus.Gauge
	AuditTableRows           *prometheus.GaugeVec
}

// NewMetrics creates a new Metrics instance with a custom Prometheus registry
// and registers all afhub_* metrics. Stub: creates unregistered metric
// collectors; the full implementation will register them with the custom registry.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		Registry: reg,

		HTTPRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "afhub_http_requests_total",
			Help: "Total HTTP requests.",
		}, []string{"method", "path", "status"}),

		HTTPRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "afhub_http_request_duration_seconds",
			Help:    "HTTP request latency in seconds.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0},
		}, []string{"method", "path"}),

		AgentSessionsActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "afhub_agent_sessions_active",
			Help: "Currently active agent sessions.",
		}, []string{"workspace"}),

		AgentTokensTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "afhub_agent_tokens_total",
			Help: "Total token usage.",
		}, []string{"workspace", "model", "direction"}),

		AuditEventsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "afhub_audit_events_total",
			Help: "Total audit events ingested.",
		}, []string{"source", "event_type"}),

		SSEConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "afhub_sse_connections",
			Help: "Active SSE connections.",
		}),

		JobQueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "afhub_jobqueue_depth",
			Help: "Job queue depth.",
		}, []string{"type", "status"}),

		RetentionErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "afhub_retention_errors_total",
			Help: "Retention step failures.",
		}, []string{"step"}),

		RetentionLastRunTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "afhub_retention_last_run_timestamp_seconds",
			Help: "Unix timestamp of last successful retention run.",
		}),

		AuditTableRows: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "afhub_audit_table_rows",
			Help: "Row count per audit table.",
		}, []string{"table"}),
	}

	// TODO(spec-19): Register all metrics with the custom registry.
	// The full implementation will call reg.MustRegister(...) for each metric.

	return m
}

// MetricsHandler returns an http.Handler that serves Prometheus metrics
// from the custom registry in Prometheus text exposition format.
func (m *Metrics) MetricsHandler() http.Handler {
	// TODO(spec-19): Use promhttp.HandlerFor with the custom registry.
	_ = promhttp.HandlerFor
	// Stub: returns an empty handler — tests will fail on content assertions.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	})
}

// PrometheusMiddleware returns Echo middleware that records HTTP request
// metrics (afhub_http_requests_total and afhub_http_request_duration_seconds).
// Uses c.Path() as the path label to prevent cardinality explosion.
func (m *Metrics) PrometheusMiddleware() echo.MiddlewareFunc {
	// TODO(spec-19): Implement middleware that observes HTTP metrics.
	// Stub: pass-through middleware — tests will fail on metric assertions.
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			return next(c)
		}
	}
}
