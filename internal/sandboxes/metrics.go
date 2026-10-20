package sandboxes

import (
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
