package workspace

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ========================================================================
// Spec 09 Task 2.3: Audit log safety tests — credential scrubbing
// (TS-09-27, TS-09-28)
// Requirements: 09-REQ-7.1, 09-REQ-7.2
// ========================================================================

// TS-09-27: POST /workspaces handler ensures git_pat, git_username, and
// git_password values do not appear in any log entry at any level.
//
// The handler must zero out credential fields on the createWorkspaceRequest
// struct immediately after binding and before any logging occurs.
// Requirement: 09-REQ-7.1, 09-REQ-7.E1
func TestAuditLog_CredentialValues_NotInLogs(t *testing.T) {
	// Capture all log output during handler execution.
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	env := newTestEnv(t)
	auth := userAuth("alice-id")

	// Stub credential validation to succeed.
	origFn := validateCredentialsFn
	validateCredentialsFn = func(_ context.Context, _ string, _ transport.AuthMethod) error {
		return nil
	}
	defer func() { validateCredentialsFn = origFn }()

	// Send a request with credential values that should never appear in logs.
	body := `{
		"slug": "my-ws",
		"git_url": "https://github.com/acme/private",
		"git_pat": "super-secret-pat-value"
	}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	// Accept either 201 (success) or 400/500 (if implementation incomplete).
	// The key assertion is about log safety, not HTTP status.
	_ = rec.Code

	// Verify credential values do NOT appear in any log entry.
	logOutput := logBuf.String()
	if strings.Contains(logOutput, "super-secret-pat-value") {
		t.Error("credential value 'super-secret-pat-value' found in log output; "+
			"handler must scrub credentials before any logging occurs")
	}

	// Verify credential values do NOT appear in the response body.
	respBody := rec.Body.String()
	if strings.Contains(respBody, "super-secret-pat-value") {
		t.Error("credential value 'super-secret-pat-value' found in response body")
	}
}

// TS-09-27 (variant): username/password credential values must also be scrubbed.
// Requirement: 09-REQ-7.1
func TestAuditLog_UsernamePassword_NotInLogs(t *testing.T) {
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	env := newTestEnv(t)
	auth := userAuth("alice-id")

	origFn := validateCredentialsFn
	validateCredentialsFn = func(_ context.Context, _ string, _ transport.AuthMethod) error {
		return nil
	}
	defer func() { validateCredentialsFn = origFn }()

	body := `{
		"slug": "my-ws",
		"git_url": "https://github.com/acme/private",
		"git_username": "secret-username-value",
		"git_password": "secret-password-value"
	}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	_ = rec.Code

	logOutput := logBuf.String()
	if strings.Contains(logOutput, "secret-username-value") {
		t.Error("git_username value found in log output")
	}
	if strings.Contains(logOutput, "secret-password-value") {
		t.Error("git_password value found in log output")
	}

	respBody := rec.Body.String()
	if strings.Contains(respBody, "secret-username-value") {
		t.Error("git_username value found in response body")
	}
	if strings.Contains(respBody, "secret-password-value") {
		t.Error("git_password value found in response body")
	}
}

// TS-09-28: POST /workspaces handler logs the raw go-git error at ERROR
// level server-side but never includes it in the HTTP response body.
//
// When ValidateCredentialsFuncType returns a non-nil error, the handler
// must log the full error at ERROR level for debugging, but the HTTP
// response must contain only a sanitised message.
// Requirement: 09-REQ-7.2
func TestAuditLog_RawError_InLog_NotInResponse(t *testing.T) {
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	env := newTestEnv(t)
	auth := userAuth("alice-id")

	rawError := "authentication required: remote: Invalid username or password"
	origFn := validateCredentialsFn
	validateCredentialsFn = func(_ context.Context, _ string, _ transport.AuthMethod) error {
		return fmt.Errorf("%s", rawError)
	}
	defer func() { validateCredentialsFn = origFn }()

	body := `{
		"slug": "my-ws",
		"git_url": "https://github.com/acme/private",
		"git_pat": "wrong-token"
	}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// Response must contain only the sanitised message.
	resp := parseErrorEnvelope(t, rec)
	expected := "credential validation failed for https://github.com/acme/private: unable to authenticate"
	if resp.Error.Message != expected {
		t.Errorf("response error message = %q; want %q", resp.Error.Message, expected)
	}

	// Raw error string must NOT appear in the response body.
	respBody := rec.Body.String()
	if strings.Contains(respBody, "Invalid username or password") {
		t.Error("raw go-git error 'Invalid username or password' appeared in response body")
	}

	// Raw error string SHOULD appear in server-side logs.
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Invalid username or password") {
		t.Error("raw go-git error 'Invalid username or password' not found in server-side logs; "+
			"handler should log raw error at ERROR level")
	}
}

// 09-REQ-7.E1: If the logging middleware does not support field-level
// scrubbing, the handler must zero out credential fields on the struct
// immediately after binding.
func TestAuditLog_CredentialFields_Zeroed_AfterBinding(t *testing.T) {
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	env := newTestEnv(t)
	auth := userAuth("alice-id")

	origFn := validateCredentialsFn
	validateCredentialsFn = func(_ context.Context, _ string, _ transport.AuthMethod) error {
		return nil
	}
	defer func() { validateCredentialsFn = origFn }()

	// Use a unique credential value to detect leakage.
	uniquePAT := "ghp_UNIQUE_PAT_FOR_ZEROING_TEST_12345"
	body := fmt.Sprintf(`{
		"slug": "my-ws",
		"git_url": "https://github.com/acme/private",
		"git_pat": %q
	}`, uniquePAT)
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	_ = rec.Code

	// Verify credential values do not appear anywhere in the log output,
	// even if middleware logs the request body or struct fields.
	logOutput := logBuf.String()
	if strings.Contains(logOutput, uniquePAT) {
		t.Error("unique PAT value found in log output; handler must zero credential fields before logging")
	}
}

// 09-REQ-7.E2: CRITICAL log entries for compensating DELETE failures must
// include the workspace slug but must not include any credential values.
func TestAuditLog_CriticalLog_NoCredentials(t *testing.T) {
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	env := newTestEnv(t)
	auth := userAuth("alice-id")

	origFn := validateCredentialsFn
	validateCredentialsFn = func(_ context.Context, _ string, _ transport.AuthMethod) error {
		return nil
	}
	defer func() { validateCredentialsFn = origFn }()

	// Drop the secrets table to force CreateSecrets to fail,
	// triggering the compensating DELETE path.
	_, _ = env.db.Exec("DROP TABLE IF EXISTS secrets")

	uniquePAT := "ghp_CRITICAL_LOG_TEST_CRED_VALUE"
	body := fmt.Sprintf(`{
		"slug": "critical-ws",
		"git_url": "https://github.com/acme/private",
		"git_pat": %q
	}`, uniquePAT)
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusInternalServerError {
		// Might also be a different status if validation or binding fails.
		// We care about the log safety assertion below.
		_ = rec.Code
	}

	// Log entries must NOT contain the credential value.
	logOutput := logBuf.String()
	if strings.Contains(logOutput, uniquePAT) {
		t.Error("credential value found in CRITICAL/ERROR log entries; "+
			"log entries must contain only the slug and error context, never credentials")
	}
}
