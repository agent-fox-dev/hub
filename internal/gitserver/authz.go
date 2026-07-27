package gitserver

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
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
//
// Scope checks (git:read vs git:write) are handled separately by
// requireGitScope in the handler, which runs after this check.
//
// Returns nil if the request is authorized. On denial it writes a pkt-line
// error response and returns errAccessDenied to halt the middleware chain.
func authorizeGitAccess(c echo.Context, ws *workspaceInfo) error {
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
		writePktLineError(c, http.StatusNotFound, "repository not found")
		return errAccessDenied
	}

	return nil
}
