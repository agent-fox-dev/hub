package boundtoken

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// sweepInterval is how often the binding sweep runs. It is a variable so that
// a test can shorten it.
var sweepInterval = time.Hour

// StartBindingSweep runs a pass of the binding sweep (wsaccess.SweepBindings)
// at once and then every sweepInterval, and returns when ctx is cancelled. A
// pass that fails is logged and tried again at the next tick; a pass the
// cancellation cut short is no failure. main starts it with go, after the
// binding table exists, so it never holds up or stops the start.
func StartBindingSweep(ctx context.Context, db *sql.DB) {
	sweep := func() {
		err := wsaccess.SweepBindings(ctx, db)
		switch {
		case ctx.Err() != nil:
			// The hub is going down; a pass that it cut short is no failure.
		case err != nil:
			slog.Error("boundtoken: the binding sweep failed", "error", err)
		}
	}
	sweep()
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}
