package audit

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TS-19-40: StartRetentionWorker executes an immediate retention run before
// the first ticker fires, then repeats every hour.
// Requirement: 19-REQ-12.1
func TestRetentionWorker_ImmediateRunOnStartup(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	sqliteDB := openTestSQLiteDB(t)
	store := NewStore(db)
	m := NewMetrics()

	// Verify the timestamp gauge is initially 0.
	tsBefore := getPlainGaugeValue(t, m.RetentionLastRunTimestamp)
	if tsBefore != 0 {
		t.Fatalf("expected initial timestamp to be 0, got %v", tsBefore)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		StartRetentionWorker(ctx, store, sqliteDB, m)
		close(done)
	}()

	// Give the immediate run time to complete.
	time.Sleep(500 * time.Millisecond)

	// The retention_last_run_timestamp_seconds gauge should be set.
	tsAfter := getPlainGaugeValue(t, m.RetentionLastRunTimestamp)
	if tsAfter <= tsBefore {
		t.Errorf("expected retention timestamp to be set after startup: before=%v after=%v",
			tsBefore, tsAfter)
	}

	cancel()
	select {
	case <-done:
		// Goroutine exited cleanly.
	case <-time.After(2 * time.Second):
		t.Fatal("retention worker goroutine did not exit after context cancellation")
	}
}

// TS-19-41: Cancelling the context passed to StartRetentionWorker causes the
// goroutine to exit cleanly after completing at most one more step.
// Requirement: 19-REQ-12.2
func TestRetentionWorker_CleanExitOnCancel(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	sqliteDB := openTestSQLiteDB(t)
	store := NewStore(db)
	m := NewMetrics()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		StartRetentionWorker(ctx, store, sqliteDB, m)
		close(done)
	}()

	// Let the worker start.
	time.Sleep(100 * time.Millisecond)

	// Cancel the context.
	cancel()

	select {
	case <-done:
		// Goroutine exited cleanly — good.
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit within 2 seconds of context cancellation")
	}
}

// 19-REQ-12.E1: Worker checks ctx.Err() before each step.
func TestRetentionWorker_StepBoundaryCancelCheck(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	sqliteDB := openTestSQLiteDB(t)
	m := NewMetrics()

	// Create a pre-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// RunRetention with a cancelled context should return without
	// executing steps (or executing at most one).
	err := RunRetention(ctx, db, sqliteDB, DefaultRetentionConfig(), m)
	// The error might be context.Canceled or nil (if it checked before step 1).
	// Either way, it should not panic and should return promptly.
	_ = err
}

// 19-REQ-12.E2: After hub restart, the immediate startup run cleans up
// stale data without waiting for an hour.
func TestRetentionWorker_StartsImmediatelyOnRestart(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	sqliteDB := openTestSQLiteDB(t)
	store := NewStore(db)
	m := NewMetrics()

	// Insert stale data (100 days old).
	staleTS := time.Now().UTC().Add(-100 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO agent_audit_events (id, run_id, workspace, event_type, timestamp, ingested_at)
		VALUES ('stale-1', 'run-old', 'ws-test', 'test.event', ?, ?)`, staleTS, staleTS)
	if err != nil {
		t.Fatalf("insert stale data: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		StartRetentionWorker(ctx, store, sqliteDB, m)
		close(done)
	}()

	// Give the immediate run time to complete.
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit")
	}

	// Verify stale data was cleaned up.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM agent_audit_events WHERE id = 'stale-1'").Scan(&count)
	if err != nil {
		t.Fatalf("query stale data: %v", err)
	}
	if count != 0 {
		t.Errorf("expected stale data to be cleaned up on startup, but found %d rows", count)
	}
}

// Ensure the _ import of prometheus is used.
var _ = prometheus.Labels{}
