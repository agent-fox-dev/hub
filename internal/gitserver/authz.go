package gitserver

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// errAccessDenied is a sentinel error returned by authorizeGitAccess when
// the request is denied. The HTTP response is already committed (pkt-line
// error written); this error signals the caller to halt the middleware chain.
var errAccessDenied = errors.New("git access denied")

// authorizeGitAccess decides whether the authenticated caller may use the
// workspace's repository for this request, by hub's shared workspace access
// rule (wsaccess.AuthorizeWorkspaceAt): the owner, admin tokens and members
// of the workspace's org may read (clone and fetch); only the owner and
// admin tokens may write (push), and a member's push is refused with 403.
// A PAT bound to another workspace is refused with 404 and the refusal is
// reported with the git site.
//
// Refusals are pkt-line errors: wsaccess's 404 as "repository not found"
// (anti-enumeration), its 403 with its message, anything else, a database
// error included, as a 500. Scope checks (git:read vs git:write) are
// handled separately by requireGitScope in the handler, which runs after
// this check.
//
// Returns nil if the request is authorized. On denial it writes a pkt-line
// error response and returns errAccessDenied to halt the middleware chain.
func authorizeGitAccess(c echo.Context, db *sql.DB, ws *workspaceInfo) error {
	info := apikit.GetAuthInfo(c)
	if info == nil {
		return echo.NewHTTPError(http.StatusUnauthorized)
	}

	_, status, msg := wsaccess.AuthorizeWorkspaceAt(db, info, ws.Slug, gitNeed(c), wsaccess.RefusalSite{
		Method: c.Request().Method, Route: c.Path(), Status: http.StatusNotFound, Surface: wsaccess.SurfaceGit,
	})
	switch status {
	case 0:
		return nil
	case http.StatusNotFound:
		_ = writePktLineError(c, http.StatusNotFound, "repository not found")
	case http.StatusForbidden:
		_ = writePktLineError(c, http.StatusForbidden, msg)
	default:
		_ = writePktLineError(c, http.StatusInternalServerError, "internal server error")
	}
	return errAccessDenied
}

// gitNeed is what the request asks of the caller's access: a push
// (git-receive-pack, or its ref discovery through info/refs) needs write,
// everything else, an info/refs request with a missing or invalid service
// included, needs read. The git server never asks for NeedPrivate.
func gitNeed(c echo.Context) wsaccess.Need {
	switch c.Path() {
	case "/git/:org/:slug.git/git-receive-pack":
		return wsaccess.NeedWrite
	case "/git/:org/:slug.git/info/refs":
		if c.QueryParam("service") == "git-receive-pack" {
			return wsaccess.NeedWrite
		}
	}
	return wsaccess.NeedRead
}
