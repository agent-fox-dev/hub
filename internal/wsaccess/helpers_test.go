package wsaccess

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// workspacesDDL is the part of the workspaces table the access rule reads and
// testutil.SeedWorkspace writes; this package cannot import workspace.
const workspacesDDL = `CREATE TABLE workspaces (
	slug TEXT PRIMARY KEY, git_url TEXT NOT NULL, branch TEXT, owner_id TEXT NOT NULL, org_id TEXT,
	status TEXT NOT NULL CHECK(status IN ('pending','cloning','ready','failed','archived')), head_sha TEXT,
	created_at TEXT NOT NULL, updated_at TEXT NOT NULL, workspace_mode TEXT NOT NULL,
	upstream_url TEXT, integration_branch TEXT, settings TEXT NOT NULL DEFAULT '{}')`

// newDB is apikit's schema plus the workspaces table and the binding table.
func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.OpenDB(t)
	testutil.Exec(t, db, workspacesDDL)
	if err := InitBindingSchema(db); err != nil {
		t.Fatalf("InitBindingSchema: %v", err)
	}
	return db
}

// The team fixture: the org acme with the users owner and member, the
// workspace team-ws of owner in it, and a stranger in no org.
const (
	owner    = "owner"
	member   = "member"
	stranger = "stranger"
	orgID    = "org-acme"
	wsSlug   = "team-ws"
)

func seedTeam(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, u := range []string{owner, member, stranger} {
		testutil.SeedUser(t, db, u)
	}
	testutil.SeedOrg(t, db, orgID, "acme", owner)
	testutil.SeedOrgMember(t, db, orgID, owner)
	testutil.SeedOrgMember(t, db, orgID, member)
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: wsSlug, OwnerID: owner, OrgID: orgID})
}

// seedPAT inserts a pats row of userID (an existing user) with permissions
// (a JSON array) and revokedAt ("" for live), and returns tokenID.
func seedPAT(t *testing.T, db *sql.DB, tokenID, userID, permissions, revokedAt string) string {
	t.Helper()
	var revoked any
	if revokedAt != "" {
		revoked = revokedAt
	}
	testutil.Exec(t, db, `INSERT INTO pats (token_id, user_id, name, secret_hash, permissions, expires_days, revoked_at, created_at)
		VALUES (?, ?, ?, 'hash', ?, 0, ?, ?)`, tokenID, userID, "pat-"+tokenID, permissions, revoked, apikit.NowUTC())
	return tokenID
}

// bindings lists the binding table as token_id=slug pairs.
func bindings(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT token_id, slug FROM token_workspace_bindings ORDER BY token_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, slug string
		if err := rows.Scan(&id, &slug); err != nil {
			t.Fatal(err)
		}
		out = append(out, id+"="+slug)
	}
	return out
}

// revokedAt returns the revoked_at of a pats row, "" while live.
func revokedAt(t *testing.T, db *sql.DB, tokenID string) string {
	t.Helper()
	var at sql.NullString
	if err := db.QueryRow(`SELECT revoked_at FROM pats WHERE token_id = ?`, tokenID).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at.String
}

// captureRefusals registers a refusal observer for the test and returns the
// refusals it receives.
func captureRefusals(t *testing.T) *[]BoundRefusal {
	t.Helper()
	var got []BoundRefusal
	SetRefusalObserver(func(r BoundRefusal) { got = append(got, r) })
	t.Cleanup(func() { SetRefusalObserver(nil) })
	return &got
}

// resetAllowlist empties the bound-token allowlist for the test.
func resetAllowlist(t *testing.T) {
	t.Helper()
	clear := func() {
		boundTokenAllowlist.Lock()
		boundTokenAllowlist.routes = make(map[string]bool)
		boundTokenAllowlist.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// probe is a handler that counts its calls and keeps the credential it saw.
type probe struct {
	calls int
	auth  *apikit.AuthInfo
}

func (p *probe) handle(c echo.Context) error {
	p.calls++
	p.auth = apikit.GetAuthInfo(c)
	return c.String(http.StatusOK, "probe")
}

// guardedAPI is an Echo instance whose /api/v1 group runs the test auth
// middleware and then the binding guard, before any route exists.
func guardedAPI(db *sql.DB) (*echo.Echo, *echo.Group) {
	e, api := testutil.API()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api.Use(BindingGuard(db))
	return e, api
}

// serve sends one request with a credential (nil for none) and an
// Authorization header ("" for none).
func serve(e *echo.Echo, method, path string, auth *apikit.AuthInfo, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if auth != nil {
		raw, _ := json.Marshal(auth)
		req.Header.Set(testutil.AuthHeader, string(raw))
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// patHeader is the Bearer header of a PAT with the given id, in the prefix
// apikit was built with.
func patHeader(tokenID string) string {
	return "Bearer " + apikit.TokenPrefix + "_pat_" + tokenID + "_secret"
}

// wantRefusal fails the test unless rec is the apikit error envelope with the
// status, message and error_type ("" for none).
func wantRefusal(t *testing.T, rec *httptest.ResponseRecorder, status int, message, errorType string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	if e := testutil.Error(t, rec); e.Message != message || e.ErrorType != errorType || e.Code != status {
		t.Fatalf("error %+v, want %d %q error_type %q", e, status, message, errorType)
	}
}
