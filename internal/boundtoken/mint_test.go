package boundtoken

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// Minting is refused before apikit is called: without a credential, for a
// bound PAT (reported), for an admin token, for anyone but the owner (the
// member of the org included, with the unknown workspace's 404) and for a
// workspace that is not active.
func TestMint_RefusesBeforeDelegating(t *testing.T) {
	v := newEnv(t)
	testutil.SeedWorkspace(t, v.db, testutil.Workspace{Slug: "archived", OwnerID: owner, Status: "archived"})
	testutil.BindToken(t, v.db, "bound", "elsewhere")
	refusals := captureRefusals(t)
	bound := testutil.BoundPAT(owner, "bound", "tokens:manage")
	for _, c := range []struct {
		name, path   string
		auth         *apikit.AuthInfo
		status       int
		message, typ string
	}{
		{"no credential", mintPath, nil, http.StatusUnauthorized, "authentication required", ""},
		{"bound PAT", mintPath, bound, http.StatusForbidden, "this token is bound to workspace elsewhere", wsaccess.ErrorTypeTokenBound},
		{"admin", mintPath, testutil.Admin(), http.StatusForbidden, "admin tokens cannot mint bound tokens; a real user is required as the token's owner", ""},
		{"member", mintPath, testutil.User(member), http.StatusNotFound, "workspace not found", ""},
		{"stranger", mintPath, testutil.PAT(stranger, "tokens:manage"), http.StatusNotFound, "workspace not found", ""},
		{"unknown", "/api/v1/workspaces/ghost/tokens", testutil.User(owner), http.StatusNotFound, "workspace not found", ""},
		{"archived", "/api/v1/workspaces/archived/tokens", testutil.User(owner), http.StatusBadRequest, "workspace is not active", ""},
	} {
		rec := testutil.Request(t, v.e, http.MethodPost, c.path, `{"name":"svc"}`, c.auth)
		if rec.Code != c.status || testutil.Error(t, rec).Message != c.message || testutil.Error(t, rec).ErrorType != c.typ {
			t.Errorf("%s: %d %s; want %d %q", c.name, rec.Code, rec.Body, c.status, c.message)
		}
	}
	if v.stub.calls != 0 {
		t.Errorf("apikit was called %d times behind a refusal", v.stub.calls)
	}
	want := wsaccess.BoundRefusal{TokenID: "bound", UserID: owner, BoundSlug: "elsewhere", RequestedSlug: slug, Method: "POST",
		Route: "/api/v1/workspaces/:slug/tokens", Status: http.StatusForbidden, Surface: wsaccess.SurfaceAPI}
	if len(*refusals) != 1 || (*refusals)[0] != want {
		t.Errorf("refusals %+v, want %+v", *refusals, want)
	}
	testutil.Exec(t, v.db, `DROP TABLE token_workspace_bindings`)
	wantRefusal(t, v.mint(t, bound, ""), http.StatusInternalServerError, "internal server error", "")
}

// The owner's request is served by apikit's own POST /user/tokens, under the
// group's mount point, with the body, the Authorization header, the content
// type and length and the context of the caller; the 201 is bound to the
// workspace, answered with the token and the workspace under Cache-Control
// no-store, and recorded as hub.token.bind on a context the caller cannot
// cancel; an emitter's error does not change the answer.
func TestMint_DelegatesToAPIKitAndBinds(t *testing.T) {
	v := newEnv(t)
	v.stub.tokenID, v.audit.err = "T1", context.DeadlineExceeded
	sent := "{ \"name\" : \"svc\",\n  \"permissions\": [\"workspaces:read\"], \"unknown\": 1 }  "
	rec := v.mint(t, testutil.User(owner), sent)
	if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("mint: %d %s, Cache-Control %q", rec.Code, rec.Body, rec.Header().Get("Cache-Control"))
	}
	r := v.stub.req
	if v.stub.calls != 1 || r.Method != http.MethodPost || r.URL.Path != "/api/v1/user/tokens" || string(v.stub.body) != sent ||
		r.Header.Get("Authorization") != "Bearer ak_key1_verbatim-SECRET" || r.Header.Get("Content-Type") != "application/json" ||
		r.ContentLength != int64(len(sent)) || r.Header.Get("Content-Length") != strconv.Itoa(len(sent)) {
		t.Errorf("inner request %s %s %v body %q", r.Method, r.URL.Path, r.Header, v.stub.body)
	}
	got := testutil.Object(t, rec)
	if got["token_id"] != "T1" || got["token"] != "ak_pat_T1_supersecret" || got["name"] != "svc" || got["workspace"] != slug ||
		got["expires_at"] != "2027-01-19T10:00:00Z" || got["created_at"] != "2026-10-21T10:00:00Z" || len(got["permissions"].([]any)) != 1 {
		t.Errorf("body %v", got)
	}
	if v.bindingsOf(t, "T1") != 1 || v.revoked(t, "T1") {
		t.Error("the token is not bound and live")
	}
	evs := v.audit.events()
	if len(evs) != 1 || evs[0].EventType != audit.EventTokenBind || evs[0].ActorID != owner || evs[0].ActorType != "api_key" ||
		evs[0].ResourceType != "token" || evs[0].ResourceID != "T1" || evs[0].Action != "bind" || evs[0].Workspace != slug ||
		fmt.Sprint(evs[0].Metadata) != "map[expires_at:2027-01-19T10:00:00Z permissions:[workspaces:read] token_id:T1 workspace:s]" {
		t.Errorf("events %+v", evs)
	}
	if v.audit.ctxs[0].Done() != nil {
		t.Error("the event's context can be cancelled by the caller")
	}
	// Without an emitter the mint succeeds and emits nothing.
	e, api := testutil.API()
	RegisterRoutes(api, Config{DB: v.db, APIKit: v.stub})
	v.e, v.stub.tokenID, v.stub.answer = e, "T2", strings.ReplaceAll(okAnswer, "T1", "T2")
	if rec := v.mint(t, testutil.User(owner), ""); rec.Code != http.StatusCreated || v.bindingsOf(t, "T2") != 1 || len(v.audit.events()) != 1 {
		t.Errorf("without an emitter: %d %s, %d events", rec.Code, rec.Body, len(v.audit.events()))
	}
}

// Any answer of apikit but a 201 reaches the caller unchanged, with no binding
// and no event; an unreadable body is 400.
func TestMint_PassesRefusalsThrough(t *testing.T) {
	v := newEnv(t)
	v.stub.status, v.stub.answer = http.StatusForbidden, `{"error":{"code":403,"message":"PAT requires tokens:manage scope"}}`
	rec := v.mint(t, testutil.PAT(owner, "workspaces:read"), "")
	if rec.Code != http.StatusForbidden || rec.Body.String() != v.stub.answer || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("%d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if testutil.Count(t, v.db, `SELECT COUNT(*) FROM token_workspace_bindings`) != 0 || len(v.audit.events()) != 0 {
		t.Error("a refused mint left a binding or an event")
	}
}

// A minted token that cannot be bound is revoked before the answer and no
// event is emitted; no path logs the token or the Authorization header.
func TestMint_RevokesTokenItCannotBind(t *testing.T) {
	cases := []struct {
		name, answer string
		before       func(v *env)
		status       int
		message      string
		revoked      bool
	}{
		{"workspace vanished", okAnswer, func(v *env) { testutil.Exec(t, v.db, `DELETE FROM workspaces`) }, http.StatusNotFound, "workspace not found", true},
		{"insert fails", okAnswer, func(v *env) { testutil.Exec(t, v.db, `DROP TABLE token_workspace_bindings`) }, http.StatusInternalServerError, msgBindFailed, true},
		{"answer cut short", `{"token_id":"T1","name":"svc","tok`, nil, http.StatusInternalServerError, msgBindFailed, true},
		{"answer without token", `{"token_id":"T1","name":"svc"}`, nil, http.StatusInternalServerError, msgBindFailed, true},
		{"answer without id", `{"token":"ak_pat_T1_supersecret"}`, nil, http.StatusInternalServerError, msgBindFailed, false},
	}
	for _, c := range cases {
		v := newEnv(t)
		logs := captureLogs(t)
		v.stub.tokenID, v.stub.answer = "T1", c.answer
		if c.before != nil {
			v.stub.before = func() { c.before(v) }
		}
		wantRefusal(t, v.mint(t, testutil.User(owner), ""), c.status, c.message, "")
		if v.revoked(t, "T1") != c.revoked || v.bindingsOf(t, "T1") != 0 || len(v.audit.events()) != 0 {
			t.Errorf("%s: revoked %v (want %v), bindings %d, events %d", c.name, v.revoked(t, "T1"), c.revoked, v.bindingsOf(t, "T1"), len(v.audit.events()))
		}
		if l := logs.String(); !strings.Contains(l, "could not be bound") || strings.Contains(l, "supersecret") || strings.Contains(l, "verbatim-SECRET") {
			t.Errorf("%s: logs %q", c.name, l)
		}
	}
	// A failed revocation is logged and the answer stays.
	v := newEnv(t)
	logs := captureLogs(t)
	v.stub.tokenID = "T1"
	v.stub.before = func() { testutil.Exec(t, v.db, `DROP TABLE token_workspace_bindings; DROP TABLE pats`) }
	wantRefusal(t, v.mint(t, testutil.User(owner), ""), http.StatusInternalServerError, msgBindFailed, "")
	if !strings.Contains(logs.String(), "revoking it failed") {
		t.Errorf("logs %q", logs.String())
	}
}
