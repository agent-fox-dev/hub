package forge

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// productionWorkspacesTable is the DDL of the workspaces table of internal/workspace
// (schema.go), so that the smoke tests read the columns the hub has.
const productionWorkspacesTable = `
CREATE TABLE IF NOT EXISTS workspaces (
	slug              TEXT PRIMARY KEY,
	git_url           TEXT NOT NULL,
	branch            TEXT,
	owner_id          TEXT NOT NULL,
	org_id            TEXT,
	status            TEXT NOT NULL DEFAULT 'active',
	display_name      TEXT NOT NULL DEFAULT '',
	description       TEXT NOT NULL DEFAULT '',
	clone_status      TEXT NOT NULL DEFAULT 'pending' CHECK(clone_status IN ('pending','cloning','ready','failed','archived')),
	head_sha          TEXT,
	clone_error       TEXT,
	created_at        TEXT NOT NULL,
	updated_at        TEXT NOT NULL,
	sync_mode         TEXT NOT NULL DEFAULT 'pull_only',
	sync_status       TEXT NOT NULL DEFAULT 'idle',
	upstream_head_sha TEXT,
	last_sync_at      TEXT,
	sync_error        TEXT,
	workspace_mode    TEXT NOT NULL DEFAULT 'standard',
	upstream_url      TEXT,
	integration_branch TEXT
)`

// smokeDB is a database with the production workspaces table and the real secrets
// store on it.
func smokeDB(t *testing.T) (*sql.DB, *secrets.Store) {
	t.Helper()
	db := openTestDB(t)
	if _, err := db.Exec(productionWorkspacesTable); err != nil {
		t.Fatalf("create the workspaces table: %v", err)
	}
	if err := secrets.InitSchema(db); err != nil {
		t.Fatalf("create the secrets tables: %v", err)
	}
	return db, secrets.NewStore(db)
}

// TS-30-53: An API-key caller lists open triage issues and the forge sees only the
// workspace credential.
// Requirement: 30-PATH-1, 30-REQ-4.7, 30-REQ-5.2
func TestSmoke_APIKeyCallerListsOpenTriageIssues_TS_30_53(t *testing.T) {
	isolateForgeEnv(t)
	db, store := smokeDB(t)
	stub := newForgeStub(t)
	stub.setHandler(githubFixture)

	if _, err := db.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, created_at, updated_at)
		VALUES ('widgets', 'https://github.com/acme/widgets', 'user-1', 'active', 'ready', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSecrets("workspace", "widgets", []secrets.EntryInput{{Key: "GIT_PAT", Value: "smoke-pat-value"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateVariables("workspace", "widgets", []secrets.EntryInput{{Key: "FORGE_API_URL", Value: stub.URL}}); err != nil {
		t.Fatal(err)
	}

	// The router: the forge routes as main.go mounts them, behind a stand-in for apikit's
	// auth middleware that knows one API key.
	const callerKey = "caller-api-key-9f31"
	router := echo.New()
	router.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := router.Group("/api/v1")
	api.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Header.Get("Authorization") == "Bearer "+callerKey {
				apikit.SetAuthInfo(c, &apikit.AuthInfo{UserID: "user-1", CredentialType: "api_key"})
			}
			return next(c)
		}
	})
	RegisterRoutes(api, Config{
		DB:                db,
		ResolveOriginAuth: func(slug string) (transport.AuthMethod, error) { return workspace.ResolveCloneAuth(store, slug) },
		ResolveUpstreamAuth: func(slug string) (transport.AuthMethod, error) {
			return workspace.ResolveUpstreamAuthMethod(store, slug)
		},
		GetVariable: store.GetVariableValue,
		Transport:   stub.Client().Transport,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/widgets/issues?labels=af:triage&state=open", nil)
	req.Header.Set("Authorization", "Bearer "+callerKey)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
	}

	hubRequests := stub.received() // before the direct call below, which the stub records too

	// The body is the issues as issuex encodes them: a bare array, the pull request that
	// the issue list also holds left out.
	direct, err := issuex.NewGitHub(issuex.Options{BaseURL: stub.URL, Token: "smoke-pat-value", HTTPClient: stub.Client(),
		Repo: issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}})
	if err != nil {
		t.Fatal(err)
	}
	list, err := direct.ListIssues(context.Background(), issuex.Repo{Owner: "acme", Name: "widgets"}, issuex.IssueFilter{Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(list.Issues)
	if got := strings.TrimSpace(rec.Body.String()); got != string(want) || len(list.Issues) != 2 {
		t.Errorf("body\n%s\nwant\n%s", got, want)
	}

	// The forge saw the filter, the workspace's credential and hub's user agent, and
	// nothing of the caller. (The limit of 30 is applied by issuex after it has read the
	// page; it is not a parameter on the wire.)
	if len(hubRequests) == 0 {
		t.Fatal("the forge received no request")
	}
	listing := hubRequests[0]
	if listing.Path != "/repos/acme/widgets/issues" || !strings.Contains(listing.Query, "labels=af%3Atriage") || !strings.Contains(listing.Query, "state=open") {
		t.Errorf("the forge saw %s?%s; want the issues of acme/widgets labelled af:triage and open", listing.Path, listing.Query)
	}
	for _, r := range hubRequests {
		if bearer(r) != "smoke-pat-value" {
			t.Errorf("a request carried the token %q; want the workspace's GIT_PAT", bearer(r))
		}
		if ua := r.Header.Get("User-Agent"); ua != "af-hub/"+apikit.Version {
			t.Errorf("User-Agent = %q; want af-hub/%s", ua, apikit.Version)
		}
		if dump := strings.Join(r.Header.Values("Authorization"), ",") + r.Query + r.Header.Get("Cookie"); strings.Contains(dump, callerKey) {
			t.Errorf("a request carried the caller's key: %+v", r)
		}
	}
}
