package auth

import (
	"fmt"
	"net/url"
)

// Allowlist validates redirect URIs against a configured set of permitted
// origins. It operates in two modes:
//
//   - Dev mode (devMode=true): allows any redirect_uri whose host is exactly
//     "localhost" (any port). Hosts that merely contain "localhost" as a
//     substring (e.g. "localhost.evil.com") are rejected.
//
//   - Production mode (devMode=false): allows only redirect_uri values whose
//     scheme+host+port exactly match the configured externalURL. The path
//     component is not considered.
type Allowlist struct {
	devMode     bool
	externalURL string // scheme+host+port of the configured external URL
}

// NewAllowlist creates a new redirect URI allowlist.
//
// Parameters:
//   - externalURL: the configured external_url for the deployment. Empty string
//     is valid in dev mode. In production mode, an empty externalURL causes
//     IsAllowed to always return an error.
//   - devMode: true when external_url is absent from configuration.
func NewAllowlist(externalURL string, devMode bool) *Allowlist {
	return &Allowlist{
		devMode:     devMode,
		externalURL: externalURL,
	}
}

// IsAllowed checks whether the given redirect URI is permitted.
// It extracts the origin (scheme+host+port) from the URI and compares
// it against the allowlist. The path component is never considered.
//
// Returns nil if the URI is allowed, or an error describing why it was rejected.
func (a *Allowlist) IsAllowed(redirectURI string) error {
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return fmt.Errorf("invalid redirect_uri: %w", err)
	}

	if a.devMode {
		return a.checkDevMode(parsed)
	}

	return a.checkProductionMode(parsed)
}

// checkDevMode validates the redirect URI in dev mode.
// Only http://localhost (any port) is allowed.
// The host must be exactly "localhost", not a substring match.
func (a *Allowlist) checkDevMode(parsed *url.URL) error {
	// In dev mode, the scheme must be http.
	if parsed.Scheme != "http" {
		return fmt.Errorf("redirect_uri scheme must be http in dev mode, got %q", parsed.Scheme)
	}

	// Extract just the hostname (without port).
	hostname := parsed.Hostname()

	// The hostname must be exactly "localhost" — not "localhost.evil.com",
	// not "mylocalhost", not any other hostname containing "localhost".
	if hostname != "localhost" {
		return fmt.Errorf("redirect_uri host must be localhost in dev mode, got %q", hostname)
	}

	return nil
}

// checkProductionMode validates the redirect URI in production mode.
// The origin (scheme+host+port) must exactly match the configured external_url.
func (a *Allowlist) checkProductionMode(parsed *url.URL) error {
	if a.externalURL == "" {
		return fmt.Errorf("server configuration error: external_url is required in production mode")
	}

	extParsed, err := url.Parse(a.externalURL)
	if err != nil {
		return fmt.Errorf("invalid external_url configuration: %w", err)
	}

	// Compare origin: scheme + host (which includes port if specified).
	// url.Host includes the port if explicitly specified.
	redirectOrigin := originOf(parsed)
	configuredOrigin := originOf(extParsed)

	if redirectOrigin != configuredOrigin {
		return fmt.Errorf("redirect_uri origin %q does not match configured external_url origin %q",
			redirectOrigin, configuredOrigin)
	}

	return nil
}

// originOf extracts the origin (scheme://host:port) from a parsed URL.
// If no port is explicitly specified, the default port for the scheme is
// not appended — the comparison relies on both sides omitting default ports
// consistently, which is the standard behavior for url.Parse.
func originOf(u *url.URL) string {
	return u.Scheme + "://" + u.Host
}
