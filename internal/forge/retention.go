package forge

import (
	"context"
	"database/sql"
	"time"

	"github.com/txsvc/apikit"
)

// deliveryRetention is how long a row of the delivery log is kept.
const deliveryRetention = 7 * 24 * time.Hour

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
