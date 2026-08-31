package carrypatch

import (
	"bufio"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// ===========================================================================
// GET /workspaces/:slug/rerere — list recorded rerere resolutions
// ===========================================================================

// handleListRerere handles GET /api/v1/workspaces/:slug/rerere.
//
// 16-REQ-4.1: Reads the rr-cache directory in the workspace's .git directory,
// enumerates subdirectories, derives the path from preimage/postimage files,
// and derives recorded_at from file modification time.
func handleListRerere(cfg RerereAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Auth check.
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "workspaces:read") {
			return apikit.WriteAPIError(c, http.StatusForbidden, "missing required scope: workspaces:read")
		}

		slug := c.Param("slug")

		// Verify workspace exists.
		var mode string
		err := cfg.DB.QueryRow(
			`SELECT workspace_mode FROM workspaces WHERE slug = ?`, slug,
		).Scan(&mode)
		if err == sql.ErrNoRows {
			return apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		}
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}

		// Read rr-cache directory.
		rrCacheDir := filepath.Join(cfg.WorkspaceRoot, slug, "trunk", ".git", "rr-cache")
		resolutions := make([]RerereResolution, 0)

		entries, err := os.ReadDir(rrCacheDir)
		if err != nil {
			// 16-REQ-4.E1: directory doesn't exist or is empty — return empty list.
			return c.JSON(http.StatusOK, RerereListResponse{Resolutions: resolutions})
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			subdir := filepath.Join(rrCacheDir, entry.Name())

			// Derive path from preimage or postimage file.
			path := derivePathFromRRCache(subdir)
			if path == nil {
				// 16-REQ-4.E3: skip malformed entries (no preimage/postimage).
				continue
			}

			// Derive recorded_at from file modification time.
			var recordedAt *string
			if info, statErr := os.Stat(filepath.Join(subdir, "preimage")); statErr == nil {
				ts := apikit.FormatUTC(info.ModTime())
				recordedAt = &ts
			} else if info, statErr := os.Stat(filepath.Join(subdir, "postimage")); statErr == nil {
				ts := apikit.FormatUTC(info.ModTime())
				recordedAt = &ts
			}

			resolutions = append(resolutions, RerereResolution{
				Path:       path,
				RecordedAt: recordedAt,
			})
		}

		return c.JSON(http.StatusOK, RerereListResponse{Resolutions: resolutions})
	}
}

// ===========================================================================
// DELETE /workspaces/:slug/rerere/*pathspec — forget a recorded resolution
// ===========================================================================

// handleForgetRerere handles DELETE /api/v1/workspaces/:slug/rerere/*pathspec.
//
// 16-REQ-4.2: Executes 'git rerere forget <pathspec>' via GitRunner.
// 16-REQ-4.E2: Uses Echo wildcard route parameter to capture the full path
// including slashes without requiring URL encoding.
func handleForgetRerere(cfg RerereAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Auth check.
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "workspaces:write") {
			return apikit.WriteAPIError(c, http.StatusForbidden, "missing required scope: workspaces:write")
		}

		slug := c.Param("slug")

		// 16-REQ-4.E2: Echo wildcard captures the full path including slashes.
		pathspec := c.Param("*")
		pathspec = strings.TrimPrefix(pathspec, "/")

		if pathspec == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "pathspec is required")
		}

		// Verify workspace exists.
		var mode string
		err := cfg.DB.QueryRow(
			`SELECT workspace_mode FROM workspaces WHERE slug = ?`, slug,
		).Scan(&mode)
		if err == sql.ErrNoRows {
			return apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		}
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}

		// 16-REQ-4.2 / 16-ERR-7: check if pathspec has a recorded resolution.
		rrCacheDir := filepath.Join(cfg.WorkspaceRoot, slug, "trunk", ".git", "rr-cache")
		if !hasRerereResolution(rrCacheDir, pathspec) {
			return apikit.WriteAPIError(c, http.StatusNotFound, "no recorded resolution for pathspec")
		}

		// Execute git rerere forget via GitRunner.
		repoPath := filepath.Join(cfg.WorkspaceRoot, slug, "trunk")
		git, gitErr := cfg.NewGitRunner(repoPath)
		if gitErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to create git runner")
		}

		_, runErr := git.Run(c.Request().Context(), "rerere", "forget", pathspec)
		if runErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "git rerere forget failed")
		}

		return c.NoContent(http.StatusNoContent)
	}
}

// ===========================================================================
// rr-cache helpers
// ===========================================================================

// derivePathFromRRCache reads a preimage or postimage file in the given
// rr-cache subdirectory and extracts the conflict path from the first
// conflict marker line (e.g., "<<<<<<< src/config.go").
//
// Returns nil if no preimage/postimage file exists or if no path can be
// extracted from the conflict markers.
func derivePathFromRRCache(dir string) *string {
	for _, filename := range []string{"preimage", "postimage"} {
		p := filepath.Join(dir, filename)
		f, err := os.Open(p)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "<<<<<<<") {
				parts := strings.SplitN(line, " ", 2)
				if len(parts) == 2 {
					path := strings.TrimSpace(parts[1])
					if path != "" {
						f.Close()
						return &path
					}
				}
			}
		}
		f.Close()
	}
	return nil
}

// hasRerereResolution checks if any rr-cache subdirectory contains a
// preimage or postimage that references the given pathspec.
func hasRerereResolution(rrCacheDir, pathspec string) bool {
	entries, err := os.ReadDir(rrCacheDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subdir := filepath.Join(rrCacheDir, entry.Name())
		path := derivePathFromRRCache(subdir)
		if path != nil && *path == pathspec {
			return true
		}
	}
	return false
}
