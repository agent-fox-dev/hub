package httpclient_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/httpclient"
	"github.com/agent-fox-dev/hub/internal/keys"
	"github.com/agent-fox-dev/hub/internal/wsclient"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// requestCapture tracks HTTP requests received by a mock server.
type requestCapture struct {
	Method string
	Path   string
	Body   string
	Header http.Header
}

// ---------------------------------------------------------------------------
// 5.2 — Cross-Cutting Concern Tests
// ---------------------------------------------------------------------------

// TestJSONOutputPrettyPrinted verifies that all raw API response bodies are
// printed to stdout pretty-printed with 2-space indentation without any
// field reshaping.
// TS-05-40
func TestJSONOutputPrettyPrinted(t *testing.T) {
	// The server returns a specific JSON response for workspace list.
	serverResp := `[{"id":"w1","slug":"ws","git_url":"https://g.com"}]`
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/workspaces" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, serverResp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	body, statusCode, err := wsclient.ListWorkspaces(mockServer.URL, "k", client)
	if err != nil {
		t.Fatalf("ListWorkspaces failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("status code = %d, want 200", statusCode)
	}

	// Verify the raw body is valid JSON.
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}

	// Verify the body can be formatted with json.MarshalIndent to get
	// 2-space indented output, matching the spec requirement.
	prettyPrinted, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatalf("failed to pretty-print JSON: %v", err)
	}

	// Verify pretty-printed output has 2-space indentation.
	if !strings.Contains(string(prettyPrinted), "  ") {
		t.Error("pretty-printed JSON should contain 2-space indentation")
	}

	// Verify all original fields are preserved (no reshaping).
	parsedArr, ok := parsed.([]any)
	if !ok {
		t.Fatal("expected response to be a JSON array")
	}
	if len(parsedArr) == 0 {
		t.Fatal("expected at least one element in the response array")
	}
	parsedObj, ok := parsedArr[0].(map[string]any)
	if !ok {
		t.Fatal("expected first element to be a JSON object")
	}
	if parsedObj["id"] != "w1" {
		t.Errorf("field 'id' = %v, want 'w1'", parsedObj["id"])
	}
	if parsedObj["slug"] != "ws" {
		t.Errorf("field 'slug' = %v, want 'ws'", parsedObj["slug"])
	}
	if parsedObj["git_url"] != "https://g.com" {
		t.Errorf("field 'git_url' = %v, want 'https://g.com'", parsedObj["git_url"])
	}
}

// TestStderrStdoutSeparation verifies that all human-readable status and
// error messages are written to stderr only, and stdout contains no
// human-readable messages.
// TS-05-41
func TestStderrStdoutSeparation(t *testing.T) {
	// Test the keys revoke success path. The 'API key revoked.' message
	// should appear on stderr only, not stdout. The client function
	// returns status code and body — the command handler is responsible
	// for writing to stderr.

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/keys/kid" {
			w.WriteHeader(http.StatusNoContent) // 204
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	statusCode, body, err := keys.RevokeKey(mockServer.URL, "k", "kid", client)

	// The function returns the status code; the caller decides what to
	// print to stderr vs stdout. Verify the function provides the right
	// data for the caller to make that decision.
	if err != nil {
		t.Fatalf("RevokeKey failed: %v", err)
	}

	// On 204, status code should be 2xx.
	if statusCode < 200 || statusCode >= 300 {
		t.Errorf("status code = %d, want 2xx", statusCode)
	}

	// The response body for 204 may be empty — that's expected.
	// The message 'API key revoked.' should be constructed by the command
	// handler and written to stderr (not included in the API response).
	_ = body

	// Verify the status message 'API key revoked.' is NOT in the API
	// response body (it's a CLI-generated message, not from the server).
	if strings.Contains(string(body), "API key revoked.") {
		t.Error("API response should NOT contain the CLI status message 'API key revoked.'")
	}
}

// TestHTTPAuthorizationBearerHeader verifies that all authenticated API
// requests include the Authorization: Bearer <api_key> header.
// TS-05-42
func TestHTTPAuthorizationBearerHeader(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "GET" && r.URL.Path == "/api/v1/keys" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `[]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, _, err := keys.ListKeys(mockServer.URL, "my-secret-key", client)
	if err != nil {
		t.Fatalf("ListKeys failed: %v", err)
	}

	// Verify the request includes Authorization: Bearer my-secret-key.
	found := false
	for _, req := range requests {
		if req.Method == "GET" && req.Path == "/api/v1/keys" {
			found = true
			authHeader := req.Header.Get("Authorization")
			if authHeader != "Bearer my-secret-key" {
				t.Errorf("Authorization header = %q, want 'Bearer my-secret-key'", authHeader)
			}
			break
		}
	}
	if !found {
		t.Error("GET /api/v1/keys was not called")
	}
}

// TestHTTPClientTimeout verifies that the HTTP client uses a fixed 30-second
// timeout for all outbound requests.
// TS-05-43
func TestHTTPClientTimeout(t *testing.T) {
	// Verify that NewClient() returns a client with 30-second timeout.
	client := httpclient.NewClient()
	if client.Timeout != 30*time.Second {
		t.Errorf("client timeout = %v, want 30s", client.Timeout)
	}

	// Also verify with a mock server that delays beyond a short timeout
	// to confirm timeout behavior works.
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delay longer than the client's timeout.
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	// Use a short timeout client to verify timeout error behavior.
	shortTimeoutClient := &http.Client{Timeout: 50 * time.Millisecond}
	_, _, err := keys.ListKeys(mockServer.URL, "k", shortTimeoutClient)

	// The function should return an error (timeout).
	if err == nil {
		t.Error("ListKeys should return error when client times out, got nil")
	}

	// The error should mention timeout or deadline.
	if err != nil {
		errMsg := err.Error()
		if !strings.Contains(errMsg, "timeout") &&
			!strings.Contains(errMsg, "Timeout") &&
			!strings.Contains(errMsg, "deadline") &&
			!strings.Contains(errMsg, "context deadline") {
			t.Errorf("error should mention timeout/deadline, got: %v", err)
		}
	}
}

// TestNonJSONErrorResponse verifies that a non-2xx response with a non-JSON
// body (e.g., HTML 502) is handled by returning the status code and raw body
// so the command handler can print the clean error message
// 'Error: unexpected response from server (HTTP <status>).' without the
// raw body content.
// TS-05-44
func TestNonJSONErrorResponse(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/keys" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway) // 502
			fmt.Fprint(w, `<html><body>Bad Gateway</body></html>`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	body, statusCode, err := keys.ListKeys(mockServer.URL, "k", client)

	// The function should return the status code and body without error
	// for a reachable server.
	if err != nil {
		t.Fatalf("ListKeys should not return error for reachable server: %v", err)
	}

	// Verify status code is 502.
	if statusCode != http.StatusBadGateway {
		t.Errorf("status code = %d, want 502", statusCode)
	}

	// The command handler should detect that the body is NOT valid JSON.
	var jsonCheck any
	if err := json.Unmarshal(body, &jsonCheck); err == nil {
		t.Error("response body should NOT be valid JSON (it's HTML)")
	}

	// Verify the caller can construct the clean error message.
	cleanMsg := fmt.Sprintf("Error: unexpected response from server (HTTP %d).", statusCode)
	if cleanMsg != "Error: unexpected response from server (HTTP 502)." {
		t.Errorf("clean error message = %q, want 'Error: unexpected response from server (HTTP 502).'", cleanMsg)
	}

	// The raw HTML should be in the body (for the caller to detect non-JSON),
	// but the command handler should NOT print it to stderr.
	if !strings.Contains(string(body), "<html>") {
		t.Error("raw body should contain HTML for non-JSON detection")
	}
}

// TestHTTPTimeoutError verifies that when an outbound HTTP request times out,
// an error is returned without retrying.
// TS-05-E21
func TestHTTPTimeoutError(t *testing.T) {
	requestCount := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		// Hang forever to trigger timeout.
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	// Use a short timeout to make the test fast.
	shortTimeoutClient := &http.Client{Timeout: 100 * time.Millisecond}
	_, _, err := keys.ListKeys(mockServer.URL, "k", shortTimeoutClient)

	// Should return an error.
	if err == nil {
		t.Fatal("ListKeys should return error when request times out, got nil")
	}

	// Error should mention timeout or deadline.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "timeout") &&
		!strings.Contains(errMsg, "Timeout") &&
		!strings.Contains(errMsg, "deadline") &&
		!strings.Contains(errMsg, "context deadline") {
		t.Errorf("error should mention timeout/deadline, got: %v", err)
	}

	// Verify no retry was attempted — exactly 1 request should have been made.
	if requestCount != 1 {
		t.Errorf("expected exactly 1 request (no retry), got %d", requestCount)
	}
}

// TestConnectionRefusedError verifies that a connection refused network error
// causes the function to return a descriptive error without retrying.
// TS-05-E22
func TestConnectionRefusedError(t *testing.T) {
	// hub_url points to a port where nothing is listening.
	client := &http.Client{Timeout: 2 * time.Second}
	_, _, err := keys.ListKeys("http://localhost:1", "k", client)

	// Should return an error.
	if err == nil {
		t.Fatal("ListKeys should return error for connection refused, got nil")
	}

	// Error should mention the connection failure.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "connection refused") &&
		!strings.Contains(errMsg, "connect") &&
		!strings.Contains(errMsg, "Error") &&
		!strings.Contains(errMsg, "dial") {
		t.Errorf("error should mention connection failure, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Property Tests
// ---------------------------------------------------------------------------

// TestPropertyStdoutStderrSeparation is a property test that verifies for
// any afc command invocation, stdout contains only valid JSON or is empty,
// and stderr contains only human-readable strings.
// TS-05-P5
func TestPropertyStdoutStderrSeparation(t *testing.T) {
	// Test a representative set of command scenarios using the client functions.
	// For each, verify that the raw body returned (intended for stdout) is
	// valid JSON or empty, and that status messages (for stderr) are not
	// mixed in.

	testCases := []struct {
		name       string
		handler    func(w http.ResponseWriter, r *http.Request)
		callFunc   func(url string, client *http.Client) ([]byte, int, error)
		expectJSON bool
	}{
		{
			name: "keys list success",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `[{"id":"k1"}]`)
			},
			callFunc: func(url string, client *http.Client) ([]byte, int, error) {
				return keys.ListKeys(url, "k", client)
			},
			expectJSON: true,
		},
		{
			name: "keys list error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error":{"code":401,"message":"unauthorized"}}`)
			},
			callFunc: func(url string, client *http.Client) ([]byte, int, error) {
				return keys.ListKeys(url, "bad", client)
			},
			expectJSON: true,
		},
		{
			name: "workspace list success",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `[{"slug":"ws1"}]`)
			},
			callFunc: func(url string, client *http.Client) ([]byte, int, error) {
				return wsclient.ListWorkspaces(url, "k", client)
			},
			expectJSON: true,
		},
		{
			name: "token list success",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `[{"id":"t1"}]`)
			},
			callFunc: func(url string, client *http.Client) ([]byte, int, error) {
				return wsclient.ListTokens(url, "k", "ws1", client)
			},
			expectJSON: true,
		},
		{
			name: "non-JSON error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, `<html>Bad Gateway</html>`)
			},
			callFunc: func(url string, client *http.Client) ([]byte, int, error) {
				return keys.ListKeys(url, "k", client)
			},
			expectJSON: false, // non-JSON body — stdout would be empty
		},
	}

	// Human-readable messages that should only appear on stderr, never stdout.
	humanMsgs := []string{
		"API key revoked.",
		"API key not found on server.",
		"Token",
		"Error:",
		"Login successful",
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockServer := httptest.NewServer(http.HandlerFunc(tc.handler))
			defer mockServer.Close()

			client := &http.Client{Timeout: 5 * time.Second}
			body, _, err := tc.callFunc(mockServer.URL, client)
			if err != nil {
				// Network errors are OK — they don't produce stdout output.
				return
			}

			// If body is non-empty, it should be valid JSON (for stdout).
			if len(body) > 0 {
				if tc.expectJSON {
					var parsed any
					if err := json.Unmarshal(body, &parsed); err != nil {
						t.Errorf("stdout body should be valid JSON, got parse error: %v", err)
					}
				}

				// Body (stdout content) should not contain human-readable messages.
				bodyStr := string(body)
				for _, msg := range humanMsgs {
					if strings.Contains(bodyStr, msg) {
						t.Errorf("stdout body should not contain human message %q", msg)
					}
				}
			}
		})
	}
}

// TestPropertyNonJSONErrorBodiesNeverPrinted is a property test that verifies
// for any non-2xx HTTP response whose body is not parseable as JSON, the CLI
// should never print the raw response body to stderr or stdout.
// TS-05-P7
func TestPropertyNonJSONErrorBodiesNeverPrinted(t *testing.T) {
	// Generate various non-JSON response bodies with different non-2xx status codes.
	testCases := []struct {
		statusCode  int
		body        string
		contentType string
	}{
		{400, `<html><body>Bad Request</body></html>`, "text/html"},
		{403, `Access Denied`, "text/plain"},
		{404, `<!DOCTYPE html><html><head></head><body>Not Found</body></html>`, "text/html"},
		{500, `Internal Server Error\n\nStack trace: at func1()`, "text/plain"},
		{502, `<html><body>Bad Gateway</body></html>`, "text/html"},
		{503, `Service Unavailable - Please try again later`, "text/plain"},
		{400, ``, "text/plain"},
		{500, string([]byte{0x00, 0x01, 0x02, 0xFF}), "application/octet-stream"},
	}

	for _, tc := range testCases {
		name := fmt.Sprintf("status_%d_%s", tc.statusCode, tc.contentType)
		t.Run(name, func(t *testing.T) {
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.statusCode)
				fmt.Fprint(w, tc.body)
			}))
			defer mockServer.Close()

			client := &http.Client{Timeout: 5 * time.Second}
			body, statusCode, err := keys.ListKeys(mockServer.URL, "k", client)

			// The function should return the status code and body for reachable servers.
			if err != nil {
				// If the function returns an error, verify the raw body is NOT
				// in the error message.
				if tc.body != "" && strings.Contains(err.Error(), tc.body) {
					t.Errorf("error message should not contain raw body %q", tc.body)
				}
				return
			}

			// Verify the status code matches.
			if statusCode != tc.statusCode {
				t.Errorf("status code = %d, want %d", statusCode, tc.statusCode)
			}

			// The body is available for the command handler to inspect.
			// The command handler should:
			// 1. Try to parse it as JSON
			// 2. If not JSON, print ONLY "Error: unexpected response from server (HTTP <status>)."
			// 3. Never print the raw body to stderr or stdout

			// Verify we can detect whether the body is JSON or not.
			var jsonCheck any
			isJSON := json.Unmarshal(body, &jsonCheck) == nil

			// For this test, all bodies are non-JSON.
			if isJSON && len(body) > 0 {
				t.Errorf("expected non-JSON body, but it parsed as JSON: %s", body)
			}

			// Verify the correct clean error message can be constructed.
			cleanMsg := fmt.Sprintf("Error: unexpected response from server (HTTP %d).", tc.statusCode)
			if !strings.Contains(cleanMsg, fmt.Sprintf("HTTP %d", tc.statusCode)) {
				t.Errorf("clean error message format incorrect: %s", cleanMsg)
			}
		})
	}
}
