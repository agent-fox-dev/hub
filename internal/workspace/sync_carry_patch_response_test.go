package workspace

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
)

// ========================================================================
// Carry-patch sync response composition
// Requirements: 16-REQ-5.1, 16-REQ-5.E4
// ========================================================================

// 16-REQ-5.1 states that the carry-patch sync response carries
// patches_merged and rebuild_triggered "in addition to standard sync fields".
// The endpoint previously returned the hook's body verbatim, dropping the
// workspace record entirely. These tests pin the composed shape.

// registerSyncHook installs a carry-patch sync hook for the duration of the
// test and restores the previous value afterwards. The hook is package-level
// state, so tests that touch it must not run in parallel.
func registerSyncHook(t *testing.T, fn CarryPatchSyncFunc) {
	t.Helper()
	previous := carryPatchSyncHook
	RegisterCarryPatchSyncHook(fn)
	t.Cleanup(func() { RegisterCarryPatchSyncHook(previous) })
}

// A carry-patch sync merges the hook's fields into the workspace response
// rather than replacing it.
func TestCarryPatchSync_ResponseIncludesStandardSyncFields(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:              "cp-ws",
		GitURL:            "https://github.com/example/repo.git",
		OwnerID:           "alice-id",
		Status:            "active",
		CloneStatus:       "ready",
		WorkspaceMode:     "carry_patch",
		UpstreamURL:       strPtr("https://github.com/upstream/repo.git"),
		IntegrationBranch: strPtr("deploy"),
	})

	jobID := "d3b07384-d113-4ec5-8a4e-a12345678901"
	registerSyncHook(t, func(_ echo.Context, _, _ string) (map[string]any, bool, error) {
		return map[string]any{
			"patches_merged":      []string{"feature/already-merged"},
			"rebuild_triggered":   true,
			"rebuild_job_id":      jobID,
			"force_push_detected": false,
		}, true, nil
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cp-ws/sync", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync returned %d; want %d. body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Standard sync fields must be present (16-REQ-5.1).
	for _, field := range []string{
		"slug", "git_url", "status", "clone_status", "sync_mode", "sync_status",
		"head_sha", "upstream_head_sha", "last_sync_at", "workspace_mode",
		"upstream_url", "integration_branch", "created_at", "updated_at",
	} {
		if _, ok := body[field]; !ok {
			t.Errorf("response is missing standard sync field %q", field)
		}
	}

	if got := body["slug"]; got != "cp-ws" {
		t.Errorf("slug = %v; want %q", got, "cp-ws")
	}
	if got := body["workspace_mode"]; got != "carry_patch" {
		t.Errorf("workspace_mode = %v; want %q", got, "carry_patch")
	}

	// Carry-patch fields must be present alongside them.
	if got := body["rebuild_triggered"]; got != true {
		t.Errorf("rebuild_triggered = %v; want true", got)
	}
	if got := body["rebuild_job_id"]; got != jobID {
		t.Errorf("rebuild_job_id = %v; want %q", got, jobID)
	}
	if got := body["force_push_detected"]; got != false {
		t.Errorf("force_push_detected = %v; want false", got)
	}
	merged, ok := body["patches_merged"].([]any)
	if !ok {
		t.Fatalf("patches_merged = %#v; want a JSON array", body["patches_merged"])
	}
	if len(merged) != 1 || merged[0] != "feature/already-merged" {
		t.Errorf("patches_merged = %v; want [feature/already-merged]", merged)
	}
}

// rebuild_job_id is omitted, not null, when no rebuild was enqueued. The
// carry-patch response struct tags it `omitempty`, and the composed response
// must not reintroduce it.
func TestCarryPatchSync_OmitsRebuildJobIDWhenNoRebuild(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:              "cp-quiet",
		GitURL:            "https://github.com/example/repo.git",
		OwnerID:           "alice-id",
		Status:            "active",
		CloneStatus:       "ready",
		WorkspaceMode:     "carry_patch",
		UpstreamURL:       strPtr("https://github.com/upstream/repo.git"),
		IntegrationBranch: strPtr("deploy"),
	})

	registerSyncHook(t, func(_ echo.Context, _, _ string) (map[string]any, bool, error) {
		return map[string]any{
			"patches_merged":      []string{},
			"rebuild_triggered":   false,
			"force_push_detected": false,
		}, true, nil
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cp-quiet/sync", "", adminAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync returned %d; want %d. body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, present := body["rebuild_job_id"]; present {
		t.Errorf("rebuild_job_id is present (%v); want it omitted when no rebuild was enqueued",
			body["rebuild_job_id"])
	}
	if _, ok := body["slug"]; !ok {
		t.Error("response is missing the standard sync fields")
	}
}

// 16-REQ-5.E4: a hook that reports "not handled" falls through to the standard
// sync flow, and the response carries no carry-patch fields.
func TestCarryPatchSync_UnhandledFallsThroughToStandardSync(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:          "cp-fallthrough",
		GitURL:        "https://github.com/example/repo.git",
		OwnerID:       "alice-id",
		Status:        "active",
		CloneStatus:   "ready",
		WorkspaceMode: "carry_patch",
		UpstreamURL:   strPtr("https://github.com/upstream/repo.git"),
	})

	called := false
	registerSyncHook(t, func(_ echo.Context, _, _ string) (map[string]any, bool, error) {
		called = true
		return nil, false, nil
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cp-fallthrough/sync", "", adminAuth())

	if !called {
		t.Fatal("carry-patch sync hook was not called for a carry_patch workspace")
	}

	// The standard sync flow runs against a workspace with no real clone, so
	// the status depends on the git layer. Whatever it returns, the response
	// must not carry carry-patch fields.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		return // an error envelope is an acceptable outcome here
	}
	for _, field := range []string{"patches_merged", "rebuild_triggered", "force_push_detected"} {
		if _, present := body[field]; present {
			t.Errorf("unhandled sync response includes carry-patch field %q", field)
		}
	}
}

// An error from the hook propagates; the handler must not also write a
// workspace body on top of the error response.
func TestCarryPatchSync_HookErrorPropagates(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:          "cp-err",
		GitURL:        "https://github.com/example/repo.git",
		OwnerID:       "alice-id",
		Status:        "active",
		CloneStatus:   "ready",
		WorkspaceMode: "carry_patch",
		UpstreamURL:   strPtr("https://github.com/upstream/repo.git"),
	})

	registerSyncHook(t, func(c echo.Context, _, _ string) (map[string]any, bool, error) {
		return nil, true, respondError(c, http.StatusBadGateway, "upstream fetch failed")
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cp-err/sync", "", adminAuth())

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("POST /sync returned %d; want %d. body: %s",
			rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want the hook's failure message")
	}
}
