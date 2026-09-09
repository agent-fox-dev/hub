package carrypatch

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wslock"
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

		// Verify workspace exists and is owned by the caller.
		if !authorizeWorkspace(c, cfg.DB, auth, slug) {
			return nil
		}

		// Read rr-cache directory (16-REQ-4.E1: missing directory is an
		// empty list).
		gitDir := filepath.Join(cfg.WorkspaceRoot, slug, "trunk", ".git")
		return c.JSON(http.StatusOK, RerereListResponse{Resolutions: listRerereEntries(gitDir)})
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
		if strings.HasPrefix(pathspec, "-") {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "pathspec must not start with '-'")
		}

		// Verify workspace exists and is owned by the caller.
		if !authorizeWorkspace(c, cfg.DB, auth, slug) {
			return nil
		}

		unlock, locked := wslock.TryLock(slug)
		if !locked {
			return apikit.WriteAPIErrorWithType(c, http.StatusConflict,
				"another operation is running on this workspace; retry later", "workspace_busy")
		}
		defer unlock()

		// 16-REQ-4.2 / 16-ERR-7: the argument is either an rr-cache entry id
		// (from GET /rerere) or a file path with a recorded resolution.
		gitDir := filepath.Join(cfg.WorkspaceRoot, slug, "trunk", ".git")
		rrCacheDir := filepath.Join(gitDir, "rr-cache")
		if isRerereEntryID(rrCacheDir, pathspec) {
			// Dropping the cache entry is exactly what `git rerere forget`
			// does at the storage level, and it works for entries whose path
			// git no longer remembers (the common case after a rebuild).
			if err := os.RemoveAll(filepath.Join(rrCacheDir, pathspec)); err != nil {
				return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to remove rerere entry")
			}
			return c.NoContent(http.StatusNoContent)
		}
		if !hasRerereResolution(gitDir, pathspec) {
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

// listRerereEntries enumerates the rr-cache of the repository whose .git
// directory is gitDir. Entries without a preimage or postimage are skipped
// (16-REQ-4.E3).
func listRerereEntries(gitDir string) []RerereResolution {
	rrCacheDir := filepath.Join(gitDir, "rr-cache")
	resolutions := make([]RerereResolution, 0)
	entries, err := os.ReadDir(rrCacheDir)
	if err != nil {
		return resolutions
	}
	pathsByID := readMergeRR(gitDir)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subdir := filepath.Join(rrCacheDir, entry.Name())
		preimage, preErr := os.Stat(filepath.Join(subdir, "preimage"))
		postimage, postErr := os.Stat(filepath.Join(subdir, "postimage"))
		if preErr != nil && postErr != nil {
			continue
		}
		res := RerereResolution{ID: entry.Name(), Resolved: postErr == nil}
		if p, ok := pathsByID[entry.Name()]; ok {
			res.Path = &p
		} else {
			res.Path = derivePathFromRRCache(subdir)
		}
		if postErr == nil {
			ts := apikit.FormatUTC(postimage.ModTime())
			res.RecordedAt = &ts
		} else {
			ts := apikit.FormatUTC(preimage.ModTime())
			res.RecordedAt = &ts
		}
		resolutions = append(resolutions, res)
	}
	return resolutions
}

// readMergeRR parses .git/MERGE_RR, which maps rr-cache ids to the paths of
// conflicts currently in progress (NUL-terminated "<id>\t<path>" records).
// It is the only place git records the path of a conflict.
func readMergeRR(gitDir string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(filepath.Join(gitDir, "MERGE_RR"))
	if err != nil {
		return out
	}
	for _, rec := range strings.Split(string(data), "\x00") {
		id, path, ok := strings.Cut(rec, "\t")
		if ok && id != "" && path != "" {
			out[id] = path
		}
	}
	return out
}

// isRerereEntryID reports whether name is the id of an existing rr-cache
// entry (a single path component naming a subdirectory of rr-cache).
func isRerereEntryID(rrCacheDir, name string) bool {
	if name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
		return false
	}
	info, err := os.Stat(filepath.Join(rrCacheDir, name))
	return err == nil && info.IsDir()
}

// derivePathFromRRCache reads a preimage or postimage file in the given
// rr-cache subdirectory and extracts the conflict path from the first
// labelled conflict marker line (e.g., "<<<<<<< src/config.go").
//
// git itself writes unlabelled markers ("<<<<<<<") into rr-cache images, so
// this only yields a path for images produced with labels; callers must
// tolerate a nil result.
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

// hasRerereResolution checks whether any rr-cache entry is known to belong
// to the given path (via MERGE_RR or a labelled preimage).
func hasRerereResolution(gitDir, pathspec string) bool {
	for _, r := range listRerereEntries(gitDir) {
		if r.Path != nil && *r.Path == pathspec {
			return true
		}
	}
	return false
}
