package forge

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/txsvc/apikit"
)

// deliveryRetention is how long a row of the delivery log is kept.
const deliveryRetention = 7 * 24 * time.Hour

// retentionEvery is how often the retention loop prunes. It is a variable so that a test can
// shorten it.
var retentionEvery = time.Hour

// PruneDeliveries deletes the rows of the delivery log that are more than seven days old and the
// rows of workspaces that no longer exist, so that a slug that is made again does not show the
// deliveries of its earlier owner for longer than a pass. It returns how many rows it deleted.
func PruneDeliveries(ctx context.Context, db *sql.DB) (int64, error) {
	cutoff := apikit.FormatUTC(time.Now().Add(-deliveryRetention))
	res, err := db.ExecContext(ctx, `DELETE FROM forge_deliveries
		WHERE received_at < ? OR workspace_slug NOT IN (SELECT slug FROM workspaces)`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// StartDeliveryRetention prunes the delivery log at once and then every hour, and returns when
// ctx is cancelled. A pass that fails is logged and the next one is tried at the next tick; main
// starts it with go, after InitSchema has made the table.
func StartDeliveryRetention(ctx context.Context, db *sql.DB) {
	runDeliveryRetention(ctx, db, retentionEvery)
}

// runDeliveryRetention is StartDeliveryRetention with the interval between the passes.
func runDeliveryRetention(ctx context.Context, db *sql.DB, interval time.Duration) {
	prune := func() {
		n, err := PruneDeliveries(ctx, db)
		switch {
		case ctx.Err() != nil:
			// The hub is going down; a pass that it cut short is no failure.
		case err != nil:
			slog.Error("forge: pruning the delivery log failed", "error", err)
		case n > 0:
			slog.Info("forge: pruned the delivery log", "rows", n)
		}
	}
	prune()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}
