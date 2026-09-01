package audit

import (
	"database/sql"
	"fmt"
)

// sessionsDDL contains the DDL for agent_sessions and token_usage tables.
// These tables are defined in spec 17 and extended with additional columns
// for spec 19 (credential_id, credential_type, duration_ms,
// cache_creation_input_tokens on agent_sessions; reported_at alias on
// token_usage).
var sessionsDDL = []string{
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

// InitSchema creates audit tables required for session and token usage
// tracking. Returns nil on success.
func InitSchema(db *sql.DB) error {
	for _, stmt := range sessionsDDL {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("audit init schema: %w", err)
		}
	}
	return nil
}
