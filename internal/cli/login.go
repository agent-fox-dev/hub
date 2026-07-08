package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// newLoginCmd creates the "login" subcommand for OAuth authentication.
func newLoginCmd() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with the hub via OAuth",
		Long:  "Run the OAuth authorization code flow to authenticate with af-hub.",
		RunE: func(cmd *cobra.Command, args []string) error {
			hub, err := resolveHubURL()
			if err != nil {
				return err
			}

			return runLogin(cmd, hub, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "github", "OAuth provider name")

	return cmd
}

// providerInfo represents a provider entry returned by the hub.
type providerInfo struct {
	Name         string `json:"name"`
	AuthorizeURL string `json:"authorize_url"`
}

// runLogin executes the full OAuth login flow.
func runLogin(cmd *cobra.Command, hubURL, provider string) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	// Step 1: Fetch and validate the provider list.
	providers, err := fetchProviders(hubURL)
	if err != nil {
		return err
	}

	var selectedProvider *providerInfo
	var availableNames []string
	for i := range providers {
		availableNames = append(availableNames, providers[i].Name)
		if providers[i].Name == provider {
			selectedProvider = &providers[i]
		}
	}

	if selectedProvider == nil {
		return fmt.Errorf("provider %q not found; available providers: %s", provider, strings.Join(availableNames, ", "))
	}

	// Step 2: Start a local callback server on a random port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to start callback server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)

	// Channel to receive the callback result.
	type callbackResult struct {
		code             string
		errCode          string
		errDescription   string
	}
	resultCh := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		if errCode := q.Get("error"); errCode != "" {
			resultCh <- callbackResult{
				errCode:        errCode,
				errDescription: q.Get("error_description"),
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, "<html><body><h1>Authentication Error</h1><p>%s: %s</p><p>You may close this window.</p></body></html>",
				errCode, q.Get("error_description"))
			return
		}

		code := q.Get("code")
		if code == "" {
			resultCh <- callbackResult{errCode: "missing_code", errDescription: "No authorization code received"}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body><h1>Error</h1><p>No authorization code received.</p></body></html>")
			return
		}

		resultCh <- callbackResult{code: code}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html><body><h1>Authentication Successful</h1><p>You may close this window.</p></body></html>")
	})

	srv := &http.Server{Handler: mux}

	// Set up signal handling for clean shutdown.
	sigCtx, sigCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer sigCancel()

	// Start serving.
	go func() {
		if serveErr := srv.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			// Server error — push an error through the result channel.
			resultCh <- callbackResult{errCode: "server_error", errDescription: serveErr.Error()}
		}
	}()

	// Ensure the server is always shut down.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(stderr, "Warning: callback server shutdown error: %v\n", err)
		}
	}()

	// Step 3: Open the authorization URL in the default browser.
	authURL := buildAuthURL(selectedProvider.AuthorizeURL, redirectURI)
	fmt.Fprintf(stderr, "Opening browser to %s\n", authURL)
	fmt.Fprintf(stderr, "Waiting for OAuth callback on http://localhost:%d/callback\n", port)
	openBrowser(authURL)

	// Step 4: Wait for the callback with timeout.
	callbackTimeout := getCallbackTimeout()

	select {
	case result := <-resultCh:
		if result.errCode != "" {
			desc := result.errDescription
			if desc == "" {
				desc = result.errCode
			}
			return fmt.Errorf("OAuth error: %s: %s", result.errCode, desc)
		}

		// Exchange the code with the hub.
		return exchangeCode(stdout, stderr, hubURL, provider, result.code, redirectURI)

	case <-time.After(callbackTimeout):
		return fmt.Errorf("login timed out waiting for OAuth callback; please try again")

	case <-sigCtx.Done():
		return fmt.Errorf("login interrupted")
	}
}

// fetchProviders fetches the provider list from the hub.
func fetchProviders(hubURL string) ([]providerInfo, error) {
	resp, err := http.Get(hubURL + "/api/v1/auth/providers")
	if err != nil {
		return nil, fmt.Errorf("failed to connect to hub at %s: %w", hubURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ParseHTTPError(resp)
	}

	var providers []providerInfo
	if err := json.NewDecoder(resp.Body).Decode(&providers); err != nil {
		return nil, fmt.Errorf("failed to decode provider list: %w", err)
	}
	return providers, nil
}

// exchangeCode sends the authorization code to the hub and prints the user object.
func exchangeCode(stdout, _ interface{ Write([]byte) (int, error) }, hubURL, provider, code, redirectURI string) error {
	body := map[string]string{
		"provider":     provider,
		"code":         code,
		"redirect_uri": redirectURI,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal callback request: %w", err)
	}

	resp, err := http.Post(hubURL+"/api/v1/auth/callback", "application/json", bytes.NewReader(bodyJSON))
	if err != nil {
		return fmt.Errorf("failed to send callback to hub: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ParseHTTPError(resp)
	}

	// Decode and print the user object to stdout.
	var result json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode user response: %w", err)
	}

	return PrintJSON(stdout, result)
}

// buildAuthURL constructs the OAuth authorization URL with the redirect_uri.
func buildAuthURL(authorizeURL, redirectURI string) string {
	u, err := url.Parse(authorizeURL)
	if err != nil {
		return authorizeURL + "?redirect_uri=" + url.QueryEscape(redirectURI)
	}
	q := u.Query()
	q.Set("redirect_uri", redirectURI)
	u.RawQuery = q.Encode()
	return u.String()
}

// openBrowser opens a URL in the default browser using platform-appropriate commands.
// Set AFC_SKIP_BROWSER=1 to suppress (used by tests).
func openBrowser(url string) {
	if os.Getenv("AFC_SKIP_BROWSER") == "1" {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	cmd.Start()
}

// getCallbackTimeout returns the callback timeout duration.
// It checks the AFC_CALLBACK_TIMEOUT environment variable first (in seconds),
// falling back to 5 minutes.
func getCallbackTimeout() time.Duration {
	if envTimeout := os.Getenv("AFC_CALLBACK_TIMEOUT"); envTimeout != "" {
		if secs, err := strconv.Atoi(envTimeout); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 5 * time.Minute
}
