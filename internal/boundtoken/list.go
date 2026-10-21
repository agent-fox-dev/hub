package boundtoken

import (
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// listEntry is one token of GET /workspaces/:slug/tokens. It carries no
// secret: neither the token nor its hash.
type listEntry struct {
	TokenID     string   `json:"token_id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	Workspace   string   `json:"workspace"`
	ExpiresAt   *string  `json:"expires_at"`
	RevokedAt   *string  `json:"revoked_at"`
	CreatedAt   string   `json:"created_at"`
}

// handleList serves GET /workspaces/:slug/tokens: every token bound to the
// workspace, revoked and expired ones included (34-REQ-4). The checks run in
// order: a credential, not a bound token, a PAT's tokens:read or
// tokens:manage, and an admin token or the workspace's owner.
func handleList(cfg Config) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) {
			if refuseBoundToken(c, cfg.DB, auth) {
				return nil
			}
			if !hasScope(auth, "tokens:read", "tokens:manage") {
				return apikit.WriteAPIError(c, http.StatusForbidden, "PAT requires tokens:read scope")
			}
		}

		slug := c.Param("slug")
		// The owner and admin tokens list; to a member of the workspace's org
		// the workspace is private.
		if _, code, msg := wsaccess.AuthorizeWorkspace(cfg.DB, auth, slug, wsaccess.NeedPrivate); code != 0 {
			return wsaccess.WriteRefusal(c, code, msg)
		}

		tokens, err := wsaccess.ListBoundTokens(cfg.DB, slug)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}
		out := make([]listEntry, 0, len(tokens))
		for _, bt := range tokens {
			perms := []string{}
			if err := json.Unmarshal([]byte(bt.Permissions), &perms); err != nil || perms == nil {
				perms = []string{}
			}
			out = append(out, listEntry{
				TokenID:     bt.TokenID,
				Name:        bt.Name,
				Permissions: perms,
				Workspace:   slug,
				ExpiresAt:   nullable(bt.ExpiresAt.String, bt.ExpiresAt.Valid),
				RevokedAt:   nullable(bt.RevokedAt.String, bt.RevokedAt.Valid),
				CreatedAt:   bt.CreatedAt,
			})
		}
		return c.JSON(http.StatusOK, out)
	}
}

// nullable is s, or nil when it is unset (NULL or empty).
func nullable(s string, valid bool) *string {
	if !valid || s == "" {
		return nil
	}
	return &s
}
