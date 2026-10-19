package sandboxes

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"
)

const plantedSecret = "PLANTED-VALUE-123"

// TS-26-53: A successful POST emits exactly one hub.sandbox.lease event
// carrying the workspace and no secret or token; a refusal and a DELETE emit
// none; a nil emitter or a failing emit does not change the 201.
// Requirement: 26-REQ-3.1
func TestAudit_LeaseEvent_TS_26_53(t *testing.T) {
	logs := captureDefaultLogs(t)
	emitter := &recordingEmitter{}
	env := newLeaseEnv(t, leaseEnvOptions{emitter: emitter})

	issued := env.mustIssue()
	id, token := issued["id"].(string), issued["token"].(string)
	if rec := env.post("widgets", `{"profile":"nope"}`, apiKeyAuth()); rec.Code != http.StatusBadRequest {
		t.Fatalf("refused POST: status %d", rec.Code)
	}
	if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d", rec.Code)
	}

	events := emitter.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events; want exactly one: %+v", len(events), events)
	}
	e := events[0]
	if audit.EventSandboxLease != "hub.sandbox.lease" {
		t.Errorf("audit.EventSandboxLease = %q; want hub.sandbox.lease", audit.EventSandboxLease)
	}
	if e.EventType != audit.EventSandboxLease {
		t.Errorf("event type = %q; want %s", e.EventType, audit.EventSandboxLease)
	}
	if e.ResourceType != "sandbox" || e.ResourceID != id || e.Action != "lease" || e.Workspace != "widgets" {
		t.Errorf("event = %s %s %s in %s; want sandbox %s lease in widgets", e.ResourceType, e.ResourceID, e.Action, e.Workspace, id)
	}
	if e.ActorID != leaseOwner || e.ActorType != "api_key" {
		t.Errorf("actor = %q (%s); want the API key's user %s", e.ActorID, e.ActorType, leaseOwner)
	}
	want := map[string]any{
		"lease_id":              id,
		"profile":               "tools",
		"purpose":               "",
		"ttl":                   "1h0m0s",
		"secret_names":          []string{"GIT_PAT"},
		"optional_secret_names": []string{"EXTRA_TOKEN"},
	}
	if !reflect.DeepEqual(e.Metadata, want) {
		t.Errorf("metadata = %#v\nwant       %#v", e.Metadata, want)
	}

	raw, _ := json.Marshal(e)
	for _, leak := range []string{token, plantedSecret, "envval", HashToken(token)} {
		if strings.Contains(string(raw)+logs.String(), leak) {
			t.Errorf("the event or the log output contains %q", leak)
		}
	}

	// An admin token is recorded as such.
	if rec := env.post("widgets", `{"profile":"tools","purpose":"nightly","ttl":"30m"}`, adminAuth()); rec.Code != http.StatusCreated {
		t.Fatalf("admin POST: status %d", rec.Code)
	}
	admin := emitter.recorded()[1]
	if admin.ActorType != "admin_token" || admin.Metadata["purpose"] != "nightly" || admin.Metadata["ttl"] != "30m0s" {
		t.Errorf("admin event = actor %q, purpose %v, ttl %v", admin.ActorType, admin.Metadata["purpose"], admin.Metadata["ttl"])
	}
}

// TS-26-53 (continued): a nil emitter skips emission, and a failing emit is
// logged without changing the 201 or leaking the token.
func TestAudit_NilAndFailingEmitter_TS_26_53(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	if rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth()); rec.Code != http.StatusCreated {
		t.Errorf("nil emitter: status %d; want 201: %s", rec.Code, rec.Body)
	}

	logs := captureDefaultLogs(t)
	emitter := &recordingEmitter{err: errors.New("audit store down")}
	env = newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig(), emitter: emitter})
	rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		t.Fatalf("failing emitter: status %d; want 201: %s", rec.Code, rec.Body)
	}
	if len(emitter.recorded()) != 1 {
		t.Errorf("%d emit attempts; want 1", len(emitter.recorded()))
	}
	logged := logs.String()
	if !strings.Contains(logged, "hub.sandbox.lease") || !strings.Contains(logged, "audit store down") {
		t.Errorf("the failed emit was not logged: %q", logged)
	}
	if token := jsonObject(t, rec)["token"].(string); strings.Contains(logged, token) {
		t.Errorf("the log line contains the token: %q", logged)
	}
}

// TS-26-54: A planted secret value appears in no HTTP response, and the token
// appears only in the response of the POST that issued it.
// Requirements: 26-REQ-1.7, 26-REQ-3.1
func TestConfidentiality_TS_26_54(t *testing.T) {
	logs := captureDefaultLogs(t)
	env := newLeaseEnv(t, leaseEnvOptions{emitter: &recordingEmitter{}})
	env.plantSecret("user", leaseOwner, "OTHER_SECRET", plantedSecret+"-user")

	posted := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
	if posted.Code != http.StatusCreated {
		t.Fatalf("POST: status %d: %s", posted.Code, posted.Body)
	}
	id, token := jsonObject(t, posted)["id"].(string), jsonObject(t, posted)["token"].(string)

	bodies := map[string]string{
		"POST":           posted.Body.String(),
		"GET list":       env.get("/api/v1/workspaces/widgets/sandboxes", apiKeyAuth()).Body.String(),
		"GET list all":   env.get("/api/v1/workspaces/widgets/sandboxes?all=true", apiKeyAuth()).Body.String(),
		"GET one":        env.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth()).Body.String(),
		"GET /sandboxes": env.get("/api/v1/sandboxes", adminAuth()).Body.String(),
		"DELETE":         env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth()).Body.String(),
		"GET one after":  env.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth()).Body.String(),
		"400":            env.post("widgets", `{"profile":"nope"}`, apiKeyAuth()).Body.String(),
		"404":            env.post("nosuch", `{}`, apiKeyAuth()).Body.String(),
	}
	// A 422 for a profile whose secret is missing, and a 500 from the random
	// source, are logged and answered too.
	missing := defaultLeaseConfig()
	missing.Profiles[0].Secrets = []string{"GIT_PAT", "ABSENT_SECRET"}
	missingEnv := newLeaseEnv(t, leaseEnvOptions{config: missing})
	bodies["422"] = missingEnv.post("widgets", `{"profile":"tools"}`, apiKeyAuth()).Body.String()
	brokenEnv := newLeaseEnv(t, leaseEnvOptions{rand: failingReader{}})
	bodies["500"] = brokenEnv.post("widgets", `{"profile":"tools"}`, apiKeyAuth()).Body.String()

	for name, body := range bodies {
		for _, secret := range []string{plantedSecret, "envval"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s response contains %q", name, secret)
			}
		}
		if name != "POST" && strings.Contains(body, token) {
			t.Errorf("%s response contains the token", name)
		}
	}
	if !strings.Contains(bodies["POST"], token) {
		t.Error("the POST response does not carry the token")
	}
	if strings.Contains(bodies["422"], "ABSENT_SECRET") == false {
		t.Errorf("the 422 does not name the missing key: %s", bodies["422"])
	}
	for _, leak := range []string{token, plantedSecret, plantedSecret + "-user", "envval"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("the log output contains %q:\n%s", leak, logs)
		}
	}
}
