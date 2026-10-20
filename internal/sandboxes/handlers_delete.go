package sandboxes

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// leaseEndReasonAPI is the end_reason of a lease its holder gave back.
const leaseEndReasonAPI = "api"

// handleDeleteLease serves DELETE /workspaces/:slug/sandboxes/:id. It ends the
// lease through LeaseStore.EndLease, the one function every ending goes through:
// the lease becomes destroyed with the reason api and its token stops working.
// It answers 204 whether or not it changed anything, so that a retry or a clean-up
// script is harmless, and an already ended lease keeps its original ended_at and
// end_reason. It works on an archived workspace and emits no audit event: the
// event for a destroyed sandbox belongs to the gateway, which owns every
// destroy path.
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
		if _, err := s.store.EndLease(lease, leaseDestroyed, leaseEndReasonAPI); err != nil {
			return writeReadError(c, "ending", err)
		}
		return c.NoContent(http.StatusNoContent)
	}
}
