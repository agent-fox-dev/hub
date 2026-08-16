package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ========================================================================
// Spec 15 Task 4.3: Patch CLI commands
// (TS-15-41, TS-15-42, TS-15-43, TS-15-44, TS-15-45)
// Requirements: 15-REQ-14
// ========================================================================

// patchResp is the JSON patch object returned by the mock API.
type patchResp struct {
	ID             string  `json:"id"`
	WorkspaceSlug  string  `json:"workspace_slug"`
	BranchName     string  `json:"branch_name"`
	Position       int     `json:"position"`
	Status         string  `json:"status"`
	Description    *string `json:"description,omitempty"`
	UpstreamPRURL  *string `json:"upstream_pr_url,omitempty"`
	AddedAt        string  `json:"added_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// mockPatchAPIServer creates an httptest.Server that simulates the patch API.
func mockPatchAPIServer(t *testing.T, patches map[string]patchResp) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// POST /api/v1/workspaces/{slug}/patches — add patch.
	mux.HandleFunc("POST /api/v1/workspaces/{slug}/patches", func(w http.ResponseWriter, r *http.Request) {
		// Check if this is the reorder endpoint.
		if strings.HasSuffix(r.URL.Path, "/reorder") {
			// Handled below.
			return
		}

		var req struct {
			BranchName string `json:"branch_name"`
			Position   *int   `json:"position,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResp{})
			return
		}

		p := patchResp{
			ID:            "generated-uuid",
			WorkspaceSlug: r.PathValue("slug"),
			BranchName:    req.BranchName,
			Position:      1,
			Status:        "active",
			AddedAt:       "2025-01-01T00:00:00Z",
			UpdatedAt:     "2025-01-01T00:00:00Z",
		}
		if req.Position != nil {
			p.Position = *req.Position
		}
		patches[p.ID] = p
		writeJSON(w, http.StatusCreated, p)
	})

	// GET /api/v1/workspaces/{slug}/patches — list patches.
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/patches", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		var result []patchResp
		for _, p := range patches {
			if p.WorkspaceSlug == slug {
				result = append(result, p)
			}
		}
		if result == nil {
			result = []patchResp{}
		}
		writeJSON(w, http.StatusOK, result)
	})

	// DELETE /api/v1/workspaces/{slug}/patches/{id} — remove patch.
	mux.HandleFunc("DELETE /api/v1/workspaces/{slug}/patches/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, ok := patches[id]; !ok {
			e := errorResp{}
			e.Error.Code = http.StatusNotFound
			e.Error.Message = "patch not found"
			writeJSON(w, http.StatusNotFound, e)
			return
		}
		delete(patches, id)
		w.WriteHeader(http.StatusNoContent)
	})

	// PATCH /api/v1/workspaces/{slug}/patches/{id} — update patch.
	mux.HandleFunc("PATCH /api/v1/workspaces/{slug}/patches/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		p, ok := patches[id]
		if !ok {
			e := errorResp{}
			e.Error.Code = http.StatusNotFound
			e.Error.Message = "patch not found"
			writeJSON(w, http.StatusNotFound, e)
			return
		}

		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResp{})
			return
		}

		if status, ok := req["status"].(string); ok {
			p.Status = status
		}
		patches[id] = p
		writeJSON(w, http.StatusOK, p)
	})

	// POST /api/v1/workspaces/{slug}/patches/reorder — reorder patches.
	mux.HandleFunc("POST /api/v1/workspaces/{slug}/patches/reorder", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PatchIDs []string `json:"patch_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResp{})
			return
		}

		var result []patchResp
		for i, id := range req.PatchIDs {
			if p, ok := patches[id]; ok {
				p.Position = i + 1
				patches[id] = p
				result = append(result, p)
			}
		}
		writeJSON(w, http.StatusOK, result)
	})

	return httptest.NewServer(mux)
}

// runPatchCmd executes a patch subcommand through the full root command tree
// and captures stdout/stderr.
func runPatchCmd(t *testing.T, baseURL, apiKey string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	setupTestEnv(t)
	root := BuildRootCommand()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	fullArgs := append([]string{"--endpoint-url", baseURL, "--api-key", apiKey, "patch"}, args...)
	root.SetArgs(fullArgs)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// TS-15-41: Running 'afc patch add <workspace-slug> --branch <name>' sends
// POST to the patches endpoint and prints the created patch on success, exiting 0.
// Requirement: 15-REQ-14.1
func TestCLI_PatchAdd_Success(t *testing.T) {
	server := mockPatchAPIServer(t, make(map[string]patchResp))
	defer server.Close()

	stdout, _, err := runPatchCmd(t, server.URL, "test-api-key",
		"add", "cp-ws", "--branch", "feature/my-patch")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	if !strings.Contains(stdout, "feature/my-patch") {
		t.Errorf("stdout should contain 'feature/my-patch'; got: %s", stdout)
	}

	// Verify stdout is valid JSON.
	var resp map[string]any
	if jsonErr := json.Unmarshal([]byte(stdout), &resp); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
}

// 15-REQ-14.E1: Running 'afc patch add' without the required --branch flag
// prints a usage error and exits non-zero without making an API call.
// Requirement: 15-REQ-14.E1
func TestCLI_PatchAdd_MissingBranch(t *testing.T) {
	server := mockPatchAPIServer(t, make(map[string]patchResp))
	defer server.Close()

	stdout, _, err := runPatchCmd(t, server.URL, "test-api-key",
		"add", "cp-ws")

	if err == nil {
		t.Error("expected error for missing --branch; got nil")
	}

	// Check for error output indicating --branch is required.
	combined := stdout
	if !strings.Contains(strings.ToLower(combined), "branch") &&
		!strings.Contains(strings.ToLower(combined), "required") {
		t.Errorf("output should mention --branch is required; got: %s", combined)
	}
}

// TS-15-42: Running 'afc patch list <workspace-slug>' sends GET to the
// patches endpoint and prints patches in position order, exiting 0.
// Requirement: 15-REQ-14.2
func TestCLI_PatchList_Success(t *testing.T) {
	patches := map[string]patchResp{
		"uuid-1": {
			ID:            "uuid-1",
			WorkspaceSlug: "cp-ws",
			BranchName:    "feature/alpha",
			Position:      1,
			Status:        "active",
			AddedAt:       "2025-01-01T00:00:00Z",
			UpdatedAt:     "2025-01-01T00:00:00Z",
		},
		"uuid-2": {
			ID:            "uuid-2",
			WorkspaceSlug: "cp-ws",
			BranchName:    "feature/beta",
			Position:      2,
			Status:        "active",
			AddedAt:       "2025-01-01T00:00:00Z",
			UpdatedAt:     "2025-01-01T00:00:00Z",
		},
	}
	server := mockPatchAPIServer(t, patches)
	defer server.Close()

	stdout, _, err := runPatchCmd(t, server.URL, "test-api-key",
		"list", "cp-ws")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	// Verify stdout contains patch branch names.
	if !strings.Contains(stdout, "feature/alpha") {
		t.Errorf("stdout should contain 'feature/alpha'; got: %s", stdout)
	}
	if !strings.Contains(stdout, "feature/beta") {
		t.Errorf("stdout should contain 'feature/beta'; got: %s", stdout)
	}
}

// TS-15-43: Running 'afc patch remove <workspace-slug> <patch-id>' sends
// DELETE to the patches endpoint and prints confirmation, exiting 0.
// Requirement: 15-REQ-14.3
func TestCLI_PatchRemove_Success(t *testing.T) {
	patches := map[string]patchResp{
		"uuid-1": {
			ID:            "uuid-1",
			WorkspaceSlug: "cp-ws",
			BranchName:    "feature/removeme",
			Position:      1,
			Status:        "active",
			AddedAt:       "2025-01-01T00:00:00Z",
			UpdatedAt:     "2025-01-01T00:00:00Z",
		},
	}
	server := mockPatchAPIServer(t, patches)
	defer server.Close()

	_, _, err := runPatchCmd(t, server.URL, "test-api-key",
		"remove", "cp-ws", "uuid-1")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	// Verify the patch was removed from the mock server.
	if _, exists := patches["uuid-1"]; exists {
		t.Error("patch should have been deleted from mock server")
	}
}

// TS-15-44: Running 'afc patch reorder <workspace-slug> <id1> <id2> <id3>'
// sends POST to the reorder endpoint and prints the reordered list, exiting 0.
// Requirement: 15-REQ-14.4
func TestCLI_PatchReorder_Success(t *testing.T) {
	patches := map[string]patchResp{
		"uuid-1": {
			ID:            "uuid-1",
			WorkspaceSlug: "cp-ws",
			BranchName:    "feature/a",
			Position:      1,
			Status:        "active",
			AddedAt:       "2025-01-01T00:00:00Z",
			UpdatedAt:     "2025-01-01T00:00:00Z",
		},
		"uuid-2": {
			ID:            "uuid-2",
			WorkspaceSlug: "cp-ws",
			BranchName:    "feature/b",
			Position:      2,
			Status:        "active",
			AddedAt:       "2025-01-01T00:00:00Z",
			UpdatedAt:     "2025-01-01T00:00:00Z",
		},
		"uuid-3": {
			ID:            "uuid-3",
			WorkspaceSlug: "cp-ws",
			BranchName:    "feature/c",
			Position:      3,
			Status:        "active",
			AddedAt:       "2025-01-01T00:00:00Z",
			UpdatedAt:     "2025-01-01T00:00:00Z",
		},
	}
	server := mockPatchAPIServer(t, patches)
	defer server.Close()

	stdout, _, err := runPatchCmd(t, server.URL, "test-api-key",
		"reorder", "cp-ws", "uuid-3", "uuid-1", "uuid-2")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	// Verify stdout contains patch IDs.
	if !strings.Contains(stdout, "uuid-3") {
		t.Errorf("stdout should contain 'uuid-3'; got: %s", stdout)
	}
}

// 15-REQ-14.E3: Running 'afc patch reorder' with no patch IDs prints a usage
// error and exits non-zero without making an API call.
// Requirement: 15-REQ-14.E3
func TestCLI_PatchReorder_NoPatchIDs(t *testing.T) {
	server := mockPatchAPIServer(t, make(map[string]patchResp))
	defer server.Close()

	_, _, err := runPatchCmd(t, server.URL, "test-api-key",
		"reorder", "cp-ws")

	// The command requires at least a workspace-slug arg, and the patch IDs
	// are additional args. With just the workspace slug, there are no patch IDs.
	// This should result in an error or warning.
	if err == nil {
		t.Error("expected error for 'afc patch reorder' with no patch IDs; got nil")
	}
}

// TS-15-45: Running 'afc patch update <workspace-slug> <patch-id> --status disabled'
// sends PATCH to the patch endpoint and prints the updated patch, exiting 0.
// Requirement: 15-REQ-14.5
func TestCLI_PatchUpdate_Success(t *testing.T) {
	patches := map[string]patchResp{
		"uuid-1": {
			ID:            "uuid-1",
			WorkspaceSlug: "cp-ws",
			BranchName:    "feature/a",
			Position:      1,
			Status:        "active",
			AddedAt:       "2025-01-01T00:00:00Z",
			UpdatedAt:     "2025-01-01T00:00:00Z",
		},
	}
	server := mockPatchAPIServer(t, patches)
	defer server.Close()

	stdout, _, err := runPatchCmd(t, server.URL, "test-api-key",
		"update", "cp-ws", "uuid-1", "--status", "disabled")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	if !strings.Contains(stdout, "disabled") {
		t.Errorf("stdout should contain 'disabled'; got: %s", stdout)
	}
}

// 15-REQ-14.E2: API error response for any patch command displays error message
// and exits non-zero.
// Requirement: 15-REQ-14.E2
func TestCLI_PatchRemove_APIError(t *testing.T) {
	// Empty patches map — uuid-1 doesn't exist.
	server := mockPatchAPIServer(t, make(map[string]patchResp))
	defer server.Close()

	stdout, _, err := runPatchCmd(t, server.URL, "test-api-key",
		"remove", "cp-ws", "nonexistent")

	if err == nil {
		t.Error("expected error for nonexistent patch; got nil")
	}

	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope; got: %s", stdout)
	}
}
