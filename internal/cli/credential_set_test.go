package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ========================================================================
// Spec 15 Task 2.1: CLI credential set tests
// (TS-15-15, TS-15-16, 15-REQ-5.E3)
// Requirements: 15-REQ-5.2, 15-REQ-5.3, 15-REQ-5.E3
// ========================================================================

// credSetMockServer creates a mock server that accepts POST requests
// to /api/v1/workspaces/{slug}/secrets and records the stored secrets.
func credSetMockServer(t *testing.T, allowedSlugs map[string]bool, stored map[string]map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// POST /api/v1/workspaces/{slug}/secrets — store a secret.
	mux.HandleFunc("POST /api/v1/workspaces/{slug}/secrets", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		if !allowedSlugs[slug] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    404,
					"message": "workspace not found",
				},
			})
			return
		}

		body, _ := io.ReadAll(r.Body)
		var req struct {
			Entries []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    400,
					"message": "bad request",
				},
			})
			return
		}

		if stored[slug] == nil {
			stored[slug] = make(map[string]string)
		}
		var entries []map[string]any
		for _, e := range req.Entries {
			stored[slug][e.Key] = e.Value
			entries = append(entries, map[string]any{
				"key":        e.Key,
				"created_at": "2025-01-01T00:00:00Z",
				"updated_at": "2025-01-01T00:00:00Z",
			})
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(entries)
	})

	return httptest.NewServer(mux)
}

// TS-15-15: Running 'afc credential set my-ws --upstream-git-pat mytoken123'
// stores the value as workspace secret UPSTREAM_GIT_PAT and exits 0.
// Requirement: 15-REQ-5.2
func TestCLI_CredentialSet_UpstreamGitPAT(t *testing.T) {
	stored := make(map[string]map[string]string)
	srv := credSetMockServer(t, map[string]bool{"my-ws": true}, stored)
	defer srv.Close()

	stdout, stderr, err := runRootCmd(t,
		"--endpoint-url", srv.URL,
		"--api-key", "test-key",
		"credential", "set", "my-ws",
		"--upstream-git-pat", "mytoken123",
	)

	if err != nil {
		t.Fatalf("credential set command failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	// Verify the secret was stored.
	if stored["my-ws"] == nil {
		t.Fatal("no secrets stored for workspace 'my-ws'")
	}
	if stored["my-ws"]["UPSTREAM_GIT_PAT"] != "mytoken123" {
		t.Errorf("stored UPSTREAM_GIT_PAT = %q; want %q", stored["my-ws"]["UPSTREAM_GIT_PAT"], "mytoken123")
	}
}

// TS-15-16: Running 'afc credential set my-ws --upstream-git-username myuser
// --upstream-git-password mypass' stores both secrets and exits 0.
// Requirement: 15-REQ-5.3
func TestCLI_CredentialSet_UpstreamGitUsernamePassword(t *testing.T) {
	stored := make(map[string]map[string]string)
	srv := credSetMockServer(t, map[string]bool{"my-ws": true}, stored)
	defer srv.Close()

	stdout, stderr, err := runRootCmd(t,
		"--endpoint-url", srv.URL,
		"--api-key", "test-key",
		"credential", "set", "my-ws",
		"--upstream-git-username", "myuser",
		"--upstream-git-password", "mypass",
	)

	if err != nil {
		t.Fatalf("credential set command failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if stored["my-ws"] == nil {
		t.Fatal("no secrets stored for workspace 'my-ws'")
	}
	if stored["my-ws"]["UPSTREAM_GIT_USERNAME"] != "myuser" {
		t.Errorf("stored UPSTREAM_GIT_USERNAME = %q; want %q", stored["my-ws"]["UPSTREAM_GIT_USERNAME"], "myuser")
	}
	if stored["my-ws"]["UPSTREAM_GIT_PASSWORD"] != "mypass" {
		t.Errorf("stored UPSTREAM_GIT_PASSWORD = %q; want %q", stored["my-ws"]["UPSTREAM_GIT_PASSWORD"], "mypass")
	}
}

// 15-REQ-5.E3: Attempting to set upstream credentials on a workspace that
// does not exist returns an error from the API.
func TestCLI_CredentialSet_NonExistentWorkspace(t *testing.T) {
	stored := make(map[string]map[string]string)
	srv := credSetMockServer(t, map[string]bool{}, stored) // no workspaces allowed
	defer srv.Close()

	_, _, err := runRootCmd(t,
		"--endpoint-url", srv.URL,
		"--api-key", "test-key",
		"credential", "set", "nonexistent-ws",
		"--upstream-git-pat", "mytoken",
	)

	if err == nil {
		t.Error("credential set on non-existent workspace should fail; got nil error")
	}
}
