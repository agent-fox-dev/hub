package workspace

import (
	"database/sql"
	"sync"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/carrypatch"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/cphost"
)

// The patch routes belong to the carry-patch module, but several rules of the
// hub (the access of org members, bound tokens, ownership) are tested through
// them. mountPatchRoutes mounts the module's patch routes over the hub's locks
// and access rule the way cmd/af-hub does.

var bindCarryPatchOnce sync.Once

func mountPatchRoutes(api *echo.Group, db *sql.DB) {
	mountPatchRoutesWithAudit(api, db, nil)
}

// mountPatchRoutesWithAudit is mountPatchRoutes with the hub's audit emitter
// behind the module's, through cphost.Emitter as in cmd/af-hub.
func mountPatchRoutesWithAudit(api *echo.Group, db *sql.DB, emitter audit.Emitter) {
	bindCarryPatchOnce.Do(cphost.Bind)
	carrypatch.RegisterPatchRoutes(api, carrypatch.APIConfig{
		DB: db, PatchStore: carrypatch.NewSQLPatchStore(db), Audit: cphost.Emitter(emitter),
	})
}
