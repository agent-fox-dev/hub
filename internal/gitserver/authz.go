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

// authorizeGitAccess checks whether the authenticated user is authorized to
// access the given workspace. This implements the ownership check portion of
// the access control matrix (06-REQ-5).
//
// Authorization rules:
//   - Admin tokens (CredentialType "admin_token") bypass ownership checks
//     and may access any workspace (06-REQ-5.1).
//   - API keys and PATs must belong to the workspace owner. Non-owner,
//     non-admin identities receive HTTP 404 to prevent workspace enumeration
//     (06-REQ-5.E1, 06-REQ-5.E2).
//   - A PAT bound to a workspace (spec 33) receives the same 404 for every
//     other workspace; a failed binding lookup is a pkt-line 500.
//
// Scope checks (git:read vs git:write) are handled separately by
// requireGitScope in the handler, which runs after this check.
//
// Returns nil if the request is authorized. On denial it writes a pkt-line
// error response and returns errAccessDenied to halt the middleware chain.
func authorizeGitAccess(c echo.Context, db *sql.DB, ws *workspaceInfo) error {
	info := apikit.GetAuthInfo(c)
	if info == nil {
		return echo.NewHTTPError(http.StatusUnauthorized)
	}

	// Admin tokens have full access to any workspace (06-REQ-5.1).
	if info.CredentialType == "admin_token" {
		return nil
	}

	// API keys and PATs must belong to the workspace owner.
	// Non-owner, non-admin → HTTP 404 for anti-enumeration (06-PROP-3).
	if info.UserID != ws.OwnerID {
		_ = writePktLineError(c, http.StatusNotFound, "repository not found")
		return errAccessDenied
	}

	// A PAT bound to another workspace gets the non-owner's answer (spec 33).
	// API keys never reach a query; a failed lookup is not served.
	boundSlug, bound, err := wsaccess.BoundSlug(db, info)
	if err != nil {
		_ = writePktLineError(c, http.StatusInternalServerError, "internal server error")
		return errAccessDenied
	}
	if bound && boundSlug != ws.Slug {
		_ = writePktLineError(c, http.StatusNotFound, "repository not found")
		return errAccessDenied
	}

	return nil
}
