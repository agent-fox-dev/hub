package audit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// TS-28-39: EventSandboxExpire has the value hub.sandbox.expire.
// Requirement: 28-REQ-8.1
func TestEventSandboxExpire_TS_28_39(t *testing.T) {
	if EventSandboxExpire != "hub.sandbox.expire" {
		t.Errorf("EventSandboxExpire = %q; want %q", EventSandboxExpire, "hub.sandbox.expire")
	}
}

// scrapeMetrics serves GET /metrics of m and returns the body.
func scrapeMetrics(t *testing.T, m *Metrics) string {
	t.Helper()
	e := echo.New()
	e.GET("/metrics", echo.WrapHandler(m.MetricsHandler()))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics answered %d", rec.Code)
	}
	return rec.Body.String()
}

// TS-28-45: The three sandbox metrics are registered and listed on /metrics
// before any lease exists, with their pre-initialised series and buckets.
// Requirement: 28-REQ-9.1
func TestSandboxMetrics_ListedOnStartup_TS_28_45(t *testing.T) {
	body := scrapeMetrics(t, NewMetrics())

	for _, name := range []string{"afhub_sandboxes_active", "afhub_sandbox_creates_total", "afhub_sandbox_lease_seconds"} {
		if !strings.Contains(body, "# TYPE "+name+" ") {
			t.Errorf("/metrics does not list %s", name)
		}
	}
	if !strings.Contains(body, "# TYPE afhub_sandboxes_active gauge") {
		t.Error("afhub_sandboxes_active is not a gauge")
	}
	if !strings.Contains(body, "# TYPE afhub_sandbox_creates_total counter") {
		t.Error("afhub_sandbox_creates_total is not a counter")
	}
	if !strings.Contains(body, "# TYPE afhub_sandbox_lease_seconds histogram") {
		t.Error("afhub_sandbox_lease_seconds is not a histogram")
	}

	for _, series := range []string{
		`afhub_sandbox_creates_total{result="created"} 0`,
		`afhub_sandbox_creates_total{result="failed"} 0`,
		`afhub_sandboxes_active{workspace=""} 0`,
		`afhub_sandbox_lease_seconds_count 0`,
		`afhub_sandbox_lease_seconds_sum 0`,
	} {
		if !strings.Contains(body, series+"\n") {
			t.Errorf("/metrics has no line %q", series)
		}
	}

	// The histogram has these bucket bounds and no others besides +Inf.
	bounds := []string{"60", "300", "600", "1200", "1800", "3600", "7200", "14400", "28800", "+Inf"}
	var got []string
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, "afhub_sandbox_lease_seconds_bucket{") {
			continue
		}
		le := strings.TrimPrefix(line, `afhub_sandbox_lease_seconds_bucket{le="`)
		le, _, _ = strings.Cut(le, `"`)
		got = append(got, le)
		if !strings.HasSuffix(line, " 0") {
			t.Errorf("bucket line %q is not zero before any observation", line)
		}
	}
	if strings.Join(got, ",") != strings.Join(bounds, ",") {
		t.Errorf("lease_seconds buckets = %v; want %v", got, bounds)
	}
}
