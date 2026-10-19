package sandboxes

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// RegisterLeaseRoutes mounts the lease routes on api. A nil service means the
// feature is off: the routes still exist and answer 404 sandboxes_disabled, so
// a caller can tell a disabled feature from a mistyped path.
func RegisterLeaseRoutes(api *echo.Group, s *LeaseService) {
	api.POST("/workspaces/:slug/sandboxes", handleIssueLease(s))
}

// leaseRequest is what the checks every per-workspace lease route share
// establish about a request.
type leaseRequest struct {
	auth      *apikit.AuthInfo
	workspace *wsaccess.WorkspaceAccess
}

// authorize applies, in this order, the checks every per-workspace lease route
// shares: no credential is 401; a hub without [sandbox] answers 404
// sandboxes_disabled; a PAT without the scope is 403, before the workspace is
// looked up; a workspace that does not exist or is not the caller's is 404,
// without saying which. write selects sandboxes:write over the read scope.
//
// It returns the request to go on with, or nil after writing the refusal, in
// which case the caller returns err. A nil service is the disabled feature.
func (s *LeaseService) authorize(c echo.Context, write bool) (req *leaseRequest, err error) {
	auth := apikit.GetAuthInfo(c)
	if auth == nil {
		return nil, apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
	}
	if s == nil {
		return nil, apikit.WriteAPIErrorWithType(c, http.StatusNotFound, "sandboxes are not enabled on this hub", "sandboxes_disabled")
	}
	if write && !canWriteSandboxes(auth) {
		return nil, apikit.WriteAPIError(c, http.StatusForbidden, "PAT requires sandboxes:write scope")
	}
	if !write && !canReadSandboxes(auth) {
		return nil, apikit.WriteAPIError(c, http.StatusForbidden, "PAT requires sandboxes:read scope")
	}
	access, status, msg := wsaccess.AuthorizeWorkspace(s.db, auth, c.Param("slug"))
	if access == nil {
		return nil, apikit.WriteAPIError(c, status, msg)
	}
	return &leaseRequest{auth: auth, workspace: access}, nil
}
