package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// credRequestRecord captures the body of a POST /api/v1/workspaces request
// for credential flag tests.
type credRequestRecord struct {
	Slug        string  `json:"slug"`
	GitURL      string  `json:"git_url"`
	GitPAT      *string `json:"git_pat"`
	GitUsername  *string `json:"git_username"`
	GitPassword *string `json:"git_password"`
}

// credMockServer creates a test server that records all incoming
// POST /api/v1/workspaces requests and returns HTTP 201 by default.
// If respondCode/respondBody are set, the server returns those instead.
func credMockServer(t *testing.T, respondCode int, respondBody string) (*httptest.Server, *[]credRequestRecord) {
	t.Helper()
	var mu sync.Mutex
	records := &[]credRequestRecord{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)

		var rec credRequestRecord
		_ = json.Unmarshal(bodyBytes, &rec)
		mu.Lock()
		*records = append(*records, rec)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if respondCode != 0 {
			w.WriteHeader(respondCode)
			w.Write([]byte(respondBody)) //nolint:errcheck
			return
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"slug":       rec.Slug,
			"git_url":    rec.GitURL,
			"owner_id":   "test-user-id",
			"status":     "active",
			"created_at": "2025-01-01T00:00:00Z",
			"updated_at": "2025-01-01T00:00:00Z",
		})
	}))

	return server, records
}

func getCredRecords(records *[]credRequestRecord) []credRequestRecord {
	return append([]credRequestRecord{}, *records...)
}

// TS-09-1: CLI includes git_pat field in POST /workspaces JSON body when
// --git-pat flag is provided.
// Requirement: 09-REQ-1.1
func TestCLI_WorkspaceCreate_GitPATIncludedInBody(t *testing.T) {
	server, records := credMockServer(t, 0, "")
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/acme/private", "--slug", "my-ws",
		"--git-pat", "ghp_abc123")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	recs := getCredRecords(records)
	if len(recs) != 1 {
		t.Fatalf("expected 1 request; got %d", len(recs))
	}

	rec := recs[0]
	if rec.GitPAT == nil || *rec.GitPAT != "ghp_abc123" {
		t.Errorf("git_pat = %v; want %q", rec.GitPAT, "ghp_abc123")
	}
	if rec.GitURL != "https://github.com/acme/private" {
		t.Errorf("git_url = %q; want %q", rec.GitURL, "https://github.com/acme/private")
	}
	if rec.Slug != "my-ws" {
		t.Errorf("slug = %q; want %q", rec.Slug, "my-ws")
	}

	// Credential value must not appear in stdout or stderr.
	if strings.Contains(stdout, "ghp_abc123") {
		t.Error("credential value 'ghp_abc123' appeared in stdout")
	}
	if strings.Contains(stderr, "ghp_abc123") {
		t.Error("credential value 'ghp_abc123' appeared in stderr")
	}
}

// TS-09-2: CLI includes git_username and git_password fields in POST /workspaces
// JSON body when --git-username and --git-password flags are provided.
// Requirement: 09-REQ-1.2
func TestCLI_WorkspaceCreate_GitUsernamePasswordIncludedInBody(t *testing.T) {
	server, records := credMockServer(t, 0, "")
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/acme/private", "--slug", "my-ws",
		"--git-username", "alice", "--git-password", "s3cr3t")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	recs := getCredRecords(records)
	if len(recs) != 1 {
		t.Fatalf("expected 1 request; got %d", len(recs))
	}

	rec := recs[0]
	if rec.GitUsername == nil || *rec.GitUsername != "alice" {
		t.Errorf("git_username = %v; want %q", rec.GitUsername, "alice")
	}
	if rec.GitPassword == nil || *rec.GitPassword != "s3cr3t" {
		t.Errorf("git_password = %v; want %q", rec.GitPassword, "s3cr3t")
	}

	// Credential values must not appear in stdout or stderr.
	if strings.Contains(stdout, "alice") {
		t.Error("credential value 'alice' appeared in stdout")
	}
	if strings.Contains(stderr, "alice") {
		t.Error("credential value 'alice' appeared in stderr")
	}
	if strings.Contains(stdout, "s3cr3t") {
		t.Error("credential value 's3cr3t' appeared in stdout")
	}
	if strings.Contains(stderr, "s3cr3t") {
		t.Error("credential value 's3cr3t' appeared in stderr")
	}
}

// TS-09-3: CLI rejects the command with a mutual-exclusion error and non-zero
// exit code when both --git-pat and --git-username/--git-password are provided.
// Requirement: 09-REQ-1.3
func TestCLI_WorkspaceCreate_MutualExclusion(t *testing.T) {
	server, records := credMockServer(t, 0, "")
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/acme/private", "--slug", "my-ws",
		"--git-pat", "ghp_abc", "--git-username", "alice", "--git-password", "s3cr3t")

	if err == nil {
		t.Error("expected error for mutually exclusive flags; got nil")
	}

	// No HTTP request should have been sent.
	recs := getCredRecords(records)
	if len(recs) != 0 {
		t.Errorf("expected 0 HTTP requests; got %d", len(recs))
	}

	// Error message should mention mutual exclusion.
	combined := stdout + errorMessage(stdout)
	lower := strings.ToLower(combined)
	if !strings.Contains(lower, "mutually exclusive") && !strings.Contains(lower, "mutual") {
		t.Errorf("error output should mention mutual exclusion; got stdout=%q", stdout)
	}
}

// TS-09-4: CLI rejects the command with a pair-completeness error and non-zero
// exit code when only --git-username is provided without --git-password.
// Requirement: 09-REQ-1.4
func TestCLI_WorkspaceCreate_PairCompleteness_UsernameOnly(t *testing.T) {
	server, records := credMockServer(t, 0, "")
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/acme/private", "--slug", "my-ws",
		"--git-username", "alice")

	if err == nil {
		t.Error("expected error for incomplete pair; got nil")
	}

	// No HTTP request should have been sent.
	recs := getCredRecords(records)
	if len(recs) != 0 {
		t.Errorf("expected 0 HTTP requests; got %d", len(recs))
	}

	// Error message should indicate both flags are needed.
	combined := strings.ToLower(stdout + errorMessage(stdout))
	if !strings.Contains(combined, "together") && !strings.Contains(combined, "both") && !strings.Contains(combined, "pair") {
		t.Errorf("error should mention pair-completeness; got stdout=%q", stdout)
	}
}

// TS-09-4 (variant): pair-completeness when only --git-password is provided.
// Requirement: 09-REQ-1.4
func TestCLI_WorkspaceCreate_PairCompleteness_PasswordOnly(t *testing.T) {
	server, records := credMockServer(t, 0, "")
	defer server.Close()

	_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/acme/private", "--slug", "my-ws",
		"--git-password", "s3cr3t")

	if err == nil {
		t.Error("expected error for incomplete pair; got nil")
	}

	recs := getCredRecords(records)
	if len(recs) != 0 {
		t.Errorf("expected 0 HTTP requests; got %d", len(recs))
	}
}

// TS-09-5: CLI rejects the command with an HTTPS-requirement error and non-zero
// exit code when credential flags are provided with a non-HTTPS git_url.
// Requirement: 09-REQ-1.5
func TestCLI_WorkspaceCreate_HTTPSRequirement_SSH(t *testing.T) {
	server, records := credMockServer(t, 0, "")
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "git@github.com:acme/private.git", "--slug", "my-ws",
		"--git-pat", "ghp_abc")

	if err == nil {
		t.Error("expected error for non-HTTPS URL; got nil")
	}

	// No HTTP request should have been sent.
	recs := getCredRecords(records)
	if len(recs) != 0 {
		t.Errorf("expected 0 HTTP requests; got %d", len(recs))
	}

	combined := strings.ToLower(stdout + errorMessage(stdout))
	if !strings.Contains(combined, "https") {
		t.Errorf("error should mention HTTPS; got stdout=%q", stdout)
	}
}

// TS-09-5 (edge case 09-REQ-1.E3): HTTPS requirement with http:// scheme.
// Requirement: 09-REQ-1.5
func TestCLI_WorkspaceCreate_HTTPSRequirement_HTTP(t *testing.T) {
	server, records := credMockServer(t, 0, "")
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "http://github.com/acme/private", "--slug", "my-ws",
		"--git-pat", "ghp_abc")

	if err == nil {
		t.Error("expected error for http:// URL with credentials; got nil")
	}

	recs := getCredRecords(records)
	if len(recs) != 0 {
		t.Errorf("expected 0 HTTP requests; got %d", len(recs))
	}

	combined := strings.ToLower(stdout + errorMessage(stdout))
	if !strings.Contains(combined, "https") {
		t.Errorf("error should mention HTTPS; got stdout=%q", stdout)
	}
}

// TS-09-6: CLI delegates error rendering to apikit.CLIHandleError when the API
// returns HTTP 400, printing the formatted error to stderr and exiting non-zero.
// Requirement: 09-REQ-1.6
func TestCLI_WorkspaceCreate_API400DelegatedToHandleError(t *testing.T) {
	server, _ := credMockServer(t, http.StatusBadRequest,
		`{"error":{"code":400,"message":"credential validation failed for https://github.com/acme/private: unable to authenticate"}}`)
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/acme/private", "--slug", "my-ws",
		"--git-pat", "wrong-token")

	if err == nil {
		t.Error("expected error for API 400; got nil")
	}

	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope; got: %s", stdout)
	}

	msg := errorMessage(stdout)
	if !strings.Contains(msg, "credential validation failed") {
		t.Errorf("error message should contain 'credential validation failed'; got: %s", msg)
	}
}

// 09-REQ-1.E1: CLI rejects the command when --git-pat is an empty string.
func TestCLI_WorkspaceCreate_EmptyPAT(t *testing.T) {
	server, records := credMockServer(t, 0, "")
	defer server.Close()

	_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/acme/private", "--slug", "my-ws",
		"--git-pat", "")

	if err == nil {
		t.Error("expected error for empty PAT; got nil")
	}

	recs := getCredRecords(records)
	if len(recs) != 0 {
		t.Errorf("expected 0 HTTP requests for empty PAT; got %d", len(recs))
	}
}

// 09-REQ-1.E2: CLI rejects the command when --git-password is an empty string.
func TestCLI_WorkspaceCreate_EmptyPassword(t *testing.T) {
	server, records := credMockServer(t, 0, "")
	defer server.Close()

	_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/acme/private", "--slug", "my-ws",
		"--git-username", "alice", "--git-password", "")

	if err == nil {
		t.Error("expected error for empty password; got nil")
	}

	recs := getCredRecords(records)
	if len(recs) != 0 {
		t.Errorf("expected 0 HTTP requests for empty password; got %d", len(recs))
	}
}

// 09-REQ-1.E4: HTTPS requirement with git@ SSH scheme.
func TestCLI_WorkspaceCreate_HTTPSRequirement_GitAtSSH(t *testing.T) {
	server, records := credMockServer(t, 0, "")
	defer server.Close()

	_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "git@github.com:acme/private.git", "--slug", "my-ws",
		"--git-username", "alice", "--git-password", "s3cr3t")

	if err == nil {
		t.Error("expected error for SSH URL with credentials; got nil")
	}

	recs := getCredRecords(records)
	if len(recs) != 0 {
		t.Errorf("expected 0 HTTP requests; got %d", len(recs))
	}
}
