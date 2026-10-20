package sandboxes

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// metricsRecorder records the sandbox metrics of audit.Metrics. The zero value,
// and a recorder over a nil *audit.Metrics, record nothing.
type metricsRecorder struct{ m *audit.Metrics }

// activated counts a lease of the workspace that has become active: one more
// box.
func (r metricsRecorder) activated(workspace string) {
	if r.m == nil {
		return
	}
	r.m.SandboxesActive.WithLabelValues(workspace).Inc()
}

// The results afhub_sandbox_creates_total counts.
const (
	createCreated = "created"
	createFailed  = "failed"
)

// createResult counts a Create the gateway accepted for a leased lease, by how it
// turned out.
func (r metricsRecorder) createResult(result string) {
	if r.m == nil {
		return
	}
	r.m.SandboxCreatesTotal.WithLabelValues(result).Inc()
}

// ended records the ending of a live lease of the workspace into target, which
// the store has just made. A lease that had a box (wasActive) is one box fewer.
// A lease that becomes destroyed, for any reason, is observed once with the time
// from its issue to its end; one that lapses or fails is not.
func (r metricsRecorder) ended(workspace, createdAt, endedAt string, wasActive bool, target string) {
	if r.m == nil {
		return
	}
	if wasActive {
		r.m.SandboxesActive.WithLabelValues(workspace).Dec()
	}
	if target != leaseDestroyed {
		return
	}
	created, err := apikit.ParseUTC(createdAt)
	if err != nil {
		return
	}
	ended, err := apikit.ParseUTC(endedAt)
	if err != nil {
		return
	}
	r.m.SandboxLeaseSeconds.Observe(max(ended.Sub(created), time.Duration(0)).Seconds())
}

// recalibrate sets afhub_sandboxes_active from the lease table: it resets the
// gauge, which drops every stale label, and sets one value per workspace from
// a count of its active leases. The table is read first, so that a failed read
// leaves the gauge as it was.
//
// A gauge with no series is not listed on /metrics at all. When no workspace has an
// active lease the "" placeholder of NewMetrics is set again, at 0, so that the
// metric stays listed, as it is from the start.
func (r metricsRecorder) recalibrate(ctx context.Context, db *sql.DB) error {
	if r.m == nil {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT workspace, COUNT(*) FROM sandbox_leases WHERE state = 'active' GROUP BY workspace`)
	if err != nil {
		return fmt.Errorf("counting the active leases: %w", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var workspace string
		var n int
		if err := rows.Scan(&workspace, &n); err != nil {
			return fmt.Errorf("counting the active leases: %w", err)
		}
		counts[workspace] = n
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("counting the active leases: %w", err)
	}

	r.m.SandboxesActive.Reset()
	for workspace, n := range counts {
		r.m.SandboxesActive.WithLabelValues(workspace).Set(float64(n))
	}
	if len(counts) == 0 {
		r.m.SandboxesActive.WithLabelValues("")
	}
	return nil
}
