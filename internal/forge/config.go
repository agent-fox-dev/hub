package forge

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
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
	// request to the forge, and the events the webhook routes and the webhook ingress
	// relay. Nil records nothing.
	Audit audit.Emitter

	// Secrets holds the workspace's webhook secret, FORGE_WEBHOOK_SECRET in the workspace
	// scope. The webhook routes and the ingress need it.
	Secrets SecretStore

	// ExternalURL is server.external_url, which the ingress URL of a workspace is built
	// from; empty when it is not set.
	ExternalURL string
}

// SecretStore is what the webhook routes and the ingress use of the secrets store;
// *secrets.Store is one. It is an interface so that a test can make the store fail.
type SecretStore interface {
	GetSecretValue(ownerType, ownerID, key string) (string, error)
	CreateSecrets(ownerType, ownerID string, entries []secrets.EntryInput) ([]secrets.SecretEntry, error)
	UpdateSecret(ownerType, ownerID, key, value string) (*secrets.SecretEntry, error)
	DeleteSecret(ownerType, ownerID, key string) error
}

// webhookSecretKey is the workspace secret that holds a workspace's webhook secret.
const webhookSecretKey = "FORGE_WEBHOOK_SECRET"

// webhookURL is the URL a forge posts a workspace's deliveries to: external_url without
// its trailing slashes, then /hooks/forge/<slug>. It is nil when external_url is not set.
func webhookURL(externalURL, slug string) *string {
	base := strings.TrimRight(externalURL, "/")
	if base == "" {
		return nil
	}
	url := base + "/hooks/forge/" + slug
	return &url
}
