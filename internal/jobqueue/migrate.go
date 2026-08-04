package jobqueue

import "database/sql"

// MigrateGroupKey adds the group_key TEXT NOT NULL DEFAULT '' column to the
// jobs table. This migration is idempotent: if the column already exists,
// it is a no-op.
//
// Existing rows receive group_key = '' (the column default), preserving
// backward compatibility for job types that do not use group serialization.
func MigrateGroupKey(db *sql.DB) error {
	if db == nil {
		return nil
	}
	// TODO: implement in task group 7
	return nil
}
