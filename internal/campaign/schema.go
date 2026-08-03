package campaign

import "database/sql"

// InitSchema creates the campaigns and campaign_specs tables with all columns,
// constraints, and CHECK constraints. It is called during hub startup.
func InitSchema(_ *sql.DB) error {
	return nil // stub — not yet implemented
}
