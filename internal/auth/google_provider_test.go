package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Compile-time interface assertion: GoogleProvider must satisfy Provider.
var _ Provider = (*GoogleProvider)(nil)

// ---------------------------------------------------------------------------
// TS-07-2: Verifies that NewGoogleProvider sets all four defaults when the
// ProviderConfig has empty URL fields.
// Requirement: 07-REQ-1.2
// ---------------------------------------------------------------------------

func TestNewGoogleProvider_Defaults(t *testing.T) {
	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
	}
	p := NewGoogleProvider(cfg)

	if p == nil {
		t.Fatal("NewGoogleProvider returned nil")
	}
	if p.authorizeURL != DefaultGoogleAuthorizeURL {
		t.Errorf("authorizeURL = %q, want %q", p.authorizeURL, DefaultGoogleAuthorizeURL)
	}
	if p.tokenURL != DefaultGoogleTokenURL {
		t.Errorf("tokenURL = %q, want %q", p.tokenURL, DefaultGoogleTokenURL)
	}
	if p.userInfoURL != DefaultGoogleUserInfoURL {
		t.Errorf("userInfoURL = %q, want %q", p.userInfoURL, DefaultGoogleUserInfoURL)
	}
	if p.scopes != DefaultGoogleScopes {
		t.Errorf("scopes = %q, want %q", p.scopes, DefaultGoogleScopes)
	}
}

// ---------------------------------------------------------------------------
// TS-07-3: Verifies that NewGoogleProvider uses provided URL overrides
// instead of defaults when non-empty values are supplied.
// Requirement: 07-REQ-1.3
// ---------------------------------------------------------------------------

func TestNewGoogleProvider_URLOverrides(t *testing.T) {
	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		AuthorizeURL: "https://custom.example.com/auth",
		TokenURL:     "https://custom.example.com/token",
		UserInfoURL:  "https://custom.example.com/userinfo",
	}
	p := NewGoogleProvider(cfg)

	if p == nil {
		t.Fatal("NewGoogleProvider returned nil")
	}
	if p.authorizeURL != "https://custom.example.com/auth" {
		t.Errorf("authorizeURL = %q, want %q", p.authorizeURL, "https://custom.example.com/auth")
	}
	if p.tokenURL != "https://custom.example.com/token" {
		t.Errorf("tokenURL = %q, want %q", p.tokenURL, "https://custom.example.com/token")
	}
	if p.userInfoURL != "https://custom.example.com/userinfo" {
		t.Errorf("userInfoURL = %q, want %q", p.userInfoURL, "https://custom.example.com/userinfo")
	}
}

// ---------------------------------------------------------------------------
// TS-07-4: Verifies that SetHTTPClient replaces the internal HTTP client
// on GoogleProvider, enabling test injection.
// Requirement: 07-REQ-1.4
// ---------------------------------------------------------------------------

func TestGoogleProvider_SetHTTPClient(t *testing.T) {
	received := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     ts.URL,
	}
	p := NewGoogleProvider(cfg)
	p.SetHTTPClient(ts.Client())

	// Trigger an HTTP call to verify the injected client is used.
	// We call ExchangeCode because it issues an HTTP POST to tokenURL.
	_, _ = p.ExchangeCode(context.Background(), "code", "http://localhost/cb")

	if !received {
		t.Error("expected mock server to receive a request after SetHTTPClient injection")
	}
}

// ---------------------------------------------------------------------------
// TS-07-E1: Verifies that NewGoogleProvider constructs and returns a
// *GoogleProvider without panicking when client_id or client_secret is empty.
// Requirement: 07-REQ-1.E1
// ---------------------------------------------------------------------------

func TestNewGoogleProvider_EmptyCredentials(t *testing.T) {
	cfg := ProviderConfig{
		Name: "google",
		// ClientID and ClientSecret intentionally left empty.
	}
	p := NewGoogleProvider(cfg)

	if p == nil {
		t.Fatal("NewGoogleProvider returned nil for empty credentials")
	}
	// Defaults should still be applied even without credentials.
	if p.authorizeURL != DefaultGoogleAuthorizeURL {
		t.Errorf("authorizeURL = %q, want %q", p.authorizeURL, DefaultGoogleAuthorizeURL)
	}
	if p.tokenURL != DefaultGoogleTokenURL {
		t.Errorf("tokenURL = %q, want %q", p.tokenURL, DefaultGoogleTokenURL)
	}
	if p.userInfoURL != DefaultGoogleUserInfoURL {
		t.Errorf("userInfoURL = %q, want %q", p.userInfoURL, DefaultGoogleUserInfoURL)
	}
}

// ---------------------------------------------------------------------------
// TS-07-5: Verifies that AuthorizeURL returns the correct Google
// authorization base URL.
// Requirement: 07-REQ-2.1
//
// Errata: The spec's TS-07-5 describes AuthorizeURL(state, redirectURI)
// returning a full URL with query parameters. The actual Provider interface
// defines AuthorizeURL() string (zero parameters) returning only the base
// URL. The Registry.List() method appends client_id and scope; the CLI
// appends state, redirect_uri, and response_type=code.
// See docs/errata/07_authorize_url_signature.md for details.
// ---------------------------------------------------------------------------

func TestGoogleProvider_AuthorizeURL(t *testing.T) {
	cfg := ProviderConfig{
		Name:     "google",
		ClientID: "test-client-id",
	}
	p := NewGoogleProvider(cfg)

	got := p.AuthorizeURL()
	if got != DefaultGoogleAuthorizeURL {
		t.Errorf("AuthorizeURL() = %q, want %q", got, DefaultGoogleAuthorizeURL)
	}
}

// TestGoogleProvider_Scopes verifies the default scopes include openid,
// email, and profile as required by Google's OIDC flow.
// Supplements TS-07-5 (scope parameter assertion adapted to Scopes() method).
func TestGoogleProvider_Scopes(t *testing.T) {
	cfg := ProviderConfig{
		Name: "google",
	}
	p := NewGoogleProvider(cfg)

	got := p.Scopes()
	if got != DefaultGoogleScopes {
		t.Errorf("Scopes() = %q, want %q", got, DefaultGoogleScopes)
	}

	// Verify the individual required scopes are present.
	for _, scope := range []string{"openid", "email", "profile"} {
		if !strings.Contains(got, scope) {
			t.Errorf("Scopes() = %q, missing required scope %q", got, scope)
		}
	}
}

// TestGoogleProvider_AuthorizeURL_ViaRegistry verifies the full authorize
// URL integration: AuthorizeURL() provides the base URL, and Registry.List()
// appends client_id and scope query parameters.
// This is the adapted form of TS-07-5's query-parameter assertions.
func TestGoogleProvider_AuthorizeURL_ViaRegistry(t *testing.T) {
	registry := NewRegistry()
	cfg := ProviderConfig{
		Name:     "google",
		ClientID: "test-google-id",
	}
	p := NewGoogleProvider(cfg)
	registry.Register("google", p, cfg)

	entries := registry.List()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.Name != "google" {
		t.Errorf("entry.Name = %q, want %q", entry.Name, "google")
	}

	parsed, err := url.Parse(entry.AuthorizeURL)
	if err != nil {
		t.Fatalf("failed to parse authorize URL %q: %v", entry.AuthorizeURL, err)
	}

	// The base URL should be Google's authorization endpoint.
	baseURL := parsed.Scheme + "://" + parsed.Host + parsed.Path
	if baseURL != DefaultGoogleAuthorizeURL {
		t.Errorf("base URL = %q, want %q", baseURL, DefaultGoogleAuthorizeURL)
	}

	// Registry.List() should have appended client_id.
	q := parsed.Query()
	if q.Get("client_id") != "test-google-id" {
		t.Errorf("client_id = %q, want %q", q.Get("client_id"), "test-google-id")
	}

	// Registry.List() should have appended scope from Scopes().
	scope := q.Get("scope")
	for _, s := range []string{"openid", "email", "profile"} {
		if !strings.Contains(scope, s) {
			t.Errorf("scope = %q, missing required scope %q", scope, s)
		}
	}
}
