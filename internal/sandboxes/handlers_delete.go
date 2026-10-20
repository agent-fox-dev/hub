package sandboxes

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// leaseEndReasonAPI is the end_reason of a lease its holder gave back.
const leaseEndReasonAPI = "api"

// destroyTimeout bounds the destruction of a box on DELETE. Tests shorten it.
var destroyTimeout = 30 * time.Second

// handleDeleteLease serves DELETE /workspaces/:slug/sandboxes/:id. It ends the
// lease through the lease store, whose endings all clear the token, so the lease
// becomes destroyed with the reason api and its token stops working with the very
// next call to the gateway. An active lease has its box destroyed first (see
// destroyLease); if the provider cannot, the answer is 502 with the error type
// sandbox_provider_error, the lease stays active and the DELETE can be repeated.
//
// It answers 204 whether or not it changed anything, so that a retry or a
// clean-up script is harmless, and an already ended lease keeps its original
// ended_at and end_reason. It works on an archived workspace. Each change into
// destroyed emits one hub.sandbox.destroy event.
func handleDeleteLease(s *LeaseService) echo.HandlerFunc {
	return func(c echo.Context) error {
		req, err := s.authorize(c, true)
		if req == nil {
			return err
		}
		lease, err := s.store.GetWorkspaceLease(req.workspace.Slug, c.Param("id"))
		if err != nil {
			return writeReadError(c, "ending", err)
		}
		if err := s.destroyLease(c.Request().Context(), req.auth, lease); err != nil {
			var failed *providerError
			if errors.As(err, &failed) {
				return apikit.WriteAPIErrorWithType(c, http.StatusBadGateway, failed.msg, "sandbox_provider_error")
			}
			return writeReadError(c, "ending", err)
		}
		return c.NoContent(http.StatusNoContent)
	}
}
