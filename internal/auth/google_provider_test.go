package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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

// ===========================================================================
// Group 3: GetUserInfo, username derivation, and property/edge-case tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-07-9: Verifies that GetUserInfo sends a GET with Authorization: Bearer
// header to userinfo_url and returns a populated UserInfo on success.
// Requirement: 07-REQ-4.1
// ---------------------------------------------------------------------------

func TestGetUserInfo_Success(t *testing.T) {
	var capturedMethod string
	var capturedAuth string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":             "12345",
			"email":          "user@example.com",
			"verified_email": true,
			"name":           "Test User",
		})
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		UserInfoURL:  ts.URL,
	}
	p := NewGoogleProvider(cfg)
	p.SetHTTPClient(ts.Client())

	info, err := p.GetUserInfo(context.Background(), "valid-token")

	if err != nil {
		t.Fatalf("GetUserInfo() error = %v, want nil", err)
	}
	if info == nil {
		t.Fatal("GetUserInfo() returned nil UserInfo, want non-nil")
	}
	if info.ID == "" {
		t.Error("UserInfo.ID is empty, want non-empty")
	}
	if info.Login == "" {
		t.Error("UserInfo.Login is empty, want non-empty")
	}
	if info.Email == "" {
		t.Error("UserInfo.Email is empty, want non-empty")
	}
	if info.Name == "" {
		t.Error("UserInfo.Name is empty, want non-empty")
	}

	// Inspect captured request.
	if capturedMethod != http.MethodGet {
		t.Errorf("request method = %q, want %q", capturedMethod, http.MethodGet)
	}
	if capturedAuth != "Bearer valid-token" {
		t.Errorf("Authorization header = %q, want %q", capturedAuth, "Bearer valid-token")
	}
}

// ---------------------------------------------------------------------------
// TS-07-10: Verifies that GetUserInfo correctly maps Google userinfo response
// fields to the UserInfo struct fields.
// Requirement: 07-REQ-4.2
// ---------------------------------------------------------------------------

func TestGetUserInfo_FieldMapping(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Exact response from test spec TS-07-10.
		w.Write([]byte(`{"id":"117730543842840592312","email":"jane.doe+work@gmail.com","verified_email":true,"name":"Jane Doe"}`))
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		UserInfoURL:  ts.URL,
	}
	p := NewGoogleProvider(cfg)
	p.SetHTTPClient(ts.Client())

	info, err := p.GetUserInfo(context.Background(), "valid-token")

	if err != nil {
		t.Fatalf("GetUserInfo() error = %v, want nil", err)
	}
	if info == nil {
		t.Fatal("GetUserInfo() returned nil UserInfo, want non-nil")
	}
	if info.ID != "117730543842840592312" {
		t.Errorf("UserInfo.ID = %q, want %q", info.ID, "117730543842840592312")
	}
	if info.Login != "janedoework" {
		t.Errorf("UserInfo.Login = %q, want %q", info.Login, "janedoework")
	}
	if info.Email != "jane.doe+work@gmail.com" {
		t.Errorf("UserInfo.Email = %q, want %q", info.Email, "jane.doe+work@gmail.com")
	}
	if info.Name != "Jane Doe" {
		t.Errorf("UserInfo.Name = %q, want %q", info.Name, "Jane Doe")
	}
}

// ---------------------------------------------------------------------------
// TS-07-11: Verifies that GetUserInfo returns (nil, error) with the status
// code in the error message when userinfo endpoint returns non-2xx.
// Requirement: 07-REQ-4.3
// ---------------------------------------------------------------------------

func TestGetUserInfo_Non2xx(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // 403
		w.Write([]byte(`{"error": "forbidden"}`))
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		UserInfoURL:  ts.URL,
	}
	p := NewGoogleProvider(cfg)
	p.SetHTTPClient(ts.Client())

	info, err := p.GetUserInfo(context.Background(), "expired-token")

	if info != nil {
		t.Errorf("GetUserInfo() returned non-nil UserInfo on error: %+v", info)
	}
	if err == nil {
		t.Fatal("GetUserInfo() error = nil, want non-nil for 403 response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "403")
	}
}

// ---------------------------------------------------------------------------
// TS-07-12: Verifies that GetUserInfo returns (nil, error) when the userinfo
// response body cannot be decoded as JSON.
// Requirement: 07-REQ-4.4
// ---------------------------------------------------------------------------

func TestGetUserInfo_InvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html>not json</html>"))
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		UserInfoURL:  ts.URL,
	}
	p := NewGoogleProvider(cfg)
	p.SetHTTPClient(ts.Client())

	info, err := p.GetUserInfo(context.Background(), "valid-token")

	if info != nil {
		t.Errorf("GetUserInfo() returned non-nil UserInfo on error: %+v", info)
	}
	if err == nil {
		t.Fatal("GetUserInfo() error = nil, want non-nil for invalid JSON response")
	}
}

// ---------------------------------------------------------------------------
// TS-07-E4: Verifies that GetUserInfo returns a timeout-related error and
// does not leak goroutines when the userinfo endpoint hangs beyond the HTTP
// client timeout.
// Requirement: 07-REQ-4.E1
// ---------------------------------------------------------------------------

func TestGetUserInfo_Timeout(t *testing.T) {
	// Create a server that never responds (blocks until the request is done).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the request context is cancelled (the client times out).
		<-r.Context().Done()
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		UserInfoURL:  ts.URL,
	}
	p := NewGoogleProvider(cfg)

	// Inject an HTTP client with a very short timeout.
	shortClient := &http.Client{Timeout: 10 * time.Millisecond}
	p.SetHTTPClient(shortClient)

	start := time.Now()
	info, err := p.GetUserInfo(context.Background(), "valid-token")
	elapsed := time.Since(start)

	if info != nil {
		t.Errorf("GetUserInfo() returned non-nil UserInfo on timeout: %+v", info)
	}
	if err == nil {
		t.Fatal("GetUserInfo() error = nil, want non-nil timeout error")
	}
	// The error should indicate a timeout or deadline exceeded.
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "timeout") && !strings.Contains(errMsg, "deadline") {
		t.Errorf("error = %q, want it to contain 'timeout' or 'deadline'", err.Error())
	}
	// The call should complete within a bounded time (well under 500ms).
	if elapsed > 500*time.Millisecond {
		t.Errorf("GetUserInfo() took %v, want < 500ms for bounded termination", elapsed)
	}
}

// ---------------------------------------------------------------------------
// TS-07-E5: Verifies that GetUserInfo returns the underlying net/http error
// when the connection to userinfo_url is refused at the network layer.
// Requirement: 07-REQ-4.E2
// ---------------------------------------------------------------------------

func TestGetUserInfo_ConnectionRefused(t *testing.T) {
	// Use an address where nothing is listening to trigger connection refused.
	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		UserInfoURL:  "http://127.0.0.1:1/userinfo",
	}
	p := NewGoogleProvider(cfg)

	info, err := p.GetUserInfo(context.Background(), "valid-token")

	if info != nil {
		t.Errorf("GetUserInfo() returned non-nil UserInfo on connection refused: %+v", info)
	}
	if err == nil {
		t.Fatal("GetUserInfo() error = nil, want non-nil network error")
	}
	// The error should indicate a connection-level failure.
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "connection refused") && !strings.Contains(errMsg, "dial") {
		t.Errorf("error = %q, want it to contain 'connection refused' or 'dial'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TS-07-13 + TS-07-15: Verifies the canonical example: email
// jane.doe+work@gmail.com produces Login == "janedoework".
// Requirements: 07-REQ-5.1, 07-REQ-5.3
// ---------------------------------------------------------------------------

func TestUsernameDerivation_CanonicalExample(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"12345","email":"jane.doe+work@gmail.com","verified_email":true,"name":"Jane Doe"}`))
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		UserInfoURL:  ts.URL,
	}
	p := NewGoogleProvider(cfg)
	p.SetHTTPClient(ts.Client())

	info, err := p.GetUserInfo(context.Background(), "valid-token")

	if err != nil {
		t.Fatalf("GetUserInfo() error = %v, want nil", err)
	}
	if info == nil {
		t.Fatal("GetUserInfo() returned nil UserInfo, want non-nil")
	}
	if info.Login != "janedoework" {
		t.Errorf("UserInfo.Login = %q, want %q", info.Login, "janedoework")
	}
	if len(info.Login) < 1 || len(info.Login) > 39 {
		t.Errorf("UserInfo.Login length = %d, want 1-39", len(info.Login))
	}
	// Verify charset matches [0-9A-Za-z-].
	usernameRe := regexp.MustCompile(`^[0-9A-Za-z-]+$`)
	if !usernameRe.MatchString(info.Login) {
		t.Errorf("UserInfo.Login = %q does not match [0-9A-Za-z-]+", info.Login)
	}
}

// ---------------------------------------------------------------------------
// TS-07-E6: Verifies that the derived username is truncated to exactly 39
// characters when the sanitized email local part exceeds 39 characters.
// Requirement: 07-REQ-5.E1
// ---------------------------------------------------------------------------

func TestUsernameDerivation_Truncation(t *testing.T) {
	// Local part: "aaaaabbbbbcccccdddddeeeeefffff12345678901" = 41 chars,
	// all alphanumeric so sanitization keeps them all, then truncation to 39.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"12345","email":"aaaaabbbbbcccccdddddeeeeefffff12345678901@example.com","verified_email":true,"name":"Long Name"}`))
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		UserInfoURL:  ts.URL,
	}
	p := NewGoogleProvider(cfg)
	p.SetHTTPClient(ts.Client())

	info, err := p.GetUserInfo(context.Background(), "valid-token")

	if err != nil {
		t.Fatalf("GetUserInfo() error = %v, want nil", err)
	}
	if info == nil {
		t.Fatal("GetUserInfo() returned nil UserInfo, want non-nil")
	}
	if len(info.Login) != 39 {
		t.Errorf("UserInfo.Login length = %d, want 39", len(info.Login))
	}
	usernameRe := regexp.MustCompile(`^[0-9A-Za-z-]{39}$`)
	if !usernameRe.MatchString(info.Login) {
		t.Errorf("UserInfo.Login = %q does not match [0-9A-Za-z-]{39}", info.Login)
	}
	// First 39 chars of "aaaaabbbbbcccccdddddeeeeefffff12345678901".
	want := "aaaaabbbbbcccccdddddeeeeefffff123456789"
	if info.Login != want {
		t.Errorf("UserInfo.Login = %q, want %q", info.Login, want)
	}
}

// ---------------------------------------------------------------------------
// TS-07-14: Verifies that GetUserInfo returns (nil, error) when sanitization
// of the email local part yields an empty string.
// Requirement: 07-REQ-5.2
// ---------------------------------------------------------------------------

func TestUsernameDerivation_Empty(t *testing.T) {
	// Email "...@gmail.com" — local part contains only dots which are stripped.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"12345","email":"...@gmail.com","verified_email":true,"name":"Dot User"}`))
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		UserInfoURL:  ts.URL,
	}
	p := NewGoogleProvider(cfg)
	p.SetHTTPClient(ts.Client())

	info, err := p.GetUserInfo(context.Background(), "valid-token")

	if info != nil {
		t.Errorf("GetUserInfo() returned non-nil UserInfo for empty-sanitized username: %+v", info)
	}
	if err == nil {
		t.Fatal("GetUserInfo() error = nil, want non-nil for empty sanitized username")
	}
	// Error should be descriptive about username derivation failure.
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "username") && !strings.Contains(errMsg, "login") && !strings.Contains(errMsg, "deriv") && !strings.Contains(errMsg, "empty") {
		t.Errorf("error = %q, want it to describe username derivation failure", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TS-07-E7 (Variant 1): Verifies that GetUserInfo returns (nil, error) when
// the email has no @ character.
// Requirement: 07-REQ-5.E2
// ---------------------------------------------------------------------------

func TestUsernameDerivation_NoAtSign(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"12345","email":"invalidemail","verified_email":true,"name":"No At User"}`))
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		UserInfoURL:  ts.URL,
	}
	p := NewGoogleProvider(cfg)
	p.SetHTTPClient(ts.Client())

	info, err := p.GetUserInfo(context.Background(), "valid-token")

	if info != nil {
		t.Errorf("GetUserInfo() returned non-nil UserInfo for email without @: %+v", info)
	}
	if err == nil {
		t.Fatal("GetUserInfo() error = nil, want non-nil for email without @")
	}
}

// ---------------------------------------------------------------------------
// TS-07-E7 (Variant 2): Verifies that GetUserInfo returns (nil, error) when
// the email has an empty local part before @.
// Requirement: 07-REQ-5.E2
// ---------------------------------------------------------------------------

func TestUsernameDerivation_EmptyLocalPart(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"12345","email":"@gmail.com","verified_email":true,"name":"Empty Local"}`))
	}))
	defer ts.Close()

	cfg := ProviderConfig{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		UserInfoURL:  ts.URL,
	}
	p := NewGoogleProvider(cfg)
	p.SetHTTPClient(ts.Client())

	info, err := p.GetUserInfo(context.Background(), "valid-token")

	if info != nil {
		t.Errorf("GetUserInfo() returned non-nil UserInfo for empty local part: %+v", info)
	}
	if err == nil {
		t.Fatal("GetUserInfo() error = nil, want non-nil for empty local part in email")
	}
}

// ===========================================================================
// Property-based tests (TS-07-P1 through TS-07-P4)
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-07-P1: For any Google userinfo response where GetUserInfo returns a
// non-nil *UserInfo, the Login field must be non-empty and match
// usernameRegexp [0-9A-Za-z-]{1,39}.
// Property: 07-PROP-1
// Validates: 07-REQ-5.1, 07-REQ-5.2
// ---------------------------------------------------------------------------

func TestProperty_LoginAlwaysValid(t *testing.T) {
	usernameRe := regexp.MustCompile(`^[0-9A-Za-z-]{1,39}$`)

	// Generate a representative sample of emails whose local parts contain
	// at least one alphanumeric or hyphen character.
	emails := []string{
		"simple@example.com",
		"jane.doe+work@gmail.com",
		"user-name@example.com",
		"UPPERCASE@example.com",
		"123numeric@example.com",
		"a@example.com",
		"x-y-z@example.com",
		"lots.of.dots@example.com",
		"under_score@example.com",
		"mix.ed+chars_here@example.com",
		"AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcdefghijklmnop@example.com", // long → truncation
	}

	for _, email := range emails {
		t.Run(email, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				resp := map[string]any{
					"id":             "12345",
					"email":          email,
					"verified_email": true,
					"name":           "Test User",
				}
				json.NewEncoder(w).Encode(resp)
			}))
			defer ts.Close()

			cfg := ProviderConfig{
				Name:         "google",
				ClientID:     "id",
				ClientSecret: "secret",
				UserInfoURL:  ts.URL,
			}
			p := NewGoogleProvider(cfg)
			p.SetHTTPClient(ts.Client())

			info, err := p.GetUserInfo(context.Background(), "valid-token")
			if err != nil {
				t.Fatalf("GetUserInfo() error = %v, want nil for email %q", err, email)
			}
			if info == nil {
				t.Fatalf("GetUserInfo() returned nil UserInfo, want non-nil for email %q", email)
			}

			// Property: Login must match usernameRegexp when GetUserInfo succeeds.
			if !usernameRe.MatchString(info.Login) {
				t.Errorf("Login = %q does not match [0-9A-Za-z-]{1,39} for email %q", info.Login, email)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-07-P2: For any GoogleProvider constructed via NewGoogleProvider with
// empty URL fields, the three endpoint URLs always equal the documented
// Google defaults.
// Property: 07-PROP-2
// Validates: 07-REQ-1.2
// ---------------------------------------------------------------------------

func TestProperty_DefaultURLs(t *testing.T) {
	// Vary ClientID and ClientSecret while keeping URL fields empty.
	configs := []ProviderConfig{
		{Name: "google", ClientID: "id1", ClientSecret: "secret1"},
		{Name: "google", ClientID: "", ClientSecret: ""},
		{Name: "google", ClientID: "long-client-id-1234567890", ClientSecret: "long-secret-0987654321"},
		{Name: "google"},
	}

	for i, cfg := range configs {
		t.Run(fmt.Sprintf("config-%d", i), func(t *testing.T) {
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
		})
	}
}

// ---------------------------------------------------------------------------
// TS-07-P3: For any call to ExchangeCode, the HTTP POST body sent to the
// token endpoint always contains grant_type=authorization_code, regardless
// of the code or redirectURI values.
// Property: 07-PROP-3
// Validates: 07-REQ-3.1, 07-REQ-8.2
// ---------------------------------------------------------------------------

func TestProperty_GrantTypeAlwaysPresent(t *testing.T) {
	testCases := []struct {
		code        string
		redirectURI string
	}{
		{"code1", "http://localhost:8080/callback"},
		{"", ""},
		{"special-chars-!@#$%", "http://example.com/cb?param=value"},
		{"very-long-code-" + strings.Repeat("x", 100), "http://localhost/"},
		{"code", "https://example.com:443/auth/callback"},
	}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			var capturedBody url.Values

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Errorf("failed to parse form: %v", err)
				}
				capturedBody = r.PostForm
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

			_, _ = p.ExchangeCode(context.Background(), tc.code, tc.redirectURI)

			if capturedBody == nil {
				t.Fatal("no request was captured by mock server")
			}
			if capturedBody.Get("grant_type") != "authorization_code" {
				t.Errorf("grant_type = %q, want %q", capturedBody.Get("grant_type"), "authorization_code")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-07-P4: For any email address whose local part contains at least one
// alphanumeric or hyphen character, the username derivation produces a result
// of length 1-39 containing only [0-9A-Za-z-].
// Property: 07-PROP-4
// Validates: 07-REQ-5.1, 07-REQ-5.E1
// ---------------------------------------------------------------------------

func TestProperty_UsernameBounded(t *testing.T) {
	usernameRe := regexp.MustCompile(`^[0-9A-Za-z-]{1,39}$`)

	// Emails whose local parts contain at least one alphanumeric or hyphen.
	emails := []string{
		"a@example.com",
		"test-user@example.com",
		"TEST@EXAMPLE.COM",
		"a.b.c.d@example.com",
		"a+b+c@example.com",
		"0@example.com",
		"-@example.com",
		"a" + strings.Repeat("b", 100) + "@example.com", // very long local part
		"x._.!.#.$.y@example.com",
		"first-last@example.com",
		"1234567890123456789012345678901234567890@example.com", // 40 chars → truncation
	}

	for _, email := range emails {
		t.Run(email, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				resp := map[string]any{
					"id":             "12345",
					"email":          email,
					"verified_email": true,
					"name":           "Test",
				}
				json.NewEncoder(w).Encode(resp)
			}))
			defer ts.Close()

			cfg := ProviderConfig{
				Name:         "google",
				ClientID:     "id",
				ClientSecret: "secret",
				UserInfoURL:  ts.URL,
			}
			p := NewGoogleProvider(cfg)
			p.SetHTTPClient(ts.Client())

			info, err := p.GetUserInfo(context.Background(), "valid-token")

			if err != nil {
				t.Fatalf("GetUserInfo() error = %v, want nil for email %q", err, email)
			}
			if info == nil {
				t.Fatalf("GetUserInfo() returned nil UserInfo, want non-nil for email %q", email)
			}
			if !usernameRe.MatchString(info.Login) {
				t.Errorf("Login = %q (len=%d) does not match [0-9A-Za-z-]{1,39} for email %q",
					info.Login, len(info.Login), email)
			}
		})
	}
}
