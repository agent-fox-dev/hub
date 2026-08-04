package workspace

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ========================================================================
// Spec 13 Task 3.2: Reset-to-upstream recovery
// (TS-13-19, TS-13-20)
// Requirements: 13-REQ-6
// ========================================================================

// TS-13-19: Verifies that POST /api/v1/workspaces/:slug/sync?reset_to_upstream=true
// fetches upstream, resets the integration branch to upstream HEAD, and returns
// the workspace with sync_status='idle', head_sha=upstream HEAD, last_sync_at
// set, and sync_error=null.
// Requirement: 13-REQ-6.1
// Property: 13-PROP-8 (reset-to-upstream clears sync_error on success)
func TestSyncReset_ResetToUpstreamSuccess(t *testing.T) {
	env := newTestEnv(t)

	// Install stub: fetch returns the upstream SHA (outcome is ignored for reset).
	upstreamSHA := "cccc234567890abcdef1234567890abcdef123456"
	stubSyncFastForward(t, upstreamSHA)

	// Seed workspace in error state (diverged history scenario).
	localSHA := "aaaa234567890abcdef1234567890abcdef123456"
	env.seedWorkspace(t, &Workspace{
		Slug:        "reset-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
		HeadSHA:     &localSHA,
	})

	// Set sync fields to simulate post-divergence error state.
	// Fails if sync columns do not exist (expected before schema migration).
	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'error',
		 sync_error = 'upstream history has diverged; use --reset-to-upstream to recover'
		 WHERE slug = ?`,
		"reset-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/reset-ws/sync?reset_to_upstream=true", "", auth)

	// Handler should return 200 after successful reset-to-upstream.
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync?reset_to_upstream=true returned %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// sync_status must be 'idle' after successful reset (13-PROP-8).
	if resp["sync_status"] != "idle" {
		t.Errorf("sync_status = %v; want %q", resp["sync_status"], "idle")
	}

	// sync_error must be null/cleared after successful reset (13-PROP-8).
	if resp["sync_error"] != nil {
		t.Errorf("sync_error = %v; want null (cleared after successful reset)", resp["sync_error"])
	}

	// head_sha must be set to the upstream HEAD SHA.
	headSHA, ok := resp["head_sha"].(string)
	if !ok || headSHA == "" {
		t.Error("head_sha is null or empty; want non-null SHA (reset to upstream HEAD)")
	}

	// upstream_head_sha must be set and equal to head_sha.
	respUpstreamSHA, ok := resp["upstream_head_sha"].(string)
	if !ok || respUpstreamSHA == "" {
		t.Error("upstream_head_sha is null or empty; want non-null SHA")
	}
	if headSHA != respUpstreamSHA {
		t.Errorf("head_sha = %q; upstream_head_sha = %q; want equal after reset", headSHA, respUpstreamSHA)
	}

	// last_sync_at must be a non-null RFC 3339 timestamp.
	lastSyncAt, ok := resp["last_sync_at"]
	if !ok || lastSyncAt == nil {
		t.Fatal("last_sync_at is null or missing; want non-null RFC 3339 timestamp")
	}
	if ts, ok := lastSyncAt.(string); ok {
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			if _, err2 := time.Parse(time.RFC3339, ts); err2 != nil {
				t.Errorf("last_sync_at = %q; not a valid RFC 3339 timestamp", ts)
			}
		}
	} else {
		t.Errorf("last_sync_at = %v (type %T); want string", lastSyncAt, lastSyncAt)
	}
}

// TS-13-20: Verifies that 'afc workspace sync <slug> --reset-to-upstream'
// calls the API with reset_to_upstream=true query parameter. Tested at the
// API level as a round-trip verification of the query parameter handling.
// Requirement: 13-REQ-6.2
func TestSyncReset_CLIResetFlag(t *testing.T) {
	env := newTestEnv(t)

	// Install stub: fetch returns an upstream SHA for the reset operation.
	stubSyncFastForward(t, "dddd234567890abcdef1234567890abcdef123456")

	// Seed workspace in error state.
	env.seedWorkspace(t, &Workspace{
		Slug:        "cli-reset-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'error',
		 sync_error = 'upstream history has diverged; use --reset-to-upstream to recover'
		 WHERE slug = ?`,
		"cli-reset-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	auth := adminAuth()

	// Verify that reset_to_upstream=true query parameter triggers reset mode.
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/cli-reset-ws/sync?reset_to_upstream=true", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync?reset_to_upstream=true returned %d; want %d",
			rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// After reset, sync_status should be 'idle'.
	if resp["sync_status"] != "idle" {
		t.Errorf("sync_status = %v; want %q", resp["sync_status"], "idle")
	}
}

// 13-REQ-6.E1: Verifies that if the fetch step of reset-to-upstream fails
// (network or auth error), the handler sets sync_status to 'error', records
// sync_error, and does NOT modify head_sha or the integration branch.
func TestSyncReset_FetchFailureSetsError(t *testing.T) {
	env := newTestEnv(t)

	// Install stub: fetch returns a network error.
	stubSyncFetchError(t, "dial tcp: connection refused")

	originalSHA := "aaaa234567890abcdef1234567890abcdef123456"
	env.seedWorkspace(t, &Workspace{
		Slug:        "reset-fetch-fail-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
		HeadSHA:     &originalSHA,
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'error' WHERE slug = ?`,
		"reset-fetch-fail-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/reset-fetch-fail-ws/sync?reset_to_upstream=true", "", auth)

	// Fetch failure should return 502 (Bad Gateway).
	if rec.Code != http.StatusBadGateway {
		t.Errorf("POST /sync?reset_to_upstream=true with fetch failure returned %d; want %d",
			rec.Code, http.StatusBadGateway)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about fetch failure")
	}

	// Verify database state: sync_status='error', head_sha unchanged.
	var syncStatus string
	var headSHA *string
	err = env.db.QueryRow(
		`SELECT sync_status, head_sha FROM workspaces WHERE slug = ?`, "reset-fetch-fail-ws",
	).Scan(&syncStatus, &headSHA)
	if err != nil {
		t.Fatalf("failed to query sync fields: %v", err)
	}
	if syncStatus != "error" {
		t.Errorf("sync_status = %q; want %q", syncStatus, "error")
	}
	if headSHA == nil || *headSHA != originalSHA {
		got := "<nil>"
		if headSHA != nil {
			got = *headSHA
		}
		t.Errorf("head_sha = %s; want %q (unchanged after fetch failure)", got, originalSHA)
	}
}

// 13-REQ-6.E3: Verifies that a reset-to-upstream request is rejected by the
// syncing guard when sync_status is already 'syncing'.
func TestSyncReset_RejectedWhileSyncing(t *testing.T) {
	env := newTestEnv(t)

	// Install stub: should never be reached since the syncing guard rejects first.
	stubSyncUpToDate(t, "eeee234567890abcdef1234567890abcdef123456")

	env.seedWorkspace(t, &Workspace{
		Slug:        "reset-syncing-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'syncing' WHERE slug = ?`,
		"reset-syncing-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync_status: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/reset-syncing-ws/sync?reset_to_upstream=true", "", auth)

	// Should be rejected with 409 Conflict by the syncing guard.
	if rec.Code != http.StatusConflict {
		t.Errorf("POST /sync?reset_to_upstream=true while syncing returned %d; want %d",
			rec.Code, http.StatusConflict)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about sync in progress")
	}
}

// 13-REQ-6.E4: Verifies that a caller without workspaces:sync scope cannot
// trigger reset-to-upstream.
func TestSyncReset_MissingSyncScopeRejected(t *testing.T) {
	env := newTestEnv(t)

	// Install stub: should never be reached since permission check rejects first.
	stubSyncUpToDate(t, "ffff234567890abcdef1234567890abcdef123456")

	env.seedWorkspace(t, &Workspace{
		Slug:        "reset-noscope-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	// PAT with only workspaces:read, not workspaces:sync.
	auth := patAuth("alice-id", "workspaces:read")
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/reset-noscope-ws/sync?reset_to_upstream=true", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /sync?reset_to_upstream=true without scope returned %d; want %d",
			rec.Code, http.StatusForbidden)
	}

	envelope := parseErrorEnvelope(t, rec)
	if !strings.Contains(strings.ToLower(envelope.Error.Message), "permission") &&
		!strings.Contains(strings.ToLower(envelope.Error.Message), "scope") &&
		!strings.Contains(strings.ToLower(envelope.Error.Message), "forbidden") {
		t.Errorf("error.message = %q; want mention of permission/scope/forbidden", envelope.Error.Message)
	}
}
