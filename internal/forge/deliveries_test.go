package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// listDeliveries calls GET /forge/deliveries of slug as the owner and decodes the rows.
func listDeliveries(t *testing.T, e *env, slug, query string) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	path := "/workspaces/" + slug + "/forge/deliveries"
	if query != "" {
		path += "?" + query
	}
	rec := e.get(path, apiKeyAuth())
	var rows []map[string]any
	if rec.Code == http.StatusOK {
		rows = testutil.Objects(t, rec)
	}
	return rec, rows
}

func receivedAts(rows []map[string]any) []string {
	var out []string
	for _, row := range rows {
		out = append(out, row["received_at"].(string))
	}
	return out
}

// GET /forge/deliveries lists the workspace's rows newest first, the last recorded first
// within a second, from since on (rounded up to the next second when it has a fraction, in
// UTC whatever its offset) and at most limit of them, 30 by default; whatever the status of
// the workspace; [] for none; each row with its eight fields, relayed always an array and a
// NULL target as null.
func TestListDeliveries(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "old", archived)
	addWorkspace(t, e.db, "a")
	for i := 0; i < 40; i++ {
		addDelivery(t, e.db, deliveryRecord{slug: "old", id: fmt.Sprintf("d-%02d", i), receivedAt: at(i)})
	}
	rec, rows := listDeliveries(t, e, "old", "")
	if rec.Code != http.StatusOK || len(rows) != 30 || rows[0]["received_at"] != at(39) || rows[29]["received_at"] != at(10) {
		t.Errorf("default: %d with %d rows from %v; want 200 and the 30 newest first", rec.Code, len(rows), receivedAts(rows))
	}
	for query, want := range map[string][]string{
		"since=" + at(37):                   {at(39), at(38), at(37)},
		"since=" + at(40):                   nil,
		"limit=2":                           {at(39), at(38)},
		"since=2026-10-21T11:37:00%2B02:00": {at(39), at(38), at(37)},
		"since=2026-10-21T09:36:00.500Z":    {at(39), at(38), at(37)},
		"since=2026-10-21T09:37:00.000Z":    {at(39), at(38), at(37)},
	} {
		if _, rows := listDeliveries(t, e, "old", query); !reflect.DeepEqual(receivedAts(rows), want) {
			t.Errorf("?%s: %v; want %v", query, receivedAts(rows), want)
		}
	}
	rec, rows = listDeliveries(t, e, "a", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" || rows == nil {
		t.Errorf("an empty workspace: %d %q; want 200 []", rec.Code, rec.Body)
	}

	for _, id := range []string{"first", "second"} {
		addDelivery(t, e.db, deliveryRecord{slug: "a", id: id, receivedAt: at(1), relayed: "null"})
	}
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "gh-1", action: "labeled", target: ptr("origin"), relayed: `["hub.forge.issue.unlabeled","hub.forge.issue.labeled"]`, receivedAt: at(2)})
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "gl-1", forge: "gitlab", event: "Issue Hook", outcome: "repository_mismatch", receivedAt: at(3)})
	_, rows = listDeliveries(t, e, "a", "")
	want := []map[string]any{
		{"delivery_id": "gl-1", "forge": "gitlab", "event": "Issue Hook", "action": "", "target": nil, "outcome": "repository_mismatch", "relayed": []any{}, "received_at": at(3)},
		{"delivery_id": "gh-1", "forge": "github", "event": "issues", "action": "labeled", "target": "origin", "outcome": "relayed", "relayed": []any{"hub.forge.issue.unlabeled", "hub.forge.issue.labeled"}, "received_at": at(2)},
		{"delivery_id": "second", "forge": "github", "event": "issues", "action": "", "target": nil, "outcome": "relayed", "relayed": []any{}, "received_at": at(1)},
		{"delivery_id": "first", "forge": "github", "event": "issues", "action": "", "target": nil, "outcome": "relayed", "relayed": []any{}, "received_at": at(1)},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %v\nwant %v", rows, want)
	}
}

// A bad since or limit is 400 naming the parameter, and a failure of the log is 500 without
// the cause.
func TestListDeliveries_Refusals(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "a")
	for query, param := range map[string]string{
		"since=yesterday": "since", "since=": "since", "since=2026-10-21": "since", "since=" + at(0) + "&since=" + at(1): "since",
		"limit=0": "limit", "limit=101": "limit", "limit=abc": "limit", "limit=": "limit", "limit=1&limit=2": "limit",
	} {
		rec, _ := listDeliveries(t, e, "a", query)
		if rec.Code != http.StatusBadRequest || !strings.Contains(testutil.Error(t, rec).Message, param) {
			t.Errorf("?%s: %d %s; want 400 naming %s", query, rec.Code, rec.Body, param)
		}
	}
	testutil.Exec(t, e.db, `DROP TABLE forge_deliveries`)
	if rec, _ := listDeliveries(t, e, "a", ""); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "forge_deliveries") {
		t.Errorf("a failing log: %d %s; want 500 without the cause", rec.Code, rec.Body)
	}
}

// InitSchema creates the delivery log with its key on (workspace_slug, delivery_id), so that
// the same id twice conflicts and ON CONFLICT DO NOTHING inserts nothing, and running it
// again leaves the rows alone.
func TestInitSchema_CreatesTheDeliveryLogOnce(t *testing.T) {
	db := testutil.OpenDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	const insert = `INSERT INTO forge_deliveries (workspace_slug, delivery_id, forge, event, action, target, outcome, relayed, received_at)
		VALUES ('widgets', 'd-1', 'github', 'issues', 'labeled', NULL, 'relayed', '["hub.forge.issue.labeled"]', '2026-10-21T09:12:03Z')`
	testutil.Exec(t, db, insert)
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert); err == nil || !strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Errorf("the same key again: %v; want a constraint error", err)
	}
	res, err := db.Exec(insert + ` ON CONFLICT DO NOTHING`)
	if n, _ := res.RowsAffected(); err != nil || n != 0 || testutil.Count(t, db, `SELECT COUNT(*) FROM forge_deliveries`) != 1 {
		t.Errorf("ON CONFLICT DO NOTHING: %v affected %d rows; want 0 and the one row kept", err, n)
	}
}

// PruneDeliveries deletes the rows older than seven days and the rows of workspaces that no
// longer exist, whatever their outcome, and returns how many; a pass over a database
// without the log fails.
func TestPruneDeliveries(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "a")
	addWorkspace(t, e.db, "b")
	const day = 24 * time.Hour
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "a-in", outcome: "malformed", receivedAt: ago(7*day - time.Minute)})
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "a-out", outcome: "ignored", receivedAt: ago(7*day + time.Minute)})
	addDelivery(t, e.db, deliveryRecord{slug: "b", id: "b-in", receivedAt: ago(time.Hour)})
	addDelivery(t, e.db, deliveryRecord{slug: "gone", id: "gone-recent", receivedAt: ago(time.Hour)})
	if n, err := PruneDeliveries(context.Background(), e.db); err != nil || n != 2 {
		t.Fatalf("PruneDeliveries = %d, %v; want 2", n, err)
	}
	var ids []string
	for _, row := range e.deliveryRows() {
		ids = append(ids, row.id)
	}
	if strings.Join(ids, ",") != "a-in,b-in" {
		t.Errorf("rows = %v; want a-in and b-in", ids)
	}
	if n, err := PruneDeliveries(context.Background(), e.db); err != nil || n != 0 {
		t.Errorf("a second pass = %d, %v; want 0", n, err)
	}
	testutil.Exec(t, e.db, `DROP TABLE forge_deliveries`)
	if _, err := PruneDeliveries(context.Background(), e.db); err == nil {
		t.Error("a pass without the log succeeded")
	}
}
