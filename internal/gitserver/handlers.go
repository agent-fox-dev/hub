package gitserver

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
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
// All routes are protected by the git-specific HTTP Basic auth middleware
// and the workspace resolver middleware which enforces authorization.
//
// workspaceRoot is the filesystem directory under which workspace local
// clones are stored, one subdirectory per workspace slug.
//
// Must be called after NewServer and before Start.
func MountGitHandlers(e *echo.Echo, db *sql.DB, workspaceRoot string) error {
	loader := NewWorkspaceLoader(db, workspaceRoot)
	// Create the go-git server transport once at startup rather than
	// per-request. The transport is stateless and thread-safe.
	srv := server.NewServer(loader)

	g := e.Group("/git/:org/:slug.git",
		GitAuthMiddleware(db),
		requireDotGitSuffix(),
		gitResolverMiddleware(db, workspaceRoot),
	)

	g.GET("/info/refs", handleInfoRefs(db, srv))
	g.POST("/git-upload-pack", handleUploadPack(db, srv))
	g.POST("/git-receive-pack", handleReceivePack(db, srv, workspaceRoot))

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
// It validates the `service` query parameter, creates a go-git session,
// calls AdvertisedReferences to obtain refs and capabilities, and writes
// a pkt-line encoded ref advertisement with the correct Content-Type.
func handleInfoRefs(db *sql.DB, srv transport.Transport) echo.HandlerFunc {
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

		// Create sessions from the pre-initialized go-git server transport.
		ep := endpointFromContext(c)

		var ar *packp.AdvRefs
		if service == "git-upload-pack" {
			sess, err := srv.NewUploadPackSession(ep, nil)
			if err != nil {
				writeSessionError(c.Response(), err)
				return nil
			}
			defer sess.Close()

			ar, err = sess.AdvertisedReferencesContext(c.Request().Context())
			if err != nil {
				writeSessionError(c.Response(), err)
				return nil
			}
		} else {
			sess, err := srv.NewReceivePackSession(ep, nil)
			if err != nil {
				writeSessionError(c.Response(), err)
				return nil
			}
			defer sess.Close()

			ar, err = sess.AdvertisedReferencesContext(c.Request().Context())
			if err != nil {
				writeSessionError(c.Response(), err)
				return nil
			}
		}

		// Write the ref advertisement and final flush.
		writeRefAdvertisement(c.Response(), ar)
		_, _ = c.Response().Write(encodePktFlush())

		return nil
	}
}

// handleUploadPack returns the git smart HTTP upload-pack (fetch/clone) handler.
//
// It creates a go-git UploadPackSession, decodes the upload-pack request
// from the HTTP body, executes the session, and streams the pack response
// back to the client.
func handleUploadPack(db *sql.DB, srv transport.Transport) echo.HandlerFunc {
	return func(c echo.Context) error {
		if err := requireGitScope(c, "git-upload-pack"); err != nil {
			return err
		}

		c.Response().Header().Set("Content-Type", "application/x-git-upload-pack-result")
		c.Response().WriteHeader(http.StatusOK)

		// Create upload-pack session from the pre-initialized transport.
		ep := endpointFromContext(c)
		sess, err := srv.NewUploadPackSession(ep, nil)
		if err != nil {
			writeSessionError(c.Response(), err)
			return nil
		}
		defer sess.Close()

		// Initialize session capabilities (must be called before UploadPack).
		if _, err = sess.AdvertisedReferencesContext(c.Request().Context()); err != nil {
			writeSessionError(c.Response(), err)
			return nil
		}

		// Decode the upload-pack request (want lines + capabilities) from the body.
		req := packp.NewUploadPackRequest()
		if err := req.UploadRequest.Decode(c.Request().Body); err != nil {
			writeSessionError(c.Response(), err)
			return nil
		}

		// Execute the upload-pack session to generate the pack response.
		resp, err := sess.UploadPack(c.Request().Context(), req)
		if err != nil {
			writeSessionError(c.Response(), err)
			return nil
		}

		// Stream the pack response (NAK + PACK data) to the client.
		if err := resp.Encode(c.Response()); err != nil {
			log.Printf("git upload-pack: failed to encode response: %v", err)
		}

		return nil
	}
}

// handleReceivePack returns the git smart HTTP receive-pack (push) handler.
//
// It creates a go-git ReceivePackSession, decodes the reference update
// request from the HTTP body, executes the session, streams the report
// status back to the client, and updates head_sha in the database after
// a successful push.
func handleReceivePack(db *sql.DB, srv transport.Transport, wsRoot string) echo.HandlerFunc {
	return func(c echo.Context) error {
		if err := requireGitScope(c, "git-receive-pack"); err != nil {
			return err
		}

		c.Response().Header().Set("Content-Type", "application/x-git-receive-pack-result")
		c.Response().WriteHeader(http.StatusOK)

		// Create receive-pack session from the pre-initialized transport.
		ep := endpointFromContext(c)
		sess, err := srv.NewReceivePackSession(ep, nil)
		if err != nil {
			writeSessionError(c.Response(), err)
			return nil
		}
		defer sess.Close()

		// Initialize session capabilities (must be called before ReceivePack).
		if _, err = sess.AdvertisedReferencesContext(c.Request().Context()); err != nil {
			writeSessionError(c.Response(), err)
			return nil
		}

		// Read the body to distinguish empty (no-op) from invalid data.
		// An empty body means the pack was already applied to disk (e.g.
		// by a direct commit); treat it as a successful no-op push.
		bodyBytes, err := io.ReadAll(c.Request().Body)
		if err != nil {
			writeSessionError(c.Response(), err)
			return nil
		}

		slug := strings.TrimSuffix(c.Param("slug.git"), ".git")

		if len(bodyBytes) == 0 {
			// No-op push: the repository state is already on disk.
			// Report success and update head_sha from current HEAD.
			_, _ = c.Response().Write(encodePktLine("unpack ok\n"))
			_, _ = c.Response().Write(encodePktFlush())
			updateHeadSHA(db, slug, wsRoot)
			return nil
		}

		// Decode the reference update request from the body.
		req := packp.NewReferenceUpdateRequest()
		if err := req.Decode(bytes.NewReader(bodyBytes)); err != nil {
			writeSessionError(c.Response(), err)
			return nil
		}

		// Execute the receive-pack session.
		rs, err := sess.ReceivePack(c.Request().Context(), req)
		if err != nil {
			writeSessionError(c.Response(), err)
			return nil
		}

		// Encode the report status to the response.
		if err := rs.Encode(c.Response()); err != nil {
			log.Printf("git receive-pack: failed to encode response: %v", err)
		}

		// Update head_sha after successful push (06-REQ-6.1).
		// Errors are logged but do not fail the push response.
		updateHeadSHA(db, slug, wsRoot)

		return nil
	}
}

// endpointFromContext constructs a transport.Endpoint from the Echo context
// URL parameters. The endpoint path format matches what parseEndpointPath
// and the WorkspaceLoader expect.
func endpointFromContext(c echo.Context) *transport.Endpoint {
	orgSlug := c.Param("org")
	repoParam := c.Param("slug.git")
	slug := strings.TrimSuffix(repoParam, ".git")
	return &transport.Endpoint{Path: fmt.Sprintf("/%s/%s.git", orgSlug, slug)}
}

// writeSessionError writes a pkt-line ERR message to the response writer.
// Used when a go-git session encounters an error during streaming.
func writeSessionError(w io.Writer, err error) {
	errMsg := fmt.Sprintf("ERR %s\n", err.Error())
	_, _ = w.Write(encodePktLine(errMsg))
}

// writeRefAdvertisement writes the ref advertisement body from an AdvRefs
// to the response writer using standard pkt-line encoding.
func writeRefAdvertisement(w io.Writer, ar *packp.AdvRefs) {
	var refNames []string
	for name := range ar.References {
		refNames = append(refNames, name)
	}
	sort.Strings(refNames)

	capsStr := ""
	if ar.Capabilities != nil && !ar.Capabilities.IsEmpty() {
		capsStr = ar.Capabilities.String()
	}

	firstLine := true

	if ar.Head != nil {
		headSHA := ar.Head.String()
		if firstLine && capsStr != "" {
			_, _ = w.Write(encodePktLine(fmt.Sprintf("%s HEAD\x00%s\n", headSHA, capsStr)))
		} else {
			_, _ = w.Write(encodePktLine(fmt.Sprintf("%s HEAD\n", headSHA)))
		}
		firstLine = false
	}

	for _, name := range refNames {
		sha := ar.References[name].String()
		if firstLine && capsStr != "" {
			_, _ = w.Write(encodePktLine(fmt.Sprintf("%s %s\x00%s\n", sha, name, capsStr)))
			firstLine = false
		} else {
			_, _ = w.Write(encodePktLine(fmt.Sprintf("%s %s\n", sha, name)))
		}
	}
}

// updateHeadSHA reads the current HEAD commit SHA from the local clone
// and updates the head_sha column in the database. Errors are logged
// but do not fail the push response per 06-REQ-6.E1 and 06-REQ-6.E2.
func updateHeadSHA(db *sql.DB, slug, wsRoot string) {
	repoPath := filepath.Join(wsRoot, slug, "trunk")
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		log.Printf("git push: failed to open repo for head_sha update: %v", err)
		return
	}
	head, err := repo.Head()
	if err != nil {
		log.Printf("git push: failed to read HEAD after push: %v", err)
		return
	}
	sha := head.Hash().String()
	_, err = db.Exec("UPDATE workspaces SET head_sha = ? WHERE slug = ?", sha, slug)
	if err != nil {
		log.Printf("git push: failed to update head_sha in database: %v", err)
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
