// Package gitserver implements a git smart HTTP server for af-hub,
// exposing workspace repositories over HTTP at /git/<org-slug>/<workspace-slug>.git/
// on the same port as the REST API.
package gitserver

import (
	"database/sql"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// GitPermissions returns the Permission values that the git server registers
// with apikit's permission registry for PAT issuance and validation.
func GitPermissions() []apikit.Permission {
	// TODO: implement — return git:read and git:write permissions.
	return nil
}

// MountGitHandlers registers git smart HTTP routes and the git auth middleware
// on the Echo instance. It also registers git:read and git:write permission
// scopes with apikit.
//
// Routes registered:
//   - GET  /git/:org/:slug.git/info/refs
//   - POST /git/:org/:slug.git/git-upload-pack
//   - POST /git/:org/:slug.git/git-receive-pack
//
// Must be called after NewServer and before Start.
func MountGitHandlers(e *echo.Echo, db *sql.DB) error {
	// TODO: implement — register git routes and auth middleware.
	return nil
}

// GitAuthMiddleware returns an echo middleware that performs HTTP Basic
// authentication for git clients. It extracts the credential from the Basic
// auth password field (ignoring the username), resolves it to a user or admin
// identity, and attaches the identity to the request context.
//
// If no Authorization header is present, it returns HTTP 401 with
// WWW-Authenticate: Basic realm="af-hub".
//
// If the credential is unrecognized, it returns HTTP 401 without a
// WWW-Authenticate header.
func GitAuthMiddleware(db *sql.DB) echo.MiddlewareFunc {
	// TODO: implement — HTTP Basic auth credential resolution.
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			return next(c)
		}
	}
}
