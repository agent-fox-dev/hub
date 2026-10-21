package forge

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

// column is a row of PRAGMA table_info.
type column struct {
	Name    string
	NotNull bool
	PK      int
}

func tableColumns(t *testing.T, db *sql.DB, table string) []column {
	t.Helper()
	rows, err := db.Query(`SELECT name, "notnull", pk FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var cols []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.Name, &c.NotNull, &c.PK); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	return cols
}

// indexColumns returns the columns of every index of the table, by index name, in index
// order.
func indexColumns(t *testing.T, db *sql.DB, table string) map[string][]string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_index_list(?)`, table)
	if err != nil {
		t.Fatalf("index_list(%s): %v", table, err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	out := map[string][]string{}
	for _, name := range names {
		cols, err := db.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, name)
		if err != nil {
			t.Fatalf("index_info(%s): %v", name, err)
		}
		for cols.Next() {
			var col string
			if err := cols.Scan(&col); err != nil {
				t.Fatal(err)
			}
			out[name] = append(out[name], col)
		}
		cols.Close()
	}
	return out
}

func countDeliveries(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM forge_deliveries`).Scan(&n); err != nil {
		t.Fatalf("count forge_deliveries: %v", err)
	}
	return n
}

// TS-32-23: InitSchema creates forge_deliveries with its nine columns, its key and its index,
// idempotently, and leaves the rows alone.
// Requirement: 32-REQ-4.1
func TestInitSchemaCreatesDeliveryLog_TS_32_23(t *testing.T) {
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	const insert = `INSERT INTO forge_deliveries
		(workspace_slug, delivery_id, forge, event, action, target, outcome, relayed, received_at)
		VALUES ('widgets', 'd-1', 'github', 'issues', 'labeled', 'origin', 'relayed', '["hub.forge.issue.labeled"]', '2026-10-21T09:12:03Z')`
	if _, err := db.Exec(insert); err != nil {
		t.Fatalf("insert a delivery: %v", err)
	}
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema, the second time: %v", err)
	}
	if n := countDeliveries(t, db); n != 1 {
		t.Errorf("rows = %d after the second InitSchema; want the 1 inserted", n)
	}
	var event, action, relayed string
	if err := db.QueryRow(`SELECT event, action, relayed FROM forge_deliveries`).Scan(&event, &action, &relayed); err != nil {
		t.Fatal(err)
	}
	if event != "issues" || action != "labeled" || relayed != `["hub.forge.issue.labeled"]` {
		t.Errorf("the row after the second InitSchema = %q, %q, %q; want it unchanged", event, action, relayed)
	}

	// The nine columns, in the order of the spec; only target may be NULL; the key is
	// (workspace_slug, delivery_id).
	want := []column{
		{"workspace_slug", true, 1}, {"delivery_id", true, 2}, {"forge", true, 0}, {"event", true, 0},
		{"action", true, 0}, {"target", false, 0}, {"outcome", true, 0}, {"relayed", true, 0}, {"received_at", true, 0},
	}
	if got := tableColumns(t, db, "forge_deliveries"); !reflect.DeepEqual(got, want) {
		t.Errorf("columns = %+v; want %+v", got, want)
	}

	// An index on (workspace_slug, received_at), beside the key's own.
	found := false
	for _, cols := range indexColumns(t, db, "forge_deliveries") {
		if reflect.DeepEqual(cols, []string{"workspace_slug", "received_at"}) {
			found = true
		}
	}
	if !found {
		t.Errorf("indexes = %v; want one on (workspace_slug, received_at)", indexColumns(t, db, "forge_deliveries"))
	}

	// The same key twice conflicts, and ON CONFLICT DO NOTHING inserts nothing.
	if _, err := db.Exec(insert); err == nil || !strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Errorf("inserting the same (workspace_slug, delivery_id) again: %v; want a constraint error", err)
	}
	res, err := db.Exec(insert + ` ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatalf("insert ... ON CONFLICT DO NOTHING: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Errorf("ON CONFLICT DO NOTHING affected %d rows; want 0", n)
	}
	if n := countDeliveries(t, db); n != 1 {
		t.Errorf("rows = %d; want 1", n)
	}

	// target may be NULL; the other columns may not.
	if _, err := db.Exec(`INSERT INTO forge_deliveries
		(workspace_slug, delivery_id, forge, event, action, target, outcome, relayed, received_at)
		VALUES ('widgets', 'd-2', 'gitlab', 'Issue Hook', '', NULL, 'repository_mismatch', '[]', '2026-10-21T09:13:00Z')`); err != nil {
		t.Errorf("inserting a delivery with a NULL target: %v", err)
	}
}

// webhookURL is the ingress URL of a workspace: external_url without its trailing slashes,
// then /hooks/forge/<slug>, and null when external_url is unset.
// Requirement: 32-REQ-1.7
func TestWebhookURL(t *testing.T) {
	for _, tc := range []struct {
		external string
		want     *string
	}{
		{"https://hub.example.com", ptr("https://hub.example.com/hooks/forge/widgets")},
		{"https://hub.example.com/", ptr("https://hub.example.com/hooks/forge/widgets")},
		{"https://hub.example.com//", ptr("https://hub.example.com/hooks/forge/widgets")},
		{"http://localhost:8080/prefix/", ptr("http://localhost:8080/prefix/hooks/forge/widgets")},
		{"", nil},
	} {
		got := webhookURL(tc.external, "widgets")
		switch {
		case got == nil && tc.want == nil:
		case got == nil || tc.want == nil || *got != *tc.want:
			t.Errorf("webhookURL(%q) = %v; want %v", tc.external, deref(got), deref(tc.want))
		}
	}
}

// The name of the webhook secret is the one operators set with afc secrets create, so it is
// pinned.
// Requirement: 32-REQ-1.1
func TestWebhookSecretKeyIsTheDocumentedName(t *testing.T) {
	if webhookSecretKey != "FORGE_WEBHOOK_SECRET" {
		t.Errorf("webhookSecretKey = %q; want FORGE_WEBHOOK_SECRET", webhookSecretKey)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<null>"
	}
	return *s
}
