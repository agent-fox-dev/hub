package sandboxes

import "database/sql"

// leaseSchemaSQL creates the sandbox_leases table. The state column allows all
// five lifecycle states so that the specs that add the others need no
// migration (SQLite cannot alter a CHECK constraint). token_hash is unique and
// nullable: it is cleared when the lease ends. The JSON columns hold the
// profile snapshot's lists and maps and the caller's labels. No column holds a
// secret value or a plaintext token.
const leaseSchemaSQL = `CREATE TABLE IF NOT EXISTS sandbox_leases (
	id               TEXT PRIMARY KEY,
	workspace        TEXT NOT NULL,
	profile          TEXT NOT NULL,
	purpose          TEXT NOT NULL DEFAULT '',
	state            TEXT NOT NULL CHECK(state IN ('leased', 'active', 'destroyed', 'expired', 'failed')),
	token_hash       TEXT UNIQUE,
	image            TEXT NOT NULL,
	network          TEXT NOT NULL,
	cpus             REAL NOT NULL DEFAULT 0,
	memory_mb        INTEGER NOT NULL DEFAULT 0,
	pids             INTEGER NOT NULL DEFAULT 0,
	disk_mb          INTEGER NOT NULL DEFAULT 0,
	require_landlock INTEGER NOT NULL DEFAULT 0,
	secrets          TEXT NOT NULL DEFAULT '[]',
	optional_secrets TEXT NOT NULL DEFAULT '[]',
	env              TEXT NOT NULL DEFAULT '{}',
	labels           TEXT NOT NULL DEFAULT '{}',
	ttl_seconds      INTEGER NOT NULL,
	expires_at       TEXT NOT NULL,
	created_by       TEXT NOT NULL,
	created_at       TEXT NOT NULL,
	updated_at       TEXT NOT NULL,
	sandbox_id       TEXT,
	error            TEXT,
	ended_at         TEXT,
	end_reason       TEXT
)`

// leaseIndexesSQL are the lookups the lease routes make. token_hash is indexed
// by its UNIQUE constraint.
var leaseIndexesSQL = []string{
	`CREATE INDEX IF NOT EXISTS idx_sandbox_leases_workspace ON sandbox_leases (workspace)`,
	`CREATE INDEX IF NOT EXISTS idx_sandbox_leases_state ON sandbox_leases (state)`,
}

// InitSchema creates the sandbox_leases table and its indexes. It runs at every
// start, whether or not sandboxes are enabled, in the way secrets.InitSchema
// does, and running it again leaves the rows alone.
func InitSchema(db *sql.DB) error {
	if _, err := db.Exec(leaseSchemaSQL); err != nil {
		return err
	}
	for _, stmt := range leaseIndexesSQL {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
