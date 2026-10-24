package boundtoken

import (
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/carrypatch"

	"github.com/agent-fox-dev/hub/internal/cphost"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// On the real hub (apikit's server and credentials, the workspace handlers
// with both binding guards, the carry-patch routes and the bound-token
// routes minting through apikit), the owner mints a token bound to a
// workspace; that token reaches its workspace's routes and nothing else, and
// neither a member of the org nor a stranger sees the workspace's tokens.
func TestHub_BoundTokenReachesOnlyItsWorkspace(t *testing.T) {
	server, database := testutil.Server(t)
	if err := workspace.MountWorkspaceHandlers(server, database, carrypatch.CarryPatchPermissions()...); err != nil {
		t.Fatal(err)
	}
	db := database.SqlDB
	cphost.Bind()
	carrypatch.RegisterPatchRoutes(server.APIGroup(), carrypatch.APIConfig{DB: db, PatchStore: carrypatch.NewSQLPatchStore(db)})
	RegisterRoutes(server.APIGroup(), Config{DB: db, APIKit: server.Echo()})
	e := server.Echo()
	keys := map[string]string{}
	for _, u := range []string{owner, member, stranger} {
		keys[u] = testutil.APIKey(t, db, testutil.SeedUser(t, db, u))
	}
	testutil.SeedOrg(t, db, orgID, "acme", owner)
	testutil.SeedOrgMember(t, db, orgID, owner)
	testutil.SeedOrgMember(t, db, orgID, member)
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: "S", OwnerID: owner, OrgID: orgID, Status: "active"})
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: "X", OwnerID: owner, Status: "active"})

	rec := testutil.Bearer(e, http.MethodPost, "/api/v1/workspaces/S/tokens", `{"name":"svc","permissions":["workspaces:read","patches:read","tokens:read"]}`, keys[owner])
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body)
	}
	minted := testutil.Object(t, rec)
	token, tokenID := minted["token"].(string), minted["token_id"].(string)
	if testutil.Count(t, db, `SELECT COUNT(*) FROM token_workspace_bindings WHERE token_id = ? AND slug = 'S'`, tokenID) != 1 {
		t.Fatal("the minted token is not bound to S")
	}
	for _, c := range []struct {
		path, credential string
		status           int
		errorType        string
	}{
		{"/api/v1/workspaces/S/patches", token, http.StatusOK, ""},
		{"/api/v1/workspaces/S", token, http.StatusOK, ""},
		{"/api/v1/workspaces/X/patches", token, http.StatusNotFound, ""},
		{"/api/v1/workspaces/S/tokens", token, http.StatusForbidden, "token_bound"},
		{"/api/v1/user", token, http.StatusForbidden, "token_bound"},
		{"/api/v1/workspaces/S/tokens", keys[owner], http.StatusOK, ""},
		{"/api/v1/workspaces/S/tokens", keys[member], http.StatusNotFound, ""},
		{"/api/v1/workspaces/S/tokens", keys[stranger], http.StatusNotFound, ""},
	} {
		rec := testutil.Bearer(e, http.MethodGet, c.path, "", c.credential)
		if rec.Code != c.status || (c.errorType != "" && testutil.Error(t, rec).ErrorType != c.errorType) {
			t.Errorf("GET %s as %s: %d %s; want %d %s", c.path, c.credential[:6], rec.Code, rec.Body, c.status, c.errorType)
		}
		if c.status != http.StatusOK && strings.Contains(rec.Body.String(), tokenID) {
			t.Errorf("GET %s: the refusal names the token", c.path)
		}
	}
}
