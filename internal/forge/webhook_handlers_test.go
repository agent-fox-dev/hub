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
	"sync"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
)

const (
	webhookPath = "/workspaces/widgets/forge/webhook"
	hexSecret   = `^[0-9a-f]{64}$`
)

// fakeSecrets is a secrets store that counts the calls it gets and can fail them or play
// the part of a concurrent request. The real store is under it.
type fakeSecrets struct {
	SecretStore

	mu    sync.Mutex
	calls []string

	getErr, updateErr, deleteErr error
	// createRace makes CreateSecrets create the entry itself with the value "raced", as a
	// request that was faster would have, and answer with the ConflictError.
	createRace bool
}

func (f *fakeSecrets) note(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeSecrets) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeSecrets) GetSecretValue(ownerType, ownerID, key string) (string, error) {
	f.note("Get")
	if f.getErr != nil {
		return "", f.getErr
	}
	return f.SecretStore.GetSecretValue(ownerType, ownerID, key)
}

func (f *fakeSecrets) CreateSecrets(ownerType, ownerID string, entries []secrets.EntryInput) ([]secrets.SecretEntry, error) {
	f.note("Create")
	if f.createRace {
		if _, err := f.SecretStore.CreateSecrets(ownerType, ownerID, []secrets.EntryInput{{Key: entries[0].Key, Value: "raced"}}); err != nil {
			return nil, err
		}
		return nil, &secrets.ConflictError{Key: entries[0].Key}
	}
	return f.SecretStore.CreateSecrets(ownerType, ownerID, entries)
}

func (f *fakeSecrets) UpdateSecret(ownerType, ownerID, key, value string) (*secrets.SecretEntry, error) {
	f.note("Update")
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return f.SecretStore.UpdateSecret(ownerType, ownerID, key, value)
}

func (f *fakeSecrets) DeleteSecret(ownerType, ownerID, key string) error {
	f.note("Delete")
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.SecretStore.DeleteSecret(ownerType, ownerID, key)
}

// withFakeSecrets puts a fakeSecrets over the Config's store and returns it once the
// Config has been built.
func withFakeSecrets(fake *fakeSecrets) func(*Config) {
	return func(cfg *Config) {
		fake.SecretStore = cfg.Secrets
		cfg.Secrets = fake
	}
}

// newWebhookEnv is the forge routes with the workspace widgets (active, owned by ownerID)
// and a recording emitter.
func newWebhookEnv(t *testing.T, mods ...func(*Config)) (*env, *recordingEmitter) {
	t.Helper()
	em := &recordingEmitter{}
	return newWriteEnv(t, append([]func(*Config){withEmitter(em)}, mods...)...), em
}

// webhookAnswer decodes the answer of PUT .../forge/webhook into its keys.
func webhookAnswer(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("status %d: the body is not a JSON object: %v\n%s", rec.Code, err, rec.Body)
	}
	return doc
}

// storedSecret is the webhook secret of a workspace in the secrets store.
func (e *env) storedSecret(slug string) (string, error) {
	return e.secrets.GetSecretValue("workspace", slug, "FORGE_WEBHOOK_SECRET")
}

// printable makes a string of n characters from '!' to '~', which starts at the character
// the offset names, so that every character of the range, " and \ included, is used.
func printable(n, offset int) string {
	const first, count = '!', '~' - '!' + 1
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(first + (i+offset)%count)
	}
	return string(b)
}

// quoteJSON encodes s as a JSON string; unlike %q it writes control characters the JSON way.
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TS-32-1: PUT with no body, {} or {"secret": null} generates a 64-hex secret, stores it and
// returns it once.
// Requirement: 32-REQ-1.1
func TestPutWebhook_GeneratesASecret_TS_32_1(t *testing.T) {
	e, _ := newWebhookEnv(t)
	seen := map[string]bool{}
	for _, body := range []string{``, `{}`, `{"secret":null}`} {
		rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), body)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %q: status = %d; want 200\n%s", body, rec.Code, rec.Body)
		}
		doc := webhookAnswer(t, rec)
		secret, _ := doc["secret"].(string)
		if !regexp.MustCompile(hexSecret).MatchString(secret) {
			t.Errorf("PUT %q: secret = %q; want 64 lowercase hex characters", body, secret)
		}
		if doc["configured"] != true || doc["url"] != "https://hub.example.com/hooks/forge/widgets" {
			t.Errorf("PUT %q: configured, url = %v, %v; want true and the ingress URL", body, doc["configured"], doc["url"])
		}
		if len(doc) != 3 {
			t.Errorf("PUT %q: the answer has the keys %v; want configured, url and secret", body, doc)
		}
		if stored, err := e.storedSecret("widgets"); err != nil || stored != secret {
			t.Errorf("PUT %q: the stored secret = %q, %v; want the one returned", body, stored, err)
		}
		seen[secret] = true
	}
	if len(seen) != 3 {
		t.Errorf("three generated secrets are %d different ones; want 3", len(seen))
	}
}

// TS-32-2: A supplied valid secret is stored, and not echoed back.
// Requirement: 32-REQ-1.2
func TestPutWebhook_StoresASuppliedSecret_TS_32_2(t *testing.T) {
	e, _ := newWebhookEnv(t)
	for i, n := range []int{16, 40, 255} {
		secret := printable(n, i*31)
		rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":`+quoteJSON(secret)+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("a secret of %d characters: status = %d; want 200\n%s", n, rec.Code, rec.Body)
		}
		doc := webhookAnswer(t, rec)
		if _, ok := doc["secret"]; ok {
			t.Errorf("a secret of %d characters: the answer %s has a secret key", n, rec.Body)
		}
		if doc["configured"] != true || doc["url"] != "https://hub.example.com/hooks/forge/widgets" || len(doc) != 2 {
			t.Errorf("a secret of %d characters: the answer = %v; want configured and url only", n, doc)
		}
		if stored, err := e.storedSecret("widgets"); err != nil || stored != secret {
			t.Errorf("a secret of %d characters: stored = %q, %v; want the one supplied", n, stored, err)
		}
	}
}

// TS-32-3: Invalid bodies answer 400 naming secret, without repeating the value, and store
// nothing.
// Requirement: 32-REQ-1.3
func TestPutWebhook_RefusesInvalidBodies_TS_32_3(t *testing.T) {
	e, _ := newWebhookEnv(t)
	const existing = "existing-secret-value"
	if _, err := e.secrets.CreateSecrets("workspace", "widgets", []secrets.EntryInput{{Key: "FORGE_WEBHOOK_SECRET", Value: existing}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, supplied string
	}{
		{"an array", `["abcdefghijklmnopqrst"]`, "abcdefghijklmnopqrst"},
		{"null", `null`, ""},
		{"a string", `"abcdefghijklmnopqrst"`, "abcdefghijklmnopqrst"},
		{"malformed JSON", `{"secret": "abcdefghijklmnopqrst`, "abcdefghijklmnopqrst"},
		{"a number", `{"secret":123}`, ""},
		{"a boolean", `{"secret":true}`, ""},
		{"15 characters", `{"secret":"abcdefghijklmno"}`, "abcdefghijklmno"},
		{"256 characters", `{"secret":` + quoteJSON(printable(256, 0)) + `}`, printable(256, 0)},
		{"an empty secret", `{"secret":""}`, ""},
		{"a space", `{"secret":"abcdefghij klmnopqrst"}`, "abcdefghij klmnopqrst"},
		{"a control character", `{"secret":"abcdefghij\u0007klmnopqrst"}`, "abcdefghij\a" + "klmnopqrst"},
		{"a character outside ASCII", `{"secret":"abcdefghijéklmnopqrst"}`, "abcdefghijéklmnopqrst"},
		{"a delete character", `{"secret":"abcdefghij\u007fklmnopqrst"}`, "abcdefghij\x7fklmnopqrst"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d; want 400\n%s", rec.Code, rec.Body)
			}
			msg := decodeError(t, rec).Message
			if !strings.Contains(msg, "secret") {
				t.Errorf("message = %q; want it to name secret", msg)
			}
			if tc.supplied != "" && strings.Contains(msg, tc.supplied) {
				t.Errorf("message = %q; it repeats the value", msg)
			}
			if stored, err := e.storedSecret("widgets"); err != nil || stored != existing {
				t.Errorf("stored = %q, %v; want %q unchanged", stored, err, existing)
			}
		})
	}
}

// TS-32-4: PUT checks the credential, the scope, the ownership and the status in this order,
// stops at the first failure, and reads neither the body nor the store before them.
// Requirement: 32-REQ-1.4
func TestPutWebhook_ChecksInOrder_TS_32_4(t *testing.T) {
	fake := &fakeSecrets{}
	e, _ := newWebhookEnv(t, withFakeSecrets(fake))
	addWorkspace(t, e.db, "old", func(r *workspaceRow) { r.status = "archived" })
	const invalid = `["not an object"]`
	stranger := &apikit.AuthInfo{UserID: otherID, CredentialType: "api_key"}

	for _, tc := range []struct {
		name   string
		path   string
		auth   *apikit.AuthInfo
		status int
		msg    string
	}{
		{"no credential", webhookPath, nil, http.StatusUnauthorized, ""},
		{"a PAT with forge:read only", webhookPath, patAuth("forge:read"), http.StatusForbidden, "forge:write"},
		{"another user's workspace", webhookPath, stranger, http.StatusNotFound, ""},
		{"an unknown workspace", "/workspaces/nosuch/forge/webhook", apiKeyAuth(), http.StatusNotFound, ""},
		{"an archived workspace", "/workspaces/old/forge/webhook", apiKeyAuth(), http.StatusBadRequest, "workspace is archived"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.send(http.MethodPut, tc.path, tc.auth, invalid)
			if rec.Code != tc.status {
				t.Fatalf("status = %d; want %d\n%s", rec.Code, tc.status, rec.Body)
			}
			msg := decodeError(t, rec).Message
			if tc.msg != "" && !strings.Contains(msg, tc.msg) {
				t.Errorf("message = %q; want it to contain %q", msg, tc.msg)
			}
			if tc.msg == "workspace is archived" && msg != tc.msg {
				t.Errorf("message = %q; want exactly %q", msg, tc.msg)
			}
			if strings.Contains(msg, "secret") && !strings.Contains(msg, "forge:write") {
				t.Errorf("message = %q; the body was validated before the check failed", msg)
			}
		})
	}
	if calls := fake.called(); len(calls) != 0 {
		t.Errorf("the secrets store was called %v; want no call", calls)
	}
}

// secretEntries is how many entries the workspace scope holds under the key.
func (e *env) secretEntries(slug, key string) int {
	e.t.Helper()
	list, err := e.secrets.ListSecrets("workspace", slug)
	if err != nil {
		e.t.Fatal(err)
	}
	n := 0
	for _, entry := range list {
		if strings.EqualFold(entry.Key, key) {
			n++
		}
	}
	return n
}

// TS-32-5: PUT updates an existing secret, and after a concurrent create conflicts it
// updates once more.
// Requirement: 32-REQ-1.5
func TestPutWebhook_UpdatesAndRetriesOnce_TS_32_5(t *testing.T) {
	t.Run("a secret that exists", func(t *testing.T) {
		fake := &fakeSecrets{}
		e, _ := newWebhookEnv(t, withFakeSecrets(fake))
		if _, err := e.secrets.CreateSecrets("workspace", "widgets", []secrets.EntryInput{{Key: "FORGE_WEBHOOK_SECRET", Value: "old"}}); err != nil {
			t.Fatal(err)
		}
		const fresh = "a-new-secret-of-16+"
		if rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":"`+fresh+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
		}
		if stored, _ := e.storedSecret("widgets"); stored != fresh {
			t.Errorf("stored = %q; want %q", stored, fresh)
		}
		if n := e.secretEntries("widgets", "FORGE_WEBHOOK_SECRET"); n != 1 {
			t.Errorf("%d FORGE_WEBHOOK_SECRET entries; want 1", n)
		}
		if calls := fake.called(); strings.Join(calls, ",") != "Update" {
			t.Errorf("store calls = %v; want one Update", calls)
		}
	})
	t.Run("a create that conflicts", func(t *testing.T) {
		fake := &fakeSecrets{createRace: true}
		e, _ := newWebhookEnv(t, withFakeSecrets(fake))
		const fresh = "a-new-secret-of-16+"
		if rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":"`+fresh+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
		}
		if stored, _ := e.storedSecret("widgets"); stored != fresh {
			t.Errorf("stored = %q; want the new secret over the one the other request made", stored)
		}
		if n := e.secretEntries("widgets", "FORGE_WEBHOOK_SECRET"); n != 1 {
			t.Errorf("%d FORGE_WEBHOOK_SECRET entries; want 1", n)
		}
		if calls := fake.called(); strings.Join(calls, ",") != "Update,Create,Update" {
			t.Errorf("store calls = %v; want Update, Create, Update", calls)
		}
	})
}

// TS-32-6: A full scope answers 400 with the store's message and any other store failure
// 500; neither emits an event.
// Requirement: 32-REQ-1.6
func TestPutWebhook_StoreFailures_TS_32_6(t *testing.T) {
	t.Run("a full scope", func(t *testing.T) {
		e, em := newWebhookEnv(t)
		entries := make([]secrets.EntryInput, secrets.MaxEntriesPerScope)
		for i := range entries {
			entries[i] = secrets.EntryInput{Key: fmt.Sprintf("K%03d", i), Value: "v"}
		}
		if _, err := e.secrets.CreateSecrets("workspace", "widgets", entries); err != nil {
			t.Fatal(err)
		}
		rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), ``)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d; want 400\n%s", rec.Code, rec.Body)
		}
		if msg := decodeError(t, rec).Message; !strings.Contains(msg, "maximum of 100 entries per scope exceeded") {
			t.Errorf("message = %q; want the store's", msg)
		}
		if got := em.recorded(); len(got) != 0 {
			t.Errorf("events = %v; want none", got)
		}
	})
	for name, fake := range map[string]*fakeSecrets{
		"an update that fails":                           {updateErr: errors.New("boom: disk gone")},
		"an update that fails, wrapped like the store's": {updateErr: fmt.Errorf("update secret: %w", errors.New("boom"))},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			e, em := newWebhookEnv(t, withFakeSecrets(fake))
			const supplied = "supplied-secret-value-123"
			rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":"`+supplied+`"}`)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d; want 500\n%s", rec.Code, rec.Body)
			}
			if msg := decodeError(t, rec).Message; strings.Contains(msg, "boom") || strings.Contains(msg, supplied) {
				t.Errorf("message = %q; it holds the cause or the secret", msg)
			}
			if got := em.recorded(); len(got) != 0 {
				t.Errorf("events = %v; want none", got)
			}
			if strings.Contains(logs.String(), supplied) {
				t.Errorf("the log holds the secret:\n%s", logs)
			}
		})
	}
}

// TS-32-7: The ingress URL has no trailing slash and is null when external_url is unset, and
// the secret is stored either way.
// Requirement: 32-REQ-1.7
func TestPutWebhook_IngressURL_TS_32_7(t *testing.T) {
	for _, tc := range []struct {
		external string
		want     any
	}{
		{"https://hub.example.com/", "https://hub.example.com/hooks/forge/widgets"},
		{"https://hub.example.com", "https://hub.example.com/hooks/forge/widgets"},
		{"", nil},
	} {
		e, _ := newWebhookEnv(t, func(cfg *Config) { cfg.ExternalURL = tc.external })
		rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":"abcdefghijklmnop"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("external_url %q: status = %d; want 200\n%s", tc.external, rec.Code, rec.Body)
		}
		doc := webhookAnswer(t, rec)
		if url, present := doc["url"]; !present || url != tc.want {
			t.Errorf("external_url %q: url = %v (present %v); want %v", tc.external, url, present, tc.want)
		}
		if stored, err := e.storedSecret("widgets"); err != nil || stored != "abcdefghijklmnop" {
			t.Errorf("external_url %q: stored = %q, %v; want the secret stored", tc.external, stored, err)
		}
	}
}

// TS-32-8: A successful PUT emits hub.forge.webhook.set with the generated flag only.
// Requirement: 32-REQ-1.8
func TestPutWebhook_EmitsAnEvent_TS_32_8(t *testing.T) {
	e, em := newWebhookEnv(t)
	generated := webhookAnswer(t, e.send(http.MethodPut, webhookPath, apiKeyAuth(), ``))["secret"].(string)
	const supplied = "a-supplied-secret-123"
	e.send(http.MethodPut, webhookPath, apiKeyAuth(), `{"secret":"`+supplied+`"}`)

	events := em.recorded()
	if len(events) != 2 {
		t.Fatalf("%d events; want 2", len(events))
	}
	for i, wantGenerated := range []bool{true, false} {
		ev := events[i]
		if ev.EventType != audit.EventForgeWebhookSet || ev.Workspace != "widgets" {
			t.Errorf("event %d = %s for %q; want hub.forge.webhook.set for widgets", i, ev.EventType, ev.Workspace)
		}
		if len(ev.Metadata) != 1 || ev.Metadata["generated"] != wantGenerated {
			t.Errorf("event %d metadata = %v; want only generated: %v", i, ev.Metadata, wantGenerated)
		}
		if ev.ActorID != ownerID || ev.ActorType != "api_key" || ev.ResourceType != "webhook" || ev.ResourceID != "widgets" || ev.Action != "set" {
			t.Errorf("event %d = %+v; want the caller as actor, resource webhook widgets, action set", i, ev)
		}
		if b, _ := json.Marshal(ev); strings.Contains(string(b), generated) || strings.Contains(string(b), supplied) {
			t.Errorf("event %d holds a secret: %s", i, b)
		}
	}
}

// TS-32-9: DELETE removes the secret, answers 204 whether or not there was one, and works on
// an archived workspace.
// Requirement: 32-REQ-2.1
func TestDeleteWebhook_TS_32_9(t *testing.T) {
	e, em := newWebhookEnv(t)
	addWorkspace(t, e.db, "old", func(r *workspaceRow) { r.status = "archived" })
	addWorkspace(t, e.db, "bare")
	for _, slug := range []string{"widgets", "old"} {
		if _, err := e.secrets.CreateSecrets("workspace", slug, []secrets.EntryInput{{Key: "FORGE_WEBHOOK_SECRET", Value: "a-secret-of-16-chars"}}); err != nil {
			t.Fatal(err)
		}
	}

	for _, slug := range []string{"widgets", "old", "bare"} {
		rec := e.send(http.MethodDelete, "/workspaces/"+slug+"/forge/webhook", apiKeyAuth(), "")
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Errorf("%s: status = %d with body %q; want 204 and no body", slug, rec.Code, rec.Body)
		}
		if _, err := e.storedSecret(slug); err == nil {
			t.Errorf("%s: the secret is still there", slug)
		}
	}
	events := em.recorded()
	if len(events) != 3 {
		t.Fatalf("%d events; want 3", len(events))
	}
	for i, want := range []struct {
		slug    string
		existed bool
	}{{"widgets", true}, {"old", true}, {"bare", false}} {
		ev := events[i]
		if ev.EventType != audit.EventForgeWebhookUnset || ev.Workspace != want.slug {
			t.Errorf("event %d = %s for %q; want hub.forge.webhook.unset for %q", i, ev.EventType, ev.Workspace, want.slug)
		}
		if len(ev.Metadata) != 1 || ev.Metadata["existed"] != want.existed {
			t.Errorf("event %d metadata = %v; want only existed: %v", i, ev.Metadata, want.existed)
		}
		if ev.ActorID != ownerID || ev.ActorType != "api_key" || ev.ResourceType != "webhook" || ev.ResourceID != want.slug || ev.Action != "unset" {
			t.Errorf("event %d = %+v; want the caller as actor, resource webhook %s, action unset", i, ev, want.slug)
		}
	}
}

// TS-32-10: DELETE answers 401, 403 and 404 without touching the store, and 500 when the
// store fails; a failure emits no event.
// Requirement: 32-REQ-2.2
func TestDeleteWebhook_Refusals_TS_32_10(t *testing.T) {
	fake := &fakeSecrets{}
	e, em := newWebhookEnv(t, withFakeSecrets(fake))
	stranger := &apikit.AuthInfo{UserID: otherID, CredentialType: "api_key"}
	for _, tc := range []struct {
		name   string
		path   string
		auth   *apikit.AuthInfo
		status int
	}{
		{"no credential", webhookPath, nil, http.StatusUnauthorized},
		{"a PAT with forge:read only", webhookPath, patAuth("forge:read"), http.StatusForbidden},
		{"another user's workspace", webhookPath, stranger, http.StatusNotFound},
		{"an unknown workspace", "/workspaces/nosuch/forge/webhook", apiKeyAuth(), http.StatusNotFound},
	} {
		rec := e.send(http.MethodDelete, tc.path, tc.auth, "")
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d; want %d\n%s", tc.name, rec.Code, tc.status, rec.Body)
		}
		if tc.status == http.StatusForbidden {
			if msg := decodeError(t, rec).Message; !strings.Contains(msg, "forge:write") {
				t.Errorf("%s: message = %q; want it to name forge:write", tc.name, msg)
			}
		}
	}
	if calls := fake.called(); len(calls) != 0 {
		t.Errorf("the secrets store was called %v; want no call", calls)
	}

	broken := &fakeSecrets{deleteErr: errors.New("boom: disk gone")}
	e2, em2 := newWebhookEnv(t, withFakeSecrets(broken))
	rec := e2.send(http.MethodDelete, webhookPath, apiKeyAuth(), "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("a failing store: status = %d; want 500\n%s", rec.Code, rec.Body)
	}
	if msg := decodeError(t, rec).Message; strings.Contains(msg, "boom") {
		t.Errorf("a failing store: message = %q; it holds the cause", msg)
	}
	if len(em.recorded()) != 0 || len(em2.recorded()) != 0 {
		t.Errorf("events = %v and %v; want none", em.recorded(), em2.recorded())
	}
}

// The webhook routes take no target: one that would be refused on another route changes
// nothing here. A PAT holding forge:write is allowed, and so are an admin token and no emitter.
func TestWebhookRoutes_IgnoreTargetAndAcceptWriteScope(t *testing.T) {
	e := newWriteEnv(t) // no emitter
	if rec := e.send(http.MethodPut, webhookPath+"?target=bogus", patAuth("forge:write"), `{"secret":"abcdefghijklmnop"}`); rec.Code != http.StatusOK {
		t.Errorf("PUT with ?target=bogus: status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	if rec := e.send(http.MethodDelete, webhookPath+"?target=upstream", adminAuth(), ""); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE with ?target=upstream as an admin: status = %d; want 204\n%s", rec.Code, rec.Body)
	}
}

// Without a secrets store the routes are misconfigured: they answer 500 and say so in the
// log, after the checks that come first.
func TestWebhookRoutes_WithoutASecretsStore(t *testing.T) {
	logs := captureLog(t)
	e := newWriteEnv(t, func(cfg *Config) { cfg.Secrets = nil })
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		if rec := e.send(method, webhookPath, apiKeyAuth(), ""); rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d; want 500\n%s", method, rec.Code, rec.Body)
		}
		if rec := e.send(method, webhookPath, nil, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a credential: status = %d; want 401", method, rec.Code)
		}
	}
	if !strings.Contains(logs.String(), "secrets store") {
		t.Errorf("the log = %q; want it to say the secrets store is not configured", logs)
	}
}

// An emitter that fails changes nothing about the answer.
func TestWebhookRoutes_AFailingEmitterChangesNothing(t *testing.T) {
	em := &recordingEmitter{err: errors.New("audit store down")}
	e := newWriteEnv(t, withEmitter(em))
	if rec := e.send(http.MethodPut, webhookPath, apiKeyAuth(), ``); rec.Code != http.StatusOK {
		t.Errorf("PUT: status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	if rec := e.send(http.MethodDelete, webhookPath, apiKeyAuth(), ``); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE: status = %d; want 204\n%s", rec.Code, rec.Body)
	}
	if n := len(em.recorded()); n != 2 {
		t.Errorf("%d events offered; want 2", n)
	}
}

// The events of the webhook routes are recorded when the caller has gone away: the emitter
// gets a context the request's cancellation does not reach.
func TestWebhookRoutes_EventsOutliveTheRequest(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(method, "/api/v1"+webhookPath, nil).WithContext(ctx)
		b, _ := json.Marshal(apiKeyAuth())
		req.Header.Set("X-Test-Auth", string(b))
		cancel()
		rec := httptest.NewRecorder()
		e.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
			t.Fatalf("%s: status = %d; want it to succeed\n%s", method, rec.Code, rec.Body)
		}
	}
	if len(em.ctxErr) != 2 {
		t.Fatalf("%d events; want 2", len(em.ctxErr))
	}
	for i, err := range em.ctxErr {
		if err != nil {
			t.Errorf("event %d was emitted on a context with the error %v; want a live one", i, err)
		}
	}
}
