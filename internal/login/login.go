// Package login implements the OAuth authorization code flow for the afc CLI.
package login

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"time"
)

// DefaultTimeout is the maximum time to wait for an OAuth callback.
const DefaultTimeout = 2 * time.Minute

// BrowserOpenFunc is the function used to open a URL in the user's browser.
// It can be replaced in tests to capture or suppress browser opens.
var BrowserOpenFunc = openBrowser

// ListenFunc is the function used to create a TCP listener. It can be
// replaced in tests to simulate port binding failures.
var ListenFunc = net.Listen

func openBrowser(url string) error {
	// Stub: will be implemented with github.com/pkg/browser.
	return fmt.Errorf("browser open not implemented")
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

// FetchProviders fetches the list of supported OAuth providers from the hub.
// Returns an error if the request fails or returns a non-2xx response.
func FetchProviders(hubURL, apiKey string, client *http.Client) ([]Provider, error) {
	// Stub: not implemented yet.
	return nil, fmt.Errorf("FetchProviders not implemented")
}

// ExchangeCode exchanges an authorization code for user credentials by
// calling POST /api/v1/auth/callback on the hub.
func ExchangeCode(hubURL string, provider, code, redirectURI string, expires int, client *http.Client) (*CallbackResponse, error) {
	// Stub: not implemented yet.
	return nil, fmt.Errorf("ExchangeCode not implemented")
}

// BuildAuthorizationURL constructs the full authorization URL from a provider's
// base authorize_url, adding state and redirect_uri query parameters.
func BuildAuthorizationURL(baseURL, state, redirectURI string) string {
	// Stub: not implemented yet - will add state and redirect_uri params.
	return baseURL
}
