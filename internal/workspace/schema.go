package workspace

import "database/sql"

// initSchema creates the workspaces table using CREATE TABLE IF NOT EXISTS.
// It is called during server boot to ensure the schema exists.
func initSchema(db *sql.DB) error {
	panic("not implemented")
}
