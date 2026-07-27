package gitserver

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/labstack/echo/v4"
)

// workspaceInfo holds the database state needed for workspace resolution
// and authorization in the git server middleware.
type workspaceInfo struct {
	Slug        string
	OwnerID     string
	OrgID       string
	CloneStatus string
}

// resolveWorkspaceFromDB looks up a workspace by slug and verifies that it
// belongs to the organization identified by orgSlug. Returns nil with no error
// when the workspace is not found or the org does not match (callers should
// treat nil as "not found").
//
// Returns a non-nil error only for unexpected database failures, which
// should map to HTTP 500.
func resolveWorkspaceFromDB(db *sql.DB, orgSlug, wsSlug string) (*workspaceInfo, error) {
	// Look up the workspace by slug.
	var ws workspaceInfo
	err := db.QueryRow(
		`SELECT slug, owner_id, org_id, clone_status FROM workspaces WHERE slug = ?`,
		wsSlug,
	).Scan(&ws.Slug, &ws.OwnerID, &ws.OrgID, &ws.CloneStatus)
	if err == sql.ErrNoRows {
		return nil, nil // workspace not found
	}
	if err != nil {
		return nil, fmt.Errorf("workspace lookup: %w", err)
	}

	// Verify org match: look up the org by slug and compare IDs.
	// This prevents enumeration of workspaces across orgs (06-REQ-4.3).
	var orgID string
	err = db.QueryRow(
		`SELECT id FROM orgs WHERE slug = ?`,
		orgSlug,
	).Scan(&orgID)
	if err == sql.ErrNoRows {
		return nil, nil // org not found → treated as mismatch
	}
	if err != nil {
		return nil, fmt.Errorf("org lookup: %w", err)
	}

	if ws.OrgID != orgID {
		return nil, nil // org mismatch → anti-enumeration
	}

	return &ws, nil
}

// parseEndpointPath extracts the org slug and workspace slug from a git
// transport endpoint path. Supports formats:
//
//	/git/<org>/<slug>.git
//	/<org>/<slug>.git
//
// Returns an error for paths that do not contain recognizable segments.
func parseEndpointPath(path string) (orgSlug, slug string, err error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")

	var orgIdx, slugIdx int
	switch {
	case len(parts) >= 3 && parts[0] == "git":
		orgIdx = 1
		slugIdx = 2
	case len(parts) >= 2:
		orgIdx = 0
		slugIdx = 1
	default:
		return "", "", fmt.Errorf("invalid endpoint path: %s", path)
	}

	orgSlug = parts[orgIdx]
	slug = strings.TrimSuffix(parts[slugIdx], ".git")
	if slug == "" || orgSlug == "" {
		return "", "", fmt.Errorf("invalid endpoint path: %s", path)
	}

	return orgSlug, slug, nil
}

// gitResolverMiddleware returns Echo middleware that resolves the workspace
// from the URL path parameters, verifies organization membership, clone
// readiness, and user authorization, then opens the repository via PlainOpen.
//
// On success it stores the workspace info and repository path in the Echo
// context for downstream handlers. On failure it writes an appropriate
// pkt-line error response (HTTP 404 or 500) and halts the middleware chain.
//
// This middleware must run AFTER GitAuthMiddleware (which sets AuthInfo) and
// AFTER requireDotGitSuffix (which validates the URL format).
func gitResolverMiddleware(db *sql.DB, wsRoot string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Extract org and workspace slug from URL parameters.
			repoParam := c.Param("slug.git")
			slug := strings.TrimSuffix(repoParam, ".git")
			orgSlug := c.Param("org")

			// Resolve workspace from the database.
			ws, err := resolveWorkspaceFromDB(db, orgSlug, slug)
			if err != nil {
				// Database error → HTTP 500. Log the error but do not
				// expose details to the client (06-REQ-4.E3).
				log.Printf("git resolver: database error for %s/%s: %v", orgSlug, slug, err)
				c.Response().Header().Set("Content-Type", "text/plain")
				c.Response().WriteHeader(http.StatusInternalServerError)
				return nil
			}
			if ws == nil {
				// Workspace not found or org mismatch → HTTP 404 with
				// pkt-line error (06-REQ-4.2, 06-REQ-4.3).
				return writePktLineError(c, http.StatusNotFound, "repository not found")
			}

			// Verify clone_status is 'ready' (06-REQ-4.4).
			if ws.CloneStatus != "ready" {
				return writePktLineError(c, http.StatusNotFound, "repository not available")
			}

			// Check authorization: ownership or admin status (06-REQ-4.E2).
			if err := authorizeGitAccess(c, ws); err != nil {
				return err
			}

			// Open the repository via PlainOpen to verify it exists on disk
			// (06-REQ-4.E1). Filesystem errors → HTTP 500.
			repoPath := filepath.Join(wsRoot, slug, "trunk")
			_, err = git.PlainOpen(repoPath)
			if err != nil {
				log.Printf("git resolver: PlainOpen failed for %s: %v", repoPath, err)
				c.Response().Header().Set("Content-Type", "text/plain")
				c.Response().WriteHeader(http.StatusInternalServerError)
				return nil
			}

			// Store workspace info in context for downstream handlers.
			c.Set("workspace", ws)
			c.Set("workspace_root", wsRoot)

			return next(c)
		}
	}
}

// writePktLineError sends an HTTP error response with a pkt-line formatted
// error body. This ensures git clients receive protocol-compliant error
// messages, and distinguishes resolver errors from Echo's default JSON 404.
func writePktLineError(c echo.Context, status int, msg string) error {
	c.Response().Header().Set("Content-Type", "text/plain")
	c.Response().WriteHeader(status)
	_, _ = c.Response().Write(encodePktLine(fmt.Sprintf("ERR %s\n", msg)))
	return nil
}
