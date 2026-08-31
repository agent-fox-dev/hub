package jobqueue

import (
	"database/sql"
	"fmt"
)

// MigrateProgress adds the progress TEXT column to the jobs table.
// This migration is idempotent: if the column already exists, it is a no-op.
//
// The progress column stores intermediate JSON data written during job execution
// (e.g., rebuild patch results as they complete). Existing rows receive NULL.
func MigrateProgress(db *sql.DB) error {
	if db == nil {
		return nil
	}

	// Check whether the column already exists (idempotent).
	rows, err := db.Query("PRAGMA table_info('jobs')")
	if err != nil {
		return fmt.Errorf("jobqueue: MigrateProgress: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("jobqueue: MigrateProgress: scan column: %w", err)
		}
		if name == "progress" {
			return nil // Column already exists — nothing to do.
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("jobqueue: MigrateProgress: iterate columns: %w", err)
	}

	// Add the column.
	_, err = db.Exec("ALTER TABLE jobs ADD COLUMN progress TEXT")
	if err != nil {
		return fmt.Errorf("jobqueue: MigrateProgress: alter table: %w", err)
	}

	return nil
}

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

	// Check whether the column already exists (idempotent: 12-REQ-1.E1).
	rows, err := db.Query("PRAGMA table_info('jobs')")
	if err != nil {
		return fmt.Errorf("jobqueue: MigrateGroupKey: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("jobqueue: MigrateGroupKey: scan column: %w", err)
		}
		if name == "group_key" {
			return nil // Column already exists — nothing to do.
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("jobqueue: MigrateGroupKey: iterate columns: %w", err)
	}

	// Add the column.
	_, err = db.Exec("ALTER TABLE jobs ADD COLUMN group_key TEXT NOT NULL DEFAULT ''")
	if err != nil {
		return fmt.Errorf("jobqueue: MigrateGroupKey: alter table: %w", err)
	}

	return nil
}
