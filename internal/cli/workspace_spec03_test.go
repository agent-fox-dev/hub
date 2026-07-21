package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// spec03WorkspaceResp is the workspace JSON response with display_name and description fields.
// Used by spec 03 tests that exercise the new metadata fields.
type spec03WorkspaceResp struct {
	Slug        string  `json:"slug"`
	GitURL      string  `json:"git_url"`
	Branch      *string `json:"branch,omitempty"`
	OwnerID     string  `json:"owner_id"`
	OrgID       *string `json:"org_id,omitempty"`
	Status      string  `json:"status"`
	DisplayName string  `json:"display_name"`
	Description string  `json:"description"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// respondSpec03Error writes an apikit-style error envelope using the existing writeJSON helper.
func respondSpec03Error(w http.ResponseWriter, code int, msg string) {
	e := errorResp{}
	e.Error.Code = code
	e.Error.Message = msg
	writeJSON(w, code, e)
}

// mockSpec03Server creates an httptest.Server that simulates the workspace API
// with display_name/description support and PATCH /api/v1/workspaces/:slug.
// The workspaces map is keyed by slug and supports the new metadata fields.
func mockSpec03Server(t *testing.T, workspaces map[string]spec03WorkspaceResp) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// POST /api/v1/workspaces -- create workspace with display_name/description.
	mux.HandleFunc("POST /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Slug        string  `json:"slug"`
			GitURL      string  `json:"git_url"`
			Branch      *string `json:"branch,omitempty"`
			OrgID       *string `json:"org_id,omitempty"`
			DisplayName *string `json:"display_name,omitempty"`
			Description *string `json:"description,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondSpec03Error(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if _, exists := workspaces[req.Slug]; exists {
			respondSpec03Error(w, http.StatusConflict, "workspace with this slug already exists")
			return
		}

		// Apply server-side defaults.
		displayName := req.Slug
		if req.DisplayName != nil {
			displayName = *req.DisplayName
		}
		description := ""
		if req.Description != nil {
			description = *req.Description
		}

		ws := spec03WorkspaceResp{
			Slug:        req.Slug,
			GitURL:      req.GitURL,
			Branch:      req.Branch,
			OwnerID:     "test-user-id",
			OrgID:       req.OrgID,
			Status:      "active",
			DisplayName: displayName,
			Description: description,
			CreatedAt:   "2025-01-01T00:00:00Z",
			UpdatedAt:   "2025-01-01T00:00:00Z",
		}
		workspaces[req.Slug] = ws
		writeJSON(w, http.StatusCreated, ws)
	})

	// PATCH /api/v1/workspaces/{slug} -- update workspace properties.
	mux.HandleFunc("PATCH /api/v1/workspaces/{slug}", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		ws, ok := workspaces[slug]
		if !ok {
			respondSpec03Error(w, http.StatusNotFound, "workspace not found")
			return
		}
		if ws.Status == "archived" {
			respondSpec03Error(w, http.StatusBadRequest, "cannot update archived workspace")
			return
		}

		// Parse the PATCH body as raw map to handle null values.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			respondSpec03Error(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if len(body) == 0 {
			respondSpec03Error(w, http.StatusBadRequest, "no fields to update")
			return
		}

		// Apply updates.
		if v, ok := body["display_name"]; ok {
			if v == nil {
				ws.DisplayName = ws.Slug // clear to default
			} else if s, ok := v.(string); ok {
				ws.DisplayName = s
			}
		}
		if v, ok := body["description"]; ok {
			if v == nil {
				ws.Description = "" // clear to default
			} else if s, ok := v.(string); ok {
				ws.Description = s
			}
		}
		if v, ok := body["org_id"]; ok {
			if v == nil {
				ws.OrgID = nil
			} else if s, ok := v.(string); ok {
				ws.OrgID = &s
			}
		}

		ws.UpdatedAt = "2025-01-01T01:00:00Z"
		workspaces[slug] = ws
		writeJSON(w, http.StatusOK, ws)
	})

	// GET /api/v1/workspaces/{slug} -- get workspace.
	mux.HandleFunc("GET /api/v1/workspaces/{slug}", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		ws, ok := workspaces[slug]
		if !ok {
			respondSpec03Error(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeJSON(w, http.StatusOK, ws)
	})

	return httptest.NewServer(mux)
}

// ---------------------------------------------------------------------------
// Task 4.1 — Integration tests: afc workspace update success path and
//            PATCH body construction (TS-03-30, TS-03-31, TS-03-32)
// ---------------------------------------------------------------------------

// TS-03-30: Verify that 'afc workspace update <slug> --display-name' calls PATCH,
// prints the updated workspace JSON to stdout, and exits 0.
// Requirement: 03-REQ-7.1
func TestSpec03_Group4_UpdateDisplayName_TS0330(t *testing.T) {
	workspaces := map[string]spec03WorkspaceResp{
		"cli-update-ws": {
			Slug:        "cli-update-ws",
			GitURL:      "https://github.com/org/repo",
			OwnerID:     "test-user-id",
			Status:      "active",
			DisplayName: "cli-update-ws",
			Description: "",
			CreatedAt:   "2025-01-01T00:00:00Z",
			UpdatedAt:   "2025-01-01T00:00:00Z",
		},
	}
	server := mockSpec03Server(t, workspaces)
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"update", "cli-update-ws", "--display-name", "CLI Updated")

	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr is not empty: %s", stderr)
	}

	var ws spec03WorkspaceResp
	if jsonErr := json.Unmarshal([]byte(stdout), &ws); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if ws.DisplayName != "CLI Updated" {
		t.Errorf("display_name = %q; want %q", ws.DisplayName, "CLI Updated")
	}
}

// TS-03-31: Verify that --clear-display-name maps to display_name:null,
// --clear-description to description:null, --clear-org to org_id:null
// in the PATCH body.
// Requirement: 03-REQ-7.2
func TestSpec03_Group4_ClearFlagsMapToNull_TS0331(t *testing.T) {
	// Custom mock that captures the PATCH request body.
	var capturedBody []byte
	var capturedMethod string
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/v1/workspaces/{slug}", func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedBody, _ = io.ReadAll(r.Body)
		// Return a valid workspace response.
		writeJSON(w, http.StatusOK, spec03WorkspaceResp{
			Slug:        "some-ws",
			GitURL:      "https://github.com/org/repo",
			OwnerID:     "test-user-id",
			Status:      "active",
			DisplayName: "some-ws",
			Description: "",
			CreatedAt:   "2025-01-01T00:00:00Z",
			UpdatedAt:   "2025-01-01T01:00:00Z",
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"update", "some-ws", "--clear-display-name", "--clear-description", "--clear-org")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	if capturedMethod != "PATCH" {
		t.Fatalf("expected PATCH request; got %q", capturedMethod)
	}

	// Parse captured body as raw JSON to check for null values.
	var body map[string]any
	if jsonErr := json.Unmarshal(capturedBody, &body); jsonErr != nil {
		t.Fatalf("captured body is not valid JSON: %v\nbody: %s", jsonErr, capturedBody)
	}

	// display_name should be null (not absent).
	if v, ok := body["display_name"]; !ok {
		t.Error("display_name key is absent from PATCH body; want null")
	} else if v != nil {
		t.Errorf("display_name = %v; want null", v)
	}

	// description should be null (not absent).
	if v, ok := body["description"]; !ok {
		t.Error("description key is absent from PATCH body; want null")
	} else if v != nil {
		t.Errorf("description = %v; want null", v)
	}

	// org_id should be null (not absent).
	if v, ok := body["org_id"]; !ok {
		t.Error("org_id key is absent from PATCH body; want null")
	} else if v != nil {
		t.Errorf("org_id = %v; want null", v)
	}
}

// TS-03-32: Verify that afc workspace update only includes in the PATCH body
// the fields corresponding to flags that were explicitly provided.
// Requirement: 03-REQ-7.3
func TestSpec03_Group4_OnlyProvidedFieldsInBody_TS0332(t *testing.T) {
	// Custom mock that captures the PATCH request body.
	var capturedBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/v1/workspaces/{slug}", func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		writeJSON(w, http.StatusOK, spec03WorkspaceResp{
			Slug:        "some-ws",
			GitURL:      "https://github.com/org/repo",
			OwnerID:     "test-user-id",
			Status:      "active",
			DisplayName: "Only This",
			Description: "",
			CreatedAt:   "2025-01-01T00:00:00Z",
			UpdatedAt:   "2025-01-01T01:00:00Z",
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"update", "some-ws", "--display-name", "Only This")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	var body map[string]any
	if jsonErr := json.Unmarshal(capturedBody, &body); jsonErr != nil {
		t.Fatalf("captured body is not valid JSON: %v\nbody: %s", jsonErr, capturedBody)
	}

	// display_name should be present with the provided value.
	if v, ok := body["display_name"]; !ok {
		t.Error("display_name key is absent from PATCH body")
	} else if s, ok := v.(string); !ok || s != "Only This" {
		t.Errorf("display_name = %v; want %q", v, "Only This")
	}

	// description and org_id should be absent (not provided).
	if _, ok := body["description"]; ok {
		t.Error("description key is present in PATCH body; want absent")
	}
	if _, ok := body["org_id"]; ok {
		t.Error("org_id key is present in PATCH body; want absent")
	}
}

// ---------------------------------------------------------------------------
// Task 4.2 — Edge-case tests: afc workspace update error handling
//            (TS-03-E15, TS-03-E16, TS-03-E17, TS-03-E18)
// ---------------------------------------------------------------------------

// TS-03-E15: Verify that running afc workspace update without any update flags
// prints a usage hint to stderr and exits with code 1 without making an HTTP call.
// Requirement: 03-REQ-7.E1
func TestSpec03_Group4_UpdateNoFlags_TS03E15(t *testing.T) {
	// Track whether any HTTP request was made.
	var requestMade bool
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		requestMade = true
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"update", "some-ws")

	if err == nil {
		t.Error("expected error when no update flags provided; got nil")
	}
	if stderr == "" {
		t.Error("stderr is empty; want usage hint about required flags")
	}
	lower := strings.ToLower(stderr)
	if !strings.Contains(lower, "flag") && !strings.Contains(lower, "usage") &&
		!strings.Contains(lower, "at least one") {
		t.Errorf("stderr should contain usage hint about flags; got: %s", stderr)
	}
	if requestMade {
		t.Error("HTTP request was made; want no request when no flags provided")
	}
}

// TS-03-E16: Verify that when the API returns a non-2xx status, afc workspace
// update prints the error message to stderr and exits with code 1.
// Requirement: 03-REQ-7.E2
func TestSpec03_Group4_UpdateAPIError_TS03E16(t *testing.T) {
	// Mock server returns 404 for any PATCH request.
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/v1/workspaces/{slug}", func(w http.ResponseWriter, r *http.Request) {
		respondSpec03Error(w, http.StatusNotFound, "workspace not found")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"update", "missing-ws", "--display-name", "x")

	if err == nil {
		t.Error("expected error for non-2xx API response; got nil")
	}
	if stderr == "" {
		t.Error("stderr is empty; want API error message")
	}
	lower := strings.ToLower(stderr)
	if !strings.Contains(lower, "not found") && !strings.Contains(lower, "workspace") {
		t.Errorf("stderr should contain API error message; got: %s", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty on error; got: %s", stdout)
	}
}

// TS-03-E17: Verify that afc workspace update exits within the configured
// client timeout when the API call times out or fails, without hanging
// indefinitely.
// Requirement: 03-REQ-7.E3
func TestSpec03_Group4_UpdateTimeout_TS03E17(t *testing.T) {
	// Mock server that hangs until the request context is cancelled.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			// Client disconnected.
		case <-time.After(60 * time.Second):
			// Fallback — should not be reached during normal test execution.
		}
	}))
	defer server.Close()

	start := time.Now()
	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"update", "timeout-ws", "--display-name", "x")
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected error for timeout; got nil")
	}
	// The command should not hang for more than 15 seconds.
	if elapsed > 15*time.Second {
		t.Errorf("command took %v; want less than 15s (should not hang indefinitely)", elapsed)
	}
	if stderr == "" {
		t.Error("stderr is empty; want timeout or connection error message")
	}
	// The error should mention timeout/connection/deadline (not just "unknown command").
	lower := strings.ToLower(stderr)
	if !strings.Contains(lower, "timeout") && !strings.Contains(lower, "connection") &&
		!strings.Contains(lower, "deadline") {
		t.Errorf("stderr should contain timeout/connection/deadline error; got: %s", stderr)
	}
}

// TS-03-E18: Verify that afc workspace update prints a descriptive parse error
// to stderr and exits 1 when the API returns success with malformed/unexpected
// response body.
// Requirement: 03-REQ-7.E4
func TestSpec03_Group4_UpdateMalformedResponse_TS03E18(t *testing.T) {
	// Mock server returns HTTP 200 with non-JSON body.
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/v1/workspaces/{slug}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"update", "malformed-ws", "--display-name", "x")

	if err == nil {
		t.Error("expected error for malformed response; got nil")
	}
	if stderr == "" {
		t.Error("stderr is empty; want parse error description")
	}
	lower := strings.ToLower(stderr)
	if !strings.Contains(lower, "parse") && !strings.Contains(lower, "invalid") &&
		!strings.Contains(lower, "unexpected") && !strings.Contains(lower, "unmarshal") {
		t.Errorf("stderr should describe a parse/invalid/unexpected error; got: %s", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty on parse error; got: %s", stdout)
	}
}

// ---------------------------------------------------------------------------
// Task 4.3 — Integration tests: afc workspace create --display-name and
//            --description flags (TS-03-33, TS-03-34)
// ---------------------------------------------------------------------------

// TS-03-33: Verify that afc workspace create with --display-name and
// --description flags includes those values in the POST body and returns
// workspace JSON with those values.
// Requirement: 03-REQ-8.1
func TestSpec03_Group4_CreateWithDisplayNameDescription_TS0333(t *testing.T) {
	server := mockSpec03Server(t, make(map[string]spec03WorkspaceResp))
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--slug", "create-cli-ws",
		"--git-url", "https://git.example.com/repo",
		"--display-name", "CLI Created",
		"--description", "Created via CLI")

	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}

	var ws spec03WorkspaceResp
	if jsonErr := json.Unmarshal([]byte(stdout), &ws); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if ws.DisplayName != "CLI Created" {
		t.Errorf("display_name = %q; want %q", ws.DisplayName, "CLI Created")
	}
	if ws.Description != "Created via CLI" {
		t.Errorf("description = %q; want %q", ws.Description, "Created via CLI")
	}
}

// TS-03-34: Verify that afc workspace create without --display-name and
// --description remains backward-compatible and receives server-side defaults.
// Requirement: 03-REQ-8.2
func TestSpec03_Group4_CreateBackwardCompatible_TS0334(t *testing.T) {
	server := mockSpec03Server(t, make(map[string]spec03WorkspaceResp))
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--slug", "compat-cli-ws",
		"--git-url", "https://git.example.com/repo")

	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}

	var ws spec03WorkspaceResp
	if jsonErr := json.Unmarshal([]byte(stdout), &ws); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if ws.DisplayName != "compat-cli-ws" {
		t.Errorf("display_name = %q; want %q (should default to slug)", ws.DisplayName, "compat-cli-ws")
	}
	if ws.Description != "" {
		t.Errorf("description = %q; want %q (should default to empty)", ws.Description, "")
	}
}
