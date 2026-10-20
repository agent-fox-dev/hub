package forge

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// handlers holds what the forge routes share.
type handlers struct {
	cfg        Config
	httpClient *http.Client
}

// RegisterRoutes mounts the forge routes on api, under /workspaces/:slug. They are
// read-only and need forge:read.
func RegisterRoutes(api *echo.Group, cfg Config) {
	if cfg.NewClient == nil {
		cfg.NewClient = issuex.NewWithOptions
	}
	h := &handlers{cfg: cfg, httpClient: newHTTPClient(cfg.Transport)}

	api.GET("/workspaces/:slug/forge", h.diagnostic)
	api.GET("/workspaces/:slug/forge/repository", h.serve("GetRepository", prepareRepository))
	api.GET("/workspaces/:slug/issues", h.serve("ListIssues", prepareIssues))
	api.GET("/workspaces/:slug/issues/:n", h.serve("ReadIssue", numbered(false, readIssue)))
	api.GET("/workspaces/:slug/issues/:n/comments", h.serve("ListComments", numbered(false, readComments)))
	api.GET("/workspaces/:slug/pulls/:n", h.serve("ReadPullRequest", numbered(true, valueOf(issuex.Client.ReadPullRequest))))
	api.GET("/workspaces/:slug/pulls/:n/state", h.serve("GetPRState", numbered(true, valueOf(issuex.Client.GetPRState))))
	api.GET("/workspaces/:slug/pulls/:n/files", h.serve("ReadChangedFiles", numbered(true, listOf(issuex.Client.ReadChangedFiles))))
	api.GET("/workspaces/:slug/pulls/:n/checks", h.serve("GetCIChecks", numbered(true, listOf(issuex.Client.GetCIChecks))))
	api.GET("/workspaces/:slug/pulls/:n/reviews", h.serve("GetPRReviews", numbered(true, listOf(issuex.Client.GetPRReviews))))
}

// request is what the checks every forge route shares establish about a request.
type request struct {
	auth    *apikit.AuthInfo
	slug    string
	remotes workspaceRemotes
}

// authorize applies, in this order, the checks every forge route shares: no
// credential is 401; a credential without forge:read is 403, before the workspace
// is looked up; a workspace that does not exist or is not the caller's is 404,
// without saying which. It then reads the workspace's remotes.
//
// It returns the request to go on with, or nil after writing the refusal, in which
// case the caller returns err.
func (h *handlers) authorize(c echo.Context) (req *request, err error) {
	auth := apikit.GetAuthInfo(c)
	if auth == nil {
		return nil, apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
	}
	if !canReadForge(auth) {
		msg := "PAT requires forge:read scope"
		if !isPAT(auth) {
			msg = fmt.Sprintf("credential type %q may not read a workspace's forge", auth.CredentialType)
		}
		return nil, apikit.WriteAPIError(c, http.StatusForbidden, msg)
	}
	slug := c.Param("slug")
	access, status, msg := wsaccess.AuthorizeWorkspace(h.cfg.DB, auth, slug)
	if access == nil {
		return nil, apikit.WriteAPIError(c, status, msg)
	}
	remotes, err := h.loadRemotes(slug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
	}
	if err != nil {
		slog.Error("forge: reading the workspace failed", "slug", slug, "error", err)
		return nil, apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
	}
	return &request{auth: auth, slug: slug, remotes: remotes}, nil
}

// loadRemotes reads the columns of the workspace's row that decide which forge
// repository a route reads. A NULL workspace_mode is a standard workspace and a
// NULL upstream_url is none.
func (h *handlers) loadRemotes(slug string) (workspaceRemotes, error) {
	var gitURL string
	var mode, upstream sql.NullString
	err := h.cfg.DB.QueryRow(
		`SELECT git_url, workspace_mode, upstream_url FROM workspaces WHERE slug = ?`, slug,
	).Scan(&gitURL, &mode, &upstream)
	if err != nil {
		return workspaceRemotes{}, err
	}
	remotes := workspaceRemotes{gitURL: gitURL, mode: "standard", upstreamURL: upstream.String}
	if mode.Valid && mode.String != "" {
		remotes.mode = mode.String
	}
	return remotes, nil
}
