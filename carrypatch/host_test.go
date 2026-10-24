package carrypatch

import (
	"errors"
	"net/http"
	"testing"

	"github.com/txsvc/apikit"
)

// The routes answer by the host's access rule: 401 without a credential, 404
// for a stranger, 403 for a member's write, and success for a member's read,
// the owner and the admin token.
func TestAuthorize_AccessMatrix(t *testing.T) {
	e := newEnv(t)
	seedWS(t, e.db, ws{slug: "ws"})
	seedMember(t, e.db, "ws", "org-1", "bob")
	seedPatch(t, e.db, "ws", "p1", "feature/a", 1, "active")

	cases := []struct {
		name   string
		method string
		path   string
		auth   *apikit.AuthInfo
		want   int
	}{
		{"no credential", http.MethodGet, "/patches", nil, http.StatusUnauthorized},
		{"stranger read", http.MethodGet, "/patches", user("stranger"), http.StatusNotFound},
		{"member read", http.MethodGet, "/patches", user("bob"), http.StatusOK},
		{"admin read", http.MethodGet, "/patches", admin(), http.StatusOK},
		{"stranger write", http.MethodDelete, "/patches/p1", user("stranger"), http.StatusNotFound},
		{"member write", http.MethodDelete, "/patches/p1", user("bob"), http.StatusForbidden},
		{"owner write", http.MethodDelete, "/patches/p1", alice, http.StatusNoContent},
	}
	for _, c := range cases {
		rec := e.do(c.method, "/api/v1/workspaces/ws"+c.path, "", c.auth)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
		if c.want == http.StatusForbidden {
			if _, typ := e.wantErr(rec, c.want); typ != "forbidden" {
				t.Errorf("%s: error_type = %q, want forbidden", c.name, typ)
			}
		}
	}
}

// A host without an access rule cannot serve any route.
func TestAuthorize_UnconfiguredHostAnswers500(t *testing.T) {
	e := newEnv(t)
	seedWS(t, e.db, ws{slug: "ws"})
	saved := host
	SetHost(Host{Lock: saved.Lock, TryLock: saved.TryLock, BeginRebuild: saved.BeginRebuild})
	t.Cleanup(func() { SetHost(saved) })

	msg, _ := e.wantErr(e.do(http.MethodGet, "/api/v1/workspaces/ws/patches", "", alice), http.StatusInternalServerError)
	if msg != "carry-patch host is not configured" {
		t.Errorf("message = %q", msg)
	}
}

func TestStripUserinfoText_RemovesCredentialsFromEveryURL(t *testing.T) {
	cases := map[string]string{
		"push https://user:tok@example.com/r.git failed": "push https://example.com/r.git failed",
		"ssh://git@host/x and https://a:b@h/y?q=1":       "ssh://host/x and https://h/y?q=1",
		"no url here":               "no url here",
		"HTTPS://User:Pw@Host/path": "HTTPS://Host/path",
		"https://example.com/plain": "https://example.com/plain",
		"origin rejected push: https://x:y@h/r.git: forbidden": "origin rejected push: https://h/r.git: forbidden",
	}
	for in, want := range cases {
		if got := stripUserinfoText(in); got != want {
			t.Errorf("stripUserinfoText(%q) = %q, want %q", in, got, want)
		}
		if again := stripUserinfoText(stripUserinfoText(in)); again != want {
			t.Errorf("not idempotent for %q: %q", in, again)
		}
	}
}

// An audit emitter that is absent or failing changes no response; a present
// one records the caller as the actor.
func TestEmitAudit_EmitterPresenceAndFailureChangeNothing(t *testing.T) {
	e := newEnv(t)
	seedWS(t, e.db, ws{slug: "ws"})

	e.audited = false
	e.want(e.do(http.MethodPost, "/api/v1/workspaces/ws/patches", `{"branch_name":"feature/one"}`, alice), http.StatusCreated)

	e.audited = true
	e.audit.err = errors.New("audit down")
	e.want(e.do(http.MethodPost, "/api/v1/workspaces/ws/patches", `{"branch_name":"feature/two"}`, pat("alice", "patches:write")), http.StatusCreated)
	events := e.audit.ofType("hub.patch.create")
	if len(events) != 1 {
		t.Fatalf("create events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.ActorID != "alice" || ev.ActorType != "pat" || ev.Workspace != "ws" || ev.ResourceID != "feature/two" || ev.Action != "create" {
		t.Errorf("event = %+v", ev)
	}
	if ev.Metadata["branch_name"] != "feature/two" || ev.Metadata["position"] != 2 {
		t.Errorf("metadata = %v", ev.Metadata)
	}
}
