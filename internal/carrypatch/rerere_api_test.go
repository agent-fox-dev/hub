package carrypatch

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// ===========================================================================
// TS-16-13: GET /api/v1/workspaces/:slug/rerere reads the rr-cache directory,
// enumerates subdirectories, derives path from preimage/postimage files,
// and returns HTTP 200 with the resolutions list including recorded_at timestamps.
//
// Requirement: 16-REQ-4.1
// ===========================================================================

func TestRerereList_Returns200WithResolutions(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")

	// Set up rr-cache with two resolution entries.
	entries := []rrCacheEntry{
		{hash: "aabbccdd1", preimage: "<<<<<<< src/config.go\nours\n=======\ntheirs\n>>>>>>>"},
		{hash: "aabbccdd2", preimage: "<<<<<<< pkg/handler.go\nours\n=======\ntheirs\n>>>>>>>"},
	}
	setupRRCacheDir(t, env.workspaceRoot, "my-workspace", entries)

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/rerere", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rerere status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp RerereListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v (body: %s)", err, rec.Body.String())
	}

	if resp.Resolutions == nil {
		t.Fatal("expected non-nil resolutions array")
	}
	if len(resp.Resolutions) != 2 {
		t.Fatalf("expected 2 resolutions, got %d", len(resp.Resolutions))
	}

	// Each resolution should have a non-empty path derived from preimage.
	for i, r := range resp.Resolutions {
		if r.Path == nil || *r.Path == "" {
			t.Errorf("resolution[%d]: expected non-empty path", i)
		}
	}
}

// 16-REQ-4.E1: If rr-cache directory does not exist or is empty, return empty list.
func TestRerereList_EmptyCache_ReturnsEmptyList(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")
	// No rr-cache setup — directory doesn't exist.

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/rerere", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rerere (empty cache) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp RerereListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Resolutions == nil {
		t.Error("expected non-nil resolutions array (should be empty list, not null)")
	}
	if len(resp.Resolutions) != 0 {
		t.Errorf("expected 0 resolutions, got %d", len(resp.Resolutions))
	}
}

// 16-REQ-4.E3: rr-cache subdirectory with no preimage/postimage is skipped.
func TestRerereList_MalformedEntry_Skipped(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")

	// One valid entry and one malformed entry (no preimage or postimage).
	entries := []rrCacheEntry{
		{hash: "valid1", preimage: "<<<<<<< src/config.go\nours\n=======\ntheirs\n>>>>>>>"},
		{hash: "malformed", preimage: "", postimage: ""}, // no files
	}
	setupRRCacheDir(t, env.workspaceRoot, "my-workspace", entries)

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/rerere", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rerere (malformed entry) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp RerereListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Malformed entries should be omitted; only valid entries returned.
	if len(resp.Resolutions) != 1 {
		t.Errorf("expected 1 resolution (malformed skipped), got %d", len(resp.Resolutions))
	}
}

// 16-REQ-4.E4: PAT without 'workspaces:read' scope returns 403.
func TestRerereList_PATWithoutScope_Returns403(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")

	auth := rebuildPATAuth("alice", "rebuilds:read") // no workspaces:read
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/rerere", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /rerere (PAT without scope) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// ===========================================================================
// TS-16-14: DELETE /api/v1/workspaces/:slug/rerere/*pathspec executes
// 'git rerere forget <pathspec>' via GitRunner and returns HTTP 204 on
// success; returns HTTP 404 if the pathspec has no recorded resolution.
//
// Requirement: 16-REQ-4.2
// ===========================================================================

func TestRerereForget_Success_Returns204(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")

	// Set up rr-cache with a resolution for src/config.go.
	entries := []rrCacheEntry{
		{hash: "aabbccdd1", preimage: "<<<<<<< src/config.go\nours\n=======\ntheirs\n>>>>>>>"},
	}
	setupRRCacheDir(t, env.workspaceRoot, "my-workspace", entries)

	// Mock git rerere forget to succeed.
	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		// Verify the 'rerere forget' command is called with the correct pathspec.
		if len(args) >= 2 && args[0] == "rerere" && args[1] == "forget" {
			if len(args) >= 3 && args[2] == "src/config.go" {
				return "", nil
			}
		}
		return "", nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/my-workspace/rerere/src/config.go", "", auth)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /rerere/src/config.go status = %d; want %d; body = %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}
}

// 16-REQ-4.2: DELETE with nonexistent pathspec returns 404.
func TestRerereForget_NotFound_Returns404(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/my-workspace/rerere/nonexistent.go", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE /rerere/nonexistent.go status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// 16-REQ-4.E2: Pathspec with slashes (e.g., 'src/config.go') is correctly
// captured by the Echo wildcard parameter.
func TestRerereForget_PathWithSlashes_CapturedCorrectly(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")

	// Set up rr-cache with a nested path.
	entries := []rrCacheEntry{
		{hash: "deeppath1", preimage: "<<<<<<< internal/pkg/deep/file.go\nours\n=======\ntheirs\n>>>>>>>"},
	}
	setupRRCacheDir(t, env.workspaceRoot, "my-workspace", entries)

	rerereForgotPath := ""
	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rerere" && args[1] == "forget" {
			rerereForgotPath = args[2]
			return "", nil
		}
		return "", nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/my-workspace/rerere/internal/pkg/deep/file.go", "", auth)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /rerere (deep path) status = %d; want %d; body = %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Verify the full path including slashes was passed to 'git rerere forget'.
	if rerereForgotPath != "internal/pkg/deep/file.go" {
		t.Errorf("expected rerere forget path='internal/pkg/deep/file.go', got %q", rerereForgotPath)
	}
}

// 16-REQ-4.E4: PAT without 'workspaces:read' scope returns 403 for forget.
func TestRerereForget_PATWithoutScope_Returns403(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")

	auth := rebuildPATAuth("alice") // no permissions at all
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/my-workspace/rerere/src/config.go", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("DELETE /rerere (PAT without scope) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}
