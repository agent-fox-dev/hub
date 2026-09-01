package audit

import (
	"context"
	"database/sql"
	"errors"
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

// errNotImplemented is the sentinel error returned by stub retention functions.
var errNotImplemented = errors.New("not implemented")

// StartRetentionWorker starts the background retention worker goroutine.
// It executes an immediate retention run before the first ticker fires,
// then repeats every hour. The goroutine exits when the context is cancelled.
func StartRetentionWorker(_ context.Context, _ Store, _ *sql.DB, _ *Metrics) {
	// TODO(spec-19): Implement retention worker loop.
	// Stub: returns immediately — lifecycle tests will fail on timing assertions.
}

// RunRetention executes a single retention run with all steps.
// Returns nil if all steps complete successfully. If any step fails,
// it continues executing remaining steps and returns an error.
func RunRetention(_ context.Context, _ *sql.DB, _ *sql.DB, _ RetentionConfig, _ *Metrics) error {
	// TODO(spec-19): Execute all retention steps.
	return errNotImplemented
}

// RetentionStep1_DeleteAgedAgentRecords deletes agent_audit_events older
// than maxAgeDays.
func RetentionStep1_DeleteAgedAgentRecords(_ context.Context, _ *sql.DB, _ int) (int64, error) {
	return 0, errNotImplemented
}

// RetentionStep2_EnforceMaxRuns deletes the oldest run_ids from
// agent_audit_events when the distinct count exceeds maxRuns.
func RetentionStep2_EnforceMaxRuns(_ context.Context, _ *sql.DB, _ int) (int64, error) {
	return 0, errNotImplemented
}

// RetentionStep3_DeleteAgedHubEvents deletes hub_audit_events older than
// maxAgeDays.
func RetentionStep3_DeleteAgedHubEvents(_ context.Context, _ *sql.DB, _ int) (int64, error) {
	return 0, errNotImplemented
}

// RetentionStep4_DeleteAgedSessions deletes completed/failed/timeout/terminated
// sessions older than sessionMaxAgeDays. Active sessions are never deleted.
func RetentionStep4_DeleteAgedSessions(_ context.Context, _ *sql.DB, _ int) (int64, error) {
	return 0, errNotImplemented
}

// RetentionStep5_DeleteOrphanedTokenUsage deletes token_usage rows whose
// session_id does not exist in agent_sessions.
func RetentionStep5_DeleteOrphanedTokenUsage(_ context.Context, _ *sql.DB) (int64, error) {
	return 0, errNotImplemented
}

// RetentionStep6_DeleteAgedTraces deletes agent_traces older than
// traceMaxAgeDays.
func RetentionStep6_DeleteAgedTraces(_ context.Context, _ *sql.DB, _ int) (int64, error) {
	return 0, errNotImplemented
}

// RetentionStep7_DeleteAgedPostmortems deletes postmortems older than
// postmortemMaxAgeDays.
func RetentionStep7_DeleteAgedPostmortems(_ context.Context, _ *sql.DB, _ int) (int64, error) {
	return 0, errNotImplemented
}

// RetentionStep8_DeleteOrphanedWorkspaceData deletes audit records for
// workspaces no longer in SQLite, older than orphanRetentionDays.
func RetentionStep8_DeleteOrphanedWorkspaceData(_ context.Context, _ *sql.DB, _ *sql.DB, _ int) (int64, error) {
	return 0, errNotImplemented
}

// RetentionStep9_RecalibrateGauges updates afhub_audit_table_rows with
// COUNT(*) per table and recalibrates afhub_agent_sessions_active.
func RetentionStep9_RecalibrateGauges(_ context.Context, _ *sql.DB, _ *Metrics) error {
	return errNotImplemented
}

// RetentionPreStep_ForceCloseOrphanedSessions force-closes active sessions
// older than sessionMaxActiveAgeDays by setting status='timeout'.
func RetentionPreStep_ForceCloseOrphanedSessions(_ context.Context, _ *sql.DB, _ int) (int64, error) {
	return 0, errNotImplemented
}
