package secrets

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// RegisterRoutes mounts secrets API handlers on the given echo group.
// This is also used by tests which set up their own echo instance and
// auth middleware instead of using the full apikit server stack.
func RegisterRoutes(api *echo.Group, db *sql.DB) error {
	store := NewStore(db)

	api.POST("/user/secrets", handleCreateUserSecrets(store))
	api.GET("/user/secrets", handleListUserSecrets(store))
	api.PATCH("/user/secrets/:key", handleUpdateUserSecret(store))
	api.DELETE("/user/secrets/:key", handleDeleteUserSecret(store))

	api.POST("/orgs/:slug/secrets", handleCreateOrgSecrets(store, db))
	api.GET("/orgs/:slug/secrets", handleListOrgSecrets(store, db))
	api.PATCH("/orgs/:slug/secrets/:key", handleUpdateOrgSecret(store, db))
	api.DELETE("/orgs/:slug/secrets/:key", handleDeleteOrgSecret(store, db))

	api.POST("/workspaces/:slug/secrets", handleCreateWorkspaceSecrets(store, db))
	api.GET("/workspaces/:slug/secrets", handleListWorkspaceSecrets(store, db))
	api.PATCH("/workspaces/:slug/secrets/:key", handleUpdateWorkspaceSecret(store, db))
	api.DELETE("/workspaces/:slug/secrets/:key", handleDeleteWorkspaceSecret(store, db))

	return nil
}
