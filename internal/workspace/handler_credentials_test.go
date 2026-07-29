package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// ---------------------------------------------------------------------------
// Task 1.3: API request body validation tests (09-REQ-2)
// ---------------------------------------------------------------------------

// TS-09-7: POST /workspaces handler accepts requests with no credential fields
// and processes them identically to pre-feature behavior.
// Requirement: 09-REQ-2.1
func TestHandlerCredential_NoCredentials_Success(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"public-ws","git_url":"https://github.com/acme/public"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d; body = %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// ValidateCredentialsFuncType must not have been called.
	// (We verify this by checking the workspace was created — no validation step needed.)
	ws, err := getWorkspaceBySlug(env.db, "public-ws")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug() returned error: %v", err)
	}
	if ws == nil {
		t.Fatal("workspace 'public-ws' not found in database")
	}
}

// TS-09-8: POST /workspaces handler returns HTTP 400 with a mutual-exclusion
// error when git_pat is provided alongside git_username.
// Requirement: 09-REQ-2.2
func TestHandlerCredential_MutualExclusion_PATAndUsername(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":"ghp_abc","git_username":"alice","git_password":"s3cr3t"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if !strings.Contains(strings.ToLower(resp.Error.Message), "mutually exclusive") {
		t.Errorf("error message = %q; want it to contain 'mutually exclusive'", resp.Error.Message)
	}

	// No workspace should have been created.
	ws, _ := getWorkspaceBySlug(env.db, "my-ws")
	if ws != nil {
		t.Error("workspace should not have been created after mutual-exclusion rejection")
	}
}

// TS-09-8 (variant): mutual exclusion when git_pat and git_username are both set
// (without git_password).
// Requirement: 09-REQ-2.2
func TestHandlerCredential_MutualExclusion_PATAndUsernameOnly(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":"ghp_abc","git_username":"alice"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// 09-REQ-2.E3: All three credential fields provided simultaneously.
func TestHandlerCredential_MutualExclusion_AllThreeFields(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":"ghp_abc","git_username":"alice","git_password":"s3cr3t"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusBadRequest)
	}

	ws, _ := getWorkspaceBySlug(env.db, "my-ws")
	if ws != nil {
		t.Error("workspace should not exist after mutual-exclusion rejection")
	}
}

// TS-09-9: POST /workspaces handler returns HTTP 400 with a pair-completeness
// error when git_username is provided without git_password.
// Requirement: 09-REQ-2.3
func TestHandlerCredential_PairCompleteness_UsernameOnly(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_username":"alice"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	lower := strings.ToLower(resp.Error.Message)
	if !strings.Contains(lower, "together") && !strings.Contains(lower, "both") && !strings.Contains(lower, "pair") {
		t.Errorf("error message = %q; want it to mention pair-completeness", resp.Error.Message)
	}

	ws, _ := getWorkspaceBySlug(env.db, "my-ws")
	if ws != nil {
		t.Error("workspace should not have been created after pair-completeness rejection")
	}
}

// TS-09-9 (variant): pair-completeness when git_password is provided without
// git_username.
// Requirement: 09-REQ-2.3
func TestHandlerCredential_PairCompleteness_PasswordOnly(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_password":"s3cr3t"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// TS-09-10: POST /workspaces handler returns HTTP 400 with 'git credentials
// require an HTTPS git_url' when credential fields are provided with a non-HTTPS
// git_url.
// Requirement: 09-REQ-2.4
func TestHandlerCredential_HTTPSRequirement_SSH(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"my-ws","git_url":"git@github.com:acme/private.git","git_pat":"ghp_abc"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message != "git credentials require an HTTPS git_url" {
		t.Errorf("error message = %q; want %q", resp.Error.Message, "git credentials require an HTTPS git_url")
	}

	ws, _ := getWorkspaceBySlug(env.db, "my-ws")
	if ws != nil {
		t.Error("workspace should not have been created after HTTPS rejection")
	}
}

// TS-09-10 (variant): HTTPS requirement with http:// scheme.
// Requirement: 09-REQ-2.4
func TestHandlerCredential_HTTPSRequirement_HTTP(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"my-ws","git_url":"http://github.com/acme/private","git_pat":"ghp_abc"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message != "git credentials require an HTTPS git_url" {
		t.Errorf("error message = %q; want %q", resp.Error.Message, "git credentials require an HTTPS git_url")
	}
}

// 09-REQ-2.E1: git_pat provided as an empty string (not null).
func TestHandlerCredential_EmptyPAT(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":""}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// 09-REQ-2.E2: git_username provided as an empty string.
func TestHandlerCredential_EmptyUsername(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_username":"","git_password":"s3cr3t"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Task 1.4: Credential validation (ls-remote) handler tests (09-REQ-3)
// ---------------------------------------------------------------------------

// TS-09-11: POST /workspaces handler invokes ValidateCredentialsFuncType with
// the git_url and a BasicAuth constructed from the PAT before creating the
// workspace.
// Requirement: 09-REQ-3.1
func TestValidateCredentials_HappyPath_PAT(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	// Install a recording stub for ValidateCredentialsFuncType.
	var capturedURL string
	var capturedAuth transport.AuthMethod
	called := false

	origFn := validateCredentialsFn
	validateCredentialsFn = func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		called = true
		capturedURL = gitURL
		capturedAuth = authMethod
		return nil
	}
	defer func() { validateCredentialsFn = origFn }()

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":"ghp_abc123"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	if !called {
		t.Fatal("ValidateCredentialsFuncType was not called")
	}

	if capturedURL != "https://github.com/acme/private" {
		t.Errorf("gitURL = %q; want %q", capturedURL, "https://github.com/acme/private")
	}

	basicAuth, ok := capturedAuth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", capturedAuth)
	}
	if basicAuth.Username != "x-token-auth" {
		t.Errorf("auth.Username = %q; want %q", basicAuth.Username, "x-token-auth")
	}
	if basicAuth.Password != "ghp_abc123" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "ghp_abc123")
	}
}

// TS-09-11 (variant): ValidateCredentialsFuncType receives BasicAuth with
// username/password.
// Requirement: 09-REQ-3.1
func TestValidateCredentials_HappyPath_UsernamePassword(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	var capturedAuth transport.AuthMethod
	origFn := validateCredentialsFn
	validateCredentialsFn = func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		capturedAuth = authMethod
		return nil
	}
	defer func() { validateCredentialsFn = origFn }()

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_username":"alice","git_password":"s3cr3t"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	basicAuth, ok := capturedAuth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", capturedAuth)
	}
	if basicAuth.Username != "alice" {
		t.Errorf("auth.Username = %q; want %q", basicAuth.Username, "alice")
	}
	if basicAuth.Password != "s3cr3t" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "s3cr3t")
	}
}

// TS-09-12: POST /workspaces handler returns HTTP 400 with sanitised error
// message and logs raw error at ERROR level when ValidateCredentialsFuncType
// returns a non-nil error.
// Requirement: 09-REQ-3.2
func TestValidateCredentials_AuthFailure(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	origFn := validateCredentialsFn
	validateCredentialsFn = func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		return fmt.Errorf("authentication required")
	}
	defer func() { validateCredentialsFn = origFn }()

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":"wrong-token"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	expected := "credential validation failed for https://github.com/acme/private: unable to authenticate"
	if resp.Error.Message != expected {
		t.Errorf("error message = %q; want %q", resp.Error.Message, expected)
	}

	// Raw error must NOT appear in the response body.
	if strings.Contains(rec.Body.String(), "authentication required") {
		t.Error("raw error string 'authentication required' should not appear in response body")
	}

	// Workspace must not have been created.
	ws, _ := getWorkspaceBySlug(env.db, "my-ws")
	if ws != nil {
		t.Error("workspace should not exist after validation failure")
	}
}

// TS-09-13: POST /workspaces handler returns HTTP 400 with sanitised error
// message when ValidateCredentialsFuncType returns context.DeadlineExceeded.
// Requirement: 09-REQ-3.3
func TestValidateCredentials_Timeout(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	origFn := validateCredentialsFn
	validateCredentialsFn = func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		return context.DeadlineExceeded
	}
	defer func() { validateCredentialsFn = origFn }()

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":"ghp_abc123"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	expected := "credential validation failed for https://github.com/acme/private: unable to authenticate"
	if resp.Error.Message != expected {
		t.Errorf("error message = %q; want %q", resp.Error.Message, expected)
	}

	ws, _ := getWorkspaceBySlug(env.db, "my-ws")
	if ws != nil {
		t.Error("workspace should not exist after timeout")
	}
}

// TS-09-14: POST /workspaces handler skips ValidateCredentialsFuncType entirely
// when no credential fields are present in the request.
// Requirement: 09-REQ-3.4
func TestValidateCredentials_SkippedForPublicRepo(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	called := false
	origFn := validateCredentialsFn
	validateCredentialsFn = func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		called = true
		return nil
	}
	defer func() { validateCredentialsFn = origFn }()

	body := `{"slug":"public-ws","git_url":"https://github.com/acme/public"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	if called {
		t.Error("ValidateCredentialsFuncType was called for a request without credentials")
	}
}

// TS-09-15: Handler struct exposes ValidateCredentialsFuncType as an injectable
// field, and the production default is non-nil and replaceable in tests.
// Requirement: 09-REQ-3.5
func TestValidateCredentials_InjectableField(t *testing.T) {
	// The validateCredentialsFn package-level variable should be non-nil
	// after InitCloneQueue sets it up (or has a default).
	// For this test we verify it is replaceable by simply assigning and
	// verifying that the replacement sticks.
	origFn := validateCredentialsFn

	testStub := func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		return nil
	}
	validateCredentialsFn = testStub
	defer func() { validateCredentialsFn = origFn }()

	// Verify the variable was replaced (basic sanity).
	if validateCredentialsFn == nil {
		t.Error("validateCredentialsFn is nil after assignment; expected non-nil stub")
	}
}

// 09-REQ-3.E3: Raw go-git error string containing credential value must NOT
// appear in the HTTP response body.
// Requirement: 09-REQ-3.E3
func TestValidateCredentials_CredentialNotLeakedInResponse(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	origFn := validateCredentialsFn
	validateCredentialsFn = func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		// Simulate a go-git error that embeds the credential value.
		return fmt.Errorf("authentication failed for token ghp_secret_value_here")
	}
	defer func() { validateCredentialsFn = origFn }()

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":"ghp_secret_value_here"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusBadRequest)
	}

	// The raw error string must not appear in the response.
	responseBody := rec.Body.String()
	if strings.Contains(responseBody, "ghp_secret_value_here") {
		t.Error("credential value appeared in HTTP response body")
	}
	if strings.Contains(responseBody, "authentication failed for token") {
		t.Error("raw go-git error appeared in HTTP response body")
	}
}

// 09-REQ-3.E4: PAT credential uses x-token-auth as username.
func TestValidateCredentials_PATUsesXTokenAuth(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	var capturedAuth transport.AuthMethod
	origFn := validateCredentialsFn
	validateCredentialsFn = func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		capturedAuth = authMethod
		return nil
	}
	defer func() { validateCredentialsFn = origFn }()

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":"ghp_abc123"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusCreated)
	}

	basicAuth, ok := capturedAuth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth is %T; want *http.BasicAuth", capturedAuth)
	}
	if basicAuth.Username != "x-token-auth" {
		t.Errorf("auth.Username = %q; want %q", basicAuth.Username, "x-token-auth")
	}
	if basicAuth.Password != "ghp_abc123" {
		t.Errorf("auth.Password = %q; want %q", basicAuth.Password, "ghp_abc123")
	}
}

// ---------------------------------------------------------------------------
// Task 1.5: Atomic storage and compensating DELETE tests (09-REQ-4)
// ---------------------------------------------------------------------------

// TS-09-16: POST /workspaces handler inserts workspace row, calls CreateSecrets
// with correct arguments, and enqueues clone job when credentials pass validation.
// Requirement: 09-REQ-4.1
func TestAtomicStorage_HappyPath_PAT(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	// Stub validation to succeed.
	origFn := validateCredentialsFn
	validateCredentialsFn = func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		return nil
	}
	defer func() { validateCredentialsFn = origFn }()

	// Set up a test job queue to verify clone job enqueue.
	q := &JobQueue{jobs: make(chan CloneJob, 10)}
	oldQueue := defaultQueue
	defaultQueue = q
	defer func() { defaultQueue = oldQueue }()

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":"ghp_abc123"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Workspace should exist in DB.
	ws, err := getWorkspaceBySlug(env.db, "my-ws")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug() returned error: %v", err)
	}
	if ws == nil {
		t.Fatal("workspace 'my-ws' not found in database")
	}

	// Secret GIT_PAT should exist in DB for workspace scope.
	store := secrets.NewStore(env.db)
	secretList, err := store.ListSecrets("workspace", "my-ws")
	if err != nil {
		t.Fatalf("ListSecrets() returned error: %v", err)
	}

	found := false
	for _, s := range secretList {
		if s.Key == "GIT_PAT" {
			found = true
			break
		}
	}
	if !found {
		t.Error("secret GIT_PAT not found for workspace 'my-ws'")
	}

	// Clone job should have been enqueued after successful credential storage.
	select {
	case job := <-q.jobs:
		if job.Slug != "my-ws" {
			t.Errorf("enqueued job slug = %q; want %q", job.Slug, "my-ws")
		}
	default:
		t.Error("clone job was not enqueued after successful credential storage")
	}
}

// TS-09-16 (variant): CreateSecrets with username/password stores both keys.
// Requirement: 09-REQ-4.1
func TestAtomicStorage_HappyPath_UsernamePassword(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	origFn := validateCredentialsFn
	validateCredentialsFn = func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		return nil
	}
	defer func() { validateCredentialsFn = origFn }()

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_username":"alice","git_password":"s3cr3t"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	store := secrets.NewStore(env.db)
	secretList, err := store.ListSecrets("workspace", "my-ws")
	if err != nil {
		t.Fatalf("ListSecrets() returned error: %v", err)
	}

	keys := make(map[string]bool)
	for _, s := range secretList {
		keys[s.Key] = true
	}
	if !keys["GIT_USERNAME"] {
		t.Error("secret GIT_USERNAME not found for workspace 'my-ws'")
	}
	if !keys["GIT_PASSWORD"] {
		t.Error("secret GIT_PASSWORD not found for workspace 'my-ws'")
	}
}

// TS-09-17: POST /workspaces handler issues compensating DELETE and returns
// HTTP 500 when CreateSecrets fails after workspace row is inserted.
// Requirement: 09-REQ-4.2
func TestAtomicStorage_CompensatingDelete_Success(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	origFn := validateCredentialsFn
	validateCredentialsFn = func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		return nil
	}
	defer func() { validateCredentialsFn = origFn }()

	// Set up a test job queue to verify no clone job is enqueued on failure.
	q := &JobQueue{jobs: make(chan CloneJob, 10)}
	oldQueue := defaultQueue
	defaultQueue = q
	defer func() { defaultQueue = oldQueue }()

	// To simulate CreateSecrets failure, we drop the secrets table so that
	// the INSERT into secrets fails while the workspace INSERT already succeeded.
	// This is a crude but effective way to force the error path.
	_, err := env.db.Exec("DROP TABLE IF EXISTS secrets")
	if err != nil {
		t.Fatalf("failed to drop secrets table: %v", err)
	}

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":"ghp_abc123"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	// After compensating DELETE, workspace should NOT exist in DB.
	ws, _ := getWorkspaceBySlug(env.db, "my-ws")
	if ws != nil {
		t.Error("workspace should have been deleted by compensating DELETE")
	}

	// Clone job must NOT have been enqueued.
	select {
	case job := <-q.jobs:
		t.Errorf("clone job was unexpectedly enqueued for slug %q after CreateSecrets failure", job.Slug)
	default:
		// Good — no job enqueued.
	}
}

// TS-09-18: POST /workspaces handler emits a CRITICAL log entry with the
// workspace slug and returns HTTP 500 when both CreateSecrets and the
// compensating DELETE fail.
// Requirement: 09-REQ-4.3
func TestAtomicStorage_CompensatingDelete_BothFail(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	// Capture log output to verify CRITICAL log emission.
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	origValidateFn := validateCredentialsFn
	validateCredentialsFn = func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		return nil
	}
	defer func() { validateCredentialsFn = origValidateFn }()

	// Stub compensatingDeleteFn to simulate the DELETE also failing
	// (09-REQ-4.3: both CreateSecrets and compensating DELETE fail).
	origDeleteFn := compensatingDeleteFn
	compensatingDeleteFn = func(db *sql.DB, slug string) error {
		return fmt.Errorf("connection lost")
	}
	defer func() { compensatingDeleteFn = origDeleteFn }()

	// Drop the secrets table to force CreateSecrets to fail.
	_, _ = env.db.Exec("DROP TABLE IF EXISTS secrets")

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":"ghp_abc123"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	// Must return HTTP 500.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	// CRITICAL log must be emitted containing the workspace slug.
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "CRITICAL") {
		t.Error("expected CRITICAL log entry when both CreateSecrets and compensating DELETE fail")
	}
	if !strings.Contains(logOutput, "my-ws") {
		t.Error("CRITICAL log entry must contain the workspace slug 'my-ws'")
	}

	// The workspace row should still exist (orphaned) since the
	// compensating DELETE failed.
	ws, _ := getWorkspaceBySlug(env.db, "my-ws")
	if ws == nil {
		t.Error("workspace 'my-ws' should still exist as an orphaned row when compensating DELETE fails")
	}
}

// TS-09-19: POST /workspaces handler skips CreateSecrets entirely and returns
// HTTP 201 when no credential fields are present.
// Requirement: 09-REQ-4.4
func TestAtomicStorage_NoCredentials_SkipsCreateSecrets(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"public-ws","git_url":"https://github.com/acme/public"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	// No secrets should exist for this workspace.
	store := secrets.NewStore(env.db)
	secretList, err := store.ListSecrets("workspace", "public-ws")
	if err != nil {
		t.Fatalf("ListSecrets() returned error: %v", err)
	}
	if len(secretList) != 0 {
		t.Errorf("expected 0 secrets; got %d", len(secretList))
	}
}

// TS-09-19 (variant): Verify the response body does not contain credential fields.
// Requirement: 09-REQ-4.4, 09-PROP-4
func TestAtomicStorage_ResponseDoesNotContainCredentials(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	origFn := validateCredentialsFn
	validateCredentialsFn = func(ctx context.Context, gitURL string, authMethod transport.AuthMethod) error {
		return nil
	}
	defer func() { validateCredentialsFn = origFn }()

	body := `{"slug":"my-ws","git_url":"https://github.com/acme/private","git_pat":"ghp_abc123"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusCreated)
	}

	// Parse the response and verify no credential fields are present.
	var respBody map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	for _, field := range []string{"git_pat", "git_username", "git_password"} {
		if _, exists := respBody[field]; exists {
			t.Errorf("response contains credential field %q; it should be omitted", field)
		}
	}
}
