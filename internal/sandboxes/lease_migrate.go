package sandboxes

import (
	"database/sql"
	"fmt"
)

// MigrateLeaseExpiry adds the column lease_expires_at to a sandbox_leases table
// that was created without it, and leaves a table that has it alone. Rows that
// exist stay NULL, which the deadline logic reads as "use expires_at". It
// follows jobqueue.MigrateProgress: it checks PRAGMA table_info first, so a
// second run changes nothing, and a nil database is not an error. It runs at
// every start right after InitSchema, whether or not sandboxes are enabled.
func MigrateLeaseExpiry(db *sql.DB) error {
	if db == nil {
		return nil
	}

	rows, err := db.Query(`PRAGMA table_info('sandbox_leases')`)
	if err != nil {
		return fmt.Errorf("sandbox lease: MigrateLeaseExpiry: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("sandbox lease: MigrateLeaseExpiry: scan column: %w", err)
		}
		if name == "lease_expires_at" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sandbox lease: MigrateLeaseExpiry: iterate columns: %w", err)
	}
	// Close the cursor before the ALTER: a one-connection handle cannot run it
	// while the cursor is open.
	_ = rows.Close()

	if _, err := db.Exec(`ALTER TABLE sandbox_leases ADD COLUMN lease_expires_at TEXT`); err != nil {
		return fmt.Errorf("sandbox lease: MigrateLeaseExpiry: alter table: %w", err)
	}
	return nil
}
