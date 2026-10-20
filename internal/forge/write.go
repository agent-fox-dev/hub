package forge

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// writeFunc is the part of a write route that follows the checks every write route
// shares.
type writeFunc func(c echo.Context, req *request) error

// write is the handler of a write route. It applies the checks that come first on every
// write route, in this order: no credential is 401, a credential without forge:write is
// 403, a workspace that does not exist or is not the caller's is 404, a workspace that
// is not active is 400. Only then does it hand the request to run, which reads the
// request, resolves the client and sends the write.
func (h *handlers) write(run writeFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		req, err := h.authorize(c, writeAccess)
		if req == nil {
			return err
		}
		return run(c, req)
	}
}

// notImplemented answers a write route whose action is not delivered yet.
func notImplemented(c echo.Context, _ *request) error {
	return apikit.WriteAPIError(c, http.StatusNotImplemented, "not implemented")
}
