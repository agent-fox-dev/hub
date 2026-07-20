package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workspaceResp is the JSON workspace object returned by the mock API.
type workspaceResp struct {
	Slug      string  `json:"slug"`
	GitURL    string  `json:"git_url"`
	Branch    *string `json:"branch,omitempty"`
	OwnerID   string  `json:"owner_id"`
	OrgID     *string `json:"org_id,omitempty"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// errorResp is the JSON error envelope.
type errorResp struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// orgResp is a minimal org API response for slug resolution.
type orgResp struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// mockAPIServer creates an httptest.Server that simulates the workspace API.
// The workspaces map is keyed by slug. The orgs map is keyed by org slug.
func mockAPIServer(t *testing.T, workspaces map[string]workspaceResp, orgs map[string]orgResp) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// POST /api/v1/workspaces — create workspace.
	mux.HandleFunc("POST /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Slug   string  `json:"slug"`
			GitURL string  `json:"git_url"`
			Branch *string `json:"branch,omitempty"`
			OrgID  *string `json:"org_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResp{})
			return
		}
		if _, exists := workspaces[req.Slug]; exists {
			e := errorResp{}
			e.Error.Code = http.StatusConflict
			e.Error.Message = "workspace with this slug already exists"
			writeJSON(w, http.StatusConflict, e)
			return
		}
		ws := workspaceResp{
			Slug:      req.Slug,
			GitURL:    req.GitURL,
			Branch:    req.Branch,
			OwnerID:   "test-user-id",
			OrgID:     req.OrgID,
			Status:    "active",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		}
		workspaces[req.Slug] = ws
		writeJSON(w, http.StatusCreated, ws)
	})

	// GET /api/v1/workspaces — list workspaces.
	mux.HandleFunc("GET /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		includeArchived := r.URL.Query().Get("include_archived") == "true"
		var result []workspaceResp
		for _, ws := range workspaces {
			if !includeArchived && ws.Status == "archived" {
				continue
			}
			result = append(result, ws)
		}
		if result == nil {
			result = []workspaceResp{}
		}
		writeJSON(w, http.StatusOK, result)
	})

	// GET /api/v1/workspaces/{slug} — get workspace.
	mux.HandleFunc("GET /api/v1/workspaces/{slug}", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		ws, ok := workspaces[slug]
		if !ok {
			e := errorResp{}
			e.Error.Code = http.StatusNotFound
			e.Error.Message = "workspace not found"
			writeJSON(w, http.StatusNotFound, e)
			return
		}
		writeJSON(w, http.StatusOK, ws)
	})

	// POST /api/v1/workspaces/{slug}/archive — archive workspace.
	mux.HandleFunc("POST /api/v1/workspaces/{slug}/archive", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		ws, ok := workspaces[slug]
		if !ok {
			e := errorResp{}
			e.Error.Code = http.StatusNotFound
			e.Error.Message = "workspace not found"
			writeJSON(w, http.StatusNotFound, e)
			return
		}
		if ws.Status == "archived" {
			e := errorResp{}
			e.Error.Code = http.StatusBadRequest
			e.Error.Message = "workspace is already archived"
			writeJSON(w, http.StatusBadRequest, e)
			return
		}
		ws.Status = "archived"
		workspaces[slug] = ws
		writeJSON(w, http.StatusOK, ws)
	})

	// POST /api/v1/workspaces/{slug}/reactivate — reactivate workspace.
	mux.HandleFunc("POST /api/v1/workspaces/{slug}/reactivate", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		ws, ok := workspaces[slug]
		if !ok {
			e := errorResp{}
			e.Error.Code = http.StatusNotFound
			e.Error.Message = "workspace not found"
			writeJSON(w, http.StatusNotFound, e)
			return
		}
		if ws.Status == "active" {
			e := errorResp{}
			e.Error.Code = http.StatusBadRequest
			e.Error.Message = "workspace is already active"
			writeJSON(w, http.StatusBadRequest, e)
			return
		}
		ws.Status = "active"
		workspaces[slug] = ws
		writeJSON(w, http.StatusOK, ws)
	})

	// DELETE /api/v1/workspaces/{slug} — delete workspace.
	mux.HandleFunc("DELETE /api/v1/workspaces/{slug}", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		ws, ok := workspaces[slug]
		if !ok {
			e := errorResp{}
			e.Error.Code = http.StatusNotFound
			e.Error.Message = "workspace not found"
			writeJSON(w, http.StatusNotFound, e)
			return
		}
		if ws.Status == "active" {
			e := errorResp{}
			e.Error.Code = http.StatusBadRequest
			e.Error.Message = "only archived workspaces can be deleted"
			writeJSON(w, http.StatusBadRequest, e)
			return
		}
		delete(workspaces, slug)
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /user/orgs — list user orgs (used by CLI for org slug resolution).
	mux.HandleFunc("GET /user/orgs", func(w http.ResponseWriter, r *http.Request) {
		var result []orgResp
		for _, org := range orgs {
			result = append(result, org)
		}
		if result == nil {
			result = []orgResp{}
		}
		writeJSON(w, http.StatusOK, result)
	})

	return httptest.NewServer(mux)
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// runWorkspaceCmd executes a workspace subcommand and captures stdout/stderr.
func runWorkspaceCmd(t *testing.T, baseURL, apiKey string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := WorkspaceCmd(baseURL, apiKey)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

// runRootCmd executes the root command and captures stdout/stderr.
func runRootCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := BuildRootCommand()
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

// --- CLI workspace create tests ---

// TS-01-52: Verify that 'afc workspace create --git-url <url> --slug <slug>'
// calls POST /api/v1/workspaces and prints the workspace JSON to stdout,
// exiting 0.
// Requirement: 01-REQ-11.1
func TestCLI_WorkspaceCreate_Success(t *testing.T) {
	server := mockAPIServer(t, make(map[string]workspaceResp), nil)
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/org/repo", "--slug", "my-ws")

	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}

	var ws workspaceResp
	if jsonErr := json.Unmarshal([]byte(stdout), &ws); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if ws.Slug != "my-ws" {
		t.Errorf("slug = %q; want %q", ws.Slug, "my-ws")
	}
	if ws.Status != "active" {
		t.Errorf("status = %q; want %q", ws.Status, "active")
	}
}

// TS-01-53: Verify that 'afc workspace create --org <org-slug>' resolves the
// org slug to a UUID before calling the API and the workspace includes org_id.
// Requirement: 01-REQ-11.2
func TestCLI_WorkspaceCreate_WithOrg(t *testing.T) {
	orgs := map[string]orgResp{
		"my-org": {ID: "my-org-uuid", Slug: "my-org"},
	}
	server := mockAPIServer(t, make(map[string]workspaceResp), orgs)
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/org/repo", "--slug", "org-ws", "--org", "my-org")

	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}

	var ws workspaceResp
	if jsonErr := json.Unmarshal([]byte(stdout), &ws); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if ws.OrgID == nil || *ws.OrgID != "my-org-uuid" {
		t.Errorf("org_id = %v; want %q", ws.OrgID, "my-org-uuid")
	}
}

// TS-01-54: Verify that 'afc workspace create' with missing --git-url or
// --slug prints a usage hint to stderr and exits 1 without making an API call.
// Requirement: 01-REQ-11.3
func TestCLI_WorkspaceCreate_MissingFlags(t *testing.T) {
	server := mockAPIServer(t, make(map[string]workspaceResp), nil)
	defer server.Close()

	t.Run("missing --git-url", func(t *testing.T) {
		_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
			"create", "--slug", "my-ws")

		if err == nil {
			t.Error("expected error for missing --git-url; got nil")
		}
		if !strings.Contains(stderr, "git-url") && !strings.Contains(stderr, "usage") &&
			!strings.Contains(stderr, "required") {
			t.Errorf("stderr should contain usage hint; got: %s", stderr)
		}
	})

	t.Run("missing --slug", func(t *testing.T) {
		_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
			"create", "--git-url", "https://github.com/org/repo")

		if err == nil {
			t.Error("expected error for missing --slug; got nil")
		}
		if !strings.Contains(stderr, "slug") && !strings.Contains(stderr, "usage") &&
			!strings.Contains(stderr, "required") {
			t.Errorf("stderr should contain usage hint; got: %s", stderr)
		}
	})
}

// TS-01-55: Verify that 'afc workspace create --org <nonexistent-slug>' prints
// an error to stderr and exits 1 without making the workspace create API call.
// Requirement: 01-REQ-11.4
func TestCLI_WorkspaceCreate_OrgNotFound(t *testing.T) {
	// No orgs registered in the mock server.
	server := mockAPIServer(t, make(map[string]workspaceResp), make(map[string]orgResp))
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/org/repo", "--slug", "my-ws",
		"--org", "nonexistent-org")

	if err == nil {
		t.Error("expected error for nonexistent org; got nil")
	}
	if stderr == "" {
		t.Error("stderr is empty; want error about org resolution failure")
	}
}

// TS-01-56: Verify that 'afc workspace create' prints the API error to stderr
// and exits 1 when the API call returns an error.
// Requirement: 01-REQ-11.5
func TestCLI_WorkspaceCreate_APIError(t *testing.T) {
	// Pre-seed a workspace so the create call returns 409 conflict.
	workspaces := map[string]workspaceResp{
		"existing-ws": {
			Slug:      "existing-ws",
			GitURL:    "https://github.com/org/repo",
			OwnerID:   "test-user-id",
			Status:    "active",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		},
	}
	server := mockAPIServer(t, workspaces, nil)
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/org/repo", "--slug", "existing-ws")

	if err == nil {
		t.Error("expected error for duplicate slug; got nil")
	}
	if stderr == "" {
		t.Error("stderr is empty; want error message about conflict or duplicate slug")
	}
}

// --- CLI workspace list tests ---

// TS-01-57: Verify that 'afc workspace list' calls GET /api/v1/workspaces
// and prints the JSON array to stdout, exiting 0.
// Requirement: 01-REQ-12.1
func TestCLI_WorkspaceList_Success(t *testing.T) {
	workspaces := map[string]workspaceResp{
		"my-ws": {
			Slug:      "my-ws",
			GitURL:    "https://github.com/org/repo",
			OwnerID:   "test-user-id",
			Status:    "active",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		},
	}
	server := mockAPIServer(t, workspaces, nil)
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key", "list")

	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}

	var wsList []workspaceResp
	if jsonErr := json.Unmarshal([]byte(stdout), &wsList); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON array: %v\nstdout: %s", jsonErr, stdout)
	}
	if len(wsList) == 0 {
		t.Error("workspace list is empty; want at least 1")
	}
}

// TS-01-58: Verify that 'afc workspace list --include-archived' passes
// ?include_archived=true to the API and the response includes archived
// workspaces.
// Requirement: 01-REQ-12.2
func TestCLI_WorkspaceList_IncludeArchived(t *testing.T) {
	workspaces := map[string]workspaceResp{
		"active-ws": {
			Slug:      "active-ws",
			GitURL:    "https://github.com/org/repo1",
			OwnerID:   "test-user-id",
			Status:    "active",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		},
		"archived-ws": {
			Slug:      "archived-ws",
			GitURL:    "https://github.com/org/repo2",
			OwnerID:   "test-user-id",
			Status:    "archived",
			CreatedAt: "2025-01-02T00:00:00Z",
			UpdatedAt: "2025-01-02T00:00:00Z",
		},
	}
	server := mockAPIServer(t, workspaces, nil)
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"list", "--include-archived")

	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}

	var wsList []workspaceResp
	if jsonErr := json.Unmarshal([]byte(stdout), &wsList); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON array: %v\nstdout: %s", jsonErr, stdout)
	}

	slugs := make(map[string]bool)
	for _, ws := range wsList {
		slugs[ws.Slug] = true
	}
	if !slugs["active-ws"] {
		t.Error("missing active-ws in output")
	}
	if !slugs["archived-ws"] {
		t.Error("missing archived-ws in output (should be included with --include-archived)")
	}
}

// TS-01-59: Verify that 'afc workspace list' prints the error to stderr and
// exits 1 when the API call fails.
// Requirement: 01-REQ-12.3
func TestCLI_WorkspaceList_APIError(t *testing.T) {
	// Use a server that always returns 401 to simulate bad credentials.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code":    401,
				"message": "invalid credentials",
			},
		})
	}))
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "bad-key", "list")

	if err == nil {
		t.Error("expected error for bad credentials; got nil")
	}
	if stderr == "" {
		t.Error("stderr is empty; want error message")
	}
}

// --- CLI workspace get tests ---

// TS-01-60: Verify that 'afc workspace get <slug>' calls GET
// /api/v1/workspaces/:slug and prints the workspace JSON to stdout, exiting 0.
// Requirement: 01-REQ-13.1
func TestCLI_WorkspaceGet_Success(t *testing.T) {
	workspaces := map[string]workspaceResp{
		"my-ws": {
			Slug:      "my-ws",
			GitURL:    "https://github.com/org/repo",
			OwnerID:   "test-user-id",
			Status:    "active",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		},
	}
	server := mockAPIServer(t, workspaces, nil)
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"get", "my-ws")

	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}

	var ws workspaceResp
	if jsonErr := json.Unmarshal([]byte(stdout), &ws); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if ws.Slug != "my-ws" {
		t.Errorf("slug = %q; want %q", ws.Slug, "my-ws")
	}
}

// TS-01-61: Verify that 'afc workspace get <slug>' prints the error to stderr
// and exits 1 when the API returns an error (e.g. 404 or 401).
// Requirement: 01-REQ-13.2
func TestCLI_WorkspaceGet_NotFound(t *testing.T) {
	server := mockAPIServer(t, make(map[string]workspaceResp), nil)
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"get", "nonexistent")

	if err == nil {
		t.Error("expected error for nonexistent workspace; got nil")
	}
	if stderr == "" {
		t.Error("stderr is empty; want error message")
	}
}

// --- CLI workspace archive tests ---

// TS-01-62: Verify that 'afc workspace archive <slug>' calls POST
// /api/v1/workspaces/:slug/archive and prints the updated workspace JSON
// (status='archived') to stdout, exiting 0.
// Requirement: 01-REQ-14.1
func TestCLI_WorkspaceArchive_Success(t *testing.T) {
	workspaces := map[string]workspaceResp{
		"my-ws": {
			Slug:      "my-ws",
			GitURL:    "https://github.com/org/repo",
			OwnerID:   "test-user-id",
			Status:    "active",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		},
	}
	server := mockAPIServer(t, workspaces, nil)
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"archive", "my-ws")

	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}

	var ws workspaceResp
	if jsonErr := json.Unmarshal([]byte(stdout), &ws); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if ws.Status != "archived" {
		t.Errorf("status = %q; want %q", ws.Status, "archived")
	}
}

// TS-01-63: Verify that 'afc workspace archive <slug>' prints the error to
// stderr and exits 1 when the API call returns an error.
// Requirement: 01-REQ-14.2
func TestCLI_WorkspaceArchive_Error(t *testing.T) {
	// Workspace is already archived.
	workspaces := map[string]workspaceResp{
		"my-ws": {
			Slug:      "my-ws",
			GitURL:    "https://github.com/org/repo",
			OwnerID:   "test-user-id",
			Status:    "archived",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		},
	}
	server := mockAPIServer(t, workspaces, nil)
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"archive", "my-ws")

	if err == nil {
		t.Error("expected error for already-archived workspace; got nil")
	}
	if stderr == "" {
		t.Error("stderr is empty; want error message")
	}
}

// --- CLI workspace reactivate tests ---

// TS-01-64: Verify that 'afc workspace reactivate <slug>' calls POST
// /api/v1/workspaces/:slug/reactivate and prints the updated workspace JSON
// (status='active') to stdout, exiting 0.
// Requirement: 01-REQ-15.1
func TestCLI_WorkspaceReactivate_Success(t *testing.T) {
	workspaces := map[string]workspaceResp{
		"my-ws": {
			Slug:      "my-ws",
			GitURL:    "https://github.com/org/repo",
			OwnerID:   "test-user-id",
			Status:    "archived",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		},
	}
	server := mockAPIServer(t, workspaces, nil)
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"reactivate", "my-ws")

	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}

	var ws workspaceResp
	if jsonErr := json.Unmarshal([]byte(stdout), &ws); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if ws.Status != "active" {
		t.Errorf("status = %q; want %q", ws.Status, "active")
	}
}

// TS-01-65: Verify that 'afc workspace reactivate <slug>' prints the error to
// stderr and exits 1 when the API call returns an error.
// Requirement: 01-REQ-15.2
func TestCLI_WorkspaceReactivate_Error(t *testing.T) {
	// Workspace is already active.
	workspaces := map[string]workspaceResp{
		"my-ws": {
			Slug:      "my-ws",
			GitURL:    "https://github.com/org/repo",
			OwnerID:   "test-user-id",
			Status:    "active",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		},
	}
	server := mockAPIServer(t, workspaces, nil)
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"reactivate", "my-ws")

	if err == nil {
		t.Error("expected error for already-active workspace; got nil")
	}
	if stderr == "" {
		t.Error("stderr is empty; want error message")
	}
}

// --- CLI workspace delete tests ---

// TS-01-66: Verify that 'afc workspace delete <slug> --confirm' calls DELETE
// /api/v1/workspaces/:slug, prints a confirmation message to stderr, and
// exits 0.
// Requirement: 01-REQ-16.1
func TestCLI_WorkspaceDelete_Success(t *testing.T) {
	workspaces := map[string]workspaceResp{
		"my-ws": {
			Slug:      "my-ws",
			GitURL:    "https://github.com/org/repo",
			OwnerID:   "test-user-id",
			Status:    "archived",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		},
	}
	server := mockAPIServer(t, workspaces, nil)
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"delete", "my-ws", "--confirm")

	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr == "" {
		t.Error("stderr is empty; want confirmation message")
	}
}

// TS-01-67: Verify that 'afc workspace delete <slug>' without --confirm prints
// a usage hint to stderr and exits 1 without making an API call.
// Requirement: 01-REQ-16.2
func TestCLI_WorkspaceDelete_NoConfirm(t *testing.T) {
	server := mockAPIServer(t, make(map[string]workspaceResp), nil)
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"delete", "my-ws")

	if err == nil {
		t.Error("expected error when --confirm is omitted; got nil")
	}
	if !strings.Contains(stderr, "confirm") && !strings.Contains(stderr, "--confirm") {
		t.Errorf("stderr should contain --confirm usage hint; got: %s", stderr)
	}
}

// TS-01-68: Verify that 'afc workspace delete <slug> --confirm' prints the
// error to stderr and exits 1 when the API call returns an error.
// Requirement: 01-REQ-16.3
func TestCLI_WorkspaceDelete_Error(t *testing.T) {
	// Workspace is active, so delete will fail with 400.
	workspaces := map[string]workspaceResp{
		"my-ws": {
			Slug:      "my-ws",
			GitURL:    "https://github.com/org/repo",
			OwnerID:   "test-user-id",
			Status:    "active",
			CreatedAt: "2025-01-01T00:00:00Z",
			UpdatedAt: "2025-01-01T00:00:00Z",
		},
	}
	server := mockAPIServer(t, workspaces, nil)
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"delete", "my-ws", "--confirm")

	if err == nil {
		t.Error("expected error for deleting active workspace; got nil")
	}
	if stderr == "" {
		t.Error("stderr is empty; want error message about workspace not being archived")
	}
}

// --- CLI command tree and structure tests ---

// TS-01-69: Verify that the afc CLI builds its command tree from RootCommand()
// and includes LoginCmd, UserCmd, KeysCmd, TokensCmd, OrgsCmd, AdminCmd, and
// the workspace subcommand group.
// Requirement: 01-REQ-17.1
func TestCLI_CommandTree_AllSubcommands(t *testing.T) {
	stdout, _, err := runRootCmd(t, "--help")

	if err != nil {
		t.Fatalf("--help returned error: %v", err)
	}

	// The help output should list all expected subcommands.
	expectedSubcommands := []string{
		"workspace",
		"login",
		"user",
		"keys",
		"tokens",
		"orgs",
		"admin",
	}
	for _, sub := range expectedSubcommands {
		if !strings.Contains(stdout, sub) {
			t.Errorf("help output missing subcommand %q", sub)
		}
	}
}

// TS-01-70: Verify that the afc CLI uses CLIExecute() for execution,
// CLIPrintError() for errors, and CLIExitCode() to derive the process exit
// code, returning 0 on success and 1 on error.
// Requirement: 01-REQ-17.2
func TestCLI_ExitCodes(t *testing.T) {
	t.Run("success exits 0", func(t *testing.T) {
		workspaces := map[string]workspaceResp{
			"my-ws": {
				Slug:      "my-ws",
				GitURL:    "https://github.com/org/repo",
				OwnerID:   "test-user-id",
				Status:    "active",
				CreatedAt: "2025-01-01T00:00:00Z",
				UpdatedAt: "2025-01-01T00:00:00Z",
			},
		}
		server := mockAPIServer(t, workspaces, nil)
		defer server.Close()

		_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key", "list")
		if err != nil {
			t.Errorf("expected nil error (exit 0); got: %v", err)
		}
	})

	t.Run("error exits 1", func(t *testing.T) {
		server := mockAPIServer(t, make(map[string]workspaceResp), nil)
		defer server.Close()

		_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
			"get", "nonexistent-slug")
		if err == nil {
			t.Error("expected non-nil error (exit 1); got nil")
		}
		if stderr == "" {
			t.Error("stderr is empty; error output should go to stderr via CLIPrintError")
		}
	})
}

// TS-01-71: Verify that afc CLI library functions signal errors via return
// values using Go's (*T, error) idiom and never call os.Exit directly.
// Requirement: 01-REQ-17.3
func TestCLI_NoOsExitInLibrary(t *testing.T) {
	// Scan all Go source files in internal/ and verify none contain os.Exit.
	// Only cmd/afc/main.go is allowed to call os.Exit.

	// Find the project root by looking for go.mod relative to the test file.
	root := findProjectRoot(t)

	internalDir := filepath.Join(root, "internal")
	if _, err := os.Stat(internalDir); os.IsNotExist(err) {
		t.Skip("internal/ directory not found; skipping os.Exit scan")
	}

	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip test files — they're not library code.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		content := string(data)
		if strings.Contains(content, "os.Exit(") || strings.Contains(content, "os.Exit (") {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("library file %s contains os.Exit call; library functions must use return values", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}
}

// TS-01-E13: Verify that when org slug resolution returns a success status
// but with a missing or null org UUID, the CLI treats it as a resolution
// failure and exits 1 without making the workspace create API call.
// Requirement: 01-REQ-11.E1
func TestEdgeCLI_WorkspaceCreate_OrgNullUUID(t *testing.T) {
	// Mock server that returns orgs with null/missing UUID for the requested slug.
	var workspaceCreateCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user/orgs", func(w http.ResponseWriter, r *http.Request) {
		// Return an org entry with the matching slug but empty/missing ID.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// ID is empty string — simulates null/missing UUID.
		fmt.Fprintf(w, `[{"id":"","slug":"bad-org"}]`)
	})
	mux.HandleFunc("POST /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		workspaceCreateCalled = true
		w.WriteHeader(http.StatusCreated)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/org/repo", "--slug", "my-ws",
		"--org", "bad-org")

	if err == nil {
		t.Error("expected error for null org UUID; got nil")
	}
	if stderr == "" {
		t.Error("stderr is empty; want error about resolution failure")
	}
	if workspaceCreateCalled {
		t.Error("POST /api/v1/workspaces was called; want no API call when org UUID is null")
	}
}

// TS-01-E14: Verify that when org slug resolution returns an unexpected data
// shape, the CLI exits 1 with an error to stderr and does not make the
// workspace create API call.
// Requirement: 01-REQ-11.E2
func TestEdgeCLI_WorkspaceCreate_OrgMalformedResponse(t *testing.T) {
	// Mock server that returns a malformed response for /user/orgs.
	var workspaceCreateCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user/orgs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Return malformed JSON that doesn't match the expected structure.
		fmt.Fprintf(w, `{"unexpected":"shape","not":"an array"}`)
	})
	mux.HandleFunc("POST /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		workspaceCreateCalled = true
		w.WriteHeader(http.StatusCreated)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/org/repo", "--slug", "my-ws",
		"--org", "bad-org")

	if err == nil {
		t.Error("expected error for malformed org response; got nil")
	}
	if stderr == "" {
		t.Error("stderr is empty; want error about unexpected response")
	}
	if workspaceCreateCalled {
		t.Error("POST /api/v1/workspaces was called; want no API call when org response is malformed")
	}
}

// findProjectRoot walks up from the current directory to find go.mod.
func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}
