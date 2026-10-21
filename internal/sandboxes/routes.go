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
	api.GET("/workspaces/:slug/sandboxes", handleListLeases(s))
	api.GET("/workspaces/:slug/sandboxes/:id", handleGetLease(s))
	api.DELETE("/workspaces/:slug/sandboxes/:id", handleDeleteLease(s))
	api.GET("/sandboxes", handleAdminListLeases(s))
}

// leaseRequest is what the checks every per-workspace lease route share
// establish about a request.
type leaseRequest struct {
	auth      *apikit.AuthInfo
	workspace *wsaccess.WorkspaceAccess
}

// begin applies the first two checks of every lease route: no credential is 401,
// and a hub without [sandbox] answers 404 sandboxes_disabled to every
// authenticated caller. It returns the credential to go on with, or nil after
// writing the refusal, in which case the caller returns err. A nil service is
// the disabled feature.
func (s *LeaseService) begin(c echo.Context) (auth *apikit.AuthInfo, err error) {
	auth = apikit.GetAuthInfo(c)
	if auth == nil {
		return nil, apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
	}
	if s == nil {
		return nil, apikit.WriteAPIErrorWithType(c, http.StatusNotFound, "sandboxes are not enabled on this hub", "sandboxes_disabled")
	}
	return auth, nil
}

// authorize applies, in this order, the checks every per-workspace lease route
// shares: begin's, then a PAT without the scope is 403 before the workspace is
// looked up, then wsaccess decides the workspace: a workspace that does not
// exist or that the caller may not read is 404, without saying which, and a
// member of the workspace's org who asks to write is 403 forbidden. write
// selects sandboxes:write over the read scope and NeedWrite over NeedRead.
//
// It returns the request to go on with, or nil after writing the refusal, in
// which case the caller returns err.
func (s *LeaseService) authorize(c echo.Context, write bool) (req *leaseRequest, err error) {
	auth, err := s.begin(c)
	if auth == nil {
		return nil, err
	}
	if write && !canWriteSandboxes(auth) {
		return nil, apikit.WriteAPIError(c, http.StatusForbidden, "PAT requires sandboxes:write scope")
	}
	if !write && !canReadSandboxes(auth) {
		return nil, apikit.WriteAPIError(c, http.StatusForbidden, "PAT requires sandboxes:read scope")
	}
	need := wsaccess.NeedRead
	if write {
		need = wsaccess.NeedWrite
	}
	access, status, msg := wsaccess.AuthorizeWorkspace(s.db, auth, c.Param("slug"), need)
	if access == nil {
		return nil, wsaccess.WriteRefusal(c, status, msg)
	}
	return &leaseRequest{auth: auth, workspace: access}, nil
}
