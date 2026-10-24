package forge

import (
	"net/http"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// On a real apikit server with real credentials, the forge scopes are registered so that a
// PAT is minted with them, apikit's auth middleware feeds the routes, a PAT without the
// scope is refused and one bound to another workspace does not see this one.
func TestHub_RealCredentialsAndScopes(t *testing.T) {
	server, database := testutil.Server(t)
	if err := workspace.MountWorkspaceHandlers(server, database, append(secrets.Permissions(), Permissions()...)...); err != nil {
		t.Fatal(err)
	}
	db := database.SqlDB
	if err := secrets.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	stub := newForgeStub(t)
	stub.setHandler(respond(http.StatusOK, repositoryBody))
	RegisterRoutes(server.APIGroup(), Config{
		DB: db,
		ResolveOriginAuth: func(string) (transport.AuthMethod, error) {
			return &githttp.BasicAuth{Username: patUsername, Password: "ws-token"}, nil
		},
		ResolveUpstreamAuth: func(string) (transport.AuthMethod, error) { return nil, nil },
		GetVariable:         func(_, _, key string) (string, error) { return stub.URL, nil },
		Transport:           stub.Client().Transport,
		Secrets:             secrets.NewStore(db),
	})
	testutil.SeedUser(t, db, "u1")
	apiKey := testutil.APIKey(t, db, "u1")
	e := server.Echo()
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: "widgets", OwnerID: "u1", Status: "active"})
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: "other", OwnerID: "u1", Status: "active"})
	reader, _ := testutil.MintPAT(t, e, apiKey, "forge:read")
	writer, _ := testutil.MintPAT(t, e, apiKey, "forge:write")
	none, _ := testutil.MintPAT(t, e, apiKey, "workspaces:read")
	bound, boundID := testutil.MintPAT(t, e, apiKey, "forge:read", "forge:write")
	testutil.BindToken(t, db, boundID, "other")

	for _, tc := range []struct {
		name, method, path, token string
		status                    int
	}{
		{"no credential", http.MethodGet, "/workspaces/widgets/forge/repository", "", 401},
		{"a bad credential", http.MethodGet, "/workspaces/widgets/forge/repository", "not-a-token", 401},
		{"an api key reads", http.MethodGet, "/workspaces/widgets/forge/repository", apiKey, 200},
		{"a forge:read PAT reads", http.MethodGet, "/workspaces/widgets/forge", reader, 200},
		{"a forge:read PAT may not write", http.MethodPost, "/workspaces/widgets/labels", reader, 403},
		{"a forge:write PAT reads", http.MethodGet, "/workspaces/widgets/forge/deliveries", writer, 200},
		{"a PAT without the scopes", http.MethodGet, "/workspaces/widgets/forge/repository", none, 403},
		{"a PAT bound to another workspace", http.MethodGet, "/workspaces/widgets/forge/repository", bound, 404},
		{"a PAT bound to this workspace", http.MethodGet, "/workspaces/other/forge/repository", bound, 200},
	} {
		if rec := testutil.Bearer(e, tc.method, "/api/v1"+tc.path, "", tc.token); rec.Code != tc.status {
			t.Errorf("%s: status = %d; want %d\n%s", tc.name, rec.Code, tc.status, rec.Body)
		}
	}
	for _, r := range stub.received() {
		if bearer(r) != "ws-token" {
			t.Errorf("the forge saw the token %q; want the workspace's", bearer(r))
		}
	}
}
