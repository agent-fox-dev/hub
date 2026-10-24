package forge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

const unverifiedText = "delivery signature missing or invalid"

// failReader fails the test when the body is read.
type failReader struct {
	t    *testing.T
	read atomic.Bool
}

func (f *failReader) Read([]byte) (int, error) {
	f.read.Store(true)
	f.t.Error("the body was read")
	return 0, io.EOF
}

// post sends a request with the headers of d and the body to the ingress of slug.
func (e *env) post(slug string, d signedDelivery, body io.Reader, length int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/hooks/forge/"+slug, body)
	req.Header = d.header.Clone()
	req.ContentLength = length
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	return rec
}

// The ingress is served on the root without bearer authentication and never contacts the
// forge; an unknown slug, a workspace without a workspace-scope secret and one whose secret
// is empty answer the same 404 before the body is read; a failing store answers 500 without
// the cause; an archived workspace with a secret works like an active one.
func TestIngress_LookupOfTheWorkspace(t *testing.T) {
	counting := &countingTransport{}
	e, em := ingressEnv(t, func(cfg *Config) { cfg.Transport = counting })
	e.webhookWorkspace("old", archived)
	addWorkspace(t, e.db, "nosecret")
	addWorkspace(t, e.db, "blank")
	for _, scope := range [][2]string{{"org", "acme"}, {"user", ownerID}} {
		if _, err := e.secrets.CreateSecrets(scope[0], scope[1], []secrets.EntryInput{{Key: webhookSecretKey, Value: ingressSecret}}); err != nil {
			t.Fatal(err)
		}
	}
	testutil.Exec(t, e.db, `INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES ('workspace', 'blank', ?, '', ?, ?)`, webhookSecretKey, at(0), at(0))
	for _, slug := range []string{"widgets", "old"} {
		if outcome := outcomeOf(t, e.deliver(slug, labeledDelivery("d-"+slug))); outcome != "relayed" {
			t.Errorf("%s: outcome = %q; want relayed", slug, outcome)
		}
	}
	if rec := e.get("/workspaces/widgets/forge", nil); rec.Code != http.StatusUnauthorized || counting.n.Load() != 0 {
		t.Errorf("the API without a credential answered %d and the forge saw %d requests; want 401 and none", rec.Code, counting.n.Load())
	}
	unknown := e.post("nosuch", labeledDelivery("d-1"), &failReader{t: t}, 100)
	for _, slug := range []string{"nosecret", "blank"} {
		for _, d := range []signedDelivery{githubDelivery("", "d-2", "ping", "{}"), gitlabDelivery("", "d-3", "Issue Hook", "{}")} {
			if rec := e.post(slug, d, &failReader{t: t}, 100); rec.Code != http.StatusNotFound || rec.Body.String() != unknown.Body.String() {
				t.Errorf("%s: %d %s; want the 404 of an unknown slug %s", slug, rec.Code, rec.Body, unknown.Body)
			}
		}
	}
	if rows, events := e.deliveryRows(), em.recorded(); len(rows) != 2 || len(events) != 2 {
		t.Errorf("%d rows and %d events; want the 2 of the accepted deliveries", len(rows), len(events))
	}
	testutil.Exec(t, e.db, `ALTER TABLE workspaces RENAME TO workspaces_gone`)
	if rec := e.deliver("widgets", labeledDelivery("d-4")); rec.Code != http.StatusInternalServerError {
		t.Errorf("a missing workspaces table: %d; want 500", rec.Code)
	}
	for name, cfg := range map[string]func(*Config){
		"a failing secrets store": withFakeSecrets(&fakeSecrets{getErr: errors.New("boom: disk gone")}),
		"no secrets store":        func(cfg *Config) { cfg.Secrets = nil },
	} {
		broken := newEnv(t, cfg)
		addWorkspace(t, broken.db, "widgets")
		if rec := broken.deliver("widgets", labeledDelivery("d-1")); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("%s: %d %s; want 500 without the cause", name, rec.Code, rec.Body)
		}
	}
}

// A GitHub delivery is verified by sha256= and the exact 64-digit HMAC of the body given
// once; a GitLab delivery by X-Gitlab-Token equal to the secret given once, checked before
// the body is read; with both headers GitHub's scheme decides. An unverified delivery is
// 401 with one message, records nothing, and logs the slug and the scheme and nothing else.
func TestIngress_Verification(t *testing.T) {
	logs := captureLog(t)
	e, em := ingressEnv(t)
	body := issuesPayload("labeled", "acme/widgets")
	good := signGitHub(ingressSecret, body)
	digest := strings.TrimPrefix(good, "sha256=")
	const marker = "MARKER-0123456789"
	for name, sig := range map[string]string{
		"another body": signGitHub(ingressSecret, body+" "), "another key": signGitHub("another-key-of-16-chars", body), "no prefix": digest, "sha1": "sha1=" + digest,
		"63 digits": "sha256=" + digest[:63], "65 digits": "sha256=" + digest + "0", "capitals": "sha256=" + strings.ToUpper(digest), "empty": "", "marker": "sha256=" + marker,
	} {
		rec := e.deliver("widgets", githubDelivery(ingressSecret, "d-1", "issues", body).with("X-Hub-Signature-256", sig))
		if rec.Code != http.StatusUnauthorized || testutil.Error(t, rec).Message != unverifiedText {
			t.Errorf("signature %s: %d %s; want 401 %q", name, rec.Code, rec.Body, unverifiedText)
		}
	}
	twice := githubDelivery(ingressSecret, "d-1", "issues", body)
	twice.header["X-Hub-Signature-256"] = []string{good, "sha256=" + strings.Repeat("0", 64)}
	if rec := e.deliver("widgets", twice); rec.Code != http.StatusUnauthorized {
		t.Errorf("the signature header twice: %d; want 401", rec.Code)
	}
	if rec := e.deliver("widgets", githubDelivery(ingressSecret, "d-1", "ping", body).without("X-Hub-Signature-256")); rec.Code != http.StatusUnauthorized {
		t.Errorf("no scheme header: %d; want 401", rec.Code)
	}

	wrong := gitlabDelivery("a-wrong-token-"+marker, "d-2", "Issue Hook", "")
	if rec := e.post("widgets", wrong, &failReader{t: t}, 100); rec.Code != http.StatusUnauthorized || testutil.Error(t, rec).Message != unverifiedText {
		t.Errorf("a wrong GitLab token: %d %s; want 401 before the body is read", rec.Code, rec.Body)
	}
	gl := gitlabDelivery(ingressSecret, "d-3", "Issue Hook", gitlabIssuePayload("update", "acme/widgets"))
	for _, token := range []string{ingressSecret + "x", ingressSecret[1:], strings.ToUpper(ingressSecret), ""} {
		if rec := e.deliver("widgets", gl.with("X-Gitlab-Token", token)); rec.Code != http.StatusUnauthorized {
			t.Errorf("the token %q: %d; want 401", token, rec.Code)
		}
	}
	gl.header["X-Gitlab-Token"] = []string{ingressSecret, ingressSecret}
	if rec := e.deliver("widgets", gl); rec.Code != http.StatusUnauthorized {
		t.Errorf("the token header twice: %d; want 401", rec.Code)
	}
	if rows, events := e.deliveryRows(), em.recorded(); len(rows) != 0 || len(events) != 0 {
		t.Errorf("%d rows and %d events after unverified deliveries; want none", len(rows), len(events))
	}
	log := logs.String()
	for _, want := range []string{"scheme=github", "scheme=gitlab", "scheme=none", "level=WARN", "slug=widgets"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}
	for _, leak := range []string{marker, ingressSecret, "sha256=", "another-key"} {
		if strings.Contains(log, leak) {
			t.Errorf("the log holds %q", leak)
		}
	}

	if outcome := outcomeOf(t, e.deliver("widgets", githubDelivery(ingressSecret, "d-4", "issues", body))); outcome != "relayed" {
		t.Errorf("the right signature: outcome = %q; want relayed", outcome)
	}
	if outcome := outcomeOf(t, e.deliver("widgets", gitlabDelivery(ingressSecret, "d-5", "Issue Hook", gitlabIssuePayload("update", "acme/widgets")))); outcome != "ignored" {
		t.Errorf("the right token: outcome = %q; want it accepted (ignored)", outcome)
	}
	both := githubDelivery(ingressSecret, "d-6", "issues", body).with("X-Gitlab-Token", "a-wrong-token-of-16+")
	if outcome := outcomeOf(t, e.deliver("widgets", both)); outcome != "relayed" {
		t.Errorf("both headers, the signature right: outcome = %q; want relayed", outcome)
	}
	both = githubDelivery("another-key-of-16-chars", "d-7", "issues", body).with("X-Gitlab-Token", ingressSecret)
	if rec := e.deliver("widgets", both); rec.Code != http.StatusUnauthorized {
		t.Errorf("both headers, the signature wrong: %d; want 401", rec.Code)
	}
}

// A rotated secret, through PUT or the store, applies from the next delivery; after DELETE a
// delivery answers the 404 of an unknown slug and writes no row while the earlier rows stay
// listed; a new PUT turns the ingress on again.
func TestIngress_FollowsTheSecret(t *testing.T) {
	e, em := ingressEnv(t)
	sign := func(secret, id string) signedDelivery {
		return githubDelivery(secret, id, "issues", issuesPayload("labeled", "acme/widgets"))
	}
	outcomeOf(t, e.deliver("widgets", sign(ingressSecret, "d-0")))
	e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":"the-new-secret-of-16-chars"}`)
	if _, err := e.secrets.UpdateSecret("workspace", "widgets", webhookSecretKey, "the-newest-secret-of-16+"); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{ingressSecret, "the-new-secret-of-16-chars"} {
		if rec := e.deliver("widgets", sign(old, "d-1")); rec.Code != http.StatusUnauthorized {
			t.Errorf("an old secret: %d; want 401", rec.Code)
		}
	}
	if outcome := outcomeOf(t, e.deliver("widgets", sign("the-newest-secret-of-16+", "d-2"))); outcome != "relayed" {
		t.Errorf("the newest secret: outcome = %q", outcome)
	}
	if rec := e.send(http.MethodDelete, webhookPath, apiKeyAuth(), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body)
	}
	after, unknown := e.deliver("widgets", sign("the-newest-secret-of-16+", "d-3")), e.deliver("nosuch", sign("the-newest-secret-of-16+", "d-3"))
	rec := e.get("/workspaces/widgets/forge/deliveries", apiKeyAuth())
	if after.Code != http.StatusNotFound || after.Body.String() != unknown.Body.String() || len(e.deliveryRows()) != 2 || !strings.Contains(rec.Body.String(), `"delivery_id":"d-2"`) || len(em.recorded()) != 4 {
		t.Errorf("after the unset: %d %s (unknown slug %s), %d rows, %d events, list %s; want the same 404, the 2 earlier rows and the 4 earlier events", after.Code, after.Body, unknown.Body, len(e.deliveryRows()), len(em.recorded()), rec.Body)
	}
	e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":"a-secret-set-again-16+"}`)
	if outcome := outcomeOf(t, e.deliver("widgets", sign("a-secret-set-again-16+", "d-4"))); outcome != "relayed" {
		t.Errorf("after the PUT: outcome = %q; want relayed", outcome)
	}
}

// A body over 1 MiB is 413 body_too_large, whether its length is announced or not, without
// reading past the cap; exactly 1 MiB passes.
func TestIngress_BodyLimit(t *testing.T) {
	const mib = 1 << 20
	padded := func(size int) string { return `{"zen":"` + strings.Repeat("x", size-len(`{"zen":""}`)) + `"}` }
	e, em := ingressEnv(t)
	over := githubDelivery(ingressSecret, "d-over", "ping", padded(mib+1))
	for _, length := range []int64{int64(len(over.body)), -1} {
		rec := e.post("widgets", over, strings.NewReader(over.body), length)
		if rec.Code != http.StatusRequestEntityTooLarge || testutil.Error(t, rec).ErrorType != "body_too_large" {
			t.Errorf("length %d: %d %s; want 413 body_too_large", length, rec.Code, rec.Body)
		}
	}
	if rows, events := e.deliveryRows(), em.recorded(); len(rows) != 0 || len(events) != 0 {
		t.Errorf("%d rows and %d events; want none", len(rows), len(events))
	}
	if outcome := outcomeOf(t, e.deliver("widgets", githubDelivery(ingressSecret, "d-exact", "ping", padded(mib)))); outcome != "ignored" {
		t.Errorf("exactly 1 MiB: outcome = %q; want ignored", outcome)
	}
	if rec := e.post("widgets", githubDelivery(ingressSecret, "d-1", "ping", "{}"), &failReader{t: t}, mib+1); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("an announced length over the cap: %d; want 413 without reading", rec.Code)
	}
	var read atomic.Int64
	counting := io.TeeReader(io.LimitReader(zeroReader{}, 8*mib), writerFunc(func(p []byte) { read.Add(int64(len(p))) }))
	if rec := e.post("widgets", githubDelivery(ingressSecret, "d-2", "ping", "{}"), counting, -1); rec.Code != http.StatusRequestEntityTooLarge || read.Load() > mib+1 {
		t.Errorf("a body of unknown length: %d after reading %d bytes; want 413 after at most 1 MiB + 1", rec.Code, read.Load())
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { return len(p), nil }

type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) { f(p); return len(p), nil }

// A verified delivery is recorded once with the id and the event taken from the headers of
// its forge (GitLab's Idempotency-Key before X-Gitlab-Event-UUID), the event cut to 64 bytes,
// bound to the workspace's repository even when it is not relayed; a delivery without an id
// or an event name, or with an invalid id, is 400 and records nothing.
func TestIngress_RecordsARow(t *testing.T) {
	e, em := ingressEnv(t)
	longEvent := strings.Repeat("e", 70)
	update := gitlabIssuePayload("update", "acme/widgets")
	before := time.Now().UTC().Add(-2 * time.Second)
	for _, d := range []signedDelivery{
		labeledDelivery("gh-1"),
		gitlabDelivery(ingressSecret, "idem-1", "Issue Hook", update).with("X-Gitlab-Event-UUID", "uuid-1"),
		gitlabDelivery(ingressSecret, "ignored", "Issue Hook", update).without("Idempotency-Key").with("X-Gitlab-Event-UUID", "uuid-2"),
		githubDelivery(ingressSecret, strings.Repeat("a", 255), longEvent, issuesPayload("labeled", "acme/widgets")),
	} {
		if rec := e.deliver("widgets", d); rec.Code != http.StatusAccepted {
			t.Fatalf("%d %s; want 202", rec.Code, rec.Body)
		}
	}
	rows := e.deliveryRows()
	if len(rows) != 4 {
		t.Fatalf("%d rows; want 4", len(rows))
	}
	for i, want := range []struct{ id, forge, event, action, outcome, relayed string }{
		{"gh-1", "github", "issues", "labeled", "relayed", `["hub.forge.issue.labeled"]`},
		{"idem-1", "gitlab", "Issue Hook", "update", "ignored", `[]`},
		{"uuid-2", "gitlab", "Issue Hook", "update", "ignored", `[]`},
		{strings.Repeat("a", 255), "github", longEvent[:64], "labeled", "ignored", `[]`},
	} {
		row := rows[i]
		received, err := apikit.ParseUTC(row.receivedAt)
		if row.slug != "widgets" || row.id != want.id || row.forge != want.forge || row.event != want.event || row.action != want.action || row.outcome != want.outcome ||
			row.relayed != want.relayed || row.target == nil || *row.target != "origin" || err != nil || received.Before(before) || received.After(time.Now().UTC().Add(2*time.Second)) {
			t.Errorf("row %d = %+v; want %+v bound to origin, received now", i, row, want)
		}
	}
	if got := len(em.recorded()); got != 1 {
		t.Errorf("%d events; want the 1 of the relayed delivery", got)
	}
	ping := githubDelivery(ingressSecret, "d-1", "ping", `{}`)
	glPing := gitlabDelivery(ingressSecret, "d-1", "Issue Hook", `{}`)
	for name, d := range map[string]signedDelivery{
		"GitHub without an id": ping.without("X-GitHub-Delivery"), "GitHub without an event": ping.without("X-GitHub-Event"), "an empty event": ping.with("X-GitHub-Event", ""),
		"GitLab without either id header": glPing.without("Idempotency-Key"), "GitLab without an event": glPing.without("X-Gitlab-Event"),
		"an id of 256 characters": ping.with("X-GitHub-Delivery", strings.Repeat("a", 256)), "an id outside ASCII": ping.with("X-GitHub-Delivery", "d-\u00e9\x01"),
	} {
		if rec := e.deliver("widgets", d); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s; want 400", name, rec.Code, rec.Body)
		}
	}
	if rows := e.deliveryRows(); len(rows) != 4 {
		t.Errorf("%d rows after refused deliveries; want still 4", len(rows))
	}
}

// A repeated id is 202 duplicate with no row and no event, also for a malformed delivery,
// while the same id in another workspace is another delivery; copies that arrive together
// record one row and relay once; a failure of the log is 500 and emits nothing, so that the
// redelivery is processed in full.
func TestIngress_Duplicates(t *testing.T) {
	e, em := ingressEnv(t)
	e.webhookWorkspace("other")
	outcomeOf(t, e.deliver("widgets", labeledDelivery("gh-1")))
	if outcome := outcomeOf(t, e.deliver("widgets", labeledDelivery("gh-1"))); outcome != "duplicate" {
		t.Errorf("second: outcome = %q; want duplicate", outcome)
	}
	if outcome := outcomeOf(t, e.deliver("other", labeledDelivery("gh-1"))); outcome != "relayed" {
		t.Errorf("the same id in another workspace: outcome = %q; want relayed", outcome)
	}
	if rec := e.deliver("widgets", githubDelivery(ingressSecret, "bad", "issues", `[]`)); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed: %d; want 400", rec.Code)
	}
	if outcome := outcomeOf(t, e.deliver("widgets", githubDelivery(ingressSecret, "bad", "issues", `[]`))); outcome != "duplicate" {
		t.Errorf("malformed again: outcome = %q; want duplicate", outcome)
	}
	if rows, events := e.deliveryRows(), em.recorded(); len(rows) != 3 || len(events) != 2 {
		t.Errorf("%d rows and %d events; want 3 and 2", len(rows), len(events))
	}

	const n = 12
	outcomes := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			outcomes[i] = strings.TrimSpace(e.deliver("widgets", labeledDelivery("conc")).Body.String())
		}()
	}
	close(start)
	wg.Wait()
	relayed, duplicates := 0, 0
	for _, o := range outcomes {
		switch o {
		case `{"outcome":"relayed"}`:
			relayed++
		case `{"outcome":"duplicate"}`:
			duplicates++
		}
	}
	if relayed != 1 || duplicates != n-1 || len(e.deliveryRows()) != 4 || len(em.recorded()) != 3 {
		t.Errorf("%d copies: %d relayed, %d duplicates, %d rows, %d events; want 1, %d, 4, 3", n, relayed, duplicates, len(e.deliveryRows()), len(em.recorded()), n-1)
	}

	testutil.Exec(t, e.db, `DROP TABLE forge_deliveries`)
	rec := e.deliver("widgets", labeledDelivery("gh-2"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "forge_deliveries") || len(em.recorded()) != 3 {
		t.Errorf("a failing log: %d %s with %d events; want 500 without the cause and no new event", rec.Code, rec.Body, len(em.recorded()))
	}
	if err := InitSchema(e.db); err != nil {
		t.Fatal(err)
	}
	if outcome := outcomeOf(t, e.deliver("widgets", labeledDelivery("gh-2"))); outcome != "relayed" || len(em.recorded()) != 4 {
		t.Errorf("the redelivery: outcome = %q with %d events; want relayed and 4", outcome, len(em.recorded()))
	}
}

// The outcome of a delivery is the reduction's: malformed is 400 and recorded with no target;
// ignored, repository_mismatch and relayed are 202 with the outcome; the row holds the target
// the repository was bound to (origin or the upstream of a carry_patch workspace, the origin
// when both match, ignoring case) and the events relayed.
func TestIngress_OutcomesAndBinding(t *testing.T) {
	e, em := ingressEnv(t)
	e.webhookWorkspace("fork", carryPatch("https://github.com/upstream/widgets"))
	e.webhookWorkspace("same", carryPatch("https://github.com/acme/widgets"))
	e.webhookWorkspace("caps", withGitURL("https://forge.example.com/Acme/Widgets.git"))
	e.webhookWorkspace("leftover", func(w *testutil.Workspace) { w.UpstreamURL = "https://github.com/upstream/widgets" })
	e.webhookWorkspace("unreadable", withGitURL("not a remote"))
	for _, tc := range []struct {
		name, slug string
		d          signedDelivery
		status     int
		outcome    string
		target     string
		relayed    string
	}{
		{"not a JSON object", "widgets", githubDelivery(ingressSecret, "d-1", "issues", `[]`), 400, "malformed", "", "[]"},
		{"a field missing", "widgets", githubDelivery(ingressSecret, "d-2", "issues", `{"action":"labeled","repository":{"full_name":"acme/widgets"},"issue":{"number":7}}`), 400, "malformed", "", "[]"},
		{"no repository", "widgets", githubDelivery(ingressSecret, "d-3", "issues", `{"action":"labeled","label":{"name":"x"},"issue":{"number":7}}`), 400, "malformed", "", "[]"},
		{"a foreign repository lacking its identity", "widgets", githubDelivery(ingressSecret, "d-4", "issues", `{"action":"labeled","repository":{"full_name":"other/repo"},"issue":{"number":7}}`), 400, "malformed", "", "[]"},
		{"a kind hub does not relay", "widgets", githubDelivery(ingressSecret, "d-5", "star", `{"action":"created","repository":{"full_name":"acme/widgets"}}`), 202, "ignored", "origin", "[]"},
		{"a kind that says nothing, on another repository", "widgets", gitlabDelivery(ingressSecret, "d-6", "Merge Request Hook", strings.ReplaceAll(mergeRequestHook("update", nil), "acme/widgets", "other/repo")), 202, "ignored", "", "[]"},
		{"another repository", "widgets", githubDelivery(ingressSecret, "d-7", "issues", issuesPayload("labeled", "other/repo")), 202, "repository_mismatch", "", "[]"},
		{"a prefix of the path", "widgets", githubDelivery(ingressSecret, "d-8", "issues", issuesPayload("labeled", "acme/widget")), 202, "repository_mismatch", "", "[]"},
		{"the upstream on a standard workspace", "leftover", githubDelivery(ingressSecret, "d-9", "issues", issuesPayload("labeled", "upstream/widgets")), 202, "repository_mismatch", "", "[]"},
		{"an unreadable remote", "unreadable", githubDelivery(ingressSecret, "d-10", "issues", issuesPayload("labeled", "acme/widgets")), 202, "repository_mismatch", "", "[]"},
		{"the origin", "widgets", labeledDelivery("d-11"), 202, "relayed", "origin", `["hub.forge.issue.labeled"]`},
		{"the origin in another case", "caps", githubDelivery(ingressSecret, "d-12", "issues", issuesPayload("labeled", "ACME/WIDGETS")), 202, "relayed", "origin", `["hub.forge.issue.labeled"]`},
		{"the upstream of a fork", "fork", githubDelivery(ingressSecret, "d-13", "issues", issuesPayload("labeled", "upstream/widgets")), 202, "relayed", "upstream", `["hub.forge.issue.labeled"]`},
		{"the origin of a fork", "fork", labeledDelivery("d-14"), 202, "relayed", "origin", `["hub.forge.issue.labeled"]`},
		{"a repository that is both", "same", labeledDelivery("d-15"), 202, "relayed", "origin", `["hub.forge.issue.labeled"]`},
	} {
		rec := e.deliver(tc.slug, tc.d)
		if rec.Code != tc.status || (rec.Code == 202 && strings.TrimSpace(rec.Body.String()) != `{"outcome":"`+tc.outcome+`"}`) {
			t.Errorf("%s: %d %s; want %d %s", tc.name, rec.Code, rec.Body, tc.status, tc.outcome)
			continue
		}
		rows := e.deliveryRows()
		row := rows[len(rows)-1]
		target := ""
		if row.target != nil {
			target = *row.target
		}
		if row.outcome != tc.outcome || target != tc.target || row.relayed != tc.relayed {
			t.Errorf("%s: row = %+v; want %s bound to %q relaying %s", tc.name, row, tc.outcome, tc.target, tc.relayed)
		}
	}
	events := em.recorded()
	if len(events) != 5 || events[2].Metadata["target"] != "upstream" || events[3].Metadata["target"] != "origin" {
		t.Errorf("events = %+v; want the 5 relayed with their targets", events)
	}
}

// The 202 answer is written and flushed before any event is emitted; an emitter that fails
// on one event is logged and the others are still offered, in order; without an emitter a
// delivery is recorded and answered; the events are emitted on a context the request's
// cancellation does not reach; when the answer cannot be written the events are relayed all
// the same, since the redelivery would be a duplicate.
func TestIngress_RelaysAfterAnswering(t *testing.T) {
	logs := captureLog(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	let := func() { releaseOnce.Do(func() { close(release) }) }
	var count atomic.Int64
	blocking := emitterFunc(func(context.Context, audit.HubEvent) error {
		enteredOnce.Do(func() { close(entered) })
		<-release
		count.Add(1)
		return nil
	})
	e := newEnv(t, withEmitter(blocking))
	e.webhookWorkspace("widgets")
	server := httptest.NewServer(e.echo)
	t.Cleanup(server.Close)
	t.Cleanup(let)
	done := make(chan string, 1)
	go func() {
		d := labeledDelivery("gh-1")
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/hooks/forge/widgets", strings.NewReader(d.body))
		req.Header = d.header.Clone()
		resp, err := server.Client().Do(req)
		if err != nil {
			done <- err.Error()
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		done <- fmt.Sprintf("%d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}()
	<-entered
	select {
	case got := <-done:
		if got != `202 {"outcome":"relayed"}` || count.Load() != 0 {
			t.Errorf("while Emit was blocked the client got %q and %d events were stored; want the full 202 and 0", got, count.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the answer did not reach the client while Emit was blocked")
	}
	let()
	eventually(t, "the release of the event", func() bool { return count.Load() == 1 })

	failFirst := &recordingEmitter{}
	var calls atomic.Int64
	f, _ := ingressEnv(t, withEmitter(emitterFunc(func(ctx context.Context, ev audit.HubEvent) error {
		_ = failFirst.Emit(ctx, ev)
		if calls.Add(1) == 1 {
			return errors.New("the audit store is down")
		}
		return nil
	})))
	body := gitlabUpdateHook(map[string]any{"title": map[string]any{"previous": "a", "current": "b"}, "labels": labelChange([]string{"x"}, []string{"y"})}, "y")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := gitlabDelivery(ingressSecret, "d-1", "Issue Hook", body)
	req := httptest.NewRequest(http.MethodPost, "/hooks/forge/widgets", strings.NewReader(d.body)).WithContext(ctx)
	req.Header = d.header.Clone()
	rec := httptest.NewRecorder()
	f.echo.ServeHTTP(rec, req)
	want := []string{audit.EventForgeIssueEdited, audit.EventForgeIssueUnlabeled, audit.EventForgeIssueLabeled}
	if outcome := outcomeOf(t, rec); outcome != "relayed" || !reflect.DeepEqual(eventTypes(failFirst.recorded()), want) || failFirst.contexts()[0] != nil {
		t.Errorf("outcome %q, offered %v on %v; want relayed, all three in order on live contexts", outcome, eventTypes(failFirst.recorded()), failFirst.contexts())
	}
	if rows := f.deliveryRows(); len(rows) != 1 || rows[0].relayed != `["hub.forge.issue.edited","hub.forge.issue.unlabeled","hub.forge.issue.labeled"]` {
		t.Errorf("rows = %+v; want one relayed row listing the three events", rows)
	}
	if log := logs.String(); !strings.Contains(log, "level=ERROR") || !strings.Contains(log, audit.EventForgeIssueEdited) {
		t.Errorf("the log = %q; want an error line for the event that failed", log)
	}

	none := newEnv(t, func(cfg *Config) { cfg.Audit = nil })
	none.webhookWorkspace("widgets")
	if outcome := outcomeOf(t, none.deliver("widgets", labeledDelivery("d-1"))); outcome != "relayed" || len(none.deliveryRows()) != 1 {
		t.Errorf("without an emitter: outcome = %q with %d rows; want relayed and 1", outcome, len(none.deliveryRows()))
	}

	closed, em := ingressEnv(t)
	d = labeledDelivery("gh-1")
	req = httptest.NewRequest(http.MethodPost, "/hooks/forge/widgets", strings.NewReader(d.body))
	req.Header = d.header.Clone()
	closed.echo.ServeHTTP(&failingWriter{header: http.Header{}}, req)
	if got := eventTypes(em.recorded()); !reflect.DeepEqual(got, []string{audit.EventForgeIssueLabeled}) || !strings.Contains(logs.String(), "could not be written") {
		t.Errorf("events = %v; want the one of the delivery, and the failed write logged", got)
	}
	if outcome := outcomeOf(t, closed.deliver("widgets", d)); outcome != "duplicate" {
		t.Errorf("the redelivery: outcome = %q; want duplicate", outcome)
	}
}

// emitterFunc is an audit.Emitter made of a function.
type emitterFunc func(context.Context, audit.HubEvent) error

func (f emitterFunc) Emit(ctx context.Context, ev audit.HubEvent) error { return f(ctx, ev) }

// failingWriter is a response writer whose body cannot be written, as a connection the
// forge has closed.
type failingWriter struct {
	header http.Header
	status int
}

func (w *failingWriter) Header() http.Header       { return w.header }
func (w *failingWriter) WriteHeader(status int)    { w.status = status }
func (w *failingWriter) Write([]byte) (int, error) { return 0, errors.New("write: broken pipe") }
func (w *failingWriter) Flush()                    {}
