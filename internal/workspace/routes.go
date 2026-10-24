package workspace

import (
	"database/sql"
	"fmt"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// WorkspacePermissions returns the Permission values that hub registers with
// apikit's MountHandlers for workspace operations.
func WorkspacePermissions() []apikit.Permission {
	return []apikit.Permission{
		{Resource: "workspaces", Action: "read"},
		{Resource: "workspaces", Action: "write"},
		{Resource: "patches", Action: "read"},
		{Resource: "patches", Action: "write"},
	}
}

// recordedAPIKitRoutes are the routes apikit's MountHandlers added in the
// last MountWorkspaceHandlers call: the routes the apikit route guard covers.
var recordedAPIKitRoutes []*echo.Route

// MountWorkspaceHandlers initialises the workspace schema, registers workspace
// permission scopes with apikit (via MountHandlers), and mounts workspace API
// handlers on the server's API group.
//
// This is the single entry point for wiring up workspace support in af-hub.
// It calls s.MountHandlers to register both apikit's built-in handlers and
// workspace-specific permission scopes, then registers workspace routes.
//
// It also creates the token workspace bindings table and installs the two
// guards that confine a bound token: the apikit route guard on the
// root Echo instance for the routes MountHandlers added, and the binding
// guard on the API group, ahead of every hub route registered afterwards.
//
// Must be called after NewServer and before Start.
func MountWorkspaceHandlers(s *apikit.Server, db *apikit.DB, extraPerms ...apikit.Permission) error {
	// Initialise the workspaces table schema, then the token workspace
	// bindings, before anything is registered.
	if err := InitSchema(db.SqlDB); err != nil {
		return fmt.Errorf("workspace schema init: %w", err)
	}
	if err := wsaccess.InitBindingSchema(db.SqlDB); err != nil {
		return fmt.Errorf("token binding schema init: %w", err)
	}

	// Capture the external URL for hub_url construction in workspace responses.
	defaultExternalURL = s.ExternalURL()

	// Register workspace permissions (plus any extra module scopes) and
	// mount all built-in handlers.
	perms := WorkspacePermissions()
	perms = append(perms, extraPerms...)
	existing := make(map[string]bool)
	for _, r := range s.Echo().Routes() {
		existing[r.Method+" "+r.Path] = true
	}
	if err := s.MountHandlers(db, perms...); err != nil {
		return fmt.Errorf("mount handlers: %w", err)
	}

	// Confine tokens bound to a workspace. apikit's routes exist
	// now and never see group middleware added later, so the routes
	// MountHandlers added are guarded on the root instance, before apikit
	// authenticates; every hub route registered on the API group from here
	// on is covered by the binding guard.
	recordedAPIKitRoutes = nil
	for _, r := range s.Echo().Routes() {
		switch r.Path {
		case "/healthz", "/readyz", "/version":
			continue
		}
		if !existing[r.Method+" "+r.Path] {
			recordedAPIKitRoutes = append(recordedAPIKitRoutes, r)
		}
	}
	s.Echo().Use(wsaccess.APIKitRouteGuard(db.SqlDB, recordedAPIKitRoutes))
	api := s.APIGroup()
	api.Use(wsaccess.BindingGuard(db.SqlDB))

	// Register workspace API routes on the server's API group.
	return RegisterRoutes(api, db.SqlDB)
}

// RegisterRoutes mounts workspace API handlers on the given echo group.
// This is also used by tests which set up their own echo instance and
// auth middleware instead of using the full apikit server stack.
func RegisterRoutes(api *echo.Group, db *sql.DB) error {
	api.POST("/workspaces", handleCreateWorkspace(db))
	// A bound token may list workspaces: the handler confines the list to
	// its own.
	wsaccess.AllowBoundTokens(api.GET("/workspaces", handleListWorkspaces(db)))
	api.GET("/workspaces/:slug", handleGetWorkspace(db))
	api.PATCH("/workspaces/:slug", handleUpdateWorkspace(db))
	api.POST("/workspaces/:slug/archive", handleArchiveWorkspace(db))
	api.POST("/workspaces/:slug/reactivate", handleReactivateWorkspace(db))
	api.POST("/workspaces/:slug/sync", handleSyncWorkspace(db))
	api.POST("/workspaces/:slug/reclone", handleRecloneWorkspace(db))
	api.DELETE("/workspaces/:slug", handleDeleteWorkspace(db))
	// Push one branch of the trunk to origin. Workspace-scoped, so
	// a bound token reaches it on its own slug only.
	api.POST("/workspaces/:slug/branches/:name/push", handleBranchPush(db))

	return nil
}
