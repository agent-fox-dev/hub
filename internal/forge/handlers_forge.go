package forge

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// forgeInfo is the body of GET /workspaces/:slug/forge: how hub reaches the forge of the
// workspace's origin and, on a carry_patch workspace, of its upstream.
type forgeInfo struct {
	remoteInfo
	// Upstream has the same four fields for the upstream_url of a carry_patch
	// workspace, and is null on any other workspace.
	Upstream *remoteInfo `json:"upstream"`
}

// diagnostic serves GET /workspaces/:slug/forge. It sends nothing to the forge, takes
// no target, and answers with the kind of the credential and never its value.
func (h *handlers) diagnostic(c echo.Context) error {
	req, err := h.authorize(c)
	if req == nil {
		return err
	}
	origin, err := h.describeRemote(req, targetOrigin)
	if err != nil {
		return writeForgeError(c, err)
	}
	info := forgeInfo{remoteInfo: *origin}
	if req.remotes.hasUpstream() {
		if info.Upstream, err = h.describeRemote(req, targetUpstream); err != nil {
			return writeForgeError(c, err)
		}
	}
	return c.JSON(http.StatusOK, info)
}
