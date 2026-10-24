package main

import (
	"context"
	"log/slog"
	"time"
)

// maintenanceInterval is how often the housekeeping tasks run after the pass
// at start.
const maintenanceInterval = time.Hour

// maintenanceTask is one housekeeping pass: the audit retention, the pruning
// of the forge delivery log, the sweep of token bindings. A task is run at
// start and then once per interval; what it returns is logged and changes
// nothing else.
type maintenanceTask struct {
	name string
	run  func(ctx context.Context) error
}

// runMaintenance runs every task at once and then every interval, in order,
// until ctx is cancelled. A task that fails is logged and the others still
// run; a task the cancellation cut short is no failure. main starts it with go,
// after every table the tasks touch exists, so it never holds up the start.
func runMaintenance(ctx context.Context, interval time.Duration, tasks []maintenanceTask) {
	pass := func() {
		for _, t := range tasks {
			if ctx.Err() != nil {
				return
			}
			if err := t.run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("maintenance task failed", "task", t.name, "error", err)
			}
		}
	}
	pass()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pass()
		}
	}
}
