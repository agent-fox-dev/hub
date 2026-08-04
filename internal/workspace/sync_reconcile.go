package workspace

import (
	"database/sql"
	"fmt"
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
	// TODO: Implement stuck-sync startup reconciliation (13-REQ-5).
	// Query for all workspaces with sync_status='syncing' and update each
	// to sync_status='error' with sync_error='sync interrupted by server restart'.
	return fmt.Errorf("ReconcileStuckSyncs not implemented")
}
