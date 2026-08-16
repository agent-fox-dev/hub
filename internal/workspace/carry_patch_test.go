package workspace

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// ========================================================================
// Spec 15 Task 1.2: Carry-patch workspace creation validation
// (TS-15-4, TS-15-5, TS-15-6, TS-15-7, TS-15-8, TS-15-9)
// Requirements: 15-REQ-2
// ========================================================================

// TS-15-4: Creating a workspace with workspace_mode='carry_patch', a valid
// upstream_url, and an explicit integration_branch returns HTTP 201 with all
// three new fields populated correctly.
// Requirement: 15-REQ-2.1
func TestCarryPatch_CreateCarryPatchSuccess(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{
		"slug": "cp-create-ok",
		"git_url": "https://github.com/myfork/repo.git",
		"workspace_mode": "carry_patch",
		"upstream_url": "https://github.com/upstream/repo.git",
		"integration_branch": "deploy"
	}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if v, ok := resp["workspace_mode"]; !ok {
		t.Error("response missing 'workspace_mode' field")
	} else if v != "carry_patch" {
		t.Errorf("workspace_mode = %v; want %q", v, "carry_patch")
	}

	if v, ok := resp["upstream_url"]; !ok {
		t.Error("response missing 'upstream_url' field")
	} else if v != "https://github.com/upstream/repo.git" {
		t.Errorf("upstream_url = %v; want %q", v, "https://github.com/upstream/repo.git")
	}

	if v, ok := resp["integration_branch"]; !ok {
		t.Error("response missing 'integration_branch' field")
	} else if v != "deploy" {
		t.Errorf("integration_branch = %v; want %q", v, "deploy")
	}
}

// TS-15-5: Creating a workspace with workspace_mode='carry_patch' but
// omitting upstream_url returns HTTP 400 with a JSON error body.
// Requirement: 15-REQ-2.2
func TestCarryPatch_CreateCarryPatchMissingUpstreamURL(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{
		"slug": "cp-no-upstream",
		"git_url": "https://github.com/myfork/repo.git",
		"workspace_mode": "carry_patch"
	}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-15-6: Creating a workspace with workspace_mode='carry_patch' and an
// upstream_url that fails URL validation (e.g., missing host) returns HTTP
// 400 with a JSON error body.
// Requirement: 15-REQ-2.3
func TestCarryPatch_CreateCarryPatchInvalidUpstreamURL(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	invalidURLs := []struct {
		name string
		url  string
	}{
		{"missing_host", "http://"},
		{"empty", ""},
		{"plain_http", "http://example.com/repo"},
		{"no_path", "https://example.com"},
	}

	for _, tc := range invalidURLs {
		t.Run(tc.name, func(t *testing.T) {
			body := `{
				"slug": "cp-bad-url-` + tc.name + `",
				"git_url": "https://github.com/myfork/repo.git",
				"workspace_mode": "carry_patch",
				"upstream_url": "` + tc.url + `"
			}`
			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST status = %d; want %d; body: %s",
					rec.Code, http.StatusBadRequest, rec.Body.String())
			}

			resp := parseErrorEnvelope(t, rec)
			if resp.Error.Code != http.StatusBadRequest {
				t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
			}
			if resp.Error.Message == "" {
				t.Error("error.message is empty; want non-empty descriptive message")
			}
		})
	}
}

// TS-15-7: Creating a workspace with workspace_mode='standard' (or omitting
// workspace_mode) stores upstream_url as NULL and integration_branch as NULL
// in the response.
// Requirement: 15-REQ-2.4
func TestCarryPatch_CreateStandard(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	t.Run("explicit_standard", func(t *testing.T) {
		body := `{
			"slug": "std-explicit",
			"git_url": "https://github.com/myfork/repo.git",
			"workspace_mode": "standard"
		}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

		if rec.Code != http.StatusCreated {
			t.Fatalf("POST status = %d; want %d; body: %s",
				rec.Code, http.StatusCreated, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if v, ok := resp["workspace_mode"]; !ok {
			t.Error("response missing 'workspace_mode' field")
		} else if v != "standard" {
			t.Errorf("workspace_mode = %v; want %q", v, "standard")
		}

		if v, ok := resp["upstream_url"]; !ok {
			t.Error("response missing 'upstream_url' field")
		} else if v != nil {
			t.Errorf("upstream_url = %v; want null", v)
		}

		if v, ok := resp["integration_branch"]; !ok {
			t.Error("response missing 'integration_branch' field")
		} else if v != nil {
			t.Errorf("integration_branch = %v; want null", v)
		}
	})

	t.Run("omitted_workspace_mode", func(t *testing.T) {
		body := `{
			"slug": "std-omitted",
			"git_url": "https://github.com/myfork/repo2.git"
		}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

		if rec.Code != http.StatusCreated {
			t.Fatalf("POST status = %d; want %d; body: %s",
				rec.Code, http.StatusCreated, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if v, ok := resp["workspace_mode"]; !ok {
			t.Error("response missing 'workspace_mode' field")
		} else if v != "standard" {
			t.Errorf("workspace_mode = %v; want %q", v, "standard")
		}

		if v, ok := resp["upstream_url"]; !ok {
			t.Error("response missing 'upstream_url' field")
		} else if v != nil {
			t.Errorf("upstream_url = %v; want null", v)
		}

		if v, ok := resp["integration_branch"]; !ok {
			t.Error("response missing 'integration_branch' field")
		} else if v != nil {
			t.Errorf("integration_branch = %v; want null", v)
		}
	})
}

// TS-15-8: Creating a workspace with workspace_mode='standard' and also
// providing upstream_url returns HTTP 400 with a JSON error body.
// Requirement: 15-REQ-2.5
func TestCarryPatch_CreateStandardWithUpstreamURL(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	t.Run("upstream_url_provided", func(t *testing.T) {
		body := `{
			"slug": "std-with-upstream",
			"git_url": "https://github.com/myfork/repo.git",
			"workspace_mode": "standard",
			"upstream_url": "https://github.com/upstream/repo.git"
		}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST status = %d; want %d; body: %s",
				rec.Code, http.StatusBadRequest, rec.Body.String())
		}

		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusBadRequest {
			t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
		}
		if resp.Error.Message == "" {
			t.Error("error.message is empty; want non-empty descriptive message")
		}
	})

	t.Run("integration_branch_provided", func(t *testing.T) {
		body := `{
			"slug": "std-with-branch",
			"git_url": "https://github.com/myfork/repo.git",
			"workspace_mode": "standard",
			"integration_branch": "deploy"
		}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST status = %d; want %d; body: %s",
				rec.Code, http.StatusBadRequest, rec.Body.String())
		}

		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusBadRequest {
			t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
		}
		if resp.Error.Message == "" {
			t.Error("error.message is empty; want non-empty descriptive message")
		}
	})
}

// TS-15-9: Creating a workspace with workspace_mode='carry_patch' and
// omitting integration_branch defaults integration_branch to 'deploy'
// in the response.
// Requirement: 15-REQ-2.6
func TestCarryPatch_CreateCarryPatchDefaultIntegrationBranch(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{
		"slug": "cp-default-branch",
		"git_url": "https://github.com/myfork/repo.git",
		"workspace_mode": "carry_patch",
		"upstream_url": "https://github.com/upstream/repo.git"
	}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if v, ok := resp["integration_branch"]; !ok {
		t.Error("response missing 'integration_branch' field")
	} else if v != "deploy" {
		t.Errorf("integration_branch = %v; want %q", v, "deploy")
	}
}

// 15-REQ-2.E1: Creating a workspace with an unrecognized workspace_mode
// returns HTTP 400.
func TestCarryPatch_CreateUnrecognizedMode(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{
		"slug": "bad-mode-ws",
		"git_url": "https://github.com/myfork/repo.git",
		"workspace_mode": "invalid_mode"
	}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// 15-REQ-2.E2: Creating a workspace with workspace_mode='carry_patch' and
// integration_branch equal to an empty string defaults to 'deploy'.
func TestCarryPatch_CreateEmptyIntegrationBranchDefaultsToDeploy(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{
		"slug": "cp-empty-branch",
		"git_url": "https://github.com/myfork/repo.git",
		"workspace_mode": "carry_patch",
		"upstream_url": "https://github.com/upstream/repo.git",
		"integration_branch": ""
	}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if v, ok := resp["integration_branch"]; !ok {
		t.Error("response missing 'integration_branch' field")
	} else if v != "deploy" {
		t.Errorf("integration_branch = %v; want %q (defaulted from empty)", v, "deploy")
	}
}

// 15-REQ-2.E3: A PAT without workspace creation permission cannot create
// a carry_patch workspace.
func TestCarryPatch_CreateCarryPatchPATForbidden(t *testing.T) {
	env := newTestEnv(t)
	auth := patAuth("alice-id", "workspaces:read")

	body := `{
		"slug": "cp-pat-forbidden",
		"git_url": "https://github.com/myfork/repo.git",
		"workspace_mode": "carry_patch",
		"upstream_url": "https://github.com/upstream/repo.git"
	}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusForbidden {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusForbidden)
	}
}

// Carry-patch creation with SSH upstream URL succeeds.
func TestCarryPatch_CreateCarryPatchSSHUpstreamURL(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{
		"slug": "cp-ssh-upstream",
		"git_url": "https://github.com/myfork/repo.git",
		"workspace_mode": "carry_patch",
		"upstream_url": "git@github.com:upstream/repo.git",
		"integration_branch": "deploy"
	}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if v, ok := resp["workspace_mode"]; !ok {
		t.Error("response missing 'workspace_mode' field")
	} else if v != "carry_patch" {
		t.Errorf("workspace_mode = %v; want %q", v, "carry_patch")
	}

	if v, ok := resp["upstream_url"]; !ok {
		t.Error("response missing 'upstream_url' field")
	} else if v != "git@github.com:upstream/repo.git" {
		t.Errorf("upstream_url = %v; want %q", v, "git@github.com:upstream/repo.git")
	}
}

// ========================================================================
// Spec 15 Task 1.3: Immutability of carry-patch structural fields
// (TS-15-10)
// Requirements: 15-REQ-3
// ========================================================================

// TS-15-10: Sending PATCH /api/v1/workspaces/:slug with workspace_mode in
// the body returns HTTP 400 and does not modify any workspace fields.
// Requirement: 15-REQ-3.1
func TestCarryPatch_UpdateImmutableWorkspaceMode(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u1-id")

	env.seedWorkspace(t, &Workspace{
		Slug:    "immutable-cp-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	immutableBodies := []struct {
		name string
		body string
	}{
		{"workspace_mode", `{"workspace_mode":"carry_patch"}`},
		{"upstream_url", `{"upstream_url":"https://github.com/upstream/repo.git"}`},
		{"integration_branch", `{"integration_branch":"deploy"}`},
	}

	for _, tc := range immutableBodies {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/immutable-cp-ws", tc.body, auth)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("PATCH with %s: status = %d; want %d; body: %s",
					tc.name, rec.Code, http.StatusBadRequest, rec.Body.String())
			}

			resp := parseErrorEnvelope(t, rec)
			if resp.Error.Code != http.StatusBadRequest {
				t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
			}
			if resp.Error.Message == "" {
				t.Error("error.message is empty; want non-empty descriptive message")
			}
			// The error message must specifically identify the immutable field,
			// not just say "no updatable field".
			if !strings.Contains(resp.Error.Message, tc.name) {
				t.Errorf("error.message = %q; want message mentioning %q",
					resp.Error.Message, tc.name)
			}
		})
	}

	// Verify no database changes occurred.
	var dbGitURL string
	err := env.db.QueryRow("SELECT git_url FROM workspaces WHERE slug = ?", "immutable-cp-ws").Scan(&dbGitURL)
	if err != nil {
		t.Fatalf("DB query failed: %v", err)
	}
	if dbGitURL != "https://github.com/org/repo" {
		t.Errorf("DB git_url = %q; want %q (unchanged)", dbGitURL, "https://github.com/org/repo")
	}
}

// 15-REQ-3.E1: PATCH with workspace_mode set to the same value already
// stored is still rejected (immutability applies regardless).
func TestCarryPatch_UpdateImmutableSameValue(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u1-id")

	env.seedWorkspace(t, &Workspace{
		Slug:    "immutable-same-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	// workspace_mode is 'standard' by default; try setting it to 'standard'.
	body := `{"workspace_mode":"standard"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/immutable-same-ws", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH with same workspace_mode: status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	// Error message must specifically mention workspace_mode as immutable,
	// not just "no updatable field".
	if !strings.Contains(resp.Error.Message, "workspace_mode") {
		t.Errorf("error.message = %q; want message mentioning 'workspace_mode'",
			resp.Error.Message)
	}
}

// 15-REQ-3.E2: PATCH with both mutable fields (description) and immutable
// fields (workspace_mode) rejects the entire request without applying
// any changes.
func TestCarryPatch_UpdateMixedMutableImmutableRejected(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u1-id")

	env.seedWorkspace(t, &Workspace{
		Slug:        "mixed-fields-ws",
		GitURL:      "https://github.com/org/repo",
		OwnerID:     "u1-id",
		Status:      "active",
		Description: "original description",
	})

	body := `{"description":"new description","workspace_mode":"carry_patch"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/mixed-fields-ws", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH with mixed fields: status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}

	// Verify no fields were updated (description must remain unchanged).
	var dbDesc string
	err := env.db.QueryRow("SELECT description FROM workspaces WHERE slug = ?", "mixed-fields-ws").Scan(&dbDesc)
	if err != nil {
		t.Fatalf("DB query failed: %v", err)
	}
	if dbDesc != "original description" {
		t.Errorf("description = %q; want %q (unchanged after rejected PATCH)", dbDesc, "original description")
	}
}

// ========================================================================
// Spec 15 Task 1.3: Workspace response schema extension
// (TS-15-11, TS-15-12, TS-15-13)
// Requirements: 15-REQ-4
// ========================================================================

// TS-15-11: Every workspace JSON response (GET, POST, PATCH) always includes
// workspace_mode, upstream_url, and integration_branch fields.
// Requirement: 15-REQ-4.1
func TestCarryPatch_ResponseIncludesNewFields(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u1-id")

	// assertCarryPatchFields checks that the response body contains
	// workspace_mode, upstream_url, and integration_branch at the top level.
	assertCarryPatchFields := func(t *testing.T, body []byte, endpoint string) {
		t.Helper()
		var resp map[string]any
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("%s: failed to decode response: %v", endpoint, err)
		}
		for _, field := range []string{"workspace_mode", "upstream_url", "integration_branch"} {
			if _, ok := resp[field]; !ok {
				t.Errorf("%s: response missing %q field", endpoint, field)
			}
		}
	}

	// Create a standard workspace via API.
	t.Run("POST_create", func(t *testing.T) {
		body := `{"slug":"resp-fields-ws","git_url":"https://github.com/org/repo"}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST status = %d; want %d; body: %s",
				rec.Code, http.StatusCreated, rec.Body.String())
		}
		assertCarryPatchFields(t, rec.Body.Bytes(), "POST /api/v1/workspaces")
	})

	// GET single workspace.
	t.Run("GET_single", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/resp-fields-ws", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET status = %d; want %d", rec.Code, http.StatusOK)
		}
		assertCarryPatchFields(t, rec.Body.Bytes(), "GET /api/v1/workspaces/:slug")
	})

	// GET list workspaces — each element should include the fields.
	t.Run("GET_list", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET status = %d; want %d", rec.Code, http.StatusOK)
		}
		var list []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatalf("failed to decode list response: %v", err)
		}
		for i, item := range list {
			for _, field := range []string{"workspace_mode", "upstream_url", "integration_branch"} {
				if _, ok := item[field]; !ok {
					t.Errorf("GET /api/v1/workspaces[%d]: missing %q field", i, field)
				}
			}
		}
	})

	// PATCH workspace — response should include the fields.
	t.Run("PATCH_update", func(t *testing.T) {
		body := `{"display_name":"Updated Name"}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/resp-fields-ws", body, auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
		}
		assertCarryPatchFields(t, rec.Body.Bytes(), "PATCH /api/v1/workspaces/:slug")
	})
}

// TS-15-12: A standard workspace response serializes upstream_url as null
// and integration_branch as null.
// Requirement: 15-REQ-4.2
func TestCarryPatch_StandardResponseNullFields(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u1-id")

	env.seedWorkspace(t, &Workspace{
		Slug:    "std-resp-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/std-resp-ws", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if v, ok := resp["workspace_mode"]; !ok {
		t.Error("response missing 'workspace_mode' field")
	} else if v != "standard" {
		t.Errorf("workspace_mode = %v; want %q", v, "standard")
	}

	if v, ok := resp["upstream_url"]; !ok {
		t.Error("response missing 'upstream_url' field")
	} else if v != nil {
		t.Errorf("upstream_url = %v; want null", v)
	}

	if v, ok := resp["integration_branch"]; !ok {
		t.Error("response missing 'integration_branch' field")
	} else if v != nil {
		t.Errorf("integration_branch = %v; want null", v)
	}
}

// TS-15-13: A carry_patch workspace response serializes upstream_url and
// integration_branch with their stored non-null values.
// Requirement: 15-REQ-4.3
func TestCarryPatch_CarryPatchResponseNonNullFields(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u1-id")

	// Create a carry_patch workspace via the API.
	createBody := `{
		"slug": "cp-resp-ws",
		"git_url": "https://github.com/myfork/repo.git",
		"workspace_mode": "carry_patch",
		"upstream_url": "https://github.com/upstream/repo.git",
		"integration_branch": "deploy"
	}`
	createRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", createBody, auth)

	// If the workspace was not created (expected before implementation),
	// fall back to direct DB insertion with the new columns.
	if createRec.Code != http.StatusCreated {
		// Before implementation, workspace creation with carry_patch will fail
		// or silently create a standard workspace. Attempt direct INSERT if
		// the columns exist.
		_, err := env.db.Exec(
			`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description, created_at, updated_at, workspace_mode, upstream_url, integration_branch)
			 VALUES ('cp-resp-ws', 'https://github.com/myfork/repo.git', 'u1-id', 'active', 'cp-resp-ws', '', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z', 'carry_patch', 'https://github.com/upstream/repo.git', 'deploy')`,
		)
		if err != nil {
			t.Fatalf("could not seed carry_patch workspace (columns likely missing): %v", err)
		}
	}

	// GET the workspace and verify the response fields.
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/cp-resp-ws", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if v, ok := resp["workspace_mode"]; !ok {
		t.Error("response missing 'workspace_mode' field")
	} else if v != "carry_patch" {
		t.Errorf("workspace_mode = %v; want %q", v, "carry_patch")
	}

	if v, ok := resp["upstream_url"]; !ok {
		t.Error("response missing 'upstream_url' field")
	} else if v != "https://github.com/upstream/repo.git" {
		t.Errorf("upstream_url = %v; want %q", v, "https://github.com/upstream/repo.git")
	}

	if v, ok := resp["integration_branch"]; !ok {
		t.Error("response missing 'integration_branch' field")
	} else if v != "deploy" {
		t.Errorf("integration_branch = %v; want %q", v, "deploy")
	}
}
