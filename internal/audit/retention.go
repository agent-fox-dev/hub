package audit

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// RetentionConfig holds all configurable retention thresholds.
// Default values are used when the corresponding environment variable
// is not set.
type RetentionConfig struct {
	MaxAgeDays              int // AF_AUDIT_MAX_AGE_DAYS, default 90
	MaxRuns                 int // AF_AUDIT_MAX_RUNS, default 50
	TraceMaxAgeDays         int // AF_TRACE_MAX_AGE_DAYS, default 30
	SessionMaxAgeDays       int // AF_SESSION_MAX_AGE_DAYS, default 90
	PostmortemMaxAgeDays    int // AF_POSTMORTEM_MAX_AGE_DAYS, default 180
	OrphanRetentionDays    int // AF_AUDIT_ORPHAN_RETENTION_DAYS, default 30
	SessionMaxActiveAgeDays int // AF_SESSION_MAX_ACTIVE_AGE_DAYS, default 7
}

// DefaultRetentionConfig returns a RetentionConfig with all default values.
func DefaultRetentionConfig() RetentionConfig {
	return RetentionConfig{
		MaxAgeDays:              90,
		MaxRuns:                 50,
		TraceMaxAgeDays:         30,
		SessionMaxAgeDays:       90,
		PostmortemMaxAgeDays:    180,
		OrphanRetentionDays:    30,
		SessionMaxActiveAgeDays: 7,
	}
}

// LoadRetentionConfigFromEnv returns a RetentionConfig populated from
// environment variables, falling back to defaults for any variable that
// is not set or cannot be parsed.
func LoadRetentionConfigFromEnv() RetentionConfig {
	cfg := DefaultRetentionConfig()
	cfg.MaxAgeDays = envIntOr("AF_AUDIT_MAX_AGE_DAYS", cfg.MaxAgeDays)
	cfg.MaxRuns = envIntOr("AF_AUDIT_MAX_RUNS", cfg.MaxRuns)
	cfg.TraceMaxAgeDays = envIntOr("AF_TRACE_MAX_AGE_DAYS", cfg.TraceMaxAgeDays)
	cfg.SessionMaxAgeDays = envIntOr("AF_SESSION_MAX_AGE_DAYS", cfg.SessionMaxAgeDays)
	cfg.PostmortemMaxAgeDays = envIntOr("AF_POSTMORTEM_MAX_AGE_DAYS", cfg.PostmortemMaxAgeDays)
	cfg.OrphanRetentionDays = envIntOr("AF_AUDIT_ORPHAN_RETENTION_DAYS", cfg.OrphanRetentionDays)
	cfg.SessionMaxActiveAgeDays = envIntOr("AF_SESSION_MAX_ACTIVE_AGE_DAYS", cfg.SessionMaxActiveAgeDays)
	return cfg
}

// envIntOr returns the integer value of the named environment variable,
// or defaultVal if the variable is not set or cannot be parsed.
func envIntOr(name string, defaultVal int) int {
	s := os.Getenv(name)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}

// StartRetentionWorker starts the background retention worker goroutine.
// It executes an immediate retention run before the first ticker fires,
// then repeats every hour. The goroutine exits when the context is cancelled.
func StartRetentionWorker(ctx context.Context, store Store, sqliteDB *sql.DB, m *Metrics) {
	// Extract the DuckDB *sql.DB from the Store.
	ds, ok := store.(*duckDBStore)
	if !ok {
		slog.Error("retention worker: store is not a *duckDBStore, cannot start")
		return
	}
	duckDB := ds.db

	cfg := LoadRetentionConfigFromEnv()

	// Execute an immediate retention run before the first ticker fires
	// (19-REQ-12.1, 19-REQ-12.E2).
	runRetentionWithLogging(ctx, duckDB, sqliteDB, cfg, m)

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runRetentionWithLogging(ctx, duckDB, sqliteDB, cfg, m)
		}
	}
}

// runRetentionWithLogging runs a retention pass and logs any error.
func runRetentionWithLogging(ctx context.Context, duckDB *sql.DB, sqliteDB *sql.DB, cfg RetentionConfig, m *Metrics) {
	if err := RunRetention(ctx, duckDB, sqliteDB, cfg, m); err != nil {
		slog.Error("retention run failed", "error", err)
	}
}

// retentionStepName maps step numbers to label names for afhub_retention_errors_total.
var retentionStepNames = []string{
	"pre_step_force_close",     // 0: pre-step
	"step_1_agent_records",     // 1
	"step_2_max_runs",          // 2
	"step_3_hub_events",        // 3
	"step_4_aged_sessions",     // 4
	"step_5_orphaned_usage",    // 5
	"step_6_aged_traces",       // 6
	"step_7_aged_postmortems",  // 7
	"step_8_orphaned_workspace", // 8
	"step_9_recalibrate_gauges", // 9
}

// RunRetention executes a single retention run with all steps.
// Returns nil if all steps complete successfully. If any step fails,
// it continues executing remaining steps and returns an error.
func RunRetention(ctx context.Context, duckDB *sql.DB, sqliteDB *sql.DB, cfg RetentionConfig, m *Metrics) error {
	var anyError bool
	deletedRows := make(map[string]int64)

	// Helper to run a step with context check and error handling.
	runStep := func(stepIdx int, name string, fn func() (int64, error)) {
		if ctx.Err() != nil {
			return
		}
		deleted, err := fn()
		if err != nil {
			anyError = true
			slog.Error("retention step failed",
				"step", name,
				"error", err,
			)
			if m != nil {
				m.RetentionErrorsTotal.WithLabelValues(retentionStepNames[stepIdx]).Inc()
			}
			return
		}
		deletedRows[name] = deleted
	}

	// Helper for steps that return only error (no row count).
	runStepNoCount := func(stepIdx int, name string, fn func() error) {
		if ctx.Err() != nil {
			return
		}
		err := fn()
		if err != nil {
			anyError = true
			slog.Error("retention step failed",
				"step", name,
				"error", err,
			)
			if m != nil {
				m.RetentionErrorsTotal.WithLabelValues(retentionStepNames[stepIdx]).Inc()
			}
		}
	}

	// Step 1: Delete aged agent records.
	runStep(1, "agent_audit_events", func() (int64, error) {
		return RetentionStep1_DeleteAgedAgentRecords(ctx, duckDB, cfg.MaxAgeDays)
	})

	// Step 2: Enforce max runs.
	runStep(2, "agent_audit_events_runs", func() (int64, error) {
		return RetentionStep2_EnforceMaxRuns(ctx, duckDB, cfg.MaxRuns)
	})

	// Step 3: Delete aged hub events.
	runStep(3, "hub_audit_events", func() (int64, error) {
		return RetentionStep3_DeleteAgedHubEvents(ctx, duckDB, cfg.MaxAgeDays)
	})

	// Pre-step before step 4: Force-close orphaned active sessions (19-REQ-13.11).
	runStep(0, "force_close_orphaned", func() (int64, error) {
		return RetentionPreStep_ForceCloseOrphanedSessions(ctx, duckDB, cfg.SessionMaxActiveAgeDays)
	})

	// Step 4: Delete aged terminal sessions.
	runStep(4, "agent_sessions", func() (int64, error) {
		return RetentionStep4_DeleteAgedSessions(ctx, duckDB, cfg.SessionMaxAgeDays)
	})

	// Step 5: Delete orphaned token_usage.
	runStep(5, "token_usage", func() (int64, error) {
		return RetentionStep5_DeleteOrphanedTokenUsage(ctx, duckDB)
	})

	// Step 6: Delete aged traces.
	runStep(6, "agent_traces", func() (int64, error) {
		return RetentionStep6_DeleteAgedTraces(ctx, duckDB, cfg.TraceMaxAgeDays)
	})

	// Step 7: Delete aged postmortems.
	runStep(7, "postmortems", func() (int64, error) {
		return RetentionStep7_DeleteAgedPostmortems(ctx, duckDB, cfg.PostmortemMaxAgeDays)
	})

	// Step 8: Delete orphaned workspace data.
	runStep(8, "orphaned_workspace", func() (int64, error) {
		return RetentionStep8_DeleteOrphanedWorkspaceData(ctx, duckDB, sqliteDB, cfg.OrphanRetentionDays)
	})

	// Step 9: Recalibrate gauges.
	runStepNoCount(9, "recalibrate_gauges", func() error {
		return RetentionStep9_RecalibrateGauges(ctx, duckDB, m)
	})

	// Check for context cancellation before final timestamp update.
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Step 10: Update timestamp and log (only if all steps succeeded).
	if anyError {
		return fmt.Errorf("retention run completed with errors")
	}

	if m != nil {
		m.RetentionLastRunTimestamp.Set(float64(time.Now().Unix()))
	}

	// Log rows deleted per table.
	for table, count := range deletedRows {
		if count > 0 {
			slog.Info("retention: rows deleted", "table", table, "count", count)
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Individual retention steps
// ---------------------------------------------------------------------------

// RetentionStep1_DeleteAgedAgentRecords deletes agent_audit_events older
// than maxAgeDays.
func RetentionStep1_DeleteAgedAgentRecords(ctx context.Context, db *sql.DB, maxAgeDays int) (int64, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	cutoff := time.Now().UTC().Add(-time.Duration(maxAgeDays) * 24 * time.Hour)
	result, err := db.ExecContext(ctx,
		`DELETE FROM agent_audit_events WHERE timestamp < CAST(? AS TIMESTAMPTZ)`,
		cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("delete aged agent_audit_events: %w", err)
	}
	return result.RowsAffected()
}

// RetentionStep2_EnforceMaxRuns deletes the oldest run_ids from
// agent_audit_events when the distinct count exceeds maxRuns.
func RetentionStep2_EnforceMaxRuns(ctx context.Context, db *sql.DB, maxRuns int) (int64, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	// Count distinct run_ids.
	var distinctCount int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT run_id) FROM agent_audit_events`).Scan(&distinctCount)
	if err != nil {
		return 0, fmt.Errorf("count distinct run_ids: %w", err)
	}

	if distinctCount <= maxRuns {
		return 0, nil
	}

	// Find the oldest runs to delete.
	excess := distinctCount - maxRuns
	rows, err := db.QueryContext(ctx,
		`SELECT run_id FROM (
			SELECT run_id, MIN(timestamp) AS min_ts
			FROM agent_audit_events
			GROUP BY run_id
			ORDER BY min_ts ASC
			LIMIT ?
		)`, excess)
	if err != nil {
		return 0, fmt.Errorf("find oldest runs: %w", err)
	}
	defer rows.Close()

	var runIDs []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return 0, fmt.Errorf("scan run_id: %w", err)
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate oldest runs: %w", err)
	}

	if len(runIDs) == 0 {
		return 0, nil
	}

	// Delete rows for the oldest runs.
	placeholders := make([]string, len(runIDs))
	args := make([]any, len(runIDs))
	for i, id := range runIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		`DELETE FROM agent_audit_events WHERE run_id IN (%s)`,
		joinStrings(placeholders, ","))
	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("delete oldest runs: %w", err)
	}
	return result.RowsAffected()
}

// RetentionStep3_DeleteAgedHubEvents deletes hub_audit_events older than
// maxAgeDays.
func RetentionStep3_DeleteAgedHubEvents(ctx context.Context, db *sql.DB, maxAgeDays int) (int64, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	cutoff := time.Now().UTC().Add(-time.Duration(maxAgeDays) * 24 * time.Hour)
	result, err := db.ExecContext(ctx,
		`DELETE FROM hub_audit_events WHERE ingested_at < CAST(? AS TIMESTAMPTZ)`,
		cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("delete aged hub_audit_events: %w", err)
	}
	return result.RowsAffected()
}

// RetentionStep4_DeleteAgedSessions deletes completed/failed/timeout/terminated
// sessions older than sessionMaxAgeDays. Active sessions are never deleted.
func RetentionStep4_DeleteAgedSessions(ctx context.Context, db *sql.DB, sessionMaxAgeDays int) (int64, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	cutoff := time.Now().UTC().Add(-time.Duration(sessionMaxAgeDays) * 24 * time.Hour)
	result, err := db.ExecContext(ctx,
		`DELETE FROM agent_sessions
		 WHERE status IN ('completed', 'failed', 'timeout', 'terminated')
		   AND started_at < CAST(? AS TIMESTAMPTZ)`,
		cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("delete aged agent_sessions: %w", err)
	}
	return result.RowsAffected()
}

// RetentionStep5_DeleteOrphanedTokenUsage deletes token_usage rows whose
// session_id does not exist in agent_sessions.
func RetentionStep5_DeleteOrphanedTokenUsage(ctx context.Context, db *sql.DB) (int64, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	result, err := db.ExecContext(ctx,
		`DELETE FROM token_usage
		 WHERE session_id NOT IN (SELECT id FROM agent_sessions)`)
	if err != nil {
		return 0, fmt.Errorf("delete orphaned token_usage: %w", err)
	}
	return result.RowsAffected()
}

// RetentionStep6_DeleteAgedTraces deletes agent_traces older than
// traceMaxAgeDays.
func RetentionStep6_DeleteAgedTraces(ctx context.Context, db *sql.DB, traceMaxAgeDays int) (int64, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	cutoff := time.Now().UTC().Add(-time.Duration(traceMaxAgeDays) * 24 * time.Hour)
	result, err := db.ExecContext(ctx,
		`DELETE FROM agent_traces WHERE timestamp < CAST(? AS TIMESTAMPTZ)`,
		cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("delete aged agent_traces: %w", err)
	}
	return result.RowsAffected()
}

// RetentionStep7_DeleteAgedPostmortems deletes postmortems older than
// postmortemMaxAgeDays.
func RetentionStep7_DeleteAgedPostmortems(ctx context.Context, db *sql.DB, postmortemMaxAgeDays int) (int64, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	cutoff := time.Now().UTC().Add(-time.Duration(postmortemMaxAgeDays) * 24 * time.Hour)
	result, err := db.ExecContext(ctx,
		`DELETE FROM postmortems WHERE ingested_at < CAST(? AS TIMESTAMPTZ)`,
		cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("delete aged postmortems: %w", err)
	}
	return result.RowsAffected()
}

// RetentionStep8_DeleteOrphanedWorkspaceData deletes audit records for
// workspaces no longer in SQLite, older than orphanRetentionDays.
func RetentionStep8_DeleteOrphanedWorkspaceData(ctx context.Context, duckDB *sql.DB, sqliteDB *sql.DB, orphanRetentionDays int) (int64, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	// Query workspace slugs from SQLite.
	rows, err := sqliteDB.QueryContext(ctx, `SELECT slug FROM workspaces`)
	if err != nil {
		return 0, fmt.Errorf("query workspace slugs from SQLite: %w", err)
	}
	defer rows.Close()

	liveWorkspaces := make(map[string]bool)
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return 0, fmt.Errorf("scan workspace slug: %w", err)
		}
		liveWorkspaces[slug] = true
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate workspace slugs: %w", err)
	}

	cutoff := time.Now().UTC().Add(-time.Duration(orphanRetentionDays) * 24 * time.Hour)
	cutoffStr := cutoff.Format(time.RFC3339Nano)

	// Tables with a 'workspace' column for orphan detection.
	// agent_sessions and token_usage use 'workspace_slug' instead.
	type tableCol struct {
		table  string
		column string
	}
	tables := []tableCol{
		{"agent_audit_events", "workspace"},
		{"hub_audit_events", "workspace"},
		{"session_outcomes", "workspace"},
		{"tool_calls", "workspace"},
		{"tool_errors", "workspace"},
		{"agent_traces", "workspace"},
		{"postmortems", "workspace"},
		{"agent_sessions", "workspace_slug"},
		{"token_usage", "workspace_slug"},
	}

	var totalDeleted int64

	for _, tc := range tables {
		if ctx.Err() != nil {
			return totalDeleted, ctx.Err()
		}

		if len(liveWorkspaces) == 0 {
			// No workspaces in SQLite — all records are potentially orphaned.
			// Apply grace period filter only.
			result, err := duckDB.ExecContext(ctx,
				fmt.Sprintf(`DELETE FROM %s WHERE ingested_at < CAST(? AS TIMESTAMPTZ)`, tc.table),
				cutoffStr)
			if err != nil {
				return totalDeleted, fmt.Errorf("delete orphaned %s (all orphaned): %w", tc.table, err)
			}
			n, _ := result.RowsAffected()
			totalDeleted += n
			continue
		}

		// Build IN clause for live workspaces.
		placeholders := make([]string, 0, len(liveWorkspaces))
		args := make([]any, 0, len(liveWorkspaces)+1)
		for slug := range liveWorkspaces {
			placeholders = append(placeholders, "?")
			args = append(args, slug)
		}
		args = append(args, cutoffStr)

		query := fmt.Sprintf(
			`DELETE FROM %s WHERE %s NOT IN (%s) AND ingested_at < CAST(? AS TIMESTAMPTZ)`,
			tc.table, tc.column, joinStrings(placeholders, ","))
		result, err := duckDB.ExecContext(ctx, query, args...)
		if err != nil {
			return totalDeleted, fmt.Errorf("delete orphaned %s: %w", tc.table, err)
		}
		n, _ := result.RowsAffected()
		totalDeleted += n
	}

	return totalDeleted, nil
}

// RetentionStep9_RecalibrateGauges updates afhub_audit_table_rows with
// COUNT(*) per table and recalibrates afhub_agent_sessions_active.
func RetentionStep9_RecalibrateGauges(ctx context.Context, db *sql.DB, m *Metrics) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if m == nil {
		return nil
	}

	// Update afhub_audit_table_rows gauge with COUNT(*) per table.
	// Reset first to clear stale label combinations.
	m.AuditTableRows.Reset()

	auditTables := []string{
		"agent_audit_events",
		"hub_audit_events",
		"session_outcomes",
		"tool_calls",
		"tool_errors",
		"agent_traces",
		"postmortems",
		"agent_sessions",
		"token_usage",
	}

	for _, table := range auditTables {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var count int64
		err := db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count)
		if err != nil {
			return fmt.Errorf("count rows in %s: %w", table, err)
		}
		m.AuditTableRows.WithLabelValues(table).Set(float64(count))
	}

	// Recalibrate afhub_agent_sessions_active gauge.
	// Reset to clear stale label combinations (19-REQ-13.9).
	m.AgentSessionsActive.Reset()

	rows, err := db.QueryContext(ctx,
		`SELECT workspace_slug, COUNT(*) AS cnt
		 FROM agent_sessions
		 WHERE status = 'active'
		 GROUP BY workspace_slug`)
	if err != nil {
		return fmt.Errorf("count active sessions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ws string
		var count int64
		if err := rows.Scan(&ws, &count); err != nil {
			return fmt.Errorf("scan active session count: %w", err)
		}
		m.AgentSessionsActive.WithLabelValues(ws).Set(float64(count))
	}
	return rows.Err()
}

// RetentionPreStep_ForceCloseOrphanedSessions force-closes active sessions
// older than sessionMaxActiveAgeDays by setting status='timeout'.
func RetentionPreStep_ForceCloseOrphanedSessions(ctx context.Context, db *sql.DB, sessionMaxActiveAgeDays int) (int64, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	cutoff := time.Now().UTC().Add(-time.Duration(sessionMaxActiveAgeDays) * 24 * time.Hour)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := db.ExecContext(ctx,
		`UPDATE agent_sessions
		 SET status = 'timeout', ended_at = CAST(? AS TIMESTAMPTZ)
		 WHERE status = 'active' AND started_at < CAST(? AS TIMESTAMPTZ)`,
		now, cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("force-close orphaned active sessions: %w", err)
	}
	return result.RowsAffected()
}

// joinStrings joins a slice of strings with a separator.
func joinStrings(elems []string, sep string) string {
	if len(elems) == 0 {
		return ""
	}
	result := elems[0]
	for _, e := range elems[1:] {
		result += sep + e
	}
	return result
}
