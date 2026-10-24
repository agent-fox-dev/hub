package secrets

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// The team fixture: an org with an owner and a member, a workspace of the
// owner in the org, and a stranger who belongs to neither.
const (
	owner    = "owner"
	member   = "member"
	stranger = "stranger"
	orgID    = "org-acme"
	orgSlug  = "acme"
	wsSlug   = "team-ws"
)

// workspacesDDL is the part of the workspaces table that the workspace access
// rule reads and testutil.SeedWorkspace writes; this package cannot import
// workspace.
const workspacesDDL = `CREATE TABLE workspaces (
	slug TEXT PRIMARY KEY, git_url TEXT NOT NULL, branch TEXT, owner_id TEXT NOT NULL, org_id TEXT,
	status TEXT NOT NULL, clone_status TEXT NOT NULL DEFAULT 'ready', head_sha TEXT,
	created_at TEXT NOT NULL, updated_at TEXT NOT NULL, workspace_mode TEXT NOT NULL,
	upstream_url TEXT, integration_branch TEXT, settings TEXT NOT NULL DEFAULT '{}')`

// recorder is an audit emitter that keeps the events it is given and fails
// with err when set.
type recorder struct {
	mu     sync.Mutex
	events []audit.HubEvent
	err    error
}

func (r *recorder) Emit(_ context.Context, event audit.HubEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return r.err
}

func (r *recorder) all() []audit.HubEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.HubEvent(nil), r.events...)
}

// env is the secrets and variables API mounted behind the test auth
// middleware, on a database with apikit's schema, the workspaces table, the
// token bindings and this package's tables.
type env struct {
	e     *echo.Echo
	db    *sql.DB
	store *Store
	audit *recorder
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := testutil.OpenDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := wsaccess.InitBindingSchema(db); err != nil {
		t.Fatalf("InitBindingSchema: %v", err)
	}
	testutil.Exec(t, db, workspacesDDL)
	e, api := testutil.API()
	rec := &recorder{}
	if err := RegisterRoutesWithAudit(api, SecretsRouteConfig{DB: db, Audit: rec}); err != nil {
		t.Fatalf("RegisterRoutesWithAudit: %v", err)
	}
	return &env{e: e, db: db, store: NewStore(db), audit: rec}
}

// seedTeam seeds the team fixture.
func (v *env) seedTeam(t *testing.T) {
	t.Helper()
	for _, u := range []string{owner, member, stranger} {
		testutil.SeedUser(t, v.db, u)
	}
	testutil.SeedOrg(t, v.db, orgID, orgSlug, owner)
	testutil.SeedOrgMember(t, v.db, orgID, owner)
	testutil.SeedOrgMember(t, v.db, orgID, member)
	testutil.SeedWorkspace(t, v.db, testutil.Workspace{Slug: wsSlug, OwnerID: owner, OrgID: orgID})
}

// seed inserts one row in table ("secrets" or "variables") as the store
// would, with the value encoded.
func (v *env) seed(t *testing.T, table, ownerType, ownerID, key, value string) {
	t.Helper()
	now := apikit.NowUTC()
	testutil.Exec(t, v.db, `INSERT INTO `+table+` (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		ownerType, ownerID, key, EncodeValue(value), now, now)
}

func (v *env) do(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Request(t, v.e, method, path, body, auth)
}

// want fails the test unless rec has the status; it returns rec.
func want(t *testing.T, rec *httptest.ResponseRecorder, status int) *httptest.ResponseRecorder {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	return rec
}

// wantError fails the test unless rec is the error status with the message.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	if e := testutil.Error(t, want(t, rec, status)); e.Message != message {
		t.Fatalf("message %q, want %q", e.Message, message)
	}
}

// route is one request of the API; routes lists the four operations of a
// resource under a tier prefix, with a body that creates NEW_KEY and
// addresses K.
type route struct{ method, path, body string }

func routes(prefix, resource string) []route {
	base := prefix + "/" + resource
	return []route{
		{http.MethodPost, base, `{"entries":[{"key":"NEW_KEY","value":"v"}]}`},
		{http.MethodGet, base, ""},
		{http.MethodPatch, base + "/K", `{"value":"v2"}`},
		{http.MethodDelete, base + "/K", ""},
	}
}

// okStatus is the success status of a route's operation.
func okStatus(method string) int {
	switch method {
	case http.MethodPost:
		return http.StatusCreated
	case http.MethodDelete:
		return http.StatusNoContent
	}
	return http.StatusOK
}
