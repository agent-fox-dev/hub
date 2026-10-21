package boundtoken

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// mintRouteSuffix is the part of the mint route's template below the API
// group's mount point.
const mintRouteSuffix = "/workspaces/:slug/tokens"

// handleMint serves POST /workspaces/:slug/tokens (34-REQ-1, 34-REQ-2): it
// checks the caller in order, then mints the token through apikit's own
// POST <mount>/user/tokens, served in-process, so apikit's validation, expiry
// and privilege-escalation rules apply unchanged. Hub does no tokens:manage
// check of its own: apikit's refusal reaches the caller as it is.
func handleMint(cfg Config) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && refuseBoundToken(c, cfg.DB, auth) {
			return nil
		}
		if isAdmin(auth) {
			return apikit.WriteAPIError(c, http.StatusForbidden,
				"admin tokens cannot mint bound tokens; a real user is required as the token's owner")
		}
		slug := c.Param("slug")
		ws, code, msg := wsaccess.AuthorizeWorkspace(cfg.DB, auth, slug)
		if code != 0 {
			return apikit.WriteAPIError(c, code, msg)
		}
		// The owner, not merely a caller AuthorizeWorkspace lets read the
		// workspace, may mint for it (spec 34, Decision 4).
		if ws.OwnerID != auth.UserID {
			return apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		}
		if ws.Status != "active" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "workspace is not active")
		}

		answer, err := delegateMint(c, cfg.APIKit)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}
		return finishMint(c, answer)
	}
}

// delegateMint serves POST <mount>/user/tokens with apikit's handler, carrying
// the caller's body unchanged, its Authorization header verbatim, Content-Type
// and Content-Length, and its context, and returns apikit's answer recorded in
// memory. <mount> is the route template without mintRouteSuffix.
func delegateMint(c echo.Context, apikitHandler http.Handler) (*httptest.ResponseRecorder, error) {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return nil, err
	}
	path := strings.TrimSuffix(c.Path(), mintRouteSuffix) + "/user/tokens"
	inner, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	inner.RemoteAddr = c.Request().RemoteAddr
	inner.Header.Set("Authorization", c.Request().Header.Get("Authorization"))
	contentType := c.Request().Header.Get("Content-Type")
	if contentType == "" {
		contentType = echo.MIMEApplicationJSON
	}
	inner.Header.Set("Content-Type", contentType)
	inner.Header.Set("Content-Length", strconv.Itoa(len(body)))
	inner.ContentLength = int64(len(body))

	answer := httptest.NewRecorder()
	apikitHandler.ServeHTTP(answer, inner)
	return answer, nil
}

// finishMint answers the caller from apikit's recorded answer.
func finishMint(c echo.Context, answer *httptest.ResponseRecorder) error {
	if ct := answer.Header().Get("Content-Type"); ct != "" {
		c.Response().Header().Set("Content-Type", ct)
	}
	c.Response().WriteHeader(answer.Code)
	_, err := c.Response().Write(answer.Body.Bytes())
	return err
}
