package testutil

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// The database has apikit's schema, the seeds land, and a credential round
// trips through the test middleware.
func TestDatabaseSeedsAndMiddleware(t *testing.T) {
	db := OpenDB(t)
	SeedUser(t, db, "u1")
	SeedPersonalOrg(t, db, "u1")
	SeedOrg(t, db, "org-1", "org-one", "")
	SeedOrgMember(t, db, "org-1", "u1")
	if n := Count(t, db, `SELECT COUNT(*) FROM org_members WHERE user_id = 'u1'`); n != 1 {
		t.Fatalf("org members of u1 = %d; want 1", n)
	}

	e, api := API()
	api.GET("/whoami", func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return c.JSON(http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": 401, "message": "authentication required"}})
		}
		return c.JSON(http.StatusOK, map[string]any{"user": auth.UserID, "type": auth.CredentialType, "scopes": auth.Permissions})
	})

	rec := Request(t, e, http.MethodGet, "/api/v1/whoami", "", nil)
	if rec.Code != http.StatusUnauthorized || Error(t, rec).Message != "authentication required" {
		t.Fatalf("unauthenticated: %d %s", rec.Code, rec.Body)
	}
	rec = Request(t, e, http.MethodGet, "/api/v1/whoami", "", PAT("u1", "workspaces:read"))
	got := Object(t, rec)
	if rec.Code != http.StatusOK || got["user"] != "u1" || got["type"] != "pat" {
		t.Fatalf("pat: %d %v", rec.Code, got)
	}
}

// A real server authenticates the API key and the PATs it mints.
func TestServerMintsPATs(t *testing.T) {
	server, database := Server(t)
	if err := server.MountHandlers(database, apikit.Permission{Resource: "workspaces", Action: "read"}); err != nil {
		t.Fatal(err)
	}
	SeedUser(t, database.SqlDB, "u1")
	key := APIKey(t, database.SqlDB, "u1")
	token, id := MintPAT(t, server.Echo(), key, "workspaces:read")
	if token == "" || id == "" {
		t.Fatal("empty token or id")
	}
	rec := Bearer(server.Echo(), http.MethodGet, "/api/v1/user/tokens", "", key)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing tokens: %d %s", rec.Code, rec.Body)
	}
}
