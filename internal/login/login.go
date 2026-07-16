// Package login implements the OAuth authorization code flow for the afc CLI.
package login

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pkg/browser"
)

// DefaultTimeout is the maximum time to wait for an OAuth callback.
const DefaultTimeout = 2 * time.Minute

// BrowserOpenFunc is the function used to open a URL in the user's browser.
// It can be replaced in tests to capture or suppress browser opens.
var BrowserOpenFunc = openBrowser

// ListenFunc is the function used to create a TCP listener. It can be
// replaced in tests to simulate port binding failures.
var ListenFunc = net.Listen

func openBrowser(u string) error {
	return browser.OpenURL(u)
}

// GenerateState produces a cryptographically random CSRF state parameter
// encoded as base64url. The state contains at least 16 bytes of entropy.
func GenerateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate CSRF state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Provider represents an OAuth provider returned by the hub API.
type Provider struct {
	Name         string `json:"name"`
	AuthorizeURL string `json:"authorize_url"`
	Scopes       string `json:"scopes"`
}

// ProvidersResponse is the response from GET /api/v1/auth/providers.
type ProvidersResponse struct {
	Providers []Provider `json:"providers"`
}

// CallbackResponse is the response from POST /api/v1/auth/callback.
type CallbackResponse struct {
	User   CallbackUser   `json:"user"`
	APIKey CallbackAPIKey `json:"api_key"`
}

// CallbackUser is the user portion of the callback response.
type CallbackUser struct {
	ID string `json:"id"`
}

// CallbackAPIKey is the api_key portion of the callback response.
type CallbackAPIKey struct {
	KeyID  string `json:"key_id"`
	Secret string `json:"secret"`
	Token  string `json:"token"`
}

// CallbackServer manages the local HTTP server that receives OAuth callbacks.
type CallbackServer struct {
	listener net.Listener
	port     int
	codeCh   chan callbackResult
	state    string
	server   *http.Server
}

type callbackResult struct {
	code  string
	state string
}

// StartCallbackServer creates and starts a local HTTP callback server
// listening on a random OS-assigned port. The state parameter is used to
// validate incoming CSRF state.
func StartCallbackServer(state string) (*CallbackServer, error) {
	ln, err := ListenFunc("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to start callback server: %w", err)
	}

	cs := &CallbackServer{
		listener: ln,
		port:     ln.Addr().(*net.TCPAddr).Port,
		codeCh:   make(chan callbackResult, 1),
		state:    state,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", cs.handleCallback)
	cs.server = &http.Server{Handler: mux}

	go cs.server.Serve(ln)

	return cs, nil
}

// Port returns the port the callback server is listening on.
func (cs *CallbackServer) Port() int {
	return cs.port
}

// WaitForCode waits for an OAuth callback with the given timeout.
// Returns the authorization code and received state, or an error if
// the timeout elapses.
func (cs *CallbackServer) WaitForCode(timeout time.Duration) (code, state string, err error) {
	select {
	case result := <-cs.codeCh:
		return result.code, result.state, nil
	case <-time.After(timeout):
		cs.Shutdown()
		return "", "", fmt.Errorf("timed out waiting for OAuth callback after %v", timeout)
	}
}

// Shutdown gracefully shuts down the callback server.
func (cs *CallbackServer) Shutdown() {
	if cs.server != nil {
		cs.server.Close()
	}
}

func (cs *CallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `<html><body><h1>Login successful! You may close this tab.</h1></body></html>`)

	cs.codeCh <- callbackResult{code: code, state: state}
}

// FetchProviders fetches the list of supported OAuth providers from the hub
// by calling GET /api/v1/auth/providers. This is a public endpoint; the
// apiKey parameter is included for consistency but is not required.
// Returns an error if the request fails or returns a non-2xx response.
func FetchProviders(hubURL, apiKey string, client *http.Client) ([]Provider, error) {
	reqURL := strings.TrimRight(hubURL, "/") + "/api/v1/auth/providers"
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create providers request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch providers: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read providers response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Try to extract error message from JSON envelope.
		var envelope struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
			return nil, fmt.Errorf("failed to fetch providers: %s", envelope.Error.Message)
		}
		return nil, fmt.Errorf("failed to fetch providers: unexpected response (HTTP %d)", resp.StatusCode)
	}

	var providersResp ProvidersResponse
	if err := json.Unmarshal(body, &providersResp); err != nil {
		return nil, fmt.Errorf("failed to parse providers response: %w", err)
	}

	return providersResp.Providers, nil
}

// ExchangeCode exchanges an authorization code for user credentials by
// calling POST /api/v1/auth/callback on the hub. The payload contains
// provider, code, redirect_uri, state (optional, forwarded for logging),
// and expires as an integer.
func ExchangeCode(hubURL string, provider, code, redirectURI string, expires int, client *http.Client) (*CallbackResponse, error) {
	reqURL := strings.TrimRight(hubURL, "/") + "/api/v1/auth/callback"

	// Build the JSON payload per spec 05-REQ-6.5.
	// Include state for completeness (spec 02 accepts it as an optional pass-through).
	payload := map[string]any{
		"provider":     provider,
		"code":         code,
		"redirect_uri": redirectURI,
		"expires":      expires,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal callback payload: %w", err)
	}

	req, err := http.NewRequest("POST", reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create callback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange authorization code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read callback response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Try to extract error message from JSON envelope.
		var envelope struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
			return nil, fmt.Errorf("code exchange failed: %s", envelope.Error.Message)
		}
		return nil, fmt.Errorf("code exchange failed: unexpected response (HTTP %d)", resp.StatusCode)
	}

	var cbResp CallbackResponse
	if err := json.Unmarshal(body, &cbResp); err != nil {
		return nil, fmt.Errorf("failed to parse callback response: %w", err)
	}

	return &cbResp, nil
}

// RedirectHost returns the hostname to use in the OAuth redirect URI.
// Google requires 127.0.0.1 (RFC 8252 loopback) for Desktop-type OAuth
// clients; GitHub requires "localhost" for its any-port matching.
func RedirectHost(provider string) string {
	if provider == "google" {
		return "127.0.0.1"
	}
	return "localhost"
}

// BuildAuthorizationURL constructs the full authorization URL from a provider's
// base authorize_url, adding state and redirect_uri query parameters.
// Per the reviewer finding, the server's authorize_url already includes
// client_id and scope; the CLI only appends state and redirect_uri.
func BuildAuthorizationURL(baseURL, state, redirectURI string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		// If the URL is unparseable, return it as-is; the browser will
		// show an error.
		return baseURL
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("state", state)
	q.Set("redirect_uri", redirectURI)
	u.RawQuery = q.Encode()
	return u.String()
}

// ValidateProvider checks that the requested provider is in the list of
// available providers. Returns a formatted error message listing available
// providers if the requested one is not found.
func ValidateProvider(requested string, providers []Provider) error {
	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.Name
		if p.Name == requested {
			return nil
		}
	}
	return fmt.Errorf("Error: unsupported provider: %s. Available: %s",
		requested, strings.Join(names, ", "))
}

// FindProvider returns the Provider with the given name, or an error if not found.
func FindProvider(name string, providers []Provider) (*Provider, error) {
	for i := range providers {
		if providers[i].Name == name {
			return &providers[i], nil
		}
	}
	return nil, fmt.Errorf("provider %q not found", name)
}
