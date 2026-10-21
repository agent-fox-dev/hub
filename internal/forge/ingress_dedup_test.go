package forge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// labeledDelivery is a signed GitHub issues labeled delivery of acme/widgets with the id.
func labeledDelivery(id string) signedDelivery {
	return githubDelivery(ingressSecret, id, "issues", issuesPayload("labeled", "acme/widgets"))
}

// TS-32-21: A rotated secret applies from the next delivery; nothing is cached.
// Requirement: 32-REQ-3.7
func TestIngressSecretRotation_TS_32_21(t *testing.T) {
	e, _ := newIngressEnv(t)
	const fresh, freshest = "the-new-secret-of-16-chars", "the-newest-secret-of-16+"
	sign := func(secret, id string) signedDelivery {
		return githubDelivery(secret, id, "issues", issuesPayload("labeled", "acme/widgets"))
	}

	if outcome := outcomeOf(t, e.deliver("widgets", sign(ingressSecret, "d-1"))); outcome != "relayed" {
		t.Fatalf("the secret before the rotation: outcome = %q; want relayed", outcome)
	}
	// Rotated through PUT .../forge/webhook.
	if rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":"`+fresh+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT: status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	if rec := e.deliver("widgets", sign(ingressSecret, "d-2")); rec.Code != http.StatusUnauthorized {
		t.Errorf("the old secret after the PUT: status = %d; want 401", rec.Code)
	}
	if outcome := outcomeOf(t, e.deliver("widgets", sign(fresh, "d-3"))); outcome != "relayed" {
		t.Errorf("the new secret after the PUT: outcome = %q; want relayed", outcome)
	}
	// Rotated through the secrets store, as afc secrets update does.
	if _, err := e.secrets.UpdateSecret("workspace", "widgets", "FORGE_WEBHOOK_SECRET", freshest); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{ingressSecret, fresh} {
		if rec := e.deliver("widgets", sign(old, "d-4")); rec.Code != http.StatusUnauthorized {
			t.Errorf("an old secret after the store update: status = %d; want 401", rec.Code)
		}
	}
	if outcome := outcomeOf(t, e.deliver("widgets", sign(freshest, "d-5"))); outcome != "relayed" {
		t.Errorf("the newest secret: outcome = %q; want relayed", outcome)
	}
}

// TS-32-26: A repeated delivery id answers 202 duplicate, records no row and emits no event.
// Requirement: 32-REQ-4.5
func TestIngressDuplicate_TS_32_26(t *testing.T) {
	e, em := newIngressEnv(t)
	if outcome := outcomeOf(t, e.deliver("widgets", labeledDelivery("gh-1"))); outcome != "relayed" {
		t.Fatalf("first: outcome = %q; want relayed", outcome)
	}
	rec := e.deliver("widgets", labeledDelivery("gh-1"))
	if outcome := outcomeOf(t, rec); outcome != "duplicate" {
		t.Errorf("second: outcome = %q; want duplicate", outcome)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"outcome":"duplicate"}` {
		t.Errorf("second: body = %s; want {\"outcome\":\"duplicate\"}", body)
	}
	if rows := e.deliveryRows(); len(rows) != 1 {
		t.Errorf("rows = %v; want 1", rows)
	}
	if got := em.recorded(); len(got) != 1 {
		t.Errorf("%d events; want the 1 of the first delivery", len(got))
	}
	// The same id in another workspace is another delivery.
	e.webhookWorkspace("other")
	if outcome := outcomeOf(t, e.deliver("other", labeledDelivery("gh-1"))); outcome != "relayed" {
		t.Errorf("the same id in another workspace: outcome = %q; want relayed", outcome)
	}
}

// TS-32-27: A failure of the delivery log answers 500 and emits nothing, so that the redelivery
// is processed in full.
// Requirement: 32-REQ-4.6
func TestIngressLogFailure_TS_32_27(t *testing.T) {
	e, em := newIngressEnv(t)
	if _, err := e.db.Exec(`DROP TABLE forge_deliveries`); err != nil {
		t.Fatal(err)
	}
	rec := e.deliver("widgets", labeledDelivery("gh-1"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500\n%s", rec.Code, rec.Body)
	}
	if msg := decodeError(t, rec).Message; strings.Contains(msg, "forge_deliveries") {
		t.Errorf("message = %q; it holds the cause", msg)
	}
	if got := em.recorded(); len(got) != 0 {
		t.Errorf("events = %v; want none", got)
	}

	if err := InitSchema(e.db); err != nil {
		t.Fatal(err)
	}
	if outcome := outcomeOf(t, e.deliver("widgets", labeledDelivery("gh-1"))); outcome != "relayed" {
		t.Errorf("the redelivery: outcome = %q; want relayed", outcome)
	}
	if rows, events := e.deliveryRows(), em.recorded(); len(rows) != 1 || len(events) != 1 {
		t.Errorf("%d rows and %d events; want 1 and 1", len(rows), len(events))
	}
}

// TS-32-28: N copies of one delivery, concurrent ones too, record one row and relay once.
// Requirement: 32-REQ-4.7
func TestIngressConcurrentCopies_TS_32_28(t *testing.T) {
	for n := 1; n <= 20; n++ {
		e, em := newIngressEnv(t)
		d := labeledDelivery(fmt.Sprintf("conc-%d", n))

		outcomes := make([]string, n)
		statuses := make([]int, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				rec := e.deliver("widgets", d)
				statuses[i] = rec.Code
				outcomes[i] = strings.TrimSpace(rec.Body.String())
			}()
		}
		close(start)
		wg.Wait()

		relayed, duplicates := 0, 0
		for i := range n {
			switch outcomes[i] {
			case `{"outcome":"relayed"}`:
				relayed++
			case `{"outcome":"duplicate"}`:
				duplicates++
			default:
				t.Errorf("n=%d: copy %d: status %d, body %s", n, i, statuses[i], outcomes[i])
			}
		}
		if relayed != 1 || duplicates != n-1 {
			t.Errorf("n=%d: %d relayed and %d duplicate; want 1 and %d", n, relayed, duplicates, n-1)
		}
		if rows := e.deliveryRows(); len(rows) != 1 {
			t.Errorf("n=%d: %d rows; want 1", n, len(rows))
		}
		if got := em.recorded(); len(got) != 1 {
			t.Errorf("n=%d: %d events; want 1", n, len(got))
		}
	}
}

// TS-32-29: A body that is not a JSON object, or lacks a field its kind needs, is recorded as
// malformed and answered 400; nothing is emitted.
// Requirement: 32-REQ-5.1
func TestIngressMalformed_TS_32_29(t *testing.T) {
	e, em := newIngressEnv(t)
	noNumber := `{"action":"labeled","repository":{"full_name":"acme/widgets"},"label":{"name":"x"},"issue":{"title":"t"}}`
	noRepository := `{"action":"labeled","label":{"name":"x"},"issue":{"number":7}}`
	noLabel := `{"action":"labeled","repository":{"full_name":"acme/widgets"},"issue":{"number":7}}`
	badNumber := func(n string) string {
		return `{"action":"labeled","repository":{"full_name":"acme/widgets"},"label":{"name":"x"},"issue":{"number":` + n + `}}`
	}
	bodies := []string{`[]`, `null`, `{"action":"labeled"`, `{"a":1} {"b":2}`, noNumber, noRepository, noLabel, `"text"`, ``,
		badNumber("0"), badNumber("-1"), badNumber("7.5"), badNumber(`"7"`), badNumber("null")}
	for i, body := range bodies {
		rec := e.deliver("widgets", githubDelivery(ingressSecret, fmt.Sprintf("d-%d", i), "issues", body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d; want 400\n%s", body, rec.Code, rec.Body)
		}
	}
	rows := e.deliveryRows()
	if len(rows) != len(bodies) {
		t.Fatalf("%d rows; want %d", len(rows), len(bodies))
	}
	for i, row := range rows {
		if row.outcome != "malformed" || row.relayed != "[]" || row.target != nil {
			t.Errorf("row %d = %+v; want malformed, relayed [] and no target", i, row)
		}
	}
	if got := em.recorded(); len(got) != 0 {
		t.Errorf("events = %v; want none", got)
	}
	// The same malformed delivery again is a redelivery like any other.
	if outcome := outcomeOf(t, e.deliver("widgets", githubDelivery(ingressSecret, "d-0", "issues", `[]`))); outcome != "duplicate" {
		t.Errorf("a malformed delivery again: outcome = %q; want duplicate", outcome)
	}
}

// TS-32-30: An event and action hub does not relay are recorded as ignored, answered 202, and
// emit nothing.
// Requirement: 32-REQ-5.2
func TestIngressIgnored_TS_32_30(t *testing.T) {
	e, em := newIngressEnv(t)
	repo := `"repository":{"full_name":"acme/widgets"}`
	cases := []struct {
		name string
		d    signedDelivery
	}{
		{"GitHub ping", githubDelivery(ingressSecret, "d-1", "ping", `{"zen":"x",`+repo+`}`)},
		{"GitHub pull_request labeled", githubDelivery(ingressSecret, "d-2", "pull_request", `{"action":"labeled",`+repo+`,"pull_request":{"number":3}}`)},
		{"GitHub issue_comment edited", githubDelivery(ingressSecret, "d-3", "issue_comment", `{"action":"edited",`+repo+`,"issue":{"number":3}}`)},
		{"GitHub star", githubDelivery(ingressSecret, "d-4", "star", `{"action":"created",`+repo+`}`)},
		{"GitHub issues assigned", githubDelivery(ingressSecret, "d-5", "issues", issuesPayload("assigned", "acme/widgets"))},
		{"GitLab Confidential Issue Hook", gitlabDelivery(ingressSecret, "d-6", "Confidential Issue Hook", gitlabIssuePayload("open", "acme/widgets"))},
		{"GitLab Confidential Note Hook", gitlabDelivery(ingressSecret, "d-7", "Confidential Note Hook", `{"project":{"path_with_namespace":"acme/widgets"},"object_attributes":{"noteable_type":"Issue"}}`)},
		{"GitLab Note Hook on a commit", gitlabDelivery(ingressSecret, "d-8", "Note Hook", `{"project":{"path_with_namespace":"acme/widgets"},"object_attributes":{"noteable_type":"Commit","note":"x"},"commit":{"id":"abc"}}`)},
		{"GitLab Pipeline Hook", gitlabDelivery(ingressSecret, "d-9", "Pipeline Hook", `{"project":{"path_with_namespace":"acme/widgets"}}`)},
	}
	for _, tc := range cases {
		rec := e.deliver("widgets", tc.d)
		if outcome := outcomeOf(t, rec); outcome != "ignored" {
			t.Errorf("%s: outcome = %q; want ignored", tc.name, outcome)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != `{"outcome":"ignored"}` {
			t.Errorf("%s: body = %s", tc.name, body)
		}
	}
	rows := e.deliveryRows()
	if len(rows) != len(cases) {
		t.Fatalf("%d rows; want %d", len(rows), len(cases))
	}
	for i, row := range rows {
		if row.outcome != "ignored" || row.relayed != "[]" {
			t.Errorf("%s: row = %+v; want ignored and relayed []", cases[i].name, row)
		}
	}
	if got := em.recorded(); len(got) != 0 {
		t.Errorf("events = %v; want none", got)
	}
}

// TS-32-32: On a carry_patch workspace a delivery for the upstream repository is bound to the
// upstream.
// Requirement: 32-REQ-5.4
func TestIngressBindsToUpstream_TS_32_32(t *testing.T) {
	em := &recordingEmitter{}
	e := newEnv(t, withEmitter(em))
	e.webhookWorkspace("fork", carryPatch("https://github.com/upstream/widgets"))

	rec := e.deliver("fork", githubDelivery(ingressSecret, "d-1", "issues", issuesPayload("labeled", "upstream/widgets")))
	if outcome := outcomeOf(t, rec); outcome != "relayed" {
		t.Fatalf("outcome = %q; want relayed", outcome)
	}
	// The origin still binds to the origin.
	if outcome := outcomeOf(t, e.deliver("fork", githubDelivery(ingressSecret, "d-2", "issues", issuesPayload("labeled", "acme/widgets")))); outcome != "relayed" {
		t.Fatalf("origin: outcome = %q; want relayed", outcome)
	}
	rows := e.deliveryRows()
	if len(rows) != 2 || rows[0].target == nil || *rows[0].target != "upstream" || rows[1].target == nil || *rows[1].target != "origin" {
		t.Fatalf("rows = %+v; want the targets upstream and origin", rows)
	}
	events := em.recorded()
	if len(events) != 2 || events[0].Metadata["target"] != "upstream" || events[1].Metadata["target"] != "origin" {
		t.Errorf("events = %+v; want the targets upstream and origin", events)
	}
}

// TS-32-33: A delivery for another repository, and one on a workspace whose remote cannot be
// read, is a repository_mismatch.
// Requirement: 32-REQ-5.5
func TestIngressRepositoryMismatch_TS_32_33(t *testing.T) {
	em := &recordingEmitter{}
	e := newEnv(t, withEmitter(em))
	e.webhookWorkspace("plain")
	e.webhookWorkspace("leftover", func(r *workspaceRow) { r.upstream = ptr("https://github.com/upstream/widgets") })
	e.webhookWorkspace("noupstream", func(r *workspaceRow) { r.mode = ptr("carry_patch") })
	e.webhookWorkspace("unreadable", func(r *workspaceRow) { r.gitURL = "not a remote" })

	for i, tc := range []struct{ name, slug, repo string }{
		{"another repository", "plain", "other/repo"},
		{"the upstream on a standard workspace", "leftover", "upstream/widgets"},
		{"an upstream on a carry_patch workspace without one", "noupstream", "upstream/widgets"},
		{"any repository on a workspace with an unreadable remote", "unreadable", "acme/widgets"},
		{"a longer path", "plain", "acme/widgets/extra"},
		{"a prefix of the path", "plain", "acme/widget"},
	} {
		d := githubDelivery(ingressSecret, fmt.Sprintf("d-%d", i), "issues", issuesPayload("labeled", tc.repo))
		rec := e.deliver(tc.slug, d)
		if outcome := outcomeOf(t, rec); outcome != "repository_mismatch" {
			t.Errorf("%s: outcome = %q; want repository_mismatch", tc.name, outcome)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != `{"outcome":"repository_mismatch"}` {
			t.Errorf("%s: body = %s", tc.name, body)
		}
	}
	for i, row := range e.deliveryRows() {
		if row.outcome != "repository_mismatch" || row.target != nil || row.relayed != "[]" {
			t.Errorf("row %d = %+v; want repository_mismatch, no target and relayed []", i, row)
		}
	}
	if got := em.recorded(); len(got) != 0 {
		t.Errorf("events = %v; want none", got)
	}
}

// blockingEmitter is an emitter whose Emit waits until the test lets it go.
type blockingEmitter struct {
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
	mu          sync.Mutex
	events      []audit.HubEvent
}

func newBlockingEmitter() *blockingEmitter {
	return &blockingEmitter{entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingEmitter) Emit(_ context.Context, event audit.HubEvent) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
	return nil
}

// let releases the emitter; it can be called more than once.
func (b *blockingEmitter) let() {
	b.releaseOnce.Do(func() { close(b.release) })
}

func (b *blockingEmitter) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.events)
}

// TS-32-47: The 202 answer is written and flushed before any event is emitted.
// Requirement: 32-REQ-8.5
func TestIngressAnswersBeforeEmitting_TS_32_47(t *testing.T) {
	em := newBlockingEmitter()
	e := newEnv(t, func(cfg *Config) { cfg.Audit = em })
	e.webhookWorkspace("widgets")
	server := httptest.NewServer(e.echo)
	t.Cleanup(server.Close)
	t.Cleanup(em.let) // runs before server.Close, which waits for the requests that are blocked

	post := func(d signedDelivery) (int, string, error) {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/hooks/forge/widgets", strings.NewReader(d.body))
		if err != nil {
			return 0, "", err
		}
		req.Header = d.header.Clone()
		resp, err := server.Client().Do(req)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), err
	}

	type result struct {
		status int
		body   string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		status, body, err := post(labeledDelivery("gh-1"))
		done <- result{status, body, err}
	}()

	select {
	case <-em.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the emitter was not offered the event")
	}
	// Emit is blocked now, and the client must have its complete answer.
	select {
	case r := <-done:
		if r.err != nil || r.status != http.StatusAccepted || strings.TrimSpace(r.body) != `{"outcome":"relayed"}` {
			t.Errorf("answer = %d %q, %v; want 202 {\"outcome\":\"relayed\"}", r.status, r.body, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the answer did not reach the client while Emit was blocked")
	}
	if n := em.count(); n != 0 {
		t.Errorf("%d events stored while Emit was blocked", n)
	}
	em.let()
	deadline := time.Now().Add(5 * time.Second)
	for em.count() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := em.count(); n != 1 {
		t.Errorf("%d events after the release; want 1", n)
	}

	// An ignored and a mismatching delivery carry their outcome, and emit nothing.
	for want, d := range map[string]signedDelivery{
		"ignored":             githubDelivery(ingressSecret, "gh-2", "ping", `{"zen":"x"}`),
		"repository_mismatch": githubDelivery(ingressSecret, "gh-3", "issues", issuesPayload("labeled", "other/repo")),
	} {
		status, body, err := post(d)
		if err != nil || status != http.StatusAccepted || strings.TrimSpace(body) != `{"outcome":"`+want+`"}` {
			t.Errorf("%s: answer = %d %q, %v", want, status, body, err)
		}
	}
	if n := em.count(); n != 1 {
		t.Errorf("%d events after the ignored and mismatching deliveries; want still 1", n)
	}
}

// Hub's answer to a delivery that comes in on a connection the forge has closed is not needed
// for the events: they are emitted on a context the request's cancellation does not reach.
func TestIngressEventsOutliveTheRequest(t *testing.T) {
	em := &recordingEmitter{}
	e := newEnv(t, withEmitter(em))
	e.webhookWorkspace("widgets")
	ctx, cancel := context.WithCancel(context.Background())
	d := labeledDelivery("gh-1")
	req := httptest.NewRequest(http.MethodPost, "/hooks/forge/widgets", strings.NewReader(d.body)).WithContext(ctx)
	req.Header = d.header.Clone()
	cancel()
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	if outcome := outcomeOf(t, rec); outcome != "relayed" {
		t.Fatalf("outcome = %q; want relayed", outcome)
	}
	if len(em.ctxErr) != 1 || em.ctxErr[0] != nil {
		t.Errorf("the event was emitted on a context with the error %v; want a live one", em.ctxErr)
	}
}

// TS-32-49: An Emit error is logged, the other events are still offered, and the answer and the
// row are what they were. A delivery of three events is a reduction the test gives the ingress.
// Requirement: 32-REQ-8.7
func TestIngressEmitError_TS_32_49(t *testing.T) {
	logs := captureLog(t)
	em := &failFirstEmitter{}
	e := newEnv(t, func(cfg *Config) { cfg.Audit = em })
	e.webhookWorkspace("widgets")
	three := func(delivery, workspaceRemotes) reduction {
		events := []audit.HubEvent{
			{EventType: audit.EventForgeIssueUnlabeled, Workspace: "widgets"},
			{EventType: audit.EventForgeIssueLabeled, Workspace: "widgets"},
			{EventType: audit.EventForgeIssueEdited, Workspace: "widgets"},
		}
		return reduction{outcome: outcomeRelayed, target: targetOrigin, events: events}
	}
	root := echo.New()
	root.HTTPErrorHandler = apikit.HTTPErrorHandler
	h := &handlers{cfg: e.cfg, reduce: three}
	root.POST("/hooks/forge/:slug", h.ingress)

	d := labeledDelivery("gh-1")
	req := httptest.NewRequest(http.MethodPost, "/hooks/forge/widgets", strings.NewReader(d.body))
	req.Header = d.header.Clone()
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, req)

	if outcome := outcomeOf(t, rec); outcome != "relayed" {
		t.Errorf("outcome = %q; want relayed", outcome)
	}
	if got := em.attempted(); len(got) != 3 {
		t.Errorf("offered %v; want all 3 in order", got)
	}
	want := []string{audit.EventForgeIssueUnlabeled, audit.EventForgeIssueLabeled, audit.EventForgeIssueEdited}
	for i, got := range em.attempted() {
		if i < len(want) && got != want[i] {
			t.Errorf("event %d = %s; want %s", i, got, want[i])
		}
	}
	if log := logs.String(); !strings.Contains(log, "level=ERROR") || !strings.Contains(log, audit.EventForgeIssueUnlabeled) {
		t.Errorf("the log = %q; want an error line for the event that failed", log)
	}
	rows := e.deliveryRows()
	if len(rows) != 1 || rows[0].outcome != "relayed" ||
		rows[0].relayed != `["hub.forge.issue.unlabeled","hub.forge.issue.labeled","hub.forge.issue.edited"]` {
		t.Errorf("rows = %+v; want one relayed row that lists the three events", rows)
	}
}

// failFirstEmitter fails its first Emit and records what it was offered.
type failFirstEmitter struct {
	mu    sync.Mutex
	types []string
}

func (f *failFirstEmitter) Emit(_ context.Context, event audit.HubEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.types = append(f.types, event.EventType)
	if len(f.types) == 1 {
		return errors.New("the audit store is down")
	}
	return nil
}

func (f *failFirstEmitter) attempted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.types...)
}

// A repository that is both the origin and the upstream is the origin.
// Requirement: 32-REQ-5.3
func TestIngressPrefersTheOrigin(t *testing.T) {
	e, _ := newIngressEnv(t)
	e.webhookWorkspace("same", carryPatch("https://github.com/acme/widgets"))
	if outcome := outcomeOf(t, e.deliver("same", labeledDelivery("d-1"))); outcome != "relayed" {
		t.Fatalf("outcome = %q; want relayed", outcome)
	}
	if rows := e.deliveryRows(); len(rows) != 1 || rows[0].target == nil || *rows[0].target != "origin" {
		t.Errorf("rows = %+v; want the target origin", rows)
	}
}
