package audit

import "database/sql"

// InitSchema creates all nine audit tables and runs column migrations
// in a single transaction. Returns nil on success.
func InitSchema(db *sql.DB) error {
	panic("not implemented")
}
