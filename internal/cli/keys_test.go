package cli_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// onRouteWithContentType registers a route with an explicit content type.
// This extends the base stubServer (defined in root_test.go) for tests that
// need to return non-JSON responses (e.g. plain text for TS-03-16).
func (s *stubServer) onRouteWithContentType(method, path string, status int, body, contentType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[method+" "+path] = routeHandler{
		StatusCode:  status,
		Body:        body,
		ContentType: contentType,
	}
}

// lastRequestFor returns the most recent recorded request matching the given
// method and path, or nil if none found.
func (s *stubServer) lastRequestFor(method, path string) *recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.requests) - 1; i >= 0; i-- {
		if s.requests[i].Method == method && s.requests[i].Path == path {
			return &s.requests[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// TS-03-10: keys create — happy path, authenticated request, default expiry
// REQ: 03-REQ-3.1
// ---------------------------------------------------------------------------

func TestKeysCreate_HappyPath(t *testing.T) {
	// TS-03-10: Stub returns a key object with the composite key field per
	// spec 02 REQ-7.1 format: {"key": "af_<key_id>_<secret>", "key_id": "...", ...}.
	// Assert: authenticated POST sent with workspace_id, label, expires_days=30
	// (default when --expires omitted); stdout JSON contains the key field;
	// exit code 0.
	//
	// NOTE (reviewer finding): Spec 02 returns the secret embedded in the "key"
	// field as "af_<key_id>_<secret>", NOT as a standalone "secret" field.

	stub := newStubServer(t)
	keyObj := `{"key":"af_k1_plaintext-secret","key_id":"k1","workspace_id":"ws-123","label":"ci-bot","expires_at":"2026-02-01T00:00:00Z","role":"member","created_at":"2025-01-01T00:00:00Z"}`
	stub.onRoute("POST", "/api/v1/keys", http.StatusCreated, keyObj)

	stdout, _, exitCode := execAfc(t, []string{
		"keys", "create",
		"--workspace", "ws-123",
		"--label", "ci-bot",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// Assert the stub received the POST request.
	if !stub.receivedRequest("POST", "/api/v1/keys") {
		t.Fatal("expected POST /api/v1/keys to be called, but it was not")
	}

	// Verify the request was authenticated.
	req := stub.lastRequestFor("POST", "/api/v1/keys")
	if req == nil {
		t.Fatal("no POST /api/v1/keys request recorded")
	}
	// Check that auth header contains the API key (could be Bearer or X-API-Key).
	authHeader := req.Header.Get("Authorization")
	apiKeyHeader := req.Header.Get("X-API-Key")
	if !strings.Contains(authHeader, "myapikey") && apiKeyHeader != "myapikey" {
		t.Errorf("expected request to be authenticated with 'myapikey'; Authorization=%q, X-API-Key=%q",
			authHeader, apiKeyHeader)
	}

	// Verify the request body contains the right fields.
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err == nil {
		if ws, ok := body["workspace_id"]; !ok || ws != "ws-123" {
			t.Errorf("expected request body workspace_id='ws-123', got: %v", ws)
		}
		if label, ok := body["label"]; !ok || label != "ci-bot" {
			t.Errorf("expected request body label='ci-bot', got: %v", label)
		}
		// expires defaults to 30 when --expires is omitted.
		if expires, ok := body["expires"]; ok {
			if ex, isFloat := expires.(float64); isFloat && ex != 30 {
				t.Errorf("expected default expires=30, got: %v", expires)
			}
		}
	}

	// Assert stdout contains valid JSON with the key field.
	if !isValidJSON(stdout) {
		t.Fatalf("expected stdout to be valid JSON, got: %q", stdout)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed); err != nil {
		t.Fatalf("failed to parse stdout JSON: %v", err)
	}
	// Per spec 02: the plaintext key is in the "key" field.
	keyVal, ok := parsed["key"]
	if !ok {
		t.Error("expected stdout JSON to contain 'key' field (spec 02 format)")
	}
	if keyStr, isStr := keyVal.(string); isStr {
		if !strings.Contains(keyStr, "plaintext-secret") {
			t.Errorf("expected key field to contain 'plaintext-secret', got: %q", keyStr)
		}
	}
}

func TestKeysCreate_ExplicitExpiry(t *testing.T) {
	// TS-03-10 variant: Test with explicit --expires 90 to verify the value
	// is sent to the hub.

	stub := newStubServer(t)
	keyObj := `{"key":"af_k2_somesecret","key_id":"k2","workspace_id":"ws-456","expires_at":"2026-04-01T00:00:00Z","role":"member","created_at":"2025-01-01T00:00:00Z"}`
	stub.onRoute("POST", "/api/v1/keys", http.StatusCreated, keyObj)

	stdout, _, exitCode := execAfc(t, []string{
		"keys", "create",
		"--workspace", "ws-456",
		"--expires", "90",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
	if !isValidJSON(stdout) {
		t.Errorf("expected stdout to be valid JSON, got: %q", stdout)
	}

	// Verify request body has expires=90.
	req := stub.lastRequestFor("POST", "/api/v1/keys")
	if req != nil {
		var body map[string]any
		if err := json.Unmarshal([]byte(req.Body), &body); err == nil {
			if expires, ok := body["expires"]; ok {
				if ex, isFloat := expires.(float64); isFloat && ex != 90 {
					t.Errorf("expected expires=90, got: %v", expires)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TS-03-11: keys create — plaintext key printed exactly once to stdout
// REQ: 03-REQ-3.2, 03-PROP-2
// ---------------------------------------------------------------------------

func TestKeysCreate_SecretPrintedExactlyOnce(t *testing.T) {
	// TS-03-11: The composite key string containing the secret appears exactly
	// once in stdout and never in stderr.
	//
	// NOTE (reviewer finding): Spec 02 embeds the secret in the composite "key"
	// field. The plaintext is "af_k1_super-secret-xyz" — the secret portion
	// "super-secret-xyz" is what must appear exactly once in stdout.

	stub := newStubServer(t)
	keyObj := `{"key":"af_k1_super-secret-xyz","key_id":"k1","workspace_id":"ws-123","expires_at":"2026-02-01T00:00:00Z","role":"member","created_at":"2025-01-01T00:00:00Z"}`
	stub.onRoute("POST", "/api/v1/keys", http.StatusCreated, keyObj)

	stdout, stderr, exitCode := execAfc(t, []string{
		"keys", "create",
		"--workspace", "ws-123",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// The composite key (which contains the secret) should appear exactly once
	// in stdout.
	if count := strings.Count(stdout, "super-secret-xyz"); count != 1 {
		t.Errorf("expected 'super-secret-xyz' to appear exactly once in stdout, got %d occurrences", count)
	}

	// It should NOT appear in stderr.
	if strings.Contains(stderr, "super-secret-xyz") {
		t.Error("plaintext secret 'super-secret-xyz' must not appear in stderr")
	}
}

func TestKeysCreate_SecretProperty_Variations(t *testing.T) {
	// TS-03-P2: Property test — for various workspace_id, label, and expiry
	// combos, the plaintext key secret appears on stdout exactly once and
	// never on stderr.

	testCases := []struct {
		name      string
		workspace string
		label     string
		expires   string
		secret    string
	}{
		{"default-expiry", "ws-1", "bot", "", "secret-aaa"},
		{"zero-expiry", "ws-2", "ci", "0", "secret-bbb"},
		{"ninety-days", "ws-3", "", "90", "secret-ccc"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newStubServer(t)
			keyObj := `{"key":"af_k1_` + tc.secret + `","key_id":"k1","workspace_id":"` + tc.workspace + `","expires_at":"2026-02-01T00:00:00Z","role":"member","created_at":"2025-01-01T00:00:00Z"}`
			stub.onRoute("POST", "/api/v1/keys", http.StatusCreated, keyObj)

			args := []string{"keys", "create", "--workspace", tc.workspace, "--api-key", "testkey"}
			if tc.label != "" {
				args = append(args, "--label", tc.label)
			}
			if tc.expires != "" {
				args = append(args, "--expires", tc.expires)
			}

			stdout, stderr, exitCode := execAfc(t, args, "AF_HUB_URL="+stub.Server.URL)

			if exitCode != 0 {
				t.Errorf("expected exit code 0, got %d", exitCode)
			}
			if count := strings.Count(stdout, tc.secret); count != 1 {
				t.Errorf("expected secret %q to appear exactly once in stdout, got %d", tc.secret, count)
			}
			if strings.Contains(stderr, tc.secret) {
				t.Errorf("secret %q must not appear in stderr", tc.secret)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-03-E8: keys create — user not a member of workspace (403)
// REQ: 03-REQ-3.E1
// ---------------------------------------------------------------------------

func TestKeysCreate_NotMemberOfWorkspace(t *testing.T) {
	// TS-03-E8: Hub returns 403 with error envelope indicating non-membership.
	// Assert exit !=0 and stderr mentions 'member' or workspace ID.
	//
	// Uses nested error envelope per spec 02 REQ-8.1.

	stub := newStubServer(t)
	stub.onRoute("POST", "/api/v1/keys", http.StatusForbidden,
		`{"error":{"code":"403","message":"not a member of this workspace"}}`)

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "create",
		"--workspace", "ws-999",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on 403, got 0")
	}
	stderrLower := strings.ToLower(stderr)
	if !strings.Contains(stderrLower, "member") && !strings.Contains(stderr, "ws-999") {
		t.Errorf("expected stderr to mention 'member' or 'ws-999', got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-03-E9: keys create — missing --workspace flag
// REQ: 03-REQ-3.E2
// ---------------------------------------------------------------------------

func TestKeysCreate_MissingWorkspaceFlag(t *testing.T) {
	// TS-03-E9: Run keys create without --workspace. Assert exit !=0 and
	// stderr mentions 'workspace'.

	stub := newStubServer(t)
	stub.onRoute("POST", "/api/v1/keys", http.StatusCreated,
		`{"key":"af_k1_secret","key_id":"k1"}`)

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "create",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code when --workspace is missing, got 0")
	}
	if !strings.Contains(strings.ToLower(stderr), "workspace") {
		t.Errorf("expected stderr to mention 'workspace', got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-03-E10: keys create — missing credentials
// REQ: 03-REQ-3.E3
// ---------------------------------------------------------------------------

func TestKeysCreate_MissingCredentials(t *testing.T) {
	// TS-03-E10: Neither --api-key nor AF_HUB_API_KEY is provided. Assert
	// exit !=0 and stderr mentions api-key/credential/auth.

	stub := newStubServer(t)
	stub.onRoute("POST", "/api/v1/keys", http.StatusCreated,
		`{"key":"af_k1_secret","key_id":"k1"}`)

	// Do NOT pass --api-key and ensure AF_HUB_API_KEY is not in the env.
	_, stderr, exitCode := execAfc(t, []string{
		"keys", "create",
		"--workspace", "ws-123",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code when credentials are missing, got 0")
	}
	stderrLower := strings.ToLower(stderr)
	hasCred := strings.Contains(stderrLower, "api-key") ||
		strings.Contains(stderrLower, "credential") ||
		strings.Contains(stderrLower, "auth") ||
		strings.Contains(stderr, "AF_HUB_API_KEY")
	if !hasCred {
		t.Errorf("expected stderr to mention api-key/credential/auth/AF_HUB_API_KEY, got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-03-12: keys list — happy path with full field verification
// REQ: 03-REQ-4.1
// ---------------------------------------------------------------------------

func TestKeysList_HappyPath(t *testing.T) {
	// TS-03-12: Stub returns a JSON array of two key objects. Assert stdout
	// is a valid JSON array and each element contains key_id, label,
	// workspace_id, expires_at, created_at, and revoked fields.

	stub := newStubServer(t)
	keysJSON := `[
		{"key_id":"k1","label":"bot","workspace_id":"ws-1","expires_at":"2026-01-01T00:00:00Z","created_at":"2025-01-01T00:00:00Z","revoked":false},
		{"key_id":"k2","label":"ci","workspace_id":"ws-2","expires_at":null,"created_at":"2025-06-01T00:00:00Z","revoked":true}
	]`
	stub.onRoute("GET", "/api/v1/keys", http.StatusOK, keysJSON)

	stdout, _, exitCode := execAfc(t, []string{
		"keys", "list",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
	if !isValidJSON(stdout) {
		t.Fatalf("expected stdout to be valid JSON, got: %q", stdout)
	}

	var arr []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &arr); err != nil {
		t.Fatalf("failed to parse JSON array: %v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("expected 2 items in array, got %d", len(arr))
	}

	requiredFields := []string{"key_id", "label", "workspace_id", "expires_at", "created_at", "revoked"}
	for i, item := range arr {
		for _, field := range requiredFields {
			if _, ok := item[field]; !ok {
				t.Errorf("item[%d] missing required field %q", i, field)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TS-03-E11: keys list — HTTP error (401) with error envelope
// REQ: 03-REQ-4.E1
// ---------------------------------------------------------------------------

func TestKeysList_HTTPError(t *testing.T) {
	// TS-03-E11: Hub returns 401 with error envelope. Assert exit !=0 and
	// stderr contains the human-readable message from the envelope.
	//
	// Uses nested error envelope per spec 02 REQ-8.1.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/keys", http.StatusUnauthorized,
		`{"error":{"code":"401","message":"Invalid API key"}}`)

	stdout, stderr, exitCode := execAfc(t, []string{
		"keys", "list",
		"--api-key", "invalid-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on 401, got 0")
	}
	if !strings.Contains(stderr, "Invalid API key") {
		t.Errorf("expected stderr to contain 'Invalid API key', got: %q", stderr)
	}
	// stdout should be empty on error.
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("expected stdout to be empty on error, got: %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// TS-03-13: keys refresh — happy path with new secret
// REQ: 03-REQ-5.1
// ---------------------------------------------------------------------------

func TestKeysRefresh_HappyPath(t *testing.T) {
	// TS-03-13: Stub returns an updated key object with new composite key
	// "af_key-id-42_refreshed-secret-abc" per spec 02 REQ-7.4 format.
	// Assert: authenticated request sent to refresh endpoint; stdout JSON
	// contains the new key with secret; exit code 0.
	//
	// NOTE (reviewer finding): Spec 02 REQ-7.4 returns
	// {"key": "af_<key_id>_<new_secret>", "key_id": "..."}, NOT a standalone
	// "secret" field. Test stubs updated accordingly.

	stub := newStubServer(t)
	keyObj := `{"key":"af_key-id-42_refreshed-secret-abc","key_id":"key-id-42"}`
	stub.onRoute("POST", "/api/v1/keys/key-id-42/refresh", http.StatusOK, keyObj)

	stdout, _, exitCode := execAfc(t, []string{
		"keys", "refresh", "key-id-42",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// Assert the refresh endpoint was called.
	if !stub.receivedRequest("POST", "/api/v1/keys/key-id-42/refresh") {
		t.Error("expected POST /api/v1/keys/key-id-42/refresh to be called, but it was not")
	}

	// Assert stdout is valid JSON.
	if !isValidJSON(stdout) {
		t.Fatalf("expected stdout to be valid JSON, got: %q", stdout)
	}

	// Assert the new key (containing the secret) is in the output.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed); err != nil {
		t.Fatalf("failed to parse stdout JSON: %v", err)
	}
	keyVal, ok := parsed["key"]
	if !ok {
		t.Error("expected stdout JSON to contain 'key' field (spec 02 format)")
	}
	if keyStr, isStr := keyVal.(string); isStr {
		if !strings.Contains(keyStr, "refreshed-secret-abc") {
			t.Errorf("expected key field to contain 'refreshed-secret-abc', got: %q", keyStr)
		}
	}
}

func TestKeysRefresh_SecretProperty(t *testing.T) {
	// TS-03-P2: Property test for keys refresh — the plaintext key secret
	// appears on stdout exactly once and never on stderr.

	stub := newStubServer(t)
	keyObj := `{"key":"af_k1_refresh-prop-secret","key_id":"k1"}`
	stub.onRoute("POST", "/api/v1/keys/k1/refresh", http.StatusOK, keyObj)

	stdout, stderr, exitCode := execAfc(t, []string{
		"keys", "refresh", "k1",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
	if count := strings.Count(stdout, "refresh-prop-secret"); count != 1 {
		t.Errorf("expected 'refresh-prop-secret' exactly once in stdout, got %d", count)
	}
	if strings.Contains(stderr, "refresh-prop-secret") {
		t.Error("secret 'refresh-prop-secret' must not appear in stderr")
	}
}

// ---------------------------------------------------------------------------
// TS-03-E12: keys refresh — key not found (404)
// REQ: 03-REQ-5.E1
// ---------------------------------------------------------------------------

func TestKeysRefresh_KeyNotFound(t *testing.T) {
	// TS-03-E12: Hub returns 404 with error envelope. Assert exit !=0 and
	// stderr contains 'Key not found'.

	stub := newStubServer(t)
	stub.onRoute("POST", "/api/v1/keys/nonexistent-key/refresh", http.StatusNotFound,
		`{"error":{"code":"404","message":"Key not found"}}`)

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "refresh", "nonexistent-key",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on 404, got 0")
	}
	if !strings.Contains(stderr, "Key not found") {
		t.Errorf("expected stderr to contain 'Key not found', got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-03-E13: keys refresh — no key_id argument
// REQ: 03-REQ-5.E2
// ---------------------------------------------------------------------------

func TestKeysRefresh_NoKeyIDArgument(t *testing.T) {
	// TS-03-E13: Run keys refresh without a key_id argument. Assert exit !=0
	// and stderr references key/argument/required.

	stub := newStubServer(t)
	// The stub is here just in case the command still tries to reach the hub.
	stub.onRoute("POST", "/api/v1/keys", http.StatusOK, `{}`)

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "refresh",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code when key_id is missing, got 0")
	}
	stderrLower := strings.ToLower(stderr)
	hasRef := strings.Contains(stderrLower, "key") ||
		strings.Contains(stderrLower, "argument") ||
		strings.Contains(stderrLower, "required")
	if !hasRef {
		t.Errorf("expected stderr to mention key/argument/required, got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-03-14: keys revoke — happy path
// REQ: 03-REQ-6.1
// ---------------------------------------------------------------------------

func TestKeysRevoke_HappyPath(t *testing.T) {
	// TS-03-14: Stub returns HTTP 200 from DELETE /api/v1/keys/key-id-42.
	// Assert: revoke request sent; stderr contains a confirmation message
	// (referencing revocation or the key ID); stdout is empty; exit 0.

	stub := newStubServer(t)
	// Per spec 02 REQ-7.5: revoke returns {"message": "key revoked"}.
	stub.onRoute("DELETE", "/api/v1/keys/key-id-42", http.StatusOK,
		`{"message":"key revoked"}`)

	stdout, stderr, exitCode := execAfc(t, []string{
		"keys", "revoke", "key-id-42",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// Assert revoke request was sent.
	if !stub.receivedRequest("DELETE", "/api/v1/keys/key-id-42") {
		t.Error("expected DELETE /api/v1/keys/key-id-42 to be called, but it was not")
	}

	// Assert stderr contains a confirmation message.
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected non-empty confirmation message on stderr")
	}
	stderrLower := strings.ToLower(stderr)
	hasConfirmation := strings.Contains(stderrLower, "revok") ||
		strings.Contains(stderrLower, "success") ||
		strings.Contains(stderr, "key-id-42")
	if !hasConfirmation {
		t.Errorf("expected stderr to contain 'revok', 'success', or 'key-id-42', got: %q", stderr)
	}

	// stdout should be empty or contain no key data.
	stdoutTrimmed := strings.TrimSpace(stdout)
	if stdoutTrimmed != "" {
		t.Errorf("expected stdout to be empty after revoke, got: %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// TS-03-E14: keys revoke — key not found (404)
// REQ: 03-REQ-6.E1
// ---------------------------------------------------------------------------

func TestKeysRevoke_KeyNotFound(t *testing.T) {
	// TS-03-E14: Hub returns 404 with error envelope. Assert exit !=0 and
	// stderr contains 'Key not found'.

	stub := newStubServer(t)
	stub.onRoute("DELETE", "/api/v1/keys/nonexistent-key", http.StatusNotFound,
		`{"error":{"code":"404","message":"Key not found"}}`)

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "revoke", "nonexistent-key",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on 404, got 0")
	}
	if !strings.Contains(stderr, "Key not found") {
		t.Errorf("expected stderr to contain 'Key not found', got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-03-E15: keys revoke — no key_id argument
// REQ: 03-REQ-6.E2
// ---------------------------------------------------------------------------

func TestKeysRevoke_NoKeyIDArgument(t *testing.T) {
	// TS-03-E15: Run keys revoke without a key_id argument. Assert exit !=0
	// and stderr references key/argument/required.

	stub := newStubServer(t)
	stub.onRoute("DELETE", "/api/v1/keys", http.StatusOK, `{}`)

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "revoke",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code when key_id is missing, got 0")
	}
	stderrLower := strings.ToLower(stderr)
	hasRef := strings.Contains(stderrLower, "key") ||
		strings.Contains(stderrLower, "argument") ||
		strings.Contains(stderrLower, "required")
	if !hasRef {
		t.Errorf("expected stderr to mention key/argument/required, got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-03-15: HTTP error envelope parsing — structured error envelope
// REQ: 03-REQ-7.1
// ---------------------------------------------------------------------------

func TestHTTPErrorEnvelope_StructuredError(t *testing.T) {
	// TS-03-15: Hub returns 403 with nested error envelope per spec 02 REQ-8.1:
	// {"error":{"code":"403","message":"You do not have access"}}
	// Assert: exit !=0; stderr contains the human-readable message; stdout empty.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/keys", http.StatusForbidden,
		`{"error":{"code":"403","message":"You do not have access"}}`)

	stdout, stderr, exitCode := execAfc(t, []string{
		"keys", "list",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on 403, got 0")
	}
	if !strings.Contains(stderr, "You do not have access") {
		t.Errorf("expected stderr to contain 'You do not have access', got: %q", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("expected stdout to be empty on error, got: %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// TS-03-16: HTTP error envelope fallback — unparseable error body
// REQ: 03-REQ-7.2
// ---------------------------------------------------------------------------

func TestHTTPErrorEnvelope_FallbackRawBody(t *testing.T) {
	// TS-03-16: Hub returns 502 with plain-text body 'Bad Gateway'.
	// Assert: exit !=0; stderr contains '502' and 'Bad Gateway'.

	stub := newStubServer(t)
	stub.onRouteWithContentType("GET", "/api/v1/keys", http.StatusBadGateway,
		"Bad Gateway", "text/plain")

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "list",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on 502, got 0")
	}
	if !strings.Contains(stderr, "502") {
		t.Errorf("expected stderr to contain '502', got: %q", stderr)
	}
	if !strings.Contains(stderr, "Bad Gateway") {
		t.Errorf("expected stderr to contain 'Bad Gateway', got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-03-P3 additional: keys revoke JSON/stderr invariant
// REQ: 03-PROP-3
// ---------------------------------------------------------------------------

func TestKeysRevoke_OutputStreamProperty(t *testing.T) {
	// TS-03-P3: For keys revoke, confirmation goes to stderr (not stdout).
	// Stdout should be empty; stderr should have the confirmation.

	stub := newStubServer(t)
	stub.onRoute("DELETE", "/api/v1/keys/k1", http.StatusOK,
		`{"message":"key revoked"}`)

	stdout, stderr, exitCode := execAfc(t, []string{
		"keys", "revoke", "k1",
		"--api-key", "myapikey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// stdout should be empty for revoke (confirmation goes to stderr).
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("expected empty stdout for revoke, got: %q", stdout)
	}
	// stderr should have the confirmation.
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected non-empty stderr confirmation for revoke")
	}
}

// ---------------------------------------------------------------------------
// TS-03-E10 additional: missing credentials on keys list
// REQ: 03-REQ-3.E3 (applies to all key management commands)
// ---------------------------------------------------------------------------

func TestKeysList_MissingCredentials(t *testing.T) {
	// The missing credential check applies to all key management commands,
	// not just keys create. Verify keys list also fails without credentials.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/keys", http.StatusOK, `[]`)

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "list",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code when credentials are missing on keys list, got 0")
	}
	stderrLower := strings.ToLower(stderr)
	hasCred := strings.Contains(stderrLower, "api-key") ||
		strings.Contains(stderrLower, "credential") ||
		strings.Contains(stderrLower, "auth") ||
		strings.Contains(stderr, "AF_HUB_API_KEY")
	if !hasCred {
		t.Errorf("expected stderr to mention api-key/credential/auth/AF_HUB_API_KEY, got: %q", stderr)
	}
}

func TestKeysRefresh_MissingCredentials(t *testing.T) {
	// Missing credential check for keys refresh.

	stub := newStubServer(t)
	stub.onRoute("POST", "/api/v1/keys/k1/refresh", http.StatusOK, `{}`)

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "refresh", "k1",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code when credentials are missing on keys refresh, got 0")
	}
	stderrLower := strings.ToLower(stderr)
	hasCred := strings.Contains(stderrLower, "api-key") ||
		strings.Contains(stderrLower, "credential") ||
		strings.Contains(stderrLower, "auth") ||
		strings.Contains(stderr, "AF_HUB_API_KEY")
	if !hasCred {
		t.Errorf("expected stderr to mention api-key/credential/auth/AF_HUB_API_KEY, got: %q", stderr)
	}
}

func TestKeysRevoke_MissingCredentials(t *testing.T) {
	// Missing credential check for keys revoke.

	stub := newStubServer(t)
	stub.onRoute("DELETE", "/api/v1/keys/k1", http.StatusOK, `{}`)

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "revoke", "k1",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code when credentials are missing on keys revoke, got 0")
	}
	stderrLower := strings.ToLower(stderr)
	hasCred := strings.Contains(stderrLower, "api-key") ||
		strings.Contains(stderrLower, "credential") ||
		strings.Contains(stderrLower, "auth") ||
		strings.Contains(stderr, "AF_HUB_API_KEY")
	if !hasCred {
		t.Errorf("expected stderr to mention api-key/credential/auth/AF_HUB_API_KEY, got: %q", stderr)
	}
}
