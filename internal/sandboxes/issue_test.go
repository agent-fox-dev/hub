package sandboxes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// capacityConfig is the default section with the given limit on live leases.
func capacityConfig(limit float64) *Config {
	cfg := defaultLeaseConfig()
	cfg.MaxSandboxes = &limit
	return cfg
}

// POST answers 201 with a snapshot of the profile and the token, the only time
// it exists in plaintext: the row holds its SHA-256 and no secret, and later
// edits of the profile do not reach the lease.
func TestIssue_CreatedWithTokenAndProfileSnapshot(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	body := env.mustIssue(`{"profile":"tools","purpose":"triage","labels":{"team":"red","Run.Id":"42"}}`)
	id, token := body["id"].(string), body["token"].(string)
	if body["state"] != "leased" || body["profile"] != "tools" || body["workspace"] != "widgets" || body["purpose"] != "triage" || body["created_by"] != owner ||
		body["image"] != "registry.example/tools:1" || body["network"] != "none" || body["require_landlock"] != true || body["gateway_url"] != "" {
		t.Errorf("body = %v", body)
	}
	if !reflect.DeepEqual(body["resources"], map[string]any{"cpus": float64(1), "memory_mb": float64(512), "pids": float64(100), "disk_mb": float64(0)}) ||
		!reflect.DeepEqual(body["secrets"], []any{"GIT_PAT"}) || !reflect.DeepEqual(body["optional_secrets"], []any{"EXTRA_TOKEN"}) ||
		!reflect.DeepEqual(body["labels"], map[string]any{"team": "red", "Run.Id": "42"}) {
		t.Errorf("resources %v, secrets %v, optional %v, labels %v", body["resources"], body["secrets"], body["optional_secrets"], body["labels"])
	}
	if body["created_at"] != "2030-01-02T03:04:05Z" || body["expires_at"] != "2030-01-02T04:04:05Z" || body["lease_expires_at"] != "2030-01-02T03:09:05Z" || body["updated_at"] != body["created_at"] {
		t.Errorf("created_at %v, expires_at %v, lease_expires_at %v; want the clock, plus 1h, plus the 5m lease_timeout", body["created_at"], body["expires_at"], body["lease_expires_at"])
	}
	for _, f := range []string{"sandbox_id", "error", "ended_at", "end_reason"} {
		if v, present := body[f]; !present || v != nil {
			t.Errorf("%s = %v; want null", f, v)
		}
	}
	if len(token) != 48 || !tokenShape.MatchString(token) {
		t.Fatalf("token %q is not sbl_ and 44 characters", token)
	}
	if s := env.readState(id); !s.hash.Valid || s.hash.String != HashToken(token) {
		t.Errorf("stored hash %v is not the SHA-256 of the token", s.hash)
	}
	if found, err := env.store.LookupByToken(token); err != nil || found.ID != id {
		t.Errorf("LookupByToken = (%v, %v); want the lease", found, err)
	}
	if row := rowText(t, env.db, id); strings.Contains(row, token) || strings.Contains(row, plantedSecret) {
		t.Errorf("the row holds the token or the secret: %s", row)
	}

	params, _ := env.svc.newIssueParams(issueRequest{})
	lease, _, err := env.svc.Issue(context.Background(), "widgets", ownerKey(), params)
	if err != nil {
		t.Fatal(err)
	}
	p := env.profile.profiles[0]
	p.Env["SECRET_ENV"], p.Secrets[0], p.OptionalSecrets[0], env.profile.profiles[0].Image = "changed", "CHANGED", "CHANGED", "registry.example/changed:9"
	got := env.lease(id)
	if got.Image != "registry.example/tools:1" || got.Env["SECRET_ENV"] != "envval" || got.Secrets[0] != "GIT_PAT" || got.TTLSeconds != 3600 || got.Labels["Run.Id"] != "42" {
		t.Errorf("the stored lease follows the edited profile: %+v", got)
	}
	// The lease Issue returns owns its lists and maps too.
	if lease.OptionalSecrets[0] != "EXTRA_TOKEN" || lease.Env["SECRET_ENV"] != "envval" {
		t.Error("the lease shares its lists with the profile")
	}
}

// An empty body or one without profile, ttl or purpose uses the default profile,
// its TTL and an empty purpose; created_by is admin for an admin token; the
// clock and the ttl are cut to whole seconds.
func TestIssue_DefaultsAndTimestamps(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig(), start: time.Date(2030, 1, 2, 3, 4, 5, 900_000_000, time.UTC)})
	for _, body := range []string{"", "{}", `{"labels":{}}`} {
		got := env.mustIssue(body)
		if got["profile"] != "tools" || got["purpose"] != "" || got["expires_at"] != "2030-01-02T04:04:05Z" || got["created_at"] != "2030-01-02T03:04:05Z" {
			t.Errorf("body %q: %v; want the default profile, '' and 1h from the clock cut to seconds", body, got)
		}
	}
	for name, tc := range map[string]struct {
		auth *apikit.AuthInfo
		want string
	}{"admin": {adminAuth(), "admin"}, "api key": {ownerKey(), owner}, "PAT": {pat("sandboxes:write"), owner}} {
		if got := env.mustIssueAs("widgets", "{}", tc.auth)["created_by"]; got != tc.want {
			t.Errorf("%s: created_by = %v; want %q", name, got, tc.want)
		}
	}
	got := env.mustIssue(`{"profile":"tools","ttl":"61.5s"}`)
	if got["expires_at"] != "2030-01-02T03:05:06Z" || got["lease_expires_at"] != got["expires_at"] {
		t.Errorf("ttl 61.5s: expires_at %v, lease_expires_at %v; want 61 whole seconds and the deadline cut to expires_at", got["expires_at"], got["lease_expires_at"])
	}
	if seconds := testutil.Count(t, env.db, `SELECT ttl_seconds FROM sandbox_leases WHERE id = ?`, got["id"]); seconds != 61 {
		t.Errorf("ttl_seconds = %d; want 61", seconds)
	}
}

// A body that is not an object with only the four fields, spelled exactly and
// of the right types, is a 400 naming the field; a body over the server's limit
// is 413 and one that fails while read is 400. Nothing is stored.
func TestIssue_BodyValidation(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	for _, tc := range []struct{ name, body, want string }{
		{"unknown field", `{"image":"evil"}`, `unknown field "image"`},
		{"unknown beside known", `{"profile":"tools","gateway_url":"x"}`, "gateway_url"},
		{"malformed JSON", `{"profile":`, "not valid JSON"},
		{"array", `[1]`, "must be a JSON object"},
		{"null", `null`, "JSON object"},
		{"trailing data", `{} {}`, "not valid JSON"},
		{"stray bracket", `{"profile":"tools"}]`, "not valid JSON"},
		{"capitals", `{"PROFILE":"tools"}`, `"PROFILE"`},
		{"profile not a string", `{"profile":7}`, `field "profile" must be a string, not a number`},
		{"purpose not a string", `{"purpose":["x"]}`, `"purpose" must be a string, not an array`},
		{"ttl not a string", `{"ttl":3600}`, "ttl"},
		{"labels not an object", `{"labels":"k=v"}`, `"labels" must be an object, not a string`},
	} {
		rec := env.post("widgets", tc.body, ownerKey())
		if rec.Code != http.StatusBadRequest || !strings.Contains(message(t, rec), tc.want) {
			t.Errorf("%s: %d %q; want 400 containing %q", tc.name, rec.Code, message(t, rec), tc.want)
		}
	}
	auth, _ := json.Marshal(ownerKey())
	send := func(body io.Reader, limit int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/widgets/sandboxes", nil)
		req.Header.Set(testutil.AuthHeader, string(auth))
		rec := httptest.NewRecorder()
		req.Body = io.NopCloser(body)
		if limit > 0 {
			req.Body = http.MaxBytesReader(rec, req.Body, limit)
		}
		env.echo.ServeHTTP(rec, req)
		return rec
	}
	if rec := send(strings.NewReader(`{"purpose":"`+strings.Repeat("x", 100)+`"}`), 20); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over the limit: %d; want 413", rec.Code)
	}
	if rec := send(io.MultiReader(strings.NewReader(`{"purpose":`), failingReader{}), 0); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that fails while read: %d; want 400", rec.Code)
	}
	if n := env.countLeases(); n != 0 {
		t.Errorf("%d leases stored for refused bodies", n)
	}
}

// profile names a configured profile exactly or is absent with a default, ttl
// runs from 1m to the profile's, purpose is at most 64 runes without control
// characters, labels at most 16 well-formed string pairs; a violation is 400.
func TestIssue_ParameterValidation(t *testing.T) {
	noDefault := bigLeaseConfig()
	noDefault.Profiles[0].Default = false
	env := newLeaseEnv(t, leaseEnvOptions{config: noDefault})
	seventeen := map[string]any{}
	for i := 1; i <= 17; i++ {
		seventeen[fmt.Sprintf("k%02d", i)] = "v"
	}
	labels := func(m map[string]any) string {
		raw, _ := json.Marshal(map[string]any{"profile": "tools", "labels": m})
		return string(raw)
	}
	for _, tc := range []struct{ name, body, want string }{
		{"unknown profile", `{"profile":"nope"}`, `unknown sandbox profile "nope"; known profiles: tools, data`},
		{"wrong case", `{"profile":"Tools"}`, "tools, data"},
		{"empty profile", `{"profile":""}`, "known profiles"},
		{"absent, no default", `{}`, "profile is required: no profile is marked as the default; known profiles: tools, data"},
		{"empty body, no default", ``, "profile is required"},
		{"ttl garbage", `{"profile":"tools","ttl":"soon"}`, `ttl "soon" is not a valid duration`},
		{"ttl too short", `{"profile":"tools","ttl":"59s"}`, "shorter than the minimum of 1m"},
		{"ttl over the profile", `{"profile":"tools","ttl":"2h"}`, `longer than the ttl of profile "tools", 1h`},
		{"ttl over data's", `{"profile":"data","ttl":"3601s"}`, "30m"},
		{"purpose 65 runes", `{"profile":"tools","purpose":"` + strings.Repeat("é", 65) + `"}`, "longer than 64 characters"},
		{"purpose control", `{"profile":"tools","purpose":"a\u0085b"}`, "control characters"},
		{"17 labels", labels(seventeen), `label "k17" is over the limit of 16`},
		{"non-string value", labels(map[string]any{"numkey": 1}), `value of label "numkey" must be a string`},
		{"leading dash", labels(map[string]any{"-bad": "v"}), `label key "-bad" is not`},
		{"space", labels(map[string]any{"has space": "v"}), "has space"},
		{"64-character key", labels(map[string]any{strings.Repeat("k", 64): "v"}), strings.Repeat("k", 64)},
		{"reserved lease", labels(map[string]any{"Lease": "v"}), `label key "Lease" is reserved`},
		{"257-byte value", labels(map[string]any{"bigval": strings.Repeat("v", 257)}), `value of label "bigval" is longer than 256 bytes`},
		{"control in value", labels(map[string]any{"ctlval": "a\nb"}), `value of label "ctlval" must not contain control`},
	} {
		rec := env.post("widgets", tc.body, ownerKey())
		if rec.Code != http.StatusBadRequest || !strings.Contains(message(t, rec), tc.want) {
			t.Errorf("%s: %d %q; want 400 containing %q", tc.name, rec.Code, message(t, rec), tc.want)
		}
	}
	if n := env.countLeases(); n != 0 {
		t.Errorf("%d leases stored for refused requests", n)
	}
	// The limits themselves are accepted and stored as given.
	atLimit := map[string]string{strings.Repeat("k", 63): strings.Repeat("v", 256), "a": "", "A.b_c-D": "mixed Case ünï", "multibyte": strings.Repeat("é", 128)}
	for i := 0; len(atLimit) < 16; i++ {
		atLimit[fmt.Sprintf("k%d", i)] = "v"
	}
	raw, _ := json.Marshal(map[string]any{"profile": "tools", "ttl": "1m", "purpose": strings.Repeat("é", 64), "labels": atLimit})
	got := env.mustIssue(string(raw))
	created, _ := apikit.ParseUTC(got["created_at"].(string))
	expires, _ := apikit.ParseUTC(got["expires_at"].(string))
	if expires.Sub(created) != time.Minute || len(got["labels"].(map[string]any)) != 16 || got["labels"].(map[string]any)["multibyte"] != strings.Repeat("é", 128) {
		t.Errorf("at the limits: %v", got)
	}
	for _, ttl := range []string{"1h", "3600s"} { // the profile's TTL itself
		env.mustIssue(fmt.Sprintf(`{"profile":"tools","ttl":%q}`, ttl))
	}
}

// A missing or unowned workspace is 404 (alike), an admin token passes, and a
// workspace that is not active is 400 'workspace is not active'.
func TestIssue_WorkspaceChecks(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	env.addWorkspace("old", "", "archived")
	stranger := testutil.User("user-2")
	unowned, missing := env.post("widgets", `{}`, stranger), env.post("nosuch", `{}`, stranger)
	if unowned.Code != http.StatusNotFound || missing.Code != http.StatusNotFound || message(t, unowned) != message(t, missing) {
		t.Errorf("unowned %d %q, missing %d %q; want the same 404", unowned.Code, message(t, unowned), missing.Code, message(t, missing))
	}
	if rec := env.post("old", `{}`, ownerKey()); rec.Code != http.StatusBadRequest || message(t, rec) != "workspace is not active" {
		t.Errorf("archived workspace: %d %q", rec.Code, message(t, rec))
	}
	if env.countLeases() != 0 {
		t.Error("a refused request stored a lease")
	}
	env.mustIssueAs("widgets", "{}", adminAuth())
}

// Missing required secrets are 422 secret_missing naming the keys in profile
// order and the scope types, never an owner, an org or a secret that exists; a
// name stored in another case counts; a store or membership lookup error is 500.
func TestIssue_RequiredSecrets(t *testing.T) {
	cfg := bigLeaseConfig()
	cfg.Profiles[0].Secrets = []string{"C_KEY", "A_KEY", "B_KEY"}
	env := newLeaseEnv(t, leaseEnvOptions{config: cfg})
	env.plantSecret("user", owner, "b_key", "value-b")
	rec := env.post("widgets", `{}`, ownerKey())
	if rec.Code != http.StatusUnprocessableEntity || errorType(t, rec) != "secret_missing" {
		t.Fatalf("%d %s; want 422 secret_missing", rec.Code, rec.Body)
	}
	if msg := message(t, rec); !strings.Contains(msg, "C_KEY, A_KEY (searched scopes: workspace, user)") || strings.Contains(msg, "B_KEY") || strings.Contains(msg, owner) || strings.Contains(msg, "value-b") {
		t.Errorf("message = %q", msg)
	}
	env.addWorkspace("teamed", "org-77", "active")
	env.addMember("org-77", owner)
	if rec := env.post("teamed", `{}`, ownerKey()); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(message(t, rec), "workspace, org, user") || strings.Contains(message(t, rec), "org-77") {
		t.Errorf("teamed: %d %q", rec.Code, message(t, rec))
	}
	env.plantSecret("workspace", "widgets", "C_KEY", "x")
	env.plantSecret("user", owner, "A_KEY", "y")
	env.mustIssue("")
	if n := env.countLeases(); n != 1 {
		t.Errorf("%d leases; want the one issued once every secret existed", n)
	}

	lister := newFakeLister()
	lister.err = errors.New("boom-secret-detail")
	failing := newLeaseEnv(t, leaseEnvOptions{lister: lister})
	if rec := failing.post("widgets", `{}`, ownerKey()); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "boom-secret-detail") {
		t.Errorf("a failing store: %d %s; want 500 without the cause", rec.Code, rec.Body)
	}
	testutil.Exec(t, env.db, `DROP TABLE org_members`)
	if rec := env.post("teamed", `{}`, ownerKey()); rec.Code != http.StatusInternalServerError {
		t.Errorf("a missing org_members table: %d; want 500", rec.Code)
	}
}

// POST at max_sandboxes is 409 sandbox_capacity naming the limit; a place frees
// up when a lease ends, passes expires_at or, leased, passes its deadline.
func TestIssue_Capacity(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: capacityConfig(2)})
	env.addWorkspace("gadgets", "", "active")
	env.plantSecret("workspace", "gadgets", "GIT_PAT", "x")
	first := env.issue("")
	env.mustIssueAs("gadgets", "", ownerKey())
	rec := env.post("widgets", `{}`, ownerKey())
	if rec.Code != http.StatusConflict || errorType(t, rec) != "sandbox_capacity" || !strings.Contains(message(t, rec), "at most 2 leases") {
		t.Fatalf("third lease: %d %s; want 409 sandbox_capacity naming 2", rec.Code, rec.Body)
	}
	if env.delete(first).Code != http.StatusNoContent {
		t.Fatal("DELETE failed")
	}
	env.mustIssue("")
	if rec := env.post("widgets", `{}`, ownerKey()); rec.Code != http.StatusConflict {
		t.Fatalf("at capacity again: %d", rec.Code)
	}
	env.clock.Advance(6 * time.Minute) // past the 5m deadline of every leased lease
	env.mustIssue("")
	env.clock.Advance(2 * time.Hour) // past expires_at of every lease
	env.mustIssue("")
}

// Concurrent POSTs never exceed max_sandboxes, even on a handle whose
// transactions are deferred: the lock in the process keeps the limit.
func TestIssue_ConcurrentPostsRespectTheLimit(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "deferred.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { db.Close() })
	initTables(t, db)
	env := newLeaseEnv(t, leaseEnvOptions{config: capacityConfig(3), db: db})
	insertRawLease(t, db, leaseID(1), "widgets", "leased", HashToken(testToken(1)))

	codes := make([]int, 12)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Go(func() { codes[i] = env.post("widgets", `{}`, ownerKey()).Code })
	}
	wg.Wait()
	created := 0
	for _, code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Errorf("a POST answered %d; want 201 or 409", code)
		}
	}
	if live, _ := env.store.countLive(db, env.clock.Now()); created != 2 || live != 3 {
		t.Errorf("%d created, %d live; want 2 and the limit 3", created, live)
	}
}

// A database or random-source failure is a 500 that names no cause, leaves no
// row and does not keep the lock, so the next POST goes through.
func TestIssue_InternalFailures(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: capacityConfig(5)})
	testutil.Exec(t, env.db, `CREATE TRIGGER fail_insert BEFORE INSERT ON sandbox_leases BEGIN SELECT RAISE(ABORT, 'injected fault'); END`)
	if rec := env.post("widgets", `{}`, ownerKey()); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "injected fault") {
		t.Errorf("failing insert: %d %s", rec.Code, rec.Body)
	}
	testutil.Exec(t, env.db, `DROP TRIGGER fail_insert`)
	testutil.Exec(t, env.db, `ALTER TABLE sandbox_leases RENAME TO away`)
	if rec := env.post("widgets", `{}`, ownerKey()); rec.Code != http.StatusInternalServerError {
		t.Errorf("failing count: %d; want 500", rec.Code)
	}
	testutil.Exec(t, env.db, `ALTER TABLE away RENAME TO sandbox_leases`)
	done := make(chan int, 1)
	go func() { done <- env.post("widgets", `{}`, ownerKey()).Code }()
	select {
	case code := <-done:
		if code != http.StatusCreated {
			t.Errorf("POST after the faults: %d; want 201", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lock was not released")
	}
	if n := env.countLeases(); n != 1 {
		t.Errorf("%d rows; want only the one issued after the faults", n)
	}
	broken := newLeaseEnv(t, leaseEnvOptions{rand: failingReader{}})
	if rec := broken.post("widgets", `{}`, ownerKey()); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "random source down") || broken.countLeases() != 0 {
		t.Errorf("failing random source: %d %s, %d rows", rec.Code, rec.Body, broken.countLeases())
	}
}

// A POST that succeeds emits one hub.sandbox.lease event with the actor, the
// names and no secret or token; a refusal emits none; a nil or failing emitter
// changes nothing about the 201, and the failure is logged without the token.
func TestIssue_AuditEvent(t *testing.T) {
	logs := captureDefaultLog(t)
	emitter := &recordingEmitter{}
	env := newLeaseEnv(t, leaseEnvOptions{emitter: emitter})
	id, token := env.issueToken(`{"profile":"tools","purpose":"nightly","ttl":"30m"}`)
	env.post("widgets", `{"profile":"nope"}`, ownerKey())
	env.mustIssueAs("widgets", "{}", adminAuth())
	events := eventsOf(emitter, audit.EventSandboxLease)
	if len(events) != 2 || len(emitter.recorded()) != 2 {
		t.Fatalf("events = %+v; want two lease events", emitter.recorded())
	}
	e := events[0]
	if e.ResourceType != "sandbox" || e.ResourceID != id || e.Action != "lease" || e.Workspace != "widgets" || e.ActorID != owner || e.ActorType != "api_key" {
		t.Errorf("event = %+v", e)
	}
	want := map[string]any{"lease_id": id, "profile": "tools", "purpose": "nightly", "ttl": "30m0s", "secret_names": []string{"GIT_PAT"}, "optional_secret_names": []string{"EXTRA_TOKEN"}}
	if !reflect.DeepEqual(e.Metadata, want) {
		t.Errorf("metadata = %#v\nwant       %#v", e.Metadata, want)
	}
	if events[1].ActorType != "admin_token" || events[1].ActorID != "" {
		t.Errorf("admin event actor = %s %q", events[1].ActorType, events[1].ActorID)
	}
	raw, _ := json.Marshal(events)
	containsNone(t, "the events and the log", string(raw)+logs.String(), token, HashToken(token), plantedSecret, "envval")

	failing := &recordingEmitter{err: errors.New("audit store down")}
	env = newLeaseEnv(t, leaseEnvOptions{emitter: failing})
	_, token = env.issueToken("")
	if len(failing.recorded()) != 1 || !strings.Contains(logs.String(), "audit store down") || strings.Contains(logs.String(), token) {
		t.Errorf("a failing emitter: %d attempts, log:\n%s", len(failing.recorded()), logs)
	}
	newLeaseEnv(t, leaseEnvOptions{}).mustIssue("")
}
