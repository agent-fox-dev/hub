package forge

import (
	"context"
	"database/sql"
	"strings"
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
