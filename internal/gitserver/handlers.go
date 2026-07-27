package gitserver

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// MountGitHandlers registers git smart HTTP routes and the git auth middleware
// on the Echo instance.
//
// Routes registered:
//   - GET  /git/:org/:slug.git/info/refs
//   - POST /git/:org/:slug.git/git-upload-pack
//   - POST /git/:org/:slug.git/git-receive-pack
//
// All routes are protected by the git-specific HTTP Basic auth middleware.
// Must be called after NewServer and before Start.
func MountGitHandlers(e *echo.Echo, db *sql.DB) error {
	g := e.Group("/git/:org/:slug.git", GitAuthMiddleware(db), requireDotGitSuffix())

	g.GET("/info/refs", handleInfoRefs(db))
	g.POST("/git-upload-pack", handleUploadPack(db))
	g.POST("/git-receive-pack", handleReceivePack(db))

	return nil
}

// requireDotGitSuffix returns middleware that verifies the :slug.git path
// parameter actually ends with ".git". Echo treats :slug.git as a named
// parameter that matches any path segment, so without this check a URL
// like /git/org/repo/info/refs (no .git) would incorrectly match.
func requireDotGitSuffix() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			repo := c.Param("slug.git")
			if !strings.HasSuffix(repo, ".git") {
				return echo.NewHTTPError(http.StatusNotFound)
			}
			return next(c)
		}
	}
}

// handleInfoRefs returns the git smart HTTP ref advertisement handler.
//
// It validates the `service` query parameter and returns a pkt-line encoded
// response with the correct Content-Type for the requested service.
func handleInfoRefs(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		service := c.QueryParam("service")
		if service != "git-upload-pack" && service != "git-receive-pack" {
			// Per 06-REQ-1.E1, reject invalid service with HTTP 403 and
			// a pkt-line error body.
			c.Response().Header().Set("Content-Type", "text/plain")
			c.Response().WriteHeader(http.StatusForbidden)
			_, _ = c.Response().Write(encodePktLine("ERR invalid or missing service parameter\n"))
			return nil
		}

		// Check git permissions for the requested service.
		if err := requireGitScope(c, service); err != nil {
			return err
		}

		contentType := fmt.Sprintf("application/x-%s-advertisement", service)
		c.Response().Header().Set("Content-Type", contentType)
		c.Response().WriteHeader(http.StatusOK)

		// Write the pkt-line service announcement followed by a flush packet.
		announcement := fmt.Sprintf("# service=%s\n", service)
		_, _ = c.Response().Write(encodePktLine(announcement))
		_, _ = c.Response().Write(encodePktFlush())

		// TODO: Add actual ref advertisement from the repository (groups 5-6).

		return nil
	}
}

// handleUploadPack returns the git smart HTTP upload-pack (fetch/clone) handler.
func handleUploadPack(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		if err := requireGitScope(c, "git-upload-pack"); err != nil {
			return err
		}

		c.Response().Header().Set("Content-Type", "application/x-git-upload-pack-result")
		c.Response().WriteHeader(http.StatusOK)

		// TODO: Bridge request body to UploadPackSession and stream response
		// (groups 5-6).

		return nil
	}
}

// handleReceivePack returns the git smart HTTP receive-pack (push) handler.
func handleReceivePack(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		if err := requireGitScope(c, "git-receive-pack"); err != nil {
			return err
		}

		c.Response().Header().Set("Content-Type", "application/x-git-receive-pack-result")
		c.Response().WriteHeader(http.StatusOK)

		// TODO: Bridge request body to ReceivePackSession and stream response
		// (groups 5-6). After successful push, update head_sha (group 7).

		return nil
	}
}

// requireGitScope checks that the authenticated credential has the required
// git permission scope for the given service. Admin tokens and API keys have
// implicit full access. PATs must carry the appropriate git:read or git:write
// scope.
//
// Returns nil if the request is authorized, or sends an HTTP 403 and returns
// a non-nil error to halt the handler chain.
func requireGitScope(c echo.Context, service string) error {
	info := apikit.GetAuthInfo(c)
	if info == nil {
		return echo.NewHTTPError(http.StatusUnauthorized)
	}

	// Admin tokens and API keys have implicit full access to git operations.
	if info.CredentialType == "admin_token" || info.CredentialType == "api_key" {
		return nil
	}

	// For PATs, determine the required scope based on the service.
	if info.CredentialType == "pat" {
		required := "git:read"
		if service == "git-receive-pack" {
			required = "git:write"
		}
		if !hasGitScope(info.Permissions, required) {
			return echo.NewHTTPError(http.StatusForbidden, "insufficient git permissions")
		}
	}

	return nil
}

// hasGitScope checks whether the permissions list satisfies the required scope.
// git:write implies git:read per 06-REQ-3.2.
func hasGitScope(permissions []string, required string) bool {
	for _, p := range permissions {
		if p == required {
			return true
		}
		// git:write implies git:read.
		if required == "git:read" && p == "git:write" {
			return true
		}
	}
	return false
}

// encodePktLine encodes a string as a git pkt-line: a 4-hex-digit length
// prefix (including the prefix itself) followed by the data.
//
// Named encodePktLine (not pktLine) to avoid collision with the test helper
// pktLine in bridge_test.go which returns string instead of []byte.
func encodePktLine(data string) []byte {
	length := len(data) + 4
	return []byte(fmt.Sprintf("%04x%s", length, data))
}

// encodePktFlush returns the git pkt-line flush packet (0000).
func encodePktFlush() []byte {
	return []byte("0000")
}
