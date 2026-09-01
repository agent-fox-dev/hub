package audit

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TS-19-42: Retention step 1 deletes agent records older than
// AF_AUDIT_MAX_AGE_DAYS and retains newer records.
// Requirement: 19-REQ-13.1
func TestRetentionStep1_DeletesAgedAgentRecords(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Insert an old record (100 days ago).
	oldTS := now.Add(-100 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO agent_audit_events (id, run_id, workspace, event_type, timestamp, ingested_at)
		VALUES ('old-1', 'run-1', 'ws-1', 'test.event', ?, ?)`, oldTS, oldTS)
	if err != nil {
		t.Fatalf("insert old record: %v", err)
	}

	// Insert a new record (10 days ago).
	newTS := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO agent_audit_events (id, run_id, workspace, event_type, timestamp, ingested_at)
		VALUES ('new-1', 'run-2', 'ws-1', 'test.event', ?, ?)`, newTS, newTS)
	if err != nil {
		t.Fatalf("insert new record: %v", err)
	}

	deleted, err := RetentionStep1_DeleteAgedAgentRecords(ctx, db, 90)
	if err != nil {
		t.Fatalf("step 1 error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM agent_audit_events").Scan(&count)
	if err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 remaining row, got %d", count)
	}

	// Verify it's the new record that was retained.
	var id string
	err = db.QueryRow("SELECT id FROM agent_audit_events").Scan(&id)
	if err != nil {
		t.Fatalf("select remaining: %v", err)
	}
	if id != "new-1" {
		t.Errorf("expected 'new-1' to be retained, got %q", id)
	}
}

// TS-19-43: Retention step 2 deletes oldest runs when distinct run_id count
// exceeds AF_AUDIT_MAX_RUNS, determined by MIN(timestamp) per run_id.
// Requirement: 19-REQ-13.2
func TestRetentionStep2_EnforcesMaxRuns(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Insert 52 distinct runs. The two oldest should be pruned when max_runs=50.
	for i := 0; i < 52; i++ {
		runID := fmt.Sprintf("run-%03d", i)
		// Oldest runs have the smallest i → earliest timestamps.
		ts := now.Add(time.Duration(-(52-i)*24) * time.Hour).Format(time.RFC3339Nano)
		_, err := db.Exec(`INSERT INTO agent_audit_events (id, run_id, workspace, event_type, timestamp, ingested_at)
			VALUES (?, ?, 'ws-1', 'test.event', ?, ?)`,
			fmt.Sprintf("evt-%03d", i), runID, ts, ts)
		if err != nil {
			t.Fatalf("insert run %d: %v", i, err)
		}
	}

	deleted, err := RetentionStep2_EnforceMaxRuns(ctx, db, 50)
	if err != nil {
		t.Fatalf("step 2 error: %v", err)
	}
	if deleted != 2 {
		t.Errorf("expected 2 rows deleted, got %d", deleted)
	}

	// Verify distinct run count.
	var distinctCount int
	err = db.QueryRow("SELECT COUNT(DISTINCT run_id) FROM agent_audit_events").Scan(&distinctCount)
	if err != nil {
		t.Fatalf("distinct count query: %v", err)
	}
	if distinctCount != 50 {
		t.Errorf("expected 50 distinct runs, got %d", distinctCount)
	}

	// Verify oldest runs were removed (run-000 and run-001).
	for _, oldRun := range []string{"run-000", "run-001"} {
		var c int
		err = db.QueryRow("SELECT COUNT(*) FROM agent_audit_events WHERE run_id = ?", oldRun).Scan(&c)
		if err != nil {
			t.Fatalf("check run %s: %v", oldRun, err)
		}
		if c != 0 {
			t.Errorf("expected run %s to be deleted, but found %d rows", oldRun, c)
		}
	}
}

// 19-REQ-13.E2: When run_id count is already at or below max, skip deletion.
func TestRetentionStep2_SkipsWhenBelowMax(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Insert only 5 distinct runs, well below max_runs=50.
	for i := 0; i < 5; i++ {
		ts := now.Add(time.Duration(-i*24) * time.Hour).Format(time.RFC3339Nano)
		_, err := db.Exec(`INSERT INTO agent_audit_events (id, run_id, workspace, event_type, timestamp, ingested_at)
			VALUES (?, ?, 'ws-1', 'test.event', ?, ?)`,
			fmt.Sprintf("evt-skip-%d", i), fmt.Sprintf("run-skip-%d", i), ts, ts)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	deleted, err := RetentionStep2_EnforceMaxRuns(ctx, db, 50)
	if err != nil {
		t.Fatalf("step 2 error: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deleted when below max, got %d", deleted)
	}

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM agent_audit_events").Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 5 {
		t.Errorf("expected all 5 rows retained, got %d", count)
	}
}

// TS-19-44: Retention step 3 deletes hub_audit_events older than
// AF_AUDIT_MAX_AGE_DAYS and retains newer records.
// Requirement: 19-REQ-13.3
func TestRetentionStep3_DeletesAgedHubEvents(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Insert old hub event (100 days ago).
	oldTS := now.Add(-100 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO hub_audit_events (id, event_type, ingested_at)
		VALUES ('hub-old', 'test.event', ?)`, oldTS)
	if err != nil {
		t.Fatalf("insert old hub event: %v", err)
	}

	// Insert new hub event (10 days ago).
	newTS := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO hub_audit_events (id, event_type, ingested_at)
		VALUES ('hub-new', 'test.event', ?)`, newTS)
	if err != nil {
		t.Fatalf("insert new hub event: %v", err)
	}

	deleted, err := RetentionStep3_DeleteAgedHubEvents(ctx, db, 90)
	if err != nil {
		t.Fatalf("step 3 error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM hub_audit_events").Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 remaining, got %d", count)
	}
}

// TS-19-45: Retention step 4 deletes completed/failed/timeout/terminated
// sessions older than AF_SESSION_MAX_AGE_DAYS but never deletes active sessions.
// Requirement: 19-REQ-13.4
func TestRetentionStep4_DeletesAgedTerminalSessions(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	ctx := context.Background()

	now := time.Now().UTC()
	oldTS := now.Add(-100 * 24 * time.Hour).Format(time.RFC3339Nano)
	newTS := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano)

	// Old completed session (should be deleted).
	_, err := db.Exec(`INSERT INTO agent_sessions (id, run_id, workspace_slug, status, started_at, ingested_at)
		VALUES ('old-completed', '', 'ws-1', 'completed', ?, ?)`, oldTS, oldTS)
	if err != nil {
		t.Fatalf("insert old completed: %v", err)
	}

	// Old active session (should NOT be deleted — active sessions are never deleted by retention).
	_, err = db.Exec(`INSERT INTO agent_sessions (id, run_id, workspace_slug, status, started_at, ingested_at)
		VALUES ('old-active', '', 'ws-1', 'active', ?, ?)`, oldTS, oldTS)
	if err != nil {
		t.Fatalf("insert old active: %v", err)
	}

	// New completed session (should be retained — within threshold).
	_, err = db.Exec(`INSERT INTO agent_sessions (id, run_id, workspace_slug, status, started_at, ingested_at)
		VALUES ('new-completed', '', 'ws-1', 'completed', ?, ?)`, newTS, newTS)
	if err != nil {
		t.Fatalf("insert new completed: %v", err)
	}

	deleted, err := RetentionStep4_DeleteAgedSessions(ctx, db, 90)
	if err != nil {
		t.Fatalf("step 4 error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	// Should have 2 rows remaining: old-active and new-completed.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM agent_sessions").Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 remaining sessions, got %d", count)
	}

	// Verify the active session was retained.
	var activeCount int
	err = db.QueryRow("SELECT COUNT(*) FROM agent_sessions WHERE status = 'active'").Scan(&activeCount)
	if err != nil {
		t.Fatalf("active count: %v", err)
	}
	if activeCount != 1 {
		t.Errorf("expected 1 active session retained, got %d", activeCount)
	}
}

// TS-19-46: Retention step 5 deletes orphaned token_usage rows whose
// session_id no longer exists in agent_sessions.
// Requirement: 19-REQ-13.5
func TestRetentionStep5_DeletesOrphanedTokenUsage(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	ctx := context.Background()

	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Insert a live session.
	_, err := db.Exec(`INSERT INTO agent_sessions (id, run_id, workspace_slug, status, started_at, ingested_at)
		VALUES ('live-sess', '', 'ws-1', 'active', ?, ?)`, now, now)
	if err != nil {
		t.Fatalf("insert live session: %v", err)
	}

	// Insert token_usage for the live session (should be retained).
	_, err = db.Exec(`INSERT INTO token_usage (id, session_id, workspace_slug, model, reported_at, ingested_at)
		VALUES ('usage-live', 'live-sess', 'ws-1', 'model-1', ?, ?)`, now, now)
	if err != nil {
		t.Fatalf("insert live usage: %v", err)
	}

	// Insert orphaned token_usage (session_id doesn't exist in agent_sessions).
	_, err = db.Exec(`INSERT INTO token_usage (id, session_id, workspace_slug, model, reported_at, ingested_at)
		VALUES ('usage-orphan', 'deleted-sess', 'ws-1', 'model-1', ?, ?)`, now, now)
	if err != nil {
		t.Fatalf("insert orphaned usage: %v", err)
	}

	deleted, err := RetentionStep5_DeleteOrphanedTokenUsage(ctx, db)
	if err != nil {
		t.Fatalf("step 5 error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	// Verify only the live usage remains.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM token_usage").Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 remaining, got %d", count)
	}

	var remainingID string
	err = db.QueryRow("SELECT session_id FROM token_usage").Scan(&remainingID)
	if err != nil {
		t.Fatalf("select remaining: %v", err)
	}
	if remainingID != "live-sess" {
		t.Errorf("expected 'live-sess' retained, got %q", remainingID)
	}
}

// 19-REQ-13.11: Pre-step force-closes active sessions older than
// AF_SESSION_MAX_ACTIVE_AGE_DAYS.
func TestRetentionPreStep_ForceClosesOrphanedActiveSessions(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	ctx := context.Background()

	now := time.Now().UTC()
	oldTS := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano)

	// Insert an active session that is 10 days old (older than 7-day threshold).
	_, err := db.Exec(`INSERT INTO agent_sessions (id, run_id, workspace_slug, status, started_at, ingested_at)
		VALUES ('orphan-active', '', 'ws-1', 'active', ?, ?)`, oldTS, oldTS)
	if err != nil {
		t.Fatalf("insert orphaned active session: %v", err)
	}

	// Insert a recent active session (2 days old, should not be force-closed).
	recentTS := now.Add(-2 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO agent_sessions (id, run_id, workspace_slug, status, started_at, ingested_at)
		VALUES ('recent-active', '', 'ws-1', 'active', ?, ?)`, recentTS, recentTS)
	if err != nil {
		t.Fatalf("insert recent active session: %v", err)
	}

	closed, err := RetentionPreStep_ForceCloseOrphanedSessions(ctx, db, 7)
	if err != nil {
		t.Fatalf("pre-step error: %v", err)
	}
	if closed != 1 {
		t.Errorf("expected 1 force-closed, got %d", closed)
	}

	// Verify orphaned session is now status='timeout'.
	var status string
	err = db.QueryRow("SELECT status FROM agent_sessions WHERE id = 'orphan-active'").Scan(&status)
	if err != nil {
		t.Fatalf("query orphan status: %v", err)
	}
	if status != "timeout" {
		t.Errorf("expected orphan session to be 'timeout', got %q", status)
	}

	// Verify recent active session is still active.
	err = db.QueryRow("SELECT status FROM agent_sessions WHERE id = 'recent-active'").Scan(&status)
	if err != nil {
		t.Fatalf("query recent status: %v", err)
	}
	if status != "active" {
		t.Errorf("expected recent session to remain 'active', got %q", status)
	}
}
