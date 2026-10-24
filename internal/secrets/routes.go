package secrets

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// RegisterRoutes mounts the secrets and variables routes on the API group:
// create, list, update and delete of each resource at each owner tier, and
// the resolved variables of a workspace. Tests mount them on their own echo
// instance behind their own auth middleware.
func RegisterRoutes(api *echo.Group, db *sql.DB) error {
	store := NewStore(db)
	for _, t := range []tier{userTier, orgTier, workspaceTier} {
		for _, r := range []resource{secretsResource, varsResource} {
			base := t.path + "/" + r.name
			api.POST(base, handle(store, db, t, r, opCreate))
			api.GET(base, handle(store, db, t, r, opList))
			if t.ownerType == "workspace" && r.name == "vars" {
				// Before the :key routes, so that "resolved" is not a key.
				api.GET(base+"/resolved", handleResolvedWorkspaceVars(store, db))
			}
			api.PATCH(base+"/:key", handle(store, db, t, r, opUpdate))
			api.DELETE(base+"/:key", handle(store, db, t, r, opDelete))
		}
	}
	return nil
}
