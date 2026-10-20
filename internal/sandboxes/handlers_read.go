package sandboxes

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// leaseNotFound is the one message for a lease that cannot be shown or ended: a
// malformed id, an unknown id and another workspace's lease look alike.
const leaseNotFound = "sandbox lease not found"

// parseAll reads the all query parameter: absent means false, and a value
// strconv.ParseBool cannot read is a request error.
func parseAll(c echo.Context) (bool, error) {
	values, present := c.QueryParams()["all"]
	if !present {
		return false, nil
	}
	all, err := strconv.ParseBool(values[0])
	if err != nil {
		return false, badRequest("query parameter all must be true or false")
	}
	return all, nil
}

// respondLeases answers a list request with a bare JSON array of leases.
func respondLeases(c echo.Context, leases []*Lease) error {
	out := make([]leaseResponse, 0, len(leases))
	for _, l := range leases {
		out = append(out, renderLease(l))
	}
	return c.JSON(http.StatusOK, out)
}

// writeReadError answers a failed read: a request the caller got wrong is a
// 400, a lease that cannot be found a 404, and anything else a 500 whose cause
// goes to the log.
func writeReadError(c echo.Context, what string, err error) error {
	if re, ok := asRequestError(err); ok {
		return apikit.WriteAPIError(c, re.status, re.msg)
	}
	if errors.Is(err, ErrLeaseNotFound) {
		return apikit.WriteAPIError(c, http.StatusNotFound, leaseNotFound)
	}
	slog.Error("sandbox lease: "+what+" failed", "error", err)
	return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
}

// handleListLeases serves GET /workspaces/:slug/sandboxes: the workspace's live
// leases, newest first, at most 200, or every lease with ?all=true. It works on
// an archived workspace.
func handleListLeases(s *LeaseService) echo.HandlerFunc {
	return func(c echo.Context) error {
		req, err := s.authorize(c, false)
		if req == nil {
			return err
		}
		all, err := parseAll(c)
		if err != nil {
			return writeReadError(c, "listing", err)
		}
		leases, err := s.store.ListLeases(req.workspace.Slug, all)
		if err != nil {
			return writeReadError(c, "listing", err)
		}
		return respondLeases(c, leases)
	}
}

// handleGetLease serves GET /workspaces/:slug/sandboxes/:id: one lease of the
// workspace, in any state. It works on an archived workspace.
func handleGetLease(s *LeaseService) echo.HandlerFunc {
	return func(c echo.Context) error {
		req, err := s.authorize(c, false)
		if req == nil {
			return err
		}
		lease, err := s.store.GetWorkspaceLease(req.workspace.Slug, c.Param("id"))
		if err != nil {
			return writeReadError(c, "reading", err)
		}
		return c.JSON(http.StatusOK, renderLease(lease))
	}
}

// handleAdminListLeases serves GET /sandboxes, which only an admin token may
// call: every workspace's leases, newest first, at most 200.
func handleAdminListLeases(s *LeaseService) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := s.begin(c)
		if auth == nil {
			return err
		}
		if !isAdmin(auth) {
			return apikit.WriteAPIError(c, http.StatusForbidden, "admin token required")
		}
		all, err := parseAll(c)
		if err != nil {
			return writeReadError(c, "listing", err)
		}
		leases, err := s.store.ListLeases("", all)
		if err != nil {
			return writeReadError(c, "listing", err)
		}
		return respondLeases(c, leases)
	}
}
