package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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

// ---------------------------------------------------------------------------
// TS-07-6 + TS-07-20: Verifies that ExchangeCode sends a POST with
// application/x-www-form-urlencoded body containing all required fields
// including grant_type=authorization_code and returns the access_token
// on success.
// Requirements: 07-REQ-3.1, 07-REQ-8.2
// ---------------------------------------------------------------------------

func TestGoogleProvider_ExchangeCode_Success(t *testing.T) {
	var capturedMethod string
	var capturedContentType string
	var capturedBody url.Values

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedContentType = r.Header.Get("Content-Type")

		if err := r.ParseForm(); err != nil {
			t.Errorf("failed to parse form body: %v", err)
		}
		capturedBody = r.PostForm

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "tok123"})
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		TokenURL:     ts.URL,
	}
	p := NewGoogleProvider(cfg)
	p.SetHTTPClient(ts.Client())

	tokenResp, err := p.ExchangeCode(context.Background(), "authcode", "http://localhost:8080/callback")

	// Assert no error and valid token response.
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v, want nil", err)
	}
	if tokenResp == nil {
		t.Fatal("ExchangeCode() returned nil TokenResponse, want non-nil")
	}
	if tokenResp.AccessToken != "tok123" {
		t.Errorf("AccessToken = %q, want %q", tokenResp.AccessToken, "tok123")
	}

	// Assert request method is POST.
	if capturedMethod != http.MethodPost {
		t.Errorf("request method = %q, want %q", capturedMethod, http.MethodPost)
	}

	// Assert Content-Type is application/x-www-form-urlencoded.
	if capturedContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want %q", capturedContentType, "application/x-www-form-urlencoded")
	}

	// Assert all required body fields are present (TS-07-20: grant_type assertion).
	bodyChecks := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "test-client-id",
		"client_secret": "test-client-secret",
		"code":          "authcode",
		"redirect_uri":  "http://localhost:8080/callback",
	}
	for field, want := range bodyChecks {
		got := capturedBody.Get(field)
		if got != want {
			t.Errorf("body field %q = %q, want %q", field, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-07-7: Verifies that ExchangeCode returns an error containing the HTTP
// status code when the token endpoint returns a non-2xx response.
// Requirement: 07-REQ-3.2
// ---------------------------------------------------------------------------

func TestGoogleProvider_ExchangeCode_Non2xx(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // 401
		w.Write([]byte(`{"error": "invalid_client"}`))
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

	tokenResp, err := p.ExchangeCode(context.Background(), "bad-code", "http://localhost:8080/callback")

	if err == nil {
		t.Fatal("ExchangeCode() error = nil, want non-nil for 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "401")
	}
	// TokenResponse should be nil on error.
	if tokenResp != nil {
		t.Errorf("ExchangeCode() returned non-nil TokenResponse on error: %+v", tokenResp)
	}
}

// ---------------------------------------------------------------------------
// TS-07-8: Verifies that ExchangeCode returns an error when the token
// endpoint response body is invalid JSON.
// Requirement: 07-REQ-3.3
// ---------------------------------------------------------------------------

func TestGoogleProvider_ExchangeCode_InvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not-json"))
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

	tokenResp, err := p.ExchangeCode(context.Background(), "authcode", "http://localhost:8080/callback")

	if err == nil {
		t.Fatal("ExchangeCode() error = nil, want non-nil for invalid JSON response")
	}
	// TokenResponse should be nil on error.
	if tokenResp != nil {
		t.Errorf("ExchangeCode() returned non-nil TokenResponse on error: %+v", tokenResp)
	}
}

// ---------------------------------------------------------------------------
// TS-07-E2: Verifies that ExchangeCode returns a timeout-related error and
// does not leak goroutines when the token endpoint hangs beyond the HTTP
// client timeout.
// Requirement: 07-REQ-3.E1
// ---------------------------------------------------------------------------

func TestGoogleProvider_ExchangeCode_Timeout(t *testing.T) {
	// Create a server that never responds (blocks until the test is done).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the request context is cancelled (the client times out).
		<-r.Context().Done()
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     ts.URL,
	}
	p := NewGoogleProvider(cfg)

	// Inject an HTTP client with a very short timeout.
	shortClient := &http.Client{Timeout: 10 * time.Millisecond}
	p.SetHTTPClient(shortClient)

	start := time.Now()
	tokenResp, err := p.ExchangeCode(context.Background(), "authcode", "http://localhost/callback")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("ExchangeCode() error = nil, want non-nil timeout error")
	}
	// The error should indicate a timeout or deadline exceeded.
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "timeout") && !strings.Contains(errMsg, "deadline") {
		t.Errorf("error = %q, want it to contain 'timeout' or 'deadline'", err.Error())
	}
	// TokenResponse should be nil on error.
	if tokenResp != nil {
		t.Errorf("ExchangeCode() returned non-nil TokenResponse on timeout: %+v", tokenResp)
	}
	// The call should complete within a bounded time (well under 500ms).
	if elapsed > 500*time.Millisecond {
		t.Errorf("ExchangeCode() took %v, want < 500ms for bounded termination", elapsed)
	}
}

// ---------------------------------------------------------------------------
// TS-07-E3: Verifies that ExchangeCode returns the underlying net/http error
// and does not leave partial state when the connection to the token endpoint
// is refused.
// Requirement: 07-REQ-3.E2
// ---------------------------------------------------------------------------

func TestGoogleProvider_ExchangeCode_ConnectionRefused(t *testing.T) {
	// Use an address where nothing is listening to trigger connection refused.
	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     "http://127.0.0.1:1/token",
	}
	p := NewGoogleProvider(cfg)

	tokenResp, err := p.ExchangeCode(context.Background(), "authcode", "http://localhost/callback")

	if err == nil {
		t.Fatal("ExchangeCode() error = nil, want non-nil network error")
	}
	// The error should indicate a connection-level failure.
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "connection refused") && !strings.Contains(errMsg, "dial") {
		t.Errorf("error = %q, want it to contain 'connection refused' or 'dial'", err.Error())
	}
	// TokenResponse should be nil on error.
	if tokenResp != nil {
		t.Errorf("ExchangeCode() returned non-nil TokenResponse on connection refused: %+v", tokenResp)
	}
}
