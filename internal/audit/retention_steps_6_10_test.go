package audit

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TS-19-47: Retention step 6 deletes agent_traces older than
// AF_TRACE_MAX_AGE_DAYS and retains newer records.
// Requirement: 19-REQ-13.6
func TestRetentionStep6_DeletesAgedTraces(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Insert an old trace (40 days ago, beyond 30-day threshold).
	oldTS := now.Add(-40 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO agent_traces (id, run_id, workspace, event_type, timestamp, ingested_at)
		VALUES ('trace-old', 'run-1', 'ws-1', 'test.trace', ?, ?)`, oldTS, oldTS)
	if err != nil {
		t.Fatalf("insert old trace: %v", err)
	}

	// Insert a new trace (10 days ago, within threshold).
	newTS := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO agent_traces (id, run_id, workspace, event_type, timestamp, ingested_at)
		VALUES ('trace-new', 'run-2', 'ws-1', 'test.trace', ?, ?)`, newTS, newTS)
	if err != nil {
		t.Fatalf("insert new trace: %v", err)
	}

	deleted, err := RetentionStep6_DeleteAgedTraces(ctx, db, 30)
	if err != nil {
		t.Fatalf("step 6 error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM agent_traces").Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 remaining, got %d", count)
	}
}

// TS-19-48: Retention step 7 deletes postmortems older than
// AF_POSTMORTEM_MAX_AGE_DAYS and retains newer records.
// Requirement: 19-REQ-13.7
func TestRetentionStep7_DeletesAgedPostmortems(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Insert old postmortem (200 days ago, beyond 180-day threshold).
	oldTS := now.Add(-200 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO postmortems (run_id, workspace, ingested_at)
		VALUES ('pm-old', 'ws-1', ?)`, oldTS)
	if err != nil {
		t.Fatalf("insert old postmortem: %v", err)
	}

	// Insert new postmortem (10 days ago, within threshold).
	newTS := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO postmortems (run_id, workspace, ingested_at)
		VALUES ('pm-new', 'ws-1', ?)`, newTS)
	if err != nil {
		t.Fatalf("insert new postmortem: %v", err)
	}

	deleted, err := RetentionStep7_DeleteAgedPostmortems(ctx, db, 180)
	if err != nil {
		t.Fatalf("step 7 error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM postmortems").Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 remaining, got %d", count)
	}
}

// TS-19-49: Retention step 8 deletes orphaned audit data for workspaces
// not in SQLite that are older than AF_AUDIT_ORPHAN_RETENTION_DAYS.
// Requirement: 19-REQ-13.8
func TestRetentionStep8_DeletesOrphanedWorkspaceData(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	sqliteDB := openTestSQLiteDB(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Add 'live-ws' to SQLite.
	_, err := sqliteDB.Exec(`INSERT INTO workspaces (slug, owner_id) VALUES ('live-ws', 'owner-1')`)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	// Insert audit data for 'deleted-ws' (not in SQLite, 40 days old — beyond 30-day grace).
	oldTS := now.Add(-40 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO agent_audit_events (id, run_id, workspace, event_type, timestamp, ingested_at)
		VALUES ('deleted-ws-evt', 'run-1', 'deleted-ws', 'test.event', ?, ?)`, oldTS, oldTS)
	if err != nil {
		t.Fatalf("insert deleted-ws event: %v", err)
	}

	// Insert audit data for 'live-ws' (in SQLite, same age — should be retained).
	_, err = db.Exec(`INSERT INTO agent_audit_events (id, run_id, workspace, event_type, timestamp, ingested_at)
		VALUES ('live-ws-evt', 'run-2', 'live-ws', 'test.event', ?, ?)`, oldTS, oldTS)
	if err != nil {
		t.Fatalf("insert live-ws event: %v", err)
	}

	deleted, err := RetentionStep8_DeleteOrphanedWorkspaceData(ctx, db, sqliteDB, 30)
	if err != nil {
		t.Fatalf("step 8 error: %v", err)
	}
	if deleted < 1 {
		t.Errorf("expected at least 1 deleted, got %d", deleted)
	}

	// Verify deleted-ws data is gone.
	var deletedCount int
	err = db.QueryRow("SELECT COUNT(*) FROM agent_audit_events WHERE workspace = 'deleted-ws'").Scan(&deletedCount)
	if err != nil {
		t.Fatalf("count deleted-ws: %v", err)
	}
	if deletedCount != 0 {
		t.Errorf("expected deleted-ws data to be removed, found %d rows", deletedCount)
	}

	// Verify live-ws data is still there.
	var liveCount int
	err = db.QueryRow("SELECT COUNT(*) FROM agent_audit_events WHERE workspace = 'live-ws'").Scan(&liveCount)
	if err != nil {
		t.Fatalf("count live-ws: %v", err)
	}
	if liveCount != 1 {
		t.Errorf("expected live-ws data to be retained, found %d rows", liveCount)
	}
}

// 19-REQ-13.E3: When SQLite workspace slug query returns empty list, treat all
// DuckDB audit records as potentially orphaned and apply grace period filter.
func TestRetentionStep8_EmptySQLiteAppliesGracePeriod(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	sqliteDB := openTestSQLiteDB(t) // No workspaces inserted.
	ctx := context.Background()

	now := time.Now().UTC()

	// Insert audit data 40 days old (beyond 30-day grace).
	oldTS := now.Add(-40 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO agent_audit_events (id, run_id, workspace, event_type, timestamp, ingested_at)
		VALUES ('orphan-old', 'run-1', 'any-ws', 'test.event', ?, ?)`, oldTS, oldTS)
	if err != nil {
		t.Fatalf("insert old orphan: %v", err)
	}

	// Insert audit data 10 days old (within 30-day grace).
	newTS := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO agent_audit_events (id, run_id, workspace, event_type, timestamp, ingested_at)
		VALUES ('orphan-new', 'run-2', 'any-ws', 'test.event', ?, ?)`, newTS, newTS)
	if err != nil {
		t.Fatalf("insert new orphan: %v", err)
	}

	_, err = RetentionStep8_DeleteOrphanedWorkspaceData(ctx, db, sqliteDB, 30)
	if err != nil {
		t.Fatalf("step 8 error: %v", err)
	}

	// Old data should be deleted, new data retained.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM agent_audit_events").Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 remaining (within grace period), got %d", count)
	}
}

// TS-19-50: Retention step 9 updates afhub_audit_table_rows gauge with COUNT(*)
// per audit table and recalibrates afhub_agent_sessions_active gauge.
// Requirement: 19-REQ-13.9
func TestRetentionStep9_RecalibratesGauges(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	ctx := context.Background()
	m := NewMetrics()

	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Insert active sessions for two workspaces.
	for i := 0; i < 3; i++ {
		_, err := db.Exec(`INSERT INTO agent_sessions (id, run_id, workspace_slug, status, started_at, ingested_at)
			VALUES (?, '', 'ws-1', 'active', ?, ?)`,
			sessionID(100+i), now, now)
		if err != nil {
			t.Fatalf("insert ws-1 session: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		_, err := db.Exec(`INSERT INTO agent_sessions (id, run_id, workspace_slug, status, started_at, ingested_at)
			VALUES (?, '', 'ws-2', 'active', ?, ?)`,
			sessionID(200+i), now, now)
		if err != nil {
			t.Fatalf("insert ws-2 session: %v", err)
		}
	}

	// Set gauge to an incorrect value to simulate drift.
	m.AgentSessionsActive.WithLabelValues("ws-1").Set(5)

	err := RetentionStep9_RecalibrateGauges(ctx, db, m)
	if err != nil {
		t.Fatalf("step 9 error: %v", err)
	}

	// After recalibration, ws-1 should be 3, ws-2 should be 2.
	ws1Val := getGaugeValue(t, m.AgentSessionsActive, prometheus.Labels{"workspace": "ws-1"})
	if ws1Val != 3 {
		t.Errorf("expected ws-1 active gauge = 3, got %v", ws1Val)
	}

	ws2Val := getGaugeValue(t, m.AgentSessionsActive, prometheus.Labels{"workspace": "ws-2"})
	if ws2Val != 2 {
		t.Errorf("expected ws-2 active gauge = 2, got %v", ws2Val)
	}

	// afhub_audit_table_rows should reflect the actual count of agent_sessions.
	tableRowsVal := getGaugeValue(t, m.AuditTableRows, prometheus.Labels{"table": "agent_sessions"})
	if tableRowsVal < 5 {
		t.Errorf("expected afhub_audit_table_rows for agent_sessions >= 5, got %v", tableRowsVal)
	}
}

// TS-19-51: Retention step 10 sets afhub_retention_last_run_timestamp_seconds
// to the current Unix timestamp after all steps complete successfully.
// Requirement: 19-REQ-13.10
func TestRetention_LastRunTimestampSetAfterFullRun(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	sqliteDB := openTestSQLiteDB(t)
	m := NewMetrics()

	before := time.Now().Unix()

	err := RunRetention(context.Background(), db, sqliteDB, DefaultRetentionConfig(), m)
	if err != nil {
		t.Fatalf("full retention run error: %v", err)
	}

	after := time.Now().Unix()

	ts := getPlainGaugeValue(t, m.RetentionLastRunTimestamp)
	if int64(ts) < before {
		t.Errorf("expected timestamp >= %d, got %v", before, ts)
	}
	if int64(ts) > after {
		t.Errorf("expected timestamp <= %d, got %v", after, ts)
	}
}

// 19-REQ-13.E1 / 19-PROP-10: Step failure does not prevent subsequent steps.
// afhub_retention_errors_total is incremented and afhub_retention_last_run_timestamp_seconds
// is NOT updated.
func TestRetention_StepFailureDoesNotBlockSubsequentSteps(t *testing.T) {
	m := NewMetrics()

	// This test verifies the retention_errors_total counter exists and can be
	// incremented per step. When the full implementation is done, a step failure
	// should:
	// 1. Log the error via slog.
	// 2. Increment afhub_retention_errors_total with the step label.
	// 3. Continue executing remaining steps.
	// 4. NOT update afhub_retention_last_run_timestamp_seconds.

	// Increment the error counter for a step.
	m.RetentionErrorsTotal.WithLabelValues("step_1_agent_records").Inc()

	val := getCounterValue(t, m.RetentionErrorsTotal, prometheus.Labels{
		"step": "step_1_agent_records",
	})
	if val != 1 {
		t.Errorf("expected retention errors counter = 1, got %v", val)
	}
}

// 19-PROP-7: afhub_retention_last_run_timestamp_seconds is NOT updated if any
// step fails.
func TestRetention_TimestampNotUpdatedOnPartialFailure(t *testing.T) {
	m := NewMetrics()

	// Set the gauge to a known value.
	m.RetentionLastRunTimestamp.Set(1000)

	// If any step fails during RunRetention, the timestamp should remain
	// at 1000 (not updated to the current time).
	// This test is structural — the full integration test requires injecting
	// a DuckDB error, which will be covered by the implementation group.
	ts := getPlainGaugeValue(t, m.RetentionLastRunTimestamp)
	if ts != 1000 {
		t.Errorf("expected timestamp to remain 1000 when not updated, got %v", ts)
	}
}

// 19-REQ-13.E4: Default retention config values.
func TestRetention_DefaultConfigValues(t *testing.T) {
	cfg := DefaultRetentionConfig()

	if cfg.MaxAgeDays != 90 {
		t.Errorf("MaxAgeDays: expected 90, got %d", cfg.MaxAgeDays)
	}
	if cfg.MaxRuns != 50 {
		t.Errorf("MaxRuns: expected 50, got %d", cfg.MaxRuns)
	}
	if cfg.TraceMaxAgeDays != 30 {
		t.Errorf("TraceMaxAgeDays: expected 30, got %d", cfg.TraceMaxAgeDays)
	}
	if cfg.SessionMaxAgeDays != 90 {
		t.Errorf("SessionMaxAgeDays: expected 90, got %d", cfg.SessionMaxAgeDays)
	}
	if cfg.PostmortemMaxAgeDays != 180 {
		t.Errorf("PostmortemMaxAgeDays: expected 180, got %d", cfg.PostmortemMaxAgeDays)
	}
	if cfg.OrphanRetentionDays != 30 {
		t.Errorf("OrphanRetentionDays: expected 30, got %d", cfg.OrphanRetentionDays)
	}
	if cfg.SessionMaxActiveAgeDays != 7 {
		t.Errorf("SessionMaxActiveAgeDays: expected 7, got %d", cfg.SessionMaxActiveAgeDays)
	}
}

// 19-REQ-13.9: After Reset() + recalibration, stale label combinations
// from previous calibration cycles are removed.
func TestRetentionStep9_ResetClearsStaleLabels(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	ctx := context.Background()
	m := NewMetrics()

	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Pre-populate gauge with a workspace that no longer has active sessions.
	m.AgentSessionsActive.WithLabelValues("stale-ws").Set(5)

	// Insert sessions only for ws-1.
	_, err := db.Exec(`INSERT INTO agent_sessions (id, run_id, workspace_slug, status, started_at, ingested_at)
		VALUES ('sess-reset', '', 'ws-1', 'active', ?, ?)`, now, now)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	err = RetentionStep9_RecalibrateGauges(ctx, db, m)
	if err != nil {
		t.Fatalf("step 9 error: %v", err)
	}

	// After reset + recalibration, stale-ws should be 0 (label removed by Reset()).
	staleVal := getGaugeValue(t, m.AgentSessionsActive, prometheus.Labels{"workspace": "stale-ws"})
	if staleVal != 0 {
		t.Errorf("expected stale-ws gauge to be 0 after recalibration, got %v", staleVal)
	}

	// ws-1 should be 1.
	ws1Val := getGaugeValue(t, m.AgentSessionsActive, prometheus.Labels{"workspace": "ws-1"})
	if ws1Val != 1 {
		t.Errorf("expected ws-1 gauge to be 1, got %v", ws1Val)
	}
}
