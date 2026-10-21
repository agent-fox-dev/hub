package forge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// deliveryRecord is a row of forge_deliveries, as a test writes one.
type deliveryRecord struct {
	slug, id, forge, event, action string
	target                         *string
	outcome                        string
	relayed                        string
	receivedAt                     string
}

// addDelivery inserts a row of the delivery log; what the record leaves empty is a plain
// GitHub issues delivery that was relayed.
func addDelivery(t *testing.T, db *sql.DB, r deliveryRecord) {
	t.Helper()
	if r.forge == "" {
		r.forge = "github"
	}
	if r.event == "" {
		r.event = "issues"
	}
	if r.outcome == "" {
		r.outcome = "relayed"
	}
	if r.relayed == "" {
		r.relayed = "[]"
	}
	if _, err := db.Exec(`INSERT INTO forge_deliveries
		(workspace_slug, delivery_id, forge, event, action, target, outcome, relayed, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.slug, r.id, r.forge, r.event, r.action, r.target, r.outcome, r.relayed, r.receivedAt); err != nil {
		t.Fatalf("insert delivery %s/%s: %v", r.slug, r.id, err)
	}
}

// at is the time minutes after 2026-10-21T09:00:00Z, as a received_at.
func at(minutes int) string {
	return apikit.FormatUTC(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute))
}

// webhookBlock is the webhook block of GET /forge.
type webhookBlock struct {
	Configured     bool    `json:"configured"`
	URL            *string `json:"url"`
	LastDeliveryAt *string `json:"last_delivery_at"`
}

// getWebhookBlock calls GET /forge as the owner and returns the webhook block with the keys
// it has.
func getWebhookBlock(t *testing.T, e *env, slug string) (webhookBlock, map[string]json.RawMessage) {
	t.Helper()
	rec := e.get("/workspaces/"+slug+"/forge", apiKeyAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status = %d; want 200\n%s", slug, rec.Code, rec.Body)
	}
	var doc struct {
		Webhook *json.RawMessage `json:"webhook"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || doc.Webhook == nil {
		t.Fatalf("%s: the body has no webhook block: %v\n%s", slug, err, rec.Body)
	}
	var block webhookBlock
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(*doc.Webhook, &block); err != nil {
		t.Fatalf("%s: the webhook block is not one: %v\n%s", slug, err, *doc.Webhook)
	}
	_ = json.Unmarshal(*doc.Webhook, &keys)
	return block, keys
}

// TS-32-12: GET /forge carries a webhook block with configured, url and last_delivery_at, and
// sends nothing to the forge.
// Requirement: 32-REQ-2.4, 32-REQ-2.5
func TestForgeStatusHasAWebhookBlock_TS_32_12(t *testing.T) {
	counting := &countingTransport{}
	e := newEnv(t, func(cfg *Config) { cfg.Transport = counting })
	addWorkspace(t, e.db, "a")
	addWorkspace(t, e.db, "b")
	if _, err := e.secrets.CreateSecrets("workspace", "a", []secrets.EntryInput{{Key: "FORGE_WEBHOOK_SECRET", Value: "a-secret-of-16-chars"}}); err != nil {
		t.Fatal(err)
	}
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "d-1", outcome: "ignored", receivedAt: "2026-10-21T09:00:00Z"})
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "d-2", outcome: "malformed", receivedAt: "2026-10-21T09:12:00Z"})

	a, keys := getWebhookBlock(t, e, "a")
	if !a.Configured || a.URL == nil || *a.URL != "https://hub.example.com/hooks/forge/a" ||
		a.LastDeliveryAt == nil || *a.LastDeliveryAt != "2026-10-21T09:12:00Z" {
		t.Errorf("a: webhook = %+v; want configured, the ingress URL and the 09:12 delivery", a)
	}
	for _, key := range []string{"configured", "url", "last_delivery_at"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("a: the webhook block has no %s key: %v", key, keys)
		}
	}
	if len(keys) != 3 {
		t.Errorf("a: the webhook block has the keys %v; want only those three", keys)
	}

	b, keys := getWebhookBlock(t, e, "b")
	if b.Configured || b.URL == nil || *b.URL != "https://hub.example.com/hooks/forge/b" || b.LastDeliveryAt != nil {
		t.Errorf("b: webhook = %+v; want not configured, the URL and no delivery", b)
	}
	if string(keys["last_delivery_at"]) != "null" {
		t.Errorf("b: last_delivery_at = %s; want null", keys["last_delivery_at"])
	}
	if n := counting.n.Load(); n != 0 {
		t.Errorf("the forge received %d requests; want none", n)
	}
}

// The url of the block is null when external_url is not set, and the block never holds the
// secret.
func TestForgeStatusWebhookBlockWithoutExternalURL(t *testing.T) {
	e := newEnv(t, func(cfg *Config) { cfg.ExternalURL = "" })
	addWorkspace(t, e.db, "a")
	const secret = "a-secret-of-16-chars"
	if _, err := e.secrets.CreateSecrets("workspace", "a", []secrets.EntryInput{{Key: "FORGE_WEBHOOK_SECRET", Value: secret}}); err != nil {
		t.Fatal(err)
	}
	block, keys := getWebhookBlock(t, e, "a")
	if !block.Configured || block.URL != nil || string(keys["url"]) != "null" {
		t.Errorf("webhook = %+v with url %s; want configured and a null url", block, keys["url"])
	}
	if body := e.get("/workspaces/a/forge", apiKeyAuth()).Body.String(); strings.Contains(body, secret) {
		t.Errorf("the body holds the secret: %s", body)
	}
}

// Only the workspace-scope secret counts as configured: an org-scoped or a user-scoped one is
// never read.
func TestForgeStatusWebhookIgnoresOtherScopes(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "a")
	for _, scope := range [][2]string{{"org", "acme"}, {"user", ownerID}} {
		if _, err := e.secrets.CreateSecrets(scope[0], scope[1], []secrets.EntryInput{{Key: "FORGE_WEBHOOK_SECRET", Value: "a-secret-of-16-chars"}}); err != nil {
			t.Fatal(err)
		}
	}
	if block, _ := getWebhookBlock(t, e, "a"); block.Configured {
		t.Errorf("webhook = %+v; want not configured", block)
	}
}

// TS-32-13: A failure of the secrets store or the delivery log answers 500, with no webhook
// block.
// Requirement: 32-REQ-2.6
func TestForgeStatusWebhookFailures_TS_32_13(t *testing.T) {
	check := func(t *testing.T, e *env) {
		t.Helper()
		rec := e.get("/workspaces/a/forge", apiKeyAuth())
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d; want 500\n%s", rec.Code, rec.Body)
		}
		if msg := decodeError(t, rec).Message; strings.Contains(msg, "boom") {
			t.Errorf("message = %q; it holds the cause", msg)
		}
		var doc map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &doc)
		if _, ok := doc["webhook"]; ok {
			t.Errorf("the body has a webhook key: %s", rec.Body)
		}
	}
	t.Run("the secrets store fails", func(t *testing.T) {
		e := newEnv(t, withFakeSecrets(&fakeSecrets{getErr: errors.New("boom: disk gone")}))
		addWorkspace(t, e.db, "a")
		check(t, e)
	})
	t.Run("the delivery log is gone", func(t *testing.T) {
		e := newEnv(t)
		addWorkspace(t, e.db, "a")
		if _, err := e.db.Exec(`DROP TABLE forge_deliveries`); err != nil {
			t.Fatal(err)
		}
		check(t, e)
	})
	t.Run("there is no secrets store", func(t *testing.T) {
		e := newEnv(t, func(cfg *Config) { cfg.Secrets = nil })
		addWorkspace(t, e.db, "a")
		check(t, e)
	})
}

// listDeliveries calls GET /forge/deliveries of the workspace as the owner.
func listDeliveries(t *testing.T, e *env, slug, query string) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	path := "/workspaces/" + slug + "/forge/deliveries"
	if query != "" {
		path += "?" + query
	}
	rec := e.get(path, apiKeyAuth())
	var rows []map[string]any
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatalf("GET %s: the body is not an array of objects: %v\n%s", path, err, rec.Body)
		}
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

// TS-32-51: GET /forge/deliveries returns the rows newest first, from since on and at most
// limit of them, whatever the status of the workspace.
// Requirement: 32-REQ-9.1
func TestListDeliveries_TS_32_51(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "old", func(r *workspaceRow) { r.status = "archived" })
	addWorkspace(t, e.db, "bare")
	for i := 0; i < 40; i++ {
		addDelivery(t, e.db, deliveryRecord{slug: "old", id: fmt.Sprintf("d-%02d", i), receivedAt: at(i)})
	}

	rec, rows := listDeliveries(t, e, "old", "")
	if rec.Code != http.StatusOK || len(rows) != 30 {
		t.Fatalf("default: status %d with %d rows; want 200 and 30", rec.Code, len(rows))
	}
	for i, row := range rows {
		if want := at(39 - i); row["received_at"] != want {
			t.Fatalf("default: row %d received_at = %v; want %s (newest first)", i, row["received_at"], want)
		}
	}

	t10 := at(30) // the 10th newest row
	if _, rows := listDeliveries(t, e, "old", "since="+t10); len(rows) != 10 || rows[9]["received_at"] != t10 {
		t.Errorf("since=%s: %v; want the 10 newest rows, the row at that time the last", t10, receivedAts(rows))
	}
	if _, rows := listDeliveries(t, e, "old", "since="+at(40)); len(rows) != 0 {
		t.Errorf("since after the newest row: %v; want none", receivedAts(rows))
	}
	if _, rows := listDeliveries(t, e, "old", "limit=5"); !reflect.DeepEqual(receivedAts(rows), []string{at(39), at(38), at(37), at(36), at(35)}) {
		t.Errorf("limit=5: %v; want the 5 newest", receivedAts(rows))
	}
	if _, rows := listDeliveries(t, e, "old", "limit=100"); len(rows) != 40 {
		t.Errorf("limit=100: %d rows; want all 40", len(rows))
	}
	// A since with an offset is the same instant in UTC.
	if _, rows := listDeliveries(t, e, "old", "since="+strings.ReplaceAll("2026-10-21T11:30:00+02:00", "+", "%2B")); len(rows) != 10 || rows[9]["received_at"] != t10 {
		t.Errorf("since with an offset: %v; want the same 10 rows", receivedAts(rows))
	}

	rec, rows = listDeliveries(t, e, "bare", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" || rows == nil || len(rows) != 0 {
		t.Errorf("an empty workspace: status %d, body %q; want 200 and []", rec.Code, rec.Body)
	}
}

// Every element of the list has the eight fields, the relayed events as an array and a NULL
// target as null.
func TestListDeliveriesFields(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "a")
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "gh-1", action: "labeled", target: ptr("origin"),
		relayed: `["hub.forge.issue.unlabeled","hub.forge.issue.labeled"]`, receivedAt: at(1)})
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "gl-1", forge: "gitlab", event: "Issue Hook", outcome: "repository_mismatch", receivedAt: at(2)})
	addDelivery(t, e.db, deliveryRecord{slug: "other", id: "x", receivedAt: at(3)})

	_, rows := listDeliveries(t, e, "a", "")
	want := []map[string]any{
		{"delivery_id": "gl-1", "forge": "gitlab", "event": "Issue Hook", "action": "", "target": nil,
			"outcome": "repository_mismatch", "relayed": []any{}, "received_at": at(2)},
		{"delivery_id": "gh-1", "forge": "github", "event": "issues", "action": "labeled", "target": "origin",
			"outcome": "relayed", "relayed": []any{"hub.forge.issue.unlabeled", "hub.forge.issue.labeled"}, "received_at": at(1)},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %v\nwant %v", rows, want)
	}
}

// Rows received in the same second keep a stable order, the last recorded first.
func TestListDeliveriesTiesNewestRecordedFirst(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "a")
	for _, id := range []string{"first", "second", "third"} {
		addDelivery(t, e.db, deliveryRecord{slug: "a", id: id, receivedAt: at(1)})
	}
	_, rows := listDeliveries(t, e, "a", "")
	var ids []string
	for _, row := range rows {
		ids = append(ids, row["delivery_id"].(string))
	}
	if !reflect.DeepEqual(ids, []string{"third", "second", "first"}) {
		t.Errorf("ids = %v; want the last recorded first", ids)
	}
}

// TS-32-52: A bad since or limit answers 400 and names the parameter.
// Requirement: 32-REQ-9.2
func TestListDeliveriesBadParameters_TS_32_52(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "a")
	for _, tc := range []struct{ query, param string }{
		{"since=yesterday", "since"},
		{"since=", "since"},
		{"since=2026-10-21", "since"},
		{"since=2026-10-21T09:00:00Z&since=2026-10-21T10:00:00Z", "since"},
		{"limit=0", "limit"},
		{"limit=101", "limit"},
		{"limit=abc", "limit"},
		{"limit=-1", "limit"},
		{"limit=", "limit"},
		{"limit=1.5", "limit"},
		{"limit=1&limit=2", "limit"},
	} {
		rec, _ := listDeliveries(t, e, "a", tc.query)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("?%s: status = %d; want 400\n%s", tc.query, rec.Code, rec.Body)
			continue
		}
		if msg := decodeError(t, rec).Message; !strings.Contains(msg, tc.param) {
			t.Errorf("?%s: message = %q; want it to name %s", tc.query, msg, tc.param)
		}
	}
}

// TS-32-53: The deliveries route answers 401, 403 naming forge:read and 404, and accepts
// forge:read and forge:write.
// Requirement: 32-REQ-9.3
func TestListDeliveriesAccess_TS_32_53(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "a")
	addWorkspace(t, e.db, "b", func(r *workspaceRow) { r.owner = otherID })
	const path = "/workspaces/a/forge/deliveries"

	if rec := e.get(path, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no credential: status = %d; want 401", rec.Code)
	}
	rec := e.get(path, patAuth("issues:read"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("a PAT with neither scope: status = %d; want 403", rec.Code)
	} else if msg := decodeError(t, rec).Message; !strings.Contains(msg, "forge:read") {
		t.Errorf("a PAT with neither scope: message = %q; want it to name forge:read", msg)
	}
	if rec := e.get("/workspaces/b/forge/deliveries", apiKeyAuth()); rec.Code != http.StatusNotFound {
		t.Errorf("another user's workspace: status = %d; want 404", rec.Code)
	}
	if rec := e.get("/workspaces/nosuch/forge/deliveries", apiKeyAuth()); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown workspace: status = %d; want 404", rec.Code)
	}
	for _, scope := range []string{"forge:read", "forge:write"} {
		if rec := e.get(path, patAuth(scope)); rec.Code != http.StatusOK {
			t.Errorf("a PAT with %s: status = %d; want 200\n%s", scope, rec.Code, rec.Body)
		}
	}
	if rec := e.get(path, adminAuth()); rec.Code != http.StatusOK {
		t.Errorf("an admin token: status = %d; want 200\n%s", rec.Code, rec.Body)
	}
}

// A failure of the delivery log answers 500 without the cause.
func TestListDeliveriesLogFailure(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "a")
	if _, err := e.db.Exec(`DROP TABLE forge_deliveries`); err != nil {
		t.Fatal(err)
	}
	rec, _ := listDeliveries(t, e, "a", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500\n%s", rec.Code, rec.Body)
	}
	if msg := decodeError(t, rec).Message; strings.Contains(msg, "forge_deliveries") {
		t.Errorf("message = %q; it holds the cause", msg)
	}
}

// The log keeps whole seconds, so a since with a fraction of a second starts at the next
// second: a row at the whole second before it is not "from since on".
func TestListDeliveriesSinceWithAFraction(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "a")
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "d-0", receivedAt: at(0)})
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "d-1", receivedAt: "2026-10-21T09:00:01Z"})
	_, rows := listDeliveries(t, e, "a", "since=2026-10-21T09:00:00.500Z")
	if got := receivedAts(rows); !reflect.DeepEqual(got, []string{"2026-10-21T09:00:01Z"}) {
		t.Errorf("since=09:00:00.5: %v; want only the row at 09:00:01", got)
	}
	_, rows = listDeliveries(t, e, "a", "since=2026-10-21T09:00:00.000Z")
	if len(rows) != 2 {
		t.Errorf("since=09:00:00.000: %d rows; want 2", len(rows))
	}
}

// relayed is always an array: a row that holds JSON null is an empty list.
func TestListDeliveriesRelayedIsAlwaysAnArray(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "a")
	addDelivery(t, e.db, deliveryRecord{slug: "a", id: "d-0", relayed: "null", receivedAt: at(0)})
	rec, rows := listDeliveries(t, e, "a", "")
	if len(rows) != 1 || !reflect.DeepEqual(rows[0]["relayed"], []any{}) {
		t.Errorf("rows = %v (%s); want relayed to be []", rows, strings.TrimSpace(rec.Body.String()))
	}
}
