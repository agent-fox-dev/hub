package forge

import (
	"database/sql"
	"net/http"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// Config holds what the forge routes need from the rest of the hub. Everything the
// package cannot get from the workspace's own row is injected, so that the package
// is testable without internal/workspace and internal/secrets.
type Config struct {
	// DB is the hub database; the workspaces table is read from it.
	DB *sql.DB

	// ResolveOriginAuth returns the credential for a workspace's git_url, and
	// ResolveUpstreamAuth the one for its upstream_url. A nil result is no credential.
	ResolveOriginAuth   func(slug string) (transport.AuthMethod, error)
	ResolveUpstreamAuth func(slug string) (transport.AuthMethod, error)

	// GetVariable reads a variable. A *secrets.NotFoundError means it is not set.
	GetVariable func(ownerType, ownerID, key string) (string, error)

	// Transport replaces the base transport of the shared forge HTTP client; nil
	// means a clone of http.DefaultTransport.
	Transport http.RoundTripper

	// NewClient builds an issuex client; nil means issuex.NewWithOptions.
	NewClient func(issuex.Options) (issuex.Client, error)

	// Audit records the events of the write routes, one for every write that sent a
	// request to the forge. Nil records nothing.
	Audit audit.Emitter
}
