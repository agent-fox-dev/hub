package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-04-32: Verify that afc workspace create without --org sends no org_id in
// the request body and receives a response with populated org_id from the
// personal org.
// Requirement: 04-REQ-9.1
// ---------------------------------------------------------------------------
func TestCLIWorkspace_CreateWithoutOrg_NoOrgIDInBody(t *testing.T) {
	// Track whether the request body includes an org_id field.
	var capturedBody map[string]any

	personalOrgID := "personal-org-uuid-123"

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(bodyBytes, &capturedBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Simulate server-side personal org defaulting: if org_id is absent,
		// the server would query for the user's personal org and populate it.
		orgID := personalOrgID
		ws := workspaceResp{
			Slug:      capturedBody["slug"].(string),
			GitURL:    capturedBody["git_url"].(string),
			OwnerID:   "test-user-id",
			OrgID:     &orgID,
			Status:    "active",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		}
		writeJSON(w, http.StatusCreated, ws)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/user/repo", "--slug", "my-workspace")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	// Assert: the CLI request body should NOT contain 'org_id'.
	if _, hasOrgID := capturedBody["org_id"]; hasOrgID {
		t.Errorf("request body contains 'org_id' = %v; want org_id to be absent when --org is not specified",
			capturedBody["org_id"])
	}

	// Assert: the response JSON should have a populated org_id from the server.
	var ws workspaceResp
	if jsonErr := json.Unmarshal([]byte(stdout), &ws); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if ws.OrgID == nil || *ws.OrgID != personalOrgID {
		t.Errorf("response org_id = %v; want %q (personal org id)", ws.OrgID, personalOrgID)
	}
}

// ---------------------------------------------------------------------------
// TS-04-33: Verify that afc workspace create with --org sends org_id in the
// request body unchanged.
// Requirement: 04-REQ-9.2
// ---------------------------------------------------------------------------
func TestCLIWorkspace_CreateWithOrg_OrgIDInBody(t *testing.T) {
	var capturedBody map[string]any

	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(bodyBytes, &capturedBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		orgID := capturedBody["org_id"].(string)
		ws := workspaceResp{
			Slug:      capturedBody["slug"].(string),
			GitURL:    capturedBody["git_url"].(string),
			OwnerID:   "test-user-id",
			OrgID:     &orgID,
			Status:    "active",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		}
		writeJSON(w, http.StatusCreated, ws)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/user/repo", "--slug", "team-ws", "--org", "my-team")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	// Assert: the CLI request body should contain 'org_id' with the slug passed via --org.
	orgIDVal, hasOrgID := capturedBody["org_id"]
	if !hasOrgID {
		t.Fatal("request body is missing 'org_id'; want org_id to be present when --org is specified")
	}
	if orgIDVal != "my-team" {
		t.Errorf("request body org_id = %v; want %q", orgIDVal, "my-team")
	}

	// Assert: the response JSON should echo back the same org_id.
	var ws workspaceResp
	if jsonErr := json.Unmarshal([]byte(stdout), &ws); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if ws.OrgID == nil || *ws.OrgID != "my-team" {
		t.Errorf("response org_id = %v; want %q", ws.OrgID, "my-team")
	}
}
