package sandboxes

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// handleIssueLease serves POST /workspaces/:slug/sandboxes. It issues a lease
// in the workspace and answers 201 with the lease and its token, the only
// response that ever carries the token. The checks run in the order of
// authorize, then the workspace must be active, then the body is validated,
// and only then are the secrets checked and the lease stored.
func handleIssueLease(s *LeaseService) echo.HandlerFunc {
	return func(c echo.Context) error {
		req, err := s.authorize(c, true)
		if req == nil {
			return err
		}
		if req.workspace.Status != "active" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "workspace is not active")
		}

		slug := req.workspace.Slug
		body, err := parseIssueRequest(c.Request().Body)
		if err != nil {
			return writeIssueError(c, slug, err)
		}
		params, err := s.newIssueParams(body)
		if err != nil {
			return writeIssueError(c, slug, err)
		}
		lease, token, err := s.Issue(c.Request().Context(), slug, req.auth, params)
		if err != nil {
			return writeIssueError(c, slug, err)
		}
		return c.JSON(http.StatusCreated, issuedLeaseResponse{leaseResponse: s.render(lease), Token: token})
	}
}

// writeIssueError answers a failed POST. A request the caller got wrong is a
// 400, required secrets that exist nowhere are a 422 secret_missing, the limit
// on live leases is a 409 sandbox_capacity, and anything else is a 500 whose
// cause goes to the log, not the response. The logged error never holds a token
// or a secret value: it comes from the database or the random source.
func writeIssueError(c echo.Context, slug string, err error) error {
	var missing *MissingSecretsError
	var full *CapacityError
	switch re, isRequestError := asRequestError(err); {
	case isRequestError:
		return apikit.WriteAPIError(c, re.status, re.msg)
	case errors.As(err, &missing):
		return apikit.WriteAPIErrorWithType(c, http.StatusUnprocessableEntity, missing.Error(), "secret_missing")
	case errors.As(err, &full):
		return apikit.WriteAPIErrorWithType(c, http.StatusConflict, full.Error(), "sandbox_capacity")
	}
	slog.Error("sandbox lease: issuing failed", "workspace", slug, "error", err)
	return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
}
