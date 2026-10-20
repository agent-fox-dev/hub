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

// RegisterRoutes mounts the forge routes on api, under /workspaces/:slug. The reads
// need forge:read and the writes forge:write.
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

	api.POST("/workspaces/:slug/issues", h.write(createIssue))
	api.PATCH("/workspaces/:slug/issues/:n", h.write(updateIssue))
	api.POST("/workspaces/:slug/issues/:n/close", h.write(closeIssue))
	api.POST("/workspaces/:slug/issues/:n/comments", h.write(commentOnIssue))
	api.POST("/workspaces/:slug/issues/:n/labels", h.write(addLabels))
	api.DELETE("/workspaces/:slug/issues/:n/labels/:name", h.write(removeLabel))
	api.POST("/workspaces/:slug/labels", h.write(ensureLabel))
	api.POST("/workspaces/:slug/pulls", h.write(notImplemented))
	api.POST("/workspaces/:slug/pulls/:n/comments", h.write(notImplemented))
}

// request is what the checks every forge route shares establish about a request.
type request struct {
	auth    *apikit.AuthInfo
	slug    string
	remotes workspaceRemotes
}

// access is what a route needs of the credential and of the workspace.
type access struct {
	// verb is read or write, as in the scope forge:<verb>.
	verb string
	// allowed reports whether the credential may use the route at all.
	allowed func(*apikit.AuthInfo) bool
	// active makes a workspace that is not active a 400, after the ownership check.
	active bool
}

var (
	readAccess  = access{verb: "read", allowed: canReadForge}
	writeAccess = access{verb: "write", allowed: canWriteForge, active: true}
)

// authorize applies, in this order, the checks every forge route shares: no
// credential is 401; a credential that acc does not allow, a PAT without the scope
// of the verb or a credential type hub does not know, is 403, before the workspace
// is looked up; a workspace that does not exist or is not the caller's is 404,
// without saying which; a workspace that is not active is 400 where acc asks for an
// active one. It then reads the workspace's remotes.
//
// It returns the request to go on with, or nil after writing the refusal, in which
// case the caller returns err.
func (h *handlers) authorize(c echo.Context, acc access) (req *request, err error) {
	auth := apikit.GetAuthInfo(c)
	if auth == nil {
		return nil, apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
	}
	if !acc.allowed(auth) {
		msg := fmt.Sprintf("PAT requires forge:%s scope", acc.verb)
		if !isPAT(auth) {
			msg = fmt.Sprintf("credential type %q may not %s a workspace's forge", auth.CredentialType, acc.verb)
		}
		return nil, apikit.WriteAPIError(c, http.StatusForbidden, msg)
	}
	slug := c.Param("slug")
	found, status, msg := wsaccess.AuthorizeWorkspace(h.cfg.DB, auth, slug)
	if found == nil {
		return nil, apikit.WriteAPIError(c, status, msg)
	}
	if acc.active && found.Status != "active" {
		return nil, apikit.WriteAPIError(c, http.StatusBadRequest, "workspace is not active")
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
