// Package boundtoken issues, lists and retires personal access tokens bound
// to one workspace (spec 34). A bound token is minted by apikit's own PAT
// route, served in-process, and hub adds only the binding (spec 33's
// token_workspace_bindings row); the SQL on that table lives in
// internal/wsaccess.
package boundtoken

import (
	"database/sql"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// Config holds what the bound-token routes need.
type Config struct {
	// DB is hub's SQLite database.
	DB *sql.DB
	// APIKit serves apikit's own routes; server.Echo() in production.
	APIKit http.Handler
	// Audit receives hub.token.bind events; nil skips them.
	Audit audit.Emitter
}

// RegisterRoutes mounts POST and GET /workspaces/:slug/tokens on the API
// group, behind
// apikit's authentication and spec 33's binding guard.
func RegisterRoutes(api *echo.Group, cfg Config) {
	api.POST("/workspaces/:slug/tokens", handleMint(cfg))
	api.GET("/workspaces/:slug/tokens", handleList(cfg))
}

// refuseBoundToken answers the caller's PAT with 403 token_bound when it is
// bound to a workspace, and reports the refusal; a failed lookup answers 500.
// It returns true when it has answered. Bound tokens may neither mint nor
// list bound tokens, whatever workspace the path names (spec 34, Decision 5).
func refuseBoundToken(c echo.Context, db *sql.DB, auth *apikit.AuthInfo) bool {
	boundSlug, bound, err := wsaccess.BoundSlug(db, auth)
	if err != nil {
		_ = apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		return true
	}
	if !bound {
		return false
	}
	wsaccess.ReportBoundRefusal(wsaccess.BoundRefusal{
		TokenID:       auth.TokenID,
		UserID:        auth.UserID,
		BoundSlug:     boundSlug,
		RequestedSlug: c.Param("slug"),
		Method:        c.Request().Method,
		Route:         c.Path(),
		Status:        http.StatusForbidden,
		Surface:       wsaccess.SurfaceAPI,
	})
	_ = apikit.WriteAPIErrorWithType(c, http.StatusForbidden,
		"this token is bound to workspace "+boundSlug, wsaccess.ErrorTypeTokenBound)
	return true
}
