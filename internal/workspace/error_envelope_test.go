package workspace

import (
	"net/http"
	"testing"
)

// TS-01-72: Verify that all error responses use apikit's standard JSON envelope
// format {"error":{"code":<HTTP_STATUS>,"message":"Human-readable description"}}.
// Requirement: 01-REQ-18.1
func TestWorkspaceErrorEnvelope_Format(t *testing.T) {
	env := newTestEnv(t)

	// Trigger a 400 error by sending an invalid slug.
	body := `{"slug":"INVALID SLUG","git_url":"https://github.com/org/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body,
		userAuth("alice-id"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusBadRequest)
	}

	resp := parseErrorEnvelope(t, rec)

	// Verify error.code is an integer matching the HTTP status.
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d (must match HTTP status)",
			resp.Error.Code, http.StatusBadRequest)
	}

	// Verify error.message is a non-empty string.
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty human-readable description")
	}
}
