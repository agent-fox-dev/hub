package audit

import (
	"context"
	"database/sql"
	"fmt"
)

// allTableDDL contains the DDL for all nine audit tables.
var allTableDDL = []string{
	`CREATE TABLE IF NOT EXISTS agent_audit_events (
		id VARCHAR PRIMARY KEY,
		run_id VARCHAR NOT NULL,
		workspace VARCHAR NOT NULL DEFAULT '',
		event_type VARCHAR NOT NULL,
		severity VARCHAR NOT NULL DEFAULT 'info',
		node_id VARCHAR NOT NULL DEFAULT '',
		session_id VARCHAR NOT NULL DEFAULT '',
		archetype VARCHAR NOT NULL DEFAULT '',
		timestamp TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
		payload VARCHAR NOT NULL DEFAULT '{}',
		ingested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS hub_audit_events (
		id VARCHAR PRIMARY KEY,
		event_type VARCHAR NOT NULL,
		actor_id VARCHAR NOT NULL DEFAULT '',
		actor_type VARCHAR NOT NULL DEFAULT '',
		resource_type VARCHAR NOT NULL DEFAULT '',
		resource_id VARCHAR NOT NULL DEFAULT '',
		action VARCHAR NOT NULL DEFAULT '',
		workspace VARCHAR NOT NULL DEFAULT '',
		severity VARCHAR NOT NULL DEFAULT 'info',
		timestamp VARCHAR NOT NULL DEFAULT '',
		metadata VARCHAR NOT NULL DEFAULT '{}',
		ingested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS session_outcomes (
		id VARCHAR PRIMARY KEY,
		run_id VARCHAR NOT NULL,
		workspace VARCHAR NOT NULL DEFAULT '',
		session_id VARCHAR NOT NULL DEFAULT '',
		node_id VARCHAR NOT NULL DEFAULT '',
		status VARCHAR NOT NULL DEFAULT '',
		timestamp TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		token_usage VARCHAR NOT NULL DEFAULT '{}',
		ingested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS tool_calls (
		id VARCHAR PRIMARY KEY,
		run_id VARCHAR NOT NULL,
		workspace VARCHAR NOT NULL DEFAULT '',
		tool_name VARCHAR NOT NULL DEFAULT '',
		node_id VARCHAR NOT NULL DEFAULT '',
		session_id VARCHAR NOT NULL DEFAULT '',
		timestamp TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		input VARCHAR NOT NULL DEFAULT '{}',
		output VARCHAR NOT NULL DEFAULT '{}',
		ingested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS tool_errors (
		id VARCHAR PRIMARY KEY,
		run_id VARCHAR NOT NULL,
		workspace VARCHAR NOT NULL DEFAULT '',
		tool_name VARCHAR NOT NULL DEFAULT '',
		node_id VARCHAR NOT NULL DEFAULT '',
		session_id VARCHAR NOT NULL DEFAULT '',
		error_code VARCHAR NOT NULL DEFAULT '',
		error_msg VARCHAR NOT NULL DEFAULT '',
		timestamp TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
		ingested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS agent_traces (
		id VARCHAR PRIMARY KEY,
		run_id VARCHAR NOT NULL,
		workspace VARCHAR NOT NULL DEFAULT '',
		event_type VARCHAR NOT NULL DEFAULT '',
		node_id VARCHAR NOT NULL DEFAULT '',
		session_id VARCHAR NOT NULL DEFAULT '',
		sequence INTEGER NOT NULL DEFAULT 0,
		timestamp TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
		data VARCHAR NOT NULL DEFAULT '{}',
		ingested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS postmortems (
		run_id VARCHAR PRIMARY KEY,
		workspace VARCHAR NOT NULL DEFAULT '',
		schema_version INTEGER NOT NULL DEFAULT 1,
		run_status VARCHAR NOT NULL DEFAULT '',
		started_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
		completed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
		task_summary VARCHAR NOT NULL DEFAULT '{}',
		cost_summary VARCHAR NOT NULL DEFAULT '{}',
		blocked_tasks VARCHAR NOT NULL DEFAULT '[]',
		session_history VARCHAR NOT NULL DEFAULT '[]',
		ingested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS agent_sessions (
		id              VARCHAR PRIMARY KEY,
		run_id          VARCHAR NOT NULL DEFAULT '',
		workspace_slug  VARCHAR NOT NULL,
		node_id         VARCHAR NOT NULL DEFAULT '',
		archetype       VARCHAR NOT NULL DEFAULT '',
		status          VARCHAR NOT NULL,
		started_at      TIMESTAMPTZ NOT NULL,
		model           VARCHAR NOT NULL DEFAULT '',
		credential_id   VARCHAR NOT NULL DEFAULT '',
		credential_type VARCHAR NOT NULL DEFAULT '',
		error_message   TEXT,
		ended_at        TIMESTAMPTZ,
		duration_ms     BIGINT,
		cache_creation_input_tokens BIGINT NOT NULL DEFAULT 0,
		metadata        JSON,
		ingested_at     TIMESTAMPTZ NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS token_usage (
		id                VARCHAR PRIMARY KEY,
		session_id        VARCHAR NOT NULL,
		workspace_slug    VARCHAR NOT NULL,
		model             VARCHAR NOT NULL DEFAULT '',
		input_tokens      BIGINT NOT NULL DEFAULT 0,
		output_tokens     BIGINT NOT NULL DEFAULT 0,
		cache_read_tokens BIGINT NOT NULL DEFAULT 0,
		reported_at       TIMESTAMPTZ NOT NULL,
		ingested_at       TIMESTAMPTZ NOT NULL
	)`,
}

// migrationDDL contains ALTER TABLE statements for non-destructive schema
// migrations. New columns added in future releases are appended here.
var migrationDDL = []string{
	// Spec 18: unified audit query requires severity and timestamp on hub events.
	// DuckDB ALTER TABLE ADD COLUMN does not support NOT NULL constraints;
	// the NOT NULL + DEFAULT is already in the CREATE TABLE DDL above.
	`ALTER TABLE hub_audit_events ADD COLUMN IF NOT EXISTS severity VARCHAR DEFAULT 'info'`,
	`ALTER TABLE hub_audit_events ADD COLUMN IF NOT EXISTS timestamp VARCHAR DEFAULT ''`,
	// Spec 18: unified audit query returns archetype for agent events.
	`ALTER TABLE agent_audit_events ADD COLUMN IF NOT EXISTS archetype VARCHAR DEFAULT ''`,
}

// InitSchema creates all nine audit tables and runs column migrations in a
// single transaction. Returns nil on success. If any statement fails, the
// transaction is rolled back and no changes are committed.
func InitSchema(db *sql.DB) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("audit init schema: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, stmt := range allTableDDL {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("audit init schema: %w", err)
		}
	}

	for _, stmt := range migrationDDL {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("audit init schema migration: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("audit init schema: commit: %w", err)
	}
	return nil
}
