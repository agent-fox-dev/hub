package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// printable is n characters from '!' to '~', starting at offset, so that every character of
// the range is used.
func printable(n, offset int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('!' + (i+offset)%('~'-'!'+1))
	}
	return string(b)
}

// webhookURL is external_url without its trailing slashes and /hooks/forge/<slug>, nil
// when external_url is not set.
func TestWebhookURL(t *testing.T) {
	for external, want := range map[string]string{"https://hub.example.com": "https://hub.example.com/hooks/forge/widgets",
		"https://hub.example.com//": "https://hub.example.com/hooks/forge/widgets", "http://localhost:8080/prefix/": "http://localhost:8080/prefix/hooks/forge/widgets", "": ""} {
		got := webhookURL(external, "widgets")
		if (want == "") != (got == nil) || (got != nil && *got != want) {
			t.Errorf("webhookURL(%q) = %v; want %q", external, got, want)
		}
	}
}

// PUT with no body, {} or a null secret generates a 64-hex secret, stores it and returns it
// once; a supplied secret of 16 to 255 printable ASCII characters is stored and not echoed;
// the answer carries the ingress URL (null without external_url); a PUT updates a secret
// that exists and, after a create lost a race, updates once more; each PUT emits
// hub.forge.webhook.set with the generated flag only.
func TestSetWebhook(t *testing.T) {
	em, fake := &recordingEmitter{}, &fakeSecrets{}
	e := widgetsEnv(t, withEmitter(em), withFakeSecrets(fake))
	seen := map[string]bool{}
	for _, body := range []string{``, `{}`, `{"secret":null}`} {
		rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), body)
		doc := testutil.Object(t, rec)
		secret, _ := doc["secret"].(string)
		stored, _ := e.storedSecret("widgets")
		if rec.Code != http.StatusOK || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(secret) || stored != secret || doc["configured"] != true ||
			doc["url"] != "https://hub.example.com/hooks/forge/widgets" || len(doc) != 3 {
			t.Errorf("PUT %q: %d %s, stored %q; want a 64-hex secret stored and returned with configured and url", body, rec.Code, rec.Body, stored)
		}
		seen[secret] = true
	}
	if len(seen) != 3 {
		t.Errorf("3 generated secrets are %d distinct ones", len(seen))
	}
	for i, n := range []int{16, 255} {
		secret := printable(n, i*31)
		rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":`+marshal(secret)+`}`)
		doc := testutil.Object(t, rec)
		if stored, _ := e.storedSecret("widgets"); rec.Code != http.StatusOK || stored != secret || len(doc) != 2 || doc["configured"] != true {
			t.Errorf("a secret of %d characters: %d %s, stored %q; want it stored and not echoed", n, rec.Code, rec.Body, stored)
		}
	}
	list, _ := e.secrets.ListSecrets("workspace", "widgets")
	if len(list) != 1 || fake.called() != "Update,Create,Update,Update,Update,Update" {
		t.Errorf("%d entries after the PUTs with the calls %s; want one entry: one create, then updates", len(list), fake.called())
	}
	events := em.recorded()
	if len(events) != 5 {
		t.Fatalf("%d events; want 5", len(events))
	}
	for i, ev := range events {
		if ev.EventType != audit.EventForgeWebhookSet || ev.Workspace != "widgets" || ev.ResourceType != "webhook" || ev.ResourceID != "widgets" || ev.Action != "set" ||
			ev.ActorID != ownerID || ev.ActorType != "api_key" || len(ev.Metadata) != 1 || ev.Metadata["generated"] != (i < 3) {
			t.Errorf("event %d = %+v; want hub.forge.webhook.set by the caller with generated %v only", i, ev, i < 3)
		}
	}

	raced := widgetsEnv(t, withFakeSecrets(&fakeSecrets{createRace: true}))
	if rec := raced.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":"a-new-secret-of-16+"}`); rec.Code != http.StatusOK {
		t.Fatalf("a create that lost a race: %d %s", rec.Code, rec.Body)
	}
	if stored, _ := raced.storedSecret("widgets"); stored != "a-new-secret-of-16+" || raced.cfg.Secrets.(*fakeSecrets).called() != "Update,Create,Update" {
		t.Errorf("stored %q after %s; want the new secret after Update, Create, Update", stored, raced.cfg.Secrets.(*fakeSecrets).called())
	}
	noURL := widgetsEnv(t, func(cfg *Config) { cfg.ExternalURL = "" })
	if rec := noURL.send(http.MethodPut, webhookPath, patAuth("forge:write"), `{"secret":"abcdefghijklmnop"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"url":null`) {
		t.Errorf("without external_url: %d %s; want 200 with a null url", rec.Code, rec.Body)
	}
}

// A body that is not an object with an optional secret string, or a secret that breaks the
// rules, is 400 naming secret and never repeating the value; the stored secret is kept.
func TestSetWebhook_RefusesInvalidBodies(t *testing.T) {
	e := widgetsEnv(t)
	if _, err := e.secrets.CreateSecrets("workspace", "widgets", []secrets.EntryInput{{Key: webhookSecretKey, Value: "existing-secret-value"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ body, supplied string }{
		{`["abcdefghijklmnopqrst"]`, "abcdefghijklmnopqrst"},
		{`null`, ""},
		{`{"secret": "abcdefghijklmnopqrst`, "abcdefghijklmnopqrst"},
		{`{"secret":123}`, ""},
		{`{"secret":"abcdefghijklmno"}`, "abcdefghijklmno"},
		{`{"secret":` + marshal(printable(256, 0)) + `}`, printable(256, 0)},
		{`{"secret":""}`, ""},
		{`{"secret":"abcdefghij klmnopqrst"}`, "abcdefghij klmnopqrst"},
		{`{"secret":"abcdefghij\u0007klmnopqrst"}`, "abcdefghij\aklmnopqrst"},
		{`{"secret":"abcdefghijéklmnopqrst"}`, "abcdefghijéklmnopqrst"},
	} {
		rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), tc.body)
		msg := testutil.Error(t, rec).Message
		if rec.Code != http.StatusBadRequest || !strings.Contains(msg, "secret") || (tc.supplied != "" && strings.Contains(msg, tc.supplied)) {
			t.Errorf("PUT %s: %d %q; want 400 naming secret without the value", tc.body, rec.Code, msg)
		}
	}
	if stored, _ := e.storedSecret("widgets"); stored != "existing-secret-value" {
		t.Errorf("stored = %q; want it unchanged", stored)
	}
}

// A full scope is 400 with the store's message and any other store failure 500 without the
// cause or the secret; neither emits an event. Without a secrets store the routes are 500.
func TestWebhook_StoreFailures(t *testing.T) {
	logs := captureLog(t)
	em := &recordingEmitter{}
	full := widgetsEnv(t, withEmitter(em))
	entries := make([]secrets.EntryInput, secrets.MaxEntriesPerScope)
	for i := range entries {
		entries[i] = secrets.EntryInput{Key: fmt.Sprintf("K%03d", i), Value: "v"}
	}
	if _, err := full.secrets.CreateSecrets("workspace", "widgets", entries); err != nil {
		t.Fatal(err)
	}
	if rec := full.send(http.MethodPut, webhookPath, apiKeyAuth(), ``); rec.Code != http.StatusBadRequest || !strings.Contains(testutil.Error(t, rec).Message, "maximum of 100 entries per scope exceeded") {
		t.Errorf("a full scope: %d %s; want 400 with the store's message", rec.Code, rec.Body)
	}
	const supplied = "supplied-secret-value-123"
	for _, tc := range []struct {
		method string
		fake   *fakeSecrets
	}{
		{http.MethodPut, &fakeSecrets{updateErr: fmt.Errorf("update secret: %w", errors.New("boom "+supplied))}},
		{http.MethodDelete, &fakeSecrets{deleteErr: errors.New("boom: disk gone")}},
	} {
		e := widgetsEnv(t, withEmitter(em), withFakeSecrets(tc.fake))
		rec := e.send(tc.method, webhookPath, apiKeyAuth(), `{"secret":"`+supplied+`"}`)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), supplied) {
			t.Errorf("%s with a failing store: %d %s; want 500 without the cause", tc.method, rec.Code, rec.Body)
		}
	}
	none := widgetsEnv(t, withEmitter(em), func(cfg *Config) { cfg.Secrets = nil })
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		if rec := none.send(method, webhookPath, apiKeyAuth(), ""); rec.Code != http.StatusInternalServerError {
			t.Errorf("%s without a store: %d; want 500", method, rec.Code)
		}
	}
	if got := em.recorded(); len(got) != 0 {
		t.Errorf("events = %v; want none", got)
	}
	if log := logs.String(); strings.Contains(log, supplied) || !strings.Contains(log, "secrets store is not configured") {
		t.Errorf("the log holds the secret or not the misconfiguration:\n%s", log)
	}
}

// DELETE removes the secret and answers 204 whether or not there was one, on an archived
// workspace too, and emits hub.forge.webhook.unset with the existed flag only; the webhook
// routes take no target; an emitter that fails changes nothing; the events are emitted on a
// context the request's cancellation does not reach.
func TestUnsetWebhook(t *testing.T) {
	em := &recordingEmitter{err: errors.New("audit store down")}
	e := widgetsEnv(t, withEmitter(em))
	addWorkspace(t, e.db, "old", archived)
	for _, slug := range []string{"widgets", "old"} {
		if _, err := e.secrets.CreateSecrets("workspace", slug, []secrets.EntryInput{{Key: webhookSecretKey, Value: "a-secret-of-16-chars"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, slug := range []string{"widgets", "old", "widgets"} {
		rec := e.send(http.MethodDelete, "/workspaces/"+slug+"/forge/webhook?target=upstream", adminAuth(), "")
		if _, err := e.storedSecret(slug); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || err == nil {
			t.Errorf("%s: %d %q, secret err %v; want 204, no body and no secret", slug, rec.Code, rec.Body, err)
		}
	}
	events := em.recorded()
	if len(events) != 3 {
		t.Fatalf("%d events; want 3", len(events))
	}
	for i, existed := range []bool{true, true, false} {
		if ev := events[i]; ev.EventType != audit.EventForgeWebhookUnset || ev.Action != "unset" || ev.ActorType != "admin_token" || len(ev.Metadata) != 1 || ev.Metadata["existed"] != existed {
			t.Errorf("event %d = %+v; want hub.forge.webhook.unset with existed %v only", i, ev, existed)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPut, "/api/v1"+webhookPath+"?target=bogus", nil).WithContext(ctx)
	req.Header.Set(testutil.AuthHeader, marshal(apiKeyAuth()))
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	if ctxs := em.contexts(); rec.Code != http.StatusOK || len(ctxs) != 4 || ctxs[3] != nil {
		t.Errorf("PUT on a cancelled request: %d, emitted on %v; want 200 and a live context", rec.Code, ctxs)
	}
}

// After DELETE a delivery answers the 404 of an unknown slug and writes no row, the earlier
// rows are still listed, and a new PUT turns the ingress on again with the new secret.
func TestUnsetWebhook_TurnsTheIngressOff(t *testing.T) {
	e, em := ingressEnv(t)
	if outcome := outcomeOf(t, e.deliver("widgets", labeledDelivery("gh-1"))); outcome != "relayed" {
		t.Fatalf("before: outcome = %q", outcome)
	}
	if rec := e.send(http.MethodDelete, webhookPath, apiKeyAuth(), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body)
	}
	after, unknown := e.deliver("widgets", labeledDelivery("gh-2")), e.deliver("nosuch", labeledDelivery("gh-2"))
	if after.Code != http.StatusNotFound || after.Body.String() != unknown.Body.String() {
		t.Errorf("after the unset: %d %s; an unknown slug: %d %s; want the same 404", after.Code, after.Body, unknown.Code, unknown.Body)
	}
	rows := e.deliveryRows()
	rec := e.get("/workspaces/widgets/forge/deliveries", apiKeyAuth())
	if len(rows) != 1 || !strings.Contains(rec.Body.String(), `"delivery_id":"gh-1"`) || len(eventTypes(em.recorded())) != 2 {
		t.Errorf("%d rows, list %s, events %v; want the earlier row and no new event", len(rows), rec.Body, eventTypes(em.recorded()))
	}
	const again = "a-secret-set-again-16+"
	e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":"`+again+`"}`)
	if outcome := outcomeOf(t, e.deliver("widgets", githubDelivery(again, "gh-3", "issues", issuesPayload("labeled", "acme/widgets")))); outcome != "relayed" {
		t.Errorf("after the PUT: outcome = %q; want relayed", outcome)
	}
}

// The secret appears in no response but the PUT that generated it, and in no log line,
// event or row of the delivery log, whatever is done with it.
func TestWebhook_SecretNeverAppearsOutsideThePut(t *testing.T) {
	logs := captureLog(t)
	em := &recordingEmitter{}
	e := widgetsEnv(t, withEmitter(em))
	secret, _ := testutil.Object(t, e.send(http.MethodPut, webhookPath, apiKeyAuth(), ``))["secret"].(string)
	if secret == "" {
		t.Fatal("the PUT did not answer with the secret it made")
	}
	var seen []string
	note := func(rec *httptest.ResponseRecorder) { seen = append(seen, rec.Body.String()) }
	payload := issuesPayload("labeled", "acme/widgets")
	for _, d := range []signedDelivery{
		githubDelivery(secret, "gh-ok", "issues", payload), githubDelivery(secret, "gh-ok", "issues", payload),
		githubDelivery(secret+"x", "gh-bad", "issues", payload), githubDelivery(secret, "gh-malformed", "issues", `{"action":"labeled"}`),
		gitlabDelivery(secret, "gl-ok", "Issue Hook", gitlabIssuePayload("update", "acme/widgets")), gitlabDelivery(secret+"x", "gl-bad", "Issue Hook", "{}"),
	} {
		note(e.deliver("widgets", d))
	}
	note(e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":`+marshal(secret+" ")+`}`))
	note(e.send(http.MethodPut, webhookPath, patAuth("forge:read"), `{"secret":`+marshal(secret)+`}`))
	note(e.get("/workspaces/widgets/forge", apiKeyAuth()))
	note(e.get("/workspaces/widgets/forge/deliveries", apiKeyAuth()))
	note(e.send(http.MethodDelete, webhookPath, apiKeyAuth(), ""))
	rows := fmt.Sprintf("%+v", e.deliveryRows())
	if len(e.deliveryRows()) < 3 || len(em.recorded()) < 2 {
		t.Fatalf("%d rows and %d events; the run proves nothing", len(e.deliveryRows()), len(em.recorded()))
	}
	for name, sink := range map[string]string{"a response": strings.Join(seen, "\n"), "the log": logs.String(), "an event": marshal(em.recorded()), "the delivery log": rows} {
		if strings.Contains(sink, secret) {
			t.Errorf("the secret is in %s:\n%.400s", name, sink)
		}
	}
}

// webhookSecretOf reads the body of a PUT: nothing, an empty object and a null secret mean
// a generated one.
func TestWebhookSecretOf(t *testing.T) {
	for _, body := range []string{"", "  ", "{}", `{"secret":null}`, `{"other":1}`} {
		if secret, ref := webhookSecretOf([]byte(body)); secret != "" || ref != nil {
			t.Errorf("webhookSecretOf(%q) = %q, %+v; want a generated secret", body, secret, ref)
		}
	}
	var raw json.RawMessage
	if secret, ref := webhookSecretOf([]byte(`{"secret":"abcdefghijklmnop"}`)); secret != "abcdefghijklmnop" || ref != nil || json.Unmarshal([]byte(`"x"`), &raw) != nil {
		t.Errorf("webhookSecretOf = %q, %+v; want the secret", secret, ref)
	}
}
