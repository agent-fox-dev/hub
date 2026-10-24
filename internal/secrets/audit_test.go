package secrets

import (
	"errors"
	"net/http"
	"testing"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// Each create, update and delete emits one hub.secret.* or hub.variable.*
// event naming the actor, the owner tier and the key; refused requests emit
// nothing.
func TestAudit_OneEventPerMutation(t *testing.T) {
	v := newEnv(t)
	v.seedTeam(t)
	cases := []struct {
		method, path, body           string
		resource, action, scope, key string
		events                       int
	}{
		{http.MethodPost, "/user/secrets", `{"entries":[{"key":"S1","value":"v"},{"key":"S2","value":"v"}]}`, "secret", "create", "user", "S1", 2},
		{http.MethodPatch, "/user/secrets/S1", `{"value":"w"}`, "secret", "update", "user", "S1", 1},
		{http.MethodDelete, "/user/secrets/S2", "", "secret", "delete", "user", "S2", 1},
		{http.MethodPost, "/orgs/acme/vars", `{"entries":[{"key":"V1","value":"v"}]}`, "variable", "create", "org", "V1", 1},
		{http.MethodPatch, "/orgs/acme/vars/V1", `{"value":"w"}`, "variable", "update", "org", "V1", 1},
		{http.MethodDelete, "/orgs/acme/vars/V1", "", "variable", "delete", "org", "V1", 1},
		{http.MethodPost, "/user/secrets", `{"entries":[{"key":"S1","value":"v"}]}`, "", "", "", "", 0},
		{http.MethodGet, "/user/secrets", "", "", "", "", "", 0},
		{http.MethodDelete, "/orgs/acme/vars/V1", "", "", "", "", "", 0},
	}
	for _, c := range cases {
		before := len(v.audit.all())
		v.do(t, c.method, "/api/v1"+c.path, c.body, testutil.PAT(owner, "secrets:write", "vars:write"))
		events := v.audit.all()[before:]
		if len(events) != c.events {
			t.Errorf("%s %s: %d events, want %d", c.method, c.path, len(events), c.events)
			continue
		}
		if c.events == 0 {
			continue
		}
		e := events[0]
		if e.EventType != "hub."+c.resource+"."+c.action || e.ResourceType != c.resource || e.Action != c.action ||
			e.ActorID != owner || e.ActorType != "pat" || e.Metadata["scope"] != c.scope || e.Metadata["key"] != c.key {
			t.Errorf("%s %s: event %+v", c.method, c.path, e)
		}
	}
}

// Without an emitter the mutations succeed and emit nothing; an emitter that
// fails does not change the response.
func TestAudit_OptionalAndNonBlocking(t *testing.T) {
	v := newEnv(t)
	testutil.SeedUser(t, v.db, owner)
	v.audit.err = errors.New("audit store down")
	want(t, v.do(t, http.MethodPost, "/api/v1/user/vars", `{"entries":[{"key":"K","value":"v"}]}`, testutil.User(owner)), http.StatusCreated)
	if len(v.audit.all()) != 1 {
		t.Errorf("%d events recorded by the failing emitter, want 1", len(v.audit.all()))
	}
	e, api := testutil.API()
	if err := RegisterRoutesWithAudit(api, SecretsRouteConfig{DB: v.db}); err != nil {
		t.Fatal(err)
	}
	want(t, testutil.Request(t, e, http.MethodDelete, "/api/v1/user/vars/K", "", testutil.User(owner)), http.StatusNoContent)
	if len(v.audit.all()) != 1 {
		t.Errorf("%d events after a mutation without an emitter, want still 1", len(v.audit.all()))
	}
}
