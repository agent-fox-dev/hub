package workspace

import (
	"database/sql"
	"fmt"
	"log"
)

// ReconcileStuckSyncs resets all workspaces with sync_status='syncing' to
// sync_status='error' with a descriptive sync_error message. This function
// is called during server startup to recover from crashes or unclean
// shutdowns that left sync operations in progress.
//
// Must be called before the HTTP server begins accepting requests (13-REQ-5.1).
// Returns an error if the database operation fails; callers should abort
// server startup on error (13-REQ-5.E1).
func ReconcileStuckSyncs(db *sql.DB) error {
	result, err := db.Exec(
		`UPDATE workspaces SET sync_status = 'error', sync_error = 'sync interrupted by server restart' WHERE sync_status = 'syncing'`,
	)
	if err != nil {
		return fmt.Errorf("reconcile stuck syncs: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reconcile stuck syncs: rows affected: %w", err)
	}

	if affected > 0 {
		log.Printf("INFO: reconciled %d workspace(s) stuck in 'syncing' state", affected)
	}

	return nil
}
