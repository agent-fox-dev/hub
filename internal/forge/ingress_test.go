package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
)

const (
	// ingressSecret is the webhook secret the ingress tests give a workspace.
	ingressSecret = "ingress-secret-of-16-chars"
	// unverifiedText is what the ingress answers a delivery that is not proven to come from the forge.
	unverifiedText = "delivery signature missing or invalid"
)

// signedDelivery is the headers and the body of a delivery.
type signedDelivery struct {
	header http.Header
	body   string
}

// signGitHub is X-Hub-Signature-256 of body under secret.
func signGitHub(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// githubDelivery is a delivery as GitHub sends it: the event and id in headers, the signature
// of the body.
func githubDelivery(secret, id, event, body string) signedDelivery {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-GitHub-Delivery", id)
	h.Set("X-GitHub-Event", event)
	h.Set("X-Hub-Signature-256", signGitHub(secret, body))
	return signedDelivery{h, body}
}

// gitlabDelivery is a delivery as GitLab sends it: the token in a header, and the id in
// Idempotency-Key.
func gitlabDelivery(secret, id, event, body string) signedDelivery {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Idempotency-Key", id)
	h.Set("X-Gitlab-Event", event)
	h.Set("X-Gitlab-Token", secret)
	return signedDelivery{h, body}
}

// without returns the delivery with the headers removed.
func (d signedDelivery) without(names ...string) signedDelivery {
	h := d.header.Clone()
	for _, name := range names {
		h.Del(name)
	}
	return signedDelivery{h, d.body}
}

// with returns the delivery with the header set.
func (d signedDelivery) with(name, value string) signedDelivery {
	h := d.header.Clone()
	h.Set(name, value)
	return signedDelivery{h, d.body}
}

// deliver posts the delivery to the ingress of the workspace.
func (e *env) deliver(slug string, d signedDelivery) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/forge/"+slug, strings.NewReader(d.body))
	req.Header = d.header.Clone()
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	return rec
}

// outcomeOf reads the outcome of a 202 answer; anything else stops the test.
func outcomeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want 202\n%s", rec.Code, rec.Body)
	}
	var doc map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || len(doc) != 1 {
		t.Fatalf("the body is not {\"outcome\": ...}: %v\n%s", err, rec.Body)
	}
	return doc["outcome"]
}

// deliveryRows returns the rows of the delivery log, in the order they were recorded.
func (e *env) deliveryRows() []deliveryRecord {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT workspace_slug, delivery_id, forge, event, action, target, outcome, relayed, received_at
		FROM forge_deliveries ORDER BY rowid`)
	if err != nil {
		e.t.Fatalf("read the delivery log: %v", err)
	}
	defer rows.Close()
	var out []deliveryRecord
	for rows.Next() {
		var r deliveryRecord
		if err := rows.Scan(&r.slug, &r.id, &r.forge, &r.event, &r.action, &r.target, &r.outcome, &r.relayed, &r.receivedAt); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// webhookWorkspace adds a workspace that has the webhook secret ingressSecret.
func (e *env) webhookWorkspace(slug string, mods ...func(*workspaceRow)) {
	e.t.Helper()
	addWorkspace(e.t, e.db, slug, mods...)
	if _, err := e.secrets.CreateSecrets("workspace", slug, []secrets.EntryInput{{Key: "FORGE_WEBHOOK_SECRET", Value: ingressSecret}}); err != nil {
		e.t.Fatal(err)
	}
}

// newIngressEnv is the routes and the ingress with a recording emitter and the workspace
// widgets (origin acme/widgets) that has a webhook secret.
func newIngressEnv(t *testing.T, mods ...func(*Config)) (*env, *recordingEmitter) {
	t.Helper()
	em := &recordingEmitter{}
	e := newEnv(t, append([]func(*Config){withEmitter(em)}, mods...)...)
	e.webhookWorkspace("widgets")
	return e, em
}

// issuesPayload is a GitHub issues delivery of the repository: issue 7 with two labels.
func issuesPayload(action, repo string) string {
	doc := map[string]any{
		"action":     action,
		"repository": map[string]any{"full_name": repo},
		"sender":     map[string]any{"login": "octocat"},
		"issue": map[string]any{
			"number": 7, "title": "Widgets break", "html_url": "https://github.com/" + repo + "/issues/7",
			"labels": []any{map[string]any{"name": "bug"}, map[string]any{"name": "af:ready"}},
		},
	}
	if action == "labeled" || action == "unlabeled" {
		doc["label"] = map[string]any{"name": "af:ready"}
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// gitlabIssuePayload is a GitLab Issue Hook delivery of the project: issue 7 with one label.
func gitlabIssuePayload(action, path string) string {
	b, _ := json.Marshal(map[string]any{
		"object_kind": "issue",
		"user":        map[string]any{"username": "alice"},
		"project":     map[string]any{"path_with_namespace": path},
		"object_attributes": map[string]any{
			"iid": 7, "title": "Widgets break", "action": action, "url": "https://gitlab.example.com/" + path + "/-/issues/7",
		},
		"labels": []any{map[string]any{"title": "bug"}},
	})
	return string(b)
}

// paddedBody is a JSON object of exactly size bytes.
func paddedBody(size int) string {
	const prefix, suffix = `{"zen":"`, `"}`
	return prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
}

// failReader fails the test when it is read.
type failReader struct {
	t    *testing.T
	read atomic.Bool
}

func (f *failReader) Read([]byte) (int, error) {
	f.read.Store(true)
	f.t.Error("the body was read")
	return 0, io.EOF
}

// TS-32-15: POST /hooks/forge/:slug is served on the Echo root without bearer authentication.
// Requirement: 32-REQ-3.1
func TestIngressIsServedWithoutBearerAuth_TS_32_15(t *testing.T) {
	counting := &countingTransport{}
	e, _ := newIngressEnv(t, func(cfg *Config) { cfg.Transport = counting })

	rec := e.deliver("widgets", githubDelivery(ingressSecret, "d-1", "issues", issuesPayload("labeled", "acme/widgets")))
	if outcome := outcomeOf(t, rec); outcome != "relayed" {
		t.Errorf("outcome = %q; want relayed", outcome)
	}
	if rec := e.get("/workspaces/widgets/forge", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("an API route without a credential: status = %d; want 401", rec.Code)
	}
	if n := counting.n.Load(); n != 0 {
		t.Errorf("the forge received %d requests; want none", n)
	}
}

// TS-32-16: An unknown slug and a workspace without a workspace-scope secret answer the same
// 404 before the body is read; a failure of the lookup answers 500.
// Requirement: 32-REQ-3.2
func TestIngressLookup_TS_32_16(t *testing.T) {
	e, em := newIngressEnv(t)
	addWorkspace(t, e.db, "nosecret")
	for _, scope := range [][2]string{{"org", "acme"}, {"user", ownerID}} {
		if _, err := e.secrets.CreateSecrets(scope[0], scope[1], []secrets.EntryInput{{Key: "FORGE_WEBHOOK_SECRET", Value: ingressSecret}}); err != nil {
			t.Fatal(err)
		}
	}
	post := func(slug string) *httptest.ResponseRecorder {
		body := &failReader{t: t}
		req := httptest.NewRequest(http.MethodPost, "/hooks/forge/"+slug, body)
		req.Header = githubDelivery(ingressSecret, "d-1", "issues", "{}").header
		rec := httptest.NewRecorder()
		e.echo.ServeHTTP(rec, req)
		return rec
	}

	unknown, bare := post("nosuch"), post("nosecret")
	if unknown.Code != http.StatusNotFound || bare.Code != http.StatusNotFound {
		t.Fatalf("status = %d and %d; want 404 for both", unknown.Code, bare.Code)
	}
	if unknown.Body.String() != bare.Body.String() {
		t.Errorf("the bodies differ: %q and %q", unknown.Body, bare.Body)
	}
	if rows := e.deliveryRows(); len(rows) != 0 {
		t.Errorf("rows = %v; want none", rows)
	}
	if got := em.recorded(); len(got) != 0 {
		t.Errorf("events = %v; want none", got)
	}

	// A failure of the lookup is 500, whichever store it is.
	if _, err := e.db.Exec(`ALTER TABLE workspaces RENAME TO workspaces_gone`); err != nil {
		t.Fatal(err)
	}
	if rec := post("widgets"); rec.Code != http.StatusInternalServerError {
		t.Errorf("a missing workspaces table: status = %d; want 500\n%s", rec.Code, rec.Body)
	}
	broken := newEnv(t, withFakeSecrets(&fakeSecrets{getErr: errors.New("boom: disk gone")}))
	addWorkspace(t, broken.db, "widgets")
	rec := broken.deliver("widgets", githubDelivery(ingressSecret, "d-1", "issues", "{}"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("a failing secrets store: status = %d, body %s; want 500 without the cause", rec.Code, rec.Body)
	}
}

// TS-32-17: A GitHub delivery is accepted only with sha256= and the exact 64-digit HMAC of the
// body.
// Requirement: 32-REQ-3.3
func TestIngressGitHubSignature_TS_32_17(t *testing.T) {
	e, _ := newIngressEnv(t)
	body := issuesPayload("labeled", "acme/widgets")
	good := signGitHub(ingressSecret, body)
	digest := strings.TrimPrefix(good, "sha256=")

	for _, tc := range []struct {
		name string
		sig  string
		ok   bool
	}{
		{"the signature of the body", good, true},
		{"the signature of another body", signGitHub(ingressSecret, body+" "), false},
		{"the signature under another key", signGitHub("another-key-of-16-chars", body), false},
		{"no sha256= prefix", digest, false},
		{"the sha1= prefix", "sha1=" + digest, false},
		{"63 digits", "sha256=" + digest[:63], false},
		{"65 digits", "sha256=" + digest + "0", false},
		{"characters that are not hex", "sha256=" + strings.Repeat("z", 64), false},
		{"the digest in capitals", "sha256=" + strings.ToUpper(digest), false},
		{"an empty signature", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := githubDelivery(ingressSecret, "d-"+strings.ReplaceAll(tc.name, " ", "-"), "issues", body).with("X-Hub-Signature-256", tc.sig)
			rec := e.deliver("widgets", d)
			if tc.ok {
				if outcome := outcomeOf(t, rec); outcome != "relayed" {
					t.Errorf("outcome = %q; want relayed", outcome)
				}
				return
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d; want 401\n%s", rec.Code, rec.Body)
			}
			if msg := decodeError(t, rec).Message; msg != unverifiedText {
				t.Errorf("message = %q; want %q", msg, unverifiedText)
			}
		})
	}
}

// A signature header given twice is no signature, even with the right one first.
// Requirement: 32-REQ-3.3
func TestIngressGitHubSignatureGivenTwice(t *testing.T) {
	e, _ := newIngressEnv(t)
	d := githubDelivery(ingressSecret, "d-twice", "issues", issuesPayload("labeled", "acme/widgets"))
	d.header[http.CanonicalHeaderKey("X-Hub-Signature-256")] = []string{d.header.Get("X-Hub-Signature-256"), "sha256=" + strings.Repeat("0", 64)}
	if rec := e.deliver("widgets", d); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

// TS-32-18: A GitLab delivery is accepted only when X-Gitlab-Token is the secret, which is
// checked before the body is read; with both headers GitHub's scheme is used.
// Requirement: 32-REQ-3.4
func TestIngressGitLabToken_TS_32_18(t *testing.T) {
	e, _ := newIngressEnv(t)

	body := &failReader{t: t}
	req := httptest.NewRequest(http.MethodPost, "/hooks/forge/widgets", body)
	req.Header = gitlabDelivery("a-wrong-token-of-16+", "d-1", "Issue Hook", "").header
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || decodeError(t, rec).Message != unverifiedText {
		t.Errorf("a wrong token: status %d, body %s; want 401 and the one message", rec.Code, rec.Body)
	}
	if body.read.Load() {
		t.Errorf("the body was read before the token was checked")
	}

	good := gitlabDelivery(ingressSecret, "d-2", "Issue Hook", gitlabIssuePayload("update", "acme/widgets"))
	if outcome := outcomeOf(t, e.deliver("widgets", good)); outcome != "ignored" {
		t.Errorf("the right token: outcome = %q; want it accepted (an update without a change is ignored)", outcome)
	}
	// A token that is the secret with something added or taken off is not the secret.
	for _, token := range []string{ingressSecret + "x", ingressSecret[1:], strings.ToUpper(ingressSecret), ""} {
		if rec := e.deliver("widgets", good.with("X-Gitlab-Token", token).with("Idempotency-Key", "d-3")); rec.Code != http.StatusUnauthorized {
			t.Errorf("the token %q: status = %d; want 401", token, rec.Code)
		}
	}

	// A header given twice is no token, even with the secret in it.
	for _, tokens := range [][]string{{ingressSecret, ingressSecret}, {"a-wrong-token-of-16+", ingressSecret}} {
		twice := good.with("Idempotency-Key", "d-6")
		twice.header["X-Gitlab-Token"] = tokens
		if rec := e.deliver("widgets", twice); rec.Code != http.StatusUnauthorized {
			t.Errorf("the token header twice, %d values: status = %d; want 401", len(tokens), rec.Code)
		}
	}

	// With both headers the signature decides, and the token is not looked at.
	both := githubDelivery(ingressSecret, "d-4", "issues", issuesPayload("labeled", "acme/widgets")).with("X-Gitlab-Token", "a-wrong-token-of-16+")
	if outcome := outcomeOf(t, e.deliver("widgets", both)); outcome != "relayed" {
		t.Errorf("both headers, the signature right: outcome = %q; want relayed", outcome)
	}
	both = githubDelivery(ingressSecret, "d-5", "issues", issuesPayload("labeled", "acme/widgets")).
		with("X-Hub-Signature-256", signGitHub("another-key-of-16-chars", "x")).with("X-Gitlab-Token", ingressSecret)
	if rec := e.deliver("widgets", both); rec.Code != http.StatusUnauthorized {
		t.Errorf("both headers, the signature wrong: status = %d; want 401", rec.Code)
	}
}

// TS-32-19: An unverified delivery answers 401 with one message, records nothing, and logs only
// the slug and the scheme.
// Requirement: 32-REQ-3.5
func TestIngressUnverified_TS_32_19(t *testing.T) {
	logs := captureLog(t)
	e, em := newIngressEnv(t)
	const marker = "MARKER-0123456789"
	body := `{"zen":"` + marker + `"}`

	for _, tc := range []struct {
		name   string
		d      signedDelivery
		scheme string
	}{
		{"no scheme header", githubDelivery(ingressSecret, "d-1", "ping", body).without("X-Hub-Signature-256"), "none"},
		{"a wrong GitHub signature", githubDelivery("another-key-of-16-chars", "d-2", "ping", body), "github"},
		{"a malformed GitHub signature", githubDelivery(ingressSecret, "d-3", "ping", body).with("X-Hub-Signature-256", "sha256="+marker), "github"},
		{"a wrong GitLab token", gitlabDelivery("a-wrong-token-"+marker, "d-4", "Issue Hook", body), "gitlab"},
	} {
		rec := e.deliver("widgets", tc.d)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d; want 401\n%s", tc.name, rec.Code, rec.Body)
			continue
		}
		if msg := decodeError(t, rec).Message; msg != unverifiedText {
			t.Errorf("%s: message = %q; want %q", tc.name, msg, unverifiedText)
		}
	}
	if rows := e.deliveryRows(); len(rows) != 0 {
		t.Errorf("rows = %v; want none", rows)
	}
	if got := em.recorded(); len(got) != 0 {
		t.Errorf("events = %v; want none", got)
	}

	log := logs.String()
	for _, scheme := range []string{"github", "gitlab", "none"} {
		if !strings.Contains(log, "scheme="+scheme) {
			t.Errorf("the log has no line with scheme=%s:\n%s", scheme, log)
		}
	}
	if !strings.Contains(log, "level=WARN") || !strings.Contains(log, "slug=widgets") {
		t.Errorf("the log = %q; want warn lines with the slug", log)
	}
	for _, leak := range []string{marker, ingressSecret, "sha256=", "another-key"} {
		if strings.Contains(log, leak) {
			t.Errorf("the log holds %q:\n%s", leak, log)
		}
	}
}

// TS-32-20: A body over 1 MiB, or over the server's limit, answers 413, with body_too_large when
// the handler is the one that refuses; exactly 1 MiB passes the size check.
// Requirement: 32-REQ-3.6
func TestIngressBodyLimit_TS_32_20(t *testing.T) {
	const mib = 1 << 20
	for _, tc := range []struct {
		name string
		// global is the body size limit installed on the whole instance, 0 for none (as
		// apikit installs it: on the API group only).
		global int64
	}{
		{"no global limit", 0},
		{"the default global limit", mib},
		{"a raised global limit", 4 * mib},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, em := newIngressEnv(t)
			if tc.global > 0 {
				e.echo.Use(apikit.BodySizeLimitMiddleware(tc.global))
			}
			over := githubDelivery(ingressSecret, "d-over", "ping", paddedBody(mib+1))
			for _, chunked := range []bool{false, true} {
				req := httptest.NewRequest(http.MethodPost, "/hooks/forge/widgets", strings.NewReader(over.body))
				req.Header = over.header.Clone()
				if chunked {
					req.ContentLength = -1
				}
				rec := httptest.NewRecorder()
				e.echo.ServeHTTP(rec, req)
				if rec.Code != http.StatusRequestEntityTooLarge {
					t.Errorf("1 MiB + 1 byte, chunked %v: status = %d; want 413", chunked, rec.Code)
					continue
				}
				// apikit's own check refuses a body whose length it knows with no error_type.
				if mine := tc.global != mib || chunked; mine && decodeError(t, rec).ErrorType != "body_too_large" {
					t.Errorf("1 MiB + 1 byte, chunked %v: error_type = %q; want body_too_large", chunked, decodeError(t, rec).ErrorType)
				}
			}
			if rows := e.deliveryRows(); len(rows) != 0 {
				t.Errorf("rows = %v; want none", rows)
			}
			if got := em.recorded(); len(got) != 0 {
				t.Errorf("events = %v; want none", got)
			}

			exact := githubDelivery(ingressSecret, "d-exact", "ping", paddedBody(mib))
			if outcome := outcomeOf(t, e.deliver("widgets", exact)); outcome != "ignored" {
				t.Errorf("exactly 1 MiB: outcome = %q; want ignored (the size check passed)", outcome)
			}
		})
	}
}

// TS-32-22: An archived workspace with a secret verifies, records and relays like an active one.
// Requirement: 32-REQ-3.8
func TestIngressArchivedWorkspace_TS_32_22(t *testing.T) {
	em := &recordingEmitter{}
	e := newEnv(t, withEmitter(em))
	e.webhookWorkspace("widgets", func(r *workspaceRow) { r.status = "archived" })

	rec := e.deliver("widgets", githubDelivery(ingressSecret, "d-1", "issues", issuesPayload("labeled", "acme/widgets")))
	if outcome := outcomeOf(t, rec); outcome != "relayed" {
		t.Errorf("outcome = %q; want relayed", outcome)
	}
	if rows := e.deliveryRows(); len(rows) != 1 {
		t.Errorf("rows = %v; want 1", rows)
	}
	events := em.recorded()
	if len(events) != 1 || events[0].EventType != audit.EventForgeIssueLabeled {
		t.Errorf("events = %v; want hub.forge.issue.labeled", events)
	}
}

// TS-32-24: A verified delivery records one row with the id and the event taken from the headers
// of its forge.
// Requirement: 32-REQ-4.2, 32-REQ-4.4
func TestIngressRecordsARow_TS_32_24(t *testing.T) {
	e, em := newIngressEnv(t)
	longEvent := strings.Repeat("e", 70)

	// GitLab sends Idempotency-Key and X-Gitlab-Event-UUID; the first is the id. An update
	// that changes nothing is not relayed, and is bound all the same.
	both := gitlabDelivery(ingressSecret, "idem-1", "Issue Hook", gitlabIssuePayload("update", "acme/widgets")).with("X-Gitlab-Event-UUID", "uuid-1")
	onlyUUID := gitlabDelivery(ingressSecret, "ignored", "Issue Hook", gitlabIssuePayload("update", "acme/widgets")).
		without("Idempotency-Key").with("X-Gitlab-Event-UUID", "uuid-2")

	before := time.Now().UTC().Add(-2 * time.Second)
	for _, d := range []signedDelivery{
		githubDelivery(ingressSecret, "gh-1", "issues", issuesPayload("labeled", "acme/widgets")),
		both,
		onlyUUID,
		githubDelivery(ingressSecret, "gh-long", longEvent, issuesPayload("labeled", "acme/widgets")),
	} {
		if rec := e.deliver("widgets", d); rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d; want 202\n%s", rec.Code, rec.Body)
		}
	}

	rows := e.deliveryRows()
	if len(rows) != 4 {
		t.Fatalf("%d rows; want 4: %v", len(rows), rows)
	}
	for i, want := range []struct {
		id, forge, event, action, outcome, relayed string
	}{
		{"gh-1", "github", "issues", "labeled", "relayed", `["hub.forge.issue.labeled"]`},
		{"idem-1", "gitlab", "Issue Hook", "update", "ignored", `[]`},
		{"uuid-2", "gitlab", "Issue Hook", "update", "ignored", `[]`},
		{"gh-long", "github", longEvent[:64], "labeled", "ignored", `[]`},
	} {
		row := rows[i]
		if row.slug != "widgets" || row.id != want.id || row.forge != want.forge || row.event != want.event ||
			row.action != want.action || row.outcome != want.outcome || row.relayed != want.relayed {
			t.Errorf("row %d = %+v; want %+v", i, row, want)
		}
		// A delivery hub does not relay is still bound when its repository is the workspace's.
		if row.target == nil || *row.target != "origin" {
			t.Errorf("row %d target = %v; want origin", i, row.target)
		}
		if at, err := apikit.ParseUTC(row.receivedAt); err != nil || !strings.HasSuffix(row.receivedAt, "Z") || at.Before(before) || at.After(time.Now().UTC().Add(2*time.Second)) {
			t.Errorf("row %d received_at = %q; want the RFC 3339 UTC time of the delivery", i, row.receivedAt)
		}
		if len(row.event) > 64 {
			t.Errorf("row %d event is %d bytes; want at most 64", i, len(row.event))
		}
	}
	if got := len(em.recorded()); got != 1 {
		t.Errorf("%d events; want the 1 of the first delivery (the others are not relayed)", got)
	}
}

// TS-32-25: A verified delivery without an id or an event name, or with an invalid id, answers
// 400 and records nothing.
// Requirement: 32-REQ-4.3
func TestIngressRefusesDeliveriesWithoutIdentity_TS_32_25(t *testing.T) {
	e, em := newIngressEnv(t)
	ping := githubDelivery(ingressSecret, "d-1", "ping", `{"zen":"x"}`)
	glPing := gitlabDelivery(ingressSecret, "d-1", "Issue Hook", `{}`)

	for _, tc := range []struct {
		name string
		d    signedDelivery
	}{
		{"GitHub without an id", ping.without("X-GitHub-Delivery")},
		{"GitHub without an event", ping.without("X-GitHub-Event")},
		{"GitHub with an empty event", ping.with("X-GitHub-Event", "")},
		{"GitLab without either id header", glPing.without("Idempotency-Key")},
		{"GitLab without an event", glPing.without("X-Gitlab-Event")},
		{"an id of 256 characters", ping.with("X-GitHub-Delivery", strings.Repeat("a", 256))},
		{"an id with a character outside ASCII", ping.with("X-GitHub-Delivery", "delivery-\u00e9")},
		{"an id with a control character", ping.with("X-GitHub-Delivery", "delivery-\x01")},
	} {
		rec := e.deliver("widgets", tc.d)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d; want 400\n%s", tc.name, rec.Code, rec.Body)
		}
	}
	if rows := e.deliveryRows(); len(rows) != 0 {
		t.Errorf("rows = %v; want none", rows)
	}
	if got := em.recorded(); len(got) != 0 {
		t.Errorf("events = %v; want none", got)
	}
	if outcome := outcomeOf(t, e.deliver("widgets", ping.with("X-GitHub-Delivery", strings.Repeat("a", 255)))); outcome != "ignored" {
		t.Errorf("an id of 255 characters: outcome = %q; want ignored", outcome)
	}
}

// TS-32-31: A repository equal to the origin's, ignoring case, binds the delivery to origin.
// Requirement: 32-REQ-5.3
func TestIngressBindsToOrigin_TS_32_31(t *testing.T) {
	em := &recordingEmitter{}
	e := newEnv(t, withEmitter(em))
	e.webhookWorkspace("caps", func(r *workspaceRow) { r.gitURL = "https://forge.example.com/Acme/Widgets.git" })
	e.webhookWorkspace("nested", func(r *workspaceRow) { r.gitURL = "https://gitlab.example.com/group/sub/widgets.git" })

	for _, tc := range []struct {
		slug string
		d    signedDelivery
	}{
		{"caps", githubDelivery(ingressSecret, "gh-1", "issues", issuesPayload("labeled", "acme/widgets"))},
		{"caps", githubDelivery(ingressSecret, "gh-2", "issues", issuesPayload("labeled", "ACME/WIDGETS"))},
		{"nested", gitlabDelivery(ingressSecret, "gl-1", "Issue Hook", gitlabIssuePayload("update", "group/sub/Widgets"))},
	} {
		want := "relayed"
		if tc.slug == "nested" {
			want = "ignored" // an update that changes nothing is not relayed, but it is bound
		}
		if outcome := outcomeOf(t, e.deliver(tc.slug, tc.d)); outcome != want {
			t.Errorf("%s: outcome = %q; want %s", tc.slug, outcome, want)
		}
	}
	rows := e.deliveryRows()
	if len(rows) != 3 {
		t.Fatalf("%d rows; want 3", len(rows))
	}
	for i, row := range rows {
		if row.target == nil || *row.target != "origin" {
			t.Errorf("row %d target = %v; want origin", i, row.target)
		}
	}
	events := em.recorded()
	if len(events) != 2 {
		t.Fatalf("%d events; want 2 (the GitHub deliveries)", len(events))
	}
	for i, ev := range events {
		if ev.Metadata["target"] != "origin" {
			t.Errorf("event %d metadata target = %v; want origin", i, ev.Metadata["target"])
		}
	}
}

// A delivery that announces more than 1 MiB is refused without its body being read.
// Requirement: 32-REQ-3.6
func TestIngressDoesNotReadABodyThatAnnouncesTooMuch(t *testing.T) {
	e, _ := newIngressEnv(t)
	body := &failReader{t: t}
	req := httptest.NewRequest(http.MethodPost, "/hooks/forge/widgets", body)
	req.Header = githubDelivery(ingressSecret, "d-1", "ping", "{}").header
	req.ContentLength = 1<<20 + 1
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || decodeError(t, rec).ErrorType != "body_too_large" {
		t.Errorf("status = %d, body %s; want 413 body_too_large", rec.Code, rec.Body)
	}
	if body.read.Load() {
		t.Errorf("the body was read")
	}
}

// A workspace whose webhook secret is empty has none: an empty token would pass, and an empty key
// would sign.
// Requirement: 32-REQ-3.2
func TestIngressRefusesAnEmptySecret(t *testing.T) {
	e, _ := newIngressEnv(t)
	addWorkspace(t, e.db, "blank")
	if _, err := e.db.Exec(`INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at)
		VALUES ('workspace', 'blank', 'FORGE_WEBHOOK_SECRET', '', '2026-10-21T09:00:00Z', '2026-10-21T09:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	unknown := e.deliver("nosuch", githubDelivery(ingressSecret, "d-0", "ping", "{}"))
	for name, d := range map[string]signedDelivery{
		"an empty GitLab token":                 gitlabDelivery("", "d-1", "Issue Hook", "{}"),
		"a GitHub signature under an empty key": githubDelivery("", "d-2", "ping", "{}"),
	} {
		rec := e.deliver("blank", d)
		if rec.Code != http.StatusNotFound || rec.Body.String() != unknown.Body.String() {
			t.Errorf("%s: status = %d, body %s; want the 404 of an unknown slug", name, rec.Code, rec.Body)
		}
	}
	if rows := e.deliveryRows(); len(rows) != 0 {
		t.Errorf("rows = %v; want none", rows)
	}
}

// The event of a labeled delivery: the forge as the actor, the issue as the resource, and the
// metadata of its kind.
// Requirement: 32-REQ-8.1, 32-REQ-8.2
func TestIngressLabeledEventContents(t *testing.T) {
	e, em := newIngressEnv(t)
	if outcome := outcomeOf(t, e.deliver("widgets", labeledGitHubDelivery("gh-1"))); outcome != "relayed" {
		t.Fatalf("outcome = %q; want relayed", outcome)
	}
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events; want 1", len(events))
	}
	ev := events[0]
	if ev.EventType != audit.EventForgeIssueLabeled || ev.Workspace != "widgets" || ev.ActorType != "forge" || ev.ActorID != "" ||
		ev.ResourceType != "issue" || ev.ResourceID != "7" || ev.Action != "labeled" {
		t.Errorf("event = %+v; want hub.forge.issue.labeled of issue 7 by the forge", ev)
	}
	want := map[string]any{
		"forge": "github", "target": "origin", "delivery_id": "gh-1", "actor": "octocat",
		"number": 7, "label": "af:ready", "labels": []string{"bug", "af:ready"},
		"url": "https://github.com/acme/widgets/issues/7",
	}
	if !reflect.DeepEqual(ev.Metadata, want) {
		t.Errorf("metadata = %#v\nwant %#v", ev.Metadata, want)
	}
}

// labeledGitHubDelivery is a signed GitHub issues labeled delivery of acme/widgets with the id.
func labeledGitHubDelivery(id string) signedDelivery {
	return githubDelivery(ingressSecret, id, "issues", issuesPayload("labeled", "acme/widgets"))
}

// An issue without labels has an empty list of them, not a null one.
func TestIngressLabeledEventWithoutLabels(t *testing.T) {
	e, em := newIngressEnv(t)
	body := strings.Replace(issuesPayload("labeled", "acme/widgets"), `"labels":[{"name":"bug"},{"name":"af:ready"}]`, `"labels":[]`, 1)
	if !strings.Contains(body, `"labels":[]`) {
		t.Fatalf("the payload still has labels: %s", body)
	}
	if outcome := outcomeOf(t, e.deliver("widgets", githubDelivery(ingressSecret, "gh-1", "issues", body))); outcome != "relayed" {
		t.Fatalf("outcome = %q; want relayed", outcome)
	}
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events; want 1", len(events))
	}
	if labels, ok := events[0].Metadata["labels"].([]string); !ok || labels == nil || len(labels) != 0 {
		t.Errorf("labels = %#v; want an empty list that is not nil", events[0].Metadata["labels"])
	}
	if b, _ := json.Marshal(events[0].Metadata); !strings.Contains(string(b), `"labels":[]`) {
		t.Errorf("metadata = %s; want labels as []", b)
	}
}
