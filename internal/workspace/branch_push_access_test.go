package workspace

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// branchPushEnv is a test hub with the workspace routes, a workspace root in a
// temporary directory, an audit emitter that captures what the branch push
// emits, and a recorder of the calls of the shared push.
type branchPushEnv struct {
	*testEnv
	root   string
	events *mockAuditEmitter
	pushes *[]pushCall
}

func newBranchPushEnv(t *testing.T) *branchPushEnv {
	t.Helper()
	env := newTestEnv(t)
	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = t.TempDir()
	t.Cleanup(func() { defaultWorkspaceRoot = oldRoot })
	return &branchPushEnv{testEnv: env, root: defaultWorkspaceRoot, events: useBranchPushEmitter(t), pushes: recordPushes(t)}
}

// useBranchPushEmitter makes a capturing emitter the workspace package's audit
// emitter for the test.
func useBranchPushEmitter(t *testing.T) *mockAuditEmitter {
	t.Helper()
	mock := newMockAuditEmitter()
	old := defaultAuditEmitter
	defaultAuditEmitter = mock
	t.Cleanup(func() { defaultAuditEmitter = old })
	return mock
}

// seedReady inserts an active standard workspace whose clone is ready, owned by
// owner, and returns it.
func (e *branchPushEnv) seedReady(t *testing.T, slug, owner string) *Workspace {
	t.Helper()
	ws := &Workspace{Slug: slug, GitURL: "https://github.com/org/" + slug, OwnerID: owner, Status: "ready"}
	e.seedWorkspace(t, ws)
	return ws
}

// push sends POST /api/v1/workspaces/<slug>/branches/<name>/push; name is the
// segment as sent, already escaped.
func (e *branchPushEnv) push(t *testing.T, slug, name, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	return e.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/branches/"+name+"/push", body, auth)
}

// branchEvents are the hub.branch.push events emitted so far.
func (e *branchPushEnv) branchEvents() []audit.HubEvent {
	var out []audit.HubEvent
	for _, ev := range e.events.Events() {
		if ev.EventType == audit.EventBranchPush {
			out = append(out, ev)
		}
	}
	return out
}

// assertNothingHappened fails when a hub.branch.push event was emitted or the
// shared push was called.
func (e *branchPushEnv) assertNothingHappened(t *testing.T) {
	t.Helper()
	if evs := e.branchEvents(); len(evs) != 0 {
		t.Errorf("%d hub.branch.push events were emitted; want none: %+v", len(evs), evs)
	}
	if len(*e.pushes) != 0 {
		t.Errorf("the shared push was called %d times; want none: %+v", len(*e.pushes), *e.pushes)
	}
}

// assertRefusal checks the status, the message and the error_type of an error
// answer.
func assertRefusal(t *testing.T, rec *httptest.ResponseRecorder, code int, message, errorType string) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d; want %d: %s", rec.Code, code, rec.Body)
	}
	env := parseErrorEnvelope(t, rec)
	if env.Error.Message != message || env.Error.ErrorType != errorType {
		t.Errorf("error = %q (type %q); want %q (type %q)", env.Error.Message, env.Error.ErrorType, message, errorType)
	}
}

// holdLock takes the workspace lock of slug until the test ends or the
// returned function is called.
func holdLock(t *testing.T, slug string) func() {
	t.Helper()
	unlock, ok := wslock.TryLock(slug)
	if !ok {
		t.Fatalf("the workspace lock of %s is already held", slug)
	}
	released := false
	release := func() {
		if !released {
			released = true
			unlock()
		}
	}
	t.Cleanup(release)
	return release
}

// TS-38-3: An unescaped slash inside the branch name matches no route and
// sends nothing.
// Requirement: 38-REQ-1.3
func TestBranchPushUnescapedSlash_TS_38_3(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedReady(t, "ws-slash", "user-a")

	rec := env.push(t, "ws-slash", "fix/x", "", userAuth("user-a"))
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("fix/x: %d %s; want the router's 404", rec.Code, rec.Body)
	}
	env.assertNothingHappened(t)

	// The same name escaped is routed to the handler.
	rec = env.push(t, "ws-slash", "fix%2Fx", "", userAuth("user-a"))
	if rec.Code == http.StatusNotFound && !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("fix%%2Fx was not routed: %d %s", rec.Code, rec.Body)
	}
}

// TS-38-5: A malformed body, a non-object body or a non-boolean force is
// rejected with 400 before the lock.
// Requirement: 38-REQ-1.5
func TestBranchPushBadBody_TS_38_5(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedReady(t, "ws-body", "user-a")
	bodies := []string{`{bad`, `[1]`, `{"force":"yes"}`, `{"force":1}`, `null`, `{"force":null}`, `"x"`}
	for _, held := range []bool{false, true} {
		release := func() {}
		if held {
			release = holdLock(t, "ws-body")
		}
		for _, body := range bodies {
			rec := env.push(t, "ws-body", "feat", body, userAuth("user-a"))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("body %s (lock held %v): status %d; want 400: %s", body, held, rec.Code, rec.Body)
				continue
			}
			if msg := parseErrorEnvelope(t, rec).Error.Message; msg == "" {
				t.Errorf("body %s: the 400 has no message", body)
			}
		}
		release()
	}
	assertLockFree(t, "ws-body")
	env.assertNothingHappened(t)
}

// TS-38-9: A request with no credential is refused with 401.
// Requirement: 38-REQ-2.1
func TestBranchPushNoCredential_TS_38_9(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedReady(t, "ws-anon", "user-a")
	rec := env.push(t, "ws-anon", "feat", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401: %s", rec.Code, rec.Body)
	}
	env.assertNothingHappened(t)
}

// TS-38-10: A PAT bound to another workspace gets 404 workspace not found
// before the scope check.
// Requirement: 38-REQ-2.2
func TestBranchPushBoundElsewhere_TS_38_10(t *testing.T) {
	hub := newRealHub(t)
	events := useBranchPushEmitter(t)
	pushes := recordPushes(t)
	hub.seed(t, "ws-a", "u1", "pending")
	hub.seed(t, "ws-b", "u1", "pending")
	withScope, withScopeID := hub.mintPAT(t, "workspaces:write")
	withoutScope, withoutScopeID := hub.mintPAT(t, "workspaces:read")
	bindToken(t, hub.db.SqlDB, withScopeID, "ws-a")
	bindToken(t, hub.db.SqlDB, withoutScopeID, "ws-a")

	for _, pat := range []string{withScope, withoutScope} {
		rec := hub.do(http.MethodPost, "/api/v1/workspaces/ws-b/branches/feat/push", pat, "")
		assertRefusal(t, rec, http.StatusNotFound, "workspace not found", "")
	}
	if n := len(events.Events()); n != 0 {
		t.Errorf("%d events were emitted; want none", n)
	}
	if len(*pushes) != 0 {
		t.Errorf("the shared push was called %d times; want none", len(*pushes))
	}
}

// TS-38-11: An unbound PAT without workspaces:write gets 403 with the scope
// message and no error_type, even for an unknown slug.
// Requirement: 38-REQ-2.3
func TestBranchPushMissingScope_TS_38_11(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedReady(t, "ws-scope", "user-a")
	for _, slug := range []string{"ws-scope", "nope"} {
		rec := env.push(t, slug, "feat", "", patAuth("user-a", "workspaces:read"))
		assertRefusal(t, rec, http.StatusForbidden, "PAT requires workspaces:write scope to push branches", "")
	}
	// No lookup comes first: with the database gone the answer is the same.
	env.db.Close()
	rec := env.push(t, "ws-scope", "feat", "", patAuth("user-a", "workspaces:read", "patches:write"))
	assertRefusal(t, rec, http.StatusForbidden, "PAT requires workspaces:write scope to push branches", "")
	env.assertNothingHappened(t)
}

// TS-38-12: An unknown slug or a stranger gets 404 workspace not found.
// Requirement: 38-REQ-2.4
func TestBranchPushStrangerAndUnknown_TS_38_12(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedReady(t, "ws-own", "user-a")
	assertRefusal(t, env.push(t, "ws-own", "feat", "", userAuth("user-b")), http.StatusNotFound, "workspace not found", "")
	assertRefusal(t, env.push(t, "ws-own", "feat", "", patAuth("user-b", "workspaces:write")), http.StatusNotFound, "workspace not found", "")
	assertRefusal(t, env.push(t, "nope", "feat", "", userAuth("user-a")), http.StatusNotFound, "workspace not found", "")
	env.assertNothingHappened(t)
}

// TS-38-13: An org member who is not the owner gets 403 forbidden.
// Requirement: 38-REQ-2.5
func TestBranchPushOrgMember_TS_38_13(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedOrg(t, "org-push", "Push Org", "push-org")
	env.seedOrgMember(t, "org-push", "user-a")
	env.seedOrgMember(t, "org-push", "user-m")
	org := "org-push"
	env.seedWorkspace(t, &Workspace{Slug: "ws-org", GitURL: "https://github.com/org/ws-org", OwnerID: "user-a", OrgID: &org, Status: "ready"})

	for _, auth := range []*apikit.AuthInfo{userAuth("user-m"), patAuth("user-m", "workspaces:write")} {
		rec := env.push(t, "ws-org", "feat", "", auth)
		assertRefusal(t, rec, http.StatusForbidden, "org members have read-only access to this workspace", "forbidden")
	}
	env.assertNothingHappened(t)
}

// TS-38-14: A database failure during the workspace lookup answers 500 with
// no event.
// Requirement: 38-REQ-2.6
func TestBranchPushLookupDatabaseFailure_TS_38_14(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedReady(t, "ws-db", "user-a")
	env.db.Close()
	rec := env.push(t, "ws-db", "feat", "", userAuth("user-a"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500: %s", rec.Code, rec.Body)
	}
	env.assertNothingHappened(t)
}

// TS-38-16: A malformed branch name answers 404 branch_not_found naming the
// broken rule, whether or not the lock is held.
// Requirement: 38-REQ-3.1
func TestBranchPushInvalidName_TS_38_16(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedReady(t, "ws-name", "user-a")
	names := map[string]string{"-x": "-x", "a..b": "a..b", "x.lock": "x.lock", "a%20b": "a b", "fix%2F%2Fx": "fix//x"}
	for _, held := range []bool{false, true} {
		release := func() {}
		if held {
			release = holdLock(t, "ws-name")
		}
		for sent, name := range names {
			rec := env.push(t, "ws-name", sent, "", userAuth("user-a"))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%q (lock held %v): status %d; want 404: %s", name, held, rec.Code, rec.Body)
				continue
			}
			e := parseErrorEnvelope(t, rec)
			if e.Error.ErrorType != "branch_not_found" || !strings.HasPrefix(e.Error.Message, "branch_not_found: ") ||
				!strings.Contains(e.Error.Message, "is not a valid branch name: ref name must not") {
				t.Errorf("%q: error %q (type %q); want branch_not_found naming the broken rule", name, e.Error.Message, e.Error.ErrorType)
			}
			if name == "-x" && e.Error.Message != `branch_not_found: "-x" is not a valid branch name: ref name must not start with '-'` {
				t.Errorf("-x: message %q", e.Error.Message)
			}
		}
		release()
	}
	env.assertNothingHappened(t)
}

// TS-38-17: A workspace that is not ready (archived, or its clone not done)
// is refused with 400 before the lock.
// Requirement: 38-REQ-3.2, 38-REQ-3.3
func TestBranchPushWorkspaceNotUsable_TS_38_17(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedWorkspace(t, &Workspace{Slug: "ws-archived", GitURL: "https://github.com/org/a", OwnerID: "user-a", Status: "archived"})
	env.seedWorkspace(t, &Workspace{Slug: "ws-cloning", GitURL: "https://github.com/org/c", OwnerID: "user-a", Status: "cloning"})
	holdLock(t, "ws-archived")
	holdLock(t, "ws-cloning")

	assertRefusal(t, env.push(t, "ws-archived", "feat", "", userAuth("user-a")),
		http.StatusBadRequest, "workspace is not ready; push requires a ready workspace", "")
	assertRefusal(t, env.push(t, "ws-cloning", "feat", "", userAuth("user-a")),
		http.StatusBadRequest, "workspace is not ready; push requires a ready workspace", "")
	env.assertNothingHappened(t)
}
