package forge

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/txsvc/apikit"
)

// ago is the received_at of a delivery that came d ago.
func ago(d time.Duration) string { return apikit.FormatUTC(time.Now().Add(-d)) }

const day = 24 * time.Hour

// retentionDB is a database with the workspaces table and the delivery log.
func retentionDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// deliveryIDs lists the ids of the rows of the delivery log, sorted.
func deliveryIDs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT delivery_id FROM forge_deliveries ORDER BY delivery_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// TS-32-54: PruneDeliveries deletes the rows older than 7 days and the rows of workspaces that no
// longer exist, and returns how many.
// Requirement: 32-REQ-9.4
func TestPruneDeliveries_TS_32_54(t *testing.T) {
	db := retentionDB(t)
	addWorkspace(t, db, "a")
	addDelivery(t, db, deliveryRecord{slug: "a", id: "old-a", receivedAt: ago(8 * day)})
	addDelivery(t, db, deliveryRecord{slug: "a", id: "recent-a", receivedAt: ago(6 * day)})
	addDelivery(t, db, deliveryRecord{slug: "gone", id: "recent-gone", receivedAt: ago(1 * day)})

	n, err := PruneDeliveries(context.Background(), db)
	if err != nil || n != 2 {
		t.Fatalf("PruneDeliveries = %d, %v; want 2 and no error", n, err)
	}
	if ids := deliveryIDs(t, db); len(ids) != 1 || ids[0] != "recent-a" {
		t.Errorf("rows = %v; want only recent-a", ids)
	}
	if n, err := PruneDeliveries(context.Background(), db); err != nil || n != 0 {
		t.Errorf("a second pass = %d, %v; want 0 and no error", n, err)
	}
}

// The cutoff is 7 days: a row a minute younger stays, a row a minute older goes, whatever the
// outcome of the delivery, and the rows of another existing workspace are not touched.
func TestPruneDeliveriesCutoff(t *testing.T) {
	db := retentionDB(t)
	addWorkspace(t, db, "a")
	addWorkspace(t, db, "b")
	addDelivery(t, db, deliveryRecord{slug: "a", id: "a-in", outcome: "malformed", receivedAt: ago(7*day - time.Minute)})
	addDelivery(t, db, deliveryRecord{slug: "a", id: "a-out", outcome: "ignored", receivedAt: ago(7*day + time.Minute)})
	addDelivery(t, db, deliveryRecord{slug: "b", id: "b-in", receivedAt: ago(time.Hour)})
	addDelivery(t, db, deliveryRecord{slug: "b", id: "b-out", receivedAt: ago(30 * day)})
	n, err := PruneDeliveries(context.Background(), db)
	if err != nil || n != 2 {
		t.Fatalf("PruneDeliveries = %d, %v; want 2", n, err)
	}
	if ids := deliveryIDs(t, db); strings.Join(ids, ",") != "a-in,b-in" {
		t.Errorf("rows = %v; want a-in and b-in", ids)
	}
}

// A pass over a database that has no delivery log fails.
func TestPruneDeliveriesFailure(t *testing.T) {
	db := retentionDB(t)
	if _, err := db.Exec(`DROP TABLE forge_deliveries`); err != nil {
		t.Fatal(err)
	}
	if n, err := PruneDeliveries(context.Background(), db); err == nil || n != 0 {
		t.Errorf("PruneDeliveries = %d, %v; want an error", n, err)
	}
}

// startRetention runs the loop with the interval and returns the channel that closes when it returns.
func startRetention(ctx context.Context, db *sql.DB, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runDeliveryRetention(ctx, db, interval)
	}()
	return done
}

// eventually waits for cond for up to five seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TS-32-55: StartDeliveryRetention prunes at once and then every interval, survives a pass that
// fails, and returns when its context is cancelled.
// Requirement: 32-REQ-9.5, 32-REQ-9.6
func TestDeliveryRetentionLoop_TS_32_55(t *testing.T) {
	db := retentionDB(t)
	addWorkspace(t, db, "a")
	addDelivery(t, db, deliveryRecord{slug: "a", id: "old", receivedAt: ago(9 * day)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The interval is an hour: what is gone before the first tick went in the first pass.
	done := startRetention(ctx, db, time.Hour)
	eventually(t, "the first pass", func() bool { return len(deliveryIDs(t, db)) == 0 })

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the loop did not return when its context was cancelled")
	}
}

// A tick prunes again, and a pass that fails is logged and followed by the next.
func TestDeliveryRetentionSurvivesAFailedPass(t *testing.T) {
	logs := captureLog(t)
	db := retentionDB(t)
	addWorkspace(t, db, "a")
	if _, err := db.Exec(`DROP TABLE forge_deliveries`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startRetention(ctx, db, 10*time.Millisecond)

	eventually(t, "the failed pass being logged", func() bool { return strings.Contains(logs.String(), "level=ERROR") })
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	addDelivery(t, db, deliveryRecord{slug: "a", id: "old", receivedAt: ago(9 * day)})
	eventually(t, "the next pass", func() bool { return len(deliveryIDs(t, db)) == 0 })
	select {
	case <-done:
		t.Fatal("the loop returned though its context is live")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the loop did not return when its context was cancelled")
	}
}

// StartDeliveryRetention is the loop at the interval of an hour, and returns when the context is
// cancelled.
func TestStartDeliveryRetention(t *testing.T) {
	if retentionEvery != time.Hour {
		t.Errorf("retentionEvery = %v; want an hour", retentionEvery)
	}
	db := retentionDB(t)
	addWorkspace(t, db, "a")
	addDelivery(t, db, deliveryRecord{slug: "a", id: "old", receivedAt: ago(9 * day)})
	ctx, cancel := context.WithCancel(context.Background())
	var returned atomic.Bool
	finished := make(chan struct{})
	go func() {
		StartDeliveryRetention(ctx, db)
		returned.Store(true)
		close(finished)
	}()
	eventually(t, "the first pass", func() bool { return len(deliveryIDs(t, db)) == 0 })
	if returned.Load() {
		t.Fatal("StartDeliveryRetention returned before its context was cancelled")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("StartDeliveryRetention did not return when its context was cancelled")
	}
}

// A context that is cancelled before the loop starts ends it without a pass being logged as a failure.
func TestDeliveryRetentionWithACancelledContext(t *testing.T) {
	logs := captureLog(t)
	db := retentionDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	select {
	case <-startRetention(ctx, db, time.Hour):
	case <-time.After(time.Second):
		t.Fatal("the loop did not return")
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("the log = %q; a cancelled context is no failure", logs)
	}
}

// StartDeliveryRetention prunes on the interval it is configured with: a row that is old by the
// second pass is gone after a tick.
func TestStartDeliveryRetentionTicks(t *testing.T) {
	defer func(previous time.Duration) { retentionEvery = previous }(retentionEvery)
	retentionEvery = 10 * time.Millisecond
	db := retentionDB(t)
	addWorkspace(t, db, "a")
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		StartDeliveryRetention(ctx, db)
		close(finished)
	}()
	time.Sleep(50 * time.Millisecond) // the first pass is over
	addDelivery(t, db, deliveryRecord{slug: "a", id: "old", receivedAt: ago(9 * day)})
	eventually(t, "a pass after a tick", func() bool { return len(deliveryIDs(t, db)) == 0 })
	cancel()
	<-finished
}
