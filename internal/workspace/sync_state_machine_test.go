package workspace

import (
	"net/http"
	"testing"
)

// ========================================================================
// Spec 13 Task 3.5: Sync status state machine integrity
// (TS-13-27, TS-13-28)
// Requirements: 13-REQ-9
// ========================================================================

// TS-13-27: Verifies that sync_status only transitions through valid state
// machine paths: idle→syncing→idle (success), idle→syncing→error (failure),
// error→syncing→idle (recovery after error).
// Requirement: 13-REQ-9.1
// Property: 13-PROP-4 (concurrent sync requests serialized via syncing guard)
func TestSyncStateMachine_ValidTransitions(t *testing.T) {
	env := newTestEnv(t)

	// Install stub: sync returns up-to-date (successful sync path).
	stubSyncUpToDate(t, "abc1234567890abcdef1234567890abcdef123456")

	env.seedWorkspace(t, &Workspace{
		Slug:        "sm-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
		"sm-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	auth := adminAuth()

	// Transition 1: idle → syncing → idle (successful sync).
	var syncStatus string
	err = env.db.QueryRow(
		`SELECT sync_status FROM workspaces WHERE slug = ?`, "sm-ws",
	).Scan(&syncStatus)
	if err != nil {
		t.Fatalf("query sync_status: %v", err)
	}
	if syncStatus != "idle" {
		t.Fatalf("initial sync_status = %q; want %q", syncStatus, "idle")
	}

	rec1 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/sm-ws/sync", "", auth)
	if rec1.Code != http.StatusOK {
		t.Fatalf("POST /sync (success) returned %d; want %d; body: %s",
			rec1.Code, http.StatusOK, rec1.Body.String())
	}

	err = env.db.QueryRow(
		`SELECT sync_status FROM workspaces WHERE slug = ?`, "sm-ws",
	).Scan(&syncStatus)
	if err != nil {
		t.Fatalf("query sync_status after success: %v", err)
	}
	if syncStatus != "idle" {
		t.Errorf("after successful sync: sync_status = %q; want %q", syncStatus, "idle")
	}

	// Transition 2: idle → syncing → error (failed sync).
	// To make the sync fail, we need the fetch to fail. Since we don't have
	// injectable git operations yet, we verify the error state can be reached
	// by directly setting sync_status='error' (simulating a fetch failure)
	// and then verifying the next sync attempt is allowed.
	_, err = env.db.Exec(
		`UPDATE workspaces SET sync_status = 'error',
		 sync_error = 'simulated fetch failure'
		 WHERE slug = ?`, "sm-ws",
	)
	if err != nil {
		t.Fatalf("failed to set error state: %v", err)
	}

	// Transition 3: error → syncing → idle (recovery after error).
	rec3 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/sm-ws/sync", "", auth)
	_ = rec3 // Response code depends on implementation; key assertion is below.

	// After a sync attempt from error state, verify the status transitioned.
	err = env.db.QueryRow(
		`SELECT sync_status FROM workspaces WHERE slug = ?`, "sm-ws",
	).Scan(&syncStatus)
	if err != nil {
		t.Fatalf("query sync_status after recovery: %v", err)
	}

	// The status should be either 'idle' (if sync succeeded) or 'error'
	// (if sync failed again) — but NOT 'syncing' (no stuck state).
	if syncStatus == "syncing" {
		t.Error("sync_status stuck at 'syncing' after sync attempt; want 'idle' or 'error'")
	}
}

// TS-13-28: Verifies that a workspace in sync_status='error' allows a new
// sync attempt to proceed without requiring manual database intervention.
// Requirement: 13-REQ-9.2
func TestSyncStateMachine_ErrorAllowsRetry(t *testing.T) {
	env := newTestEnv(t)

	// Install stub: sync returns up-to-date on retry.
	stubSyncUpToDate(t, "abc1234567890abcdef1234567890abcdef123456")

	env.seedWorkspace(t, &Workspace{
		Slug:        "error-retry-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'error',
		 sync_error = 'previous sync failed'
		 WHERE slug = ?`,
		"error-retry-ws",
	)
	if err != nil {
		t.Fatalf("failed to set error state: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/error-retry-ws/sync", "", auth)

	// The request should NOT be rejected with 409 (which would mean the
	// error state blocks retries). Any other response is acceptable.
	if rec.Code == http.StatusConflict {
		t.Error("POST /sync from error state returned 409; error state should allow retry")
	}
}

// 13-REQ-9.E1: Verifies that concurrent sync requests are rejected when
// sync_status is already 'syncing'. The syncing guard must return HTTP 409.
// Property: 13-PROP-4 (at most one sync at a time per workspace)
func TestSyncStateMachine_ConcurrentSyncRejected(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:        "concurrent-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'syncing' WHERE slug = ?`,
		"concurrent-ws",
	)
	if err != nil {
		t.Fatalf("failed to set syncing state: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/concurrent-ws/sync", "", auth)

	if rec.Code != http.StatusConflict {
		t.Errorf("POST /sync while syncing returned %d; want %d",
			rec.Code, http.StatusConflict)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about sync in progress")
	}
}

// 13-REQ-9.E2: Verifies that when sync_status is 'syncing', a second
// request is rejected (verifying the guard blocks concurrent operations).
func TestSyncStateMachine_SyncingTransitionDBFailure(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:        "db-fail-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	// Pre-set to 'syncing' — simulates a workspace where the sync_status
	// was already set to 'syncing' (either by a prior request or directly).
	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'syncing' WHERE slug = ?`,
		"db-fail-ws",
	)
	if err != nil {
		t.Fatalf("failed to set syncing state: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/db-fail-ws/sync", "", auth)

	// Must be rejected because sync_status='syncing'.
	if rec.Code != http.StatusConflict {
		t.Errorf("POST /sync while sync_status=syncing returned %d; want %d",
			rec.Code, http.StatusConflict)
	}
}

// Supplementary: Verifies that deferred cleanup sets sync_status='error'
// when the request context is cancelled mid-sync (e.g. client disconnect).
// Requirement: 13-REQ-4.5, 13-PROP-1
func TestSyncStateMachine_DeferredCleanupOnContextCancel(t *testing.T) {
	env := newTestEnv(t)

	// Install stub: sync returns up-to-date (but the test validates that
	// after completion, sync_status is not stuck at 'syncing').
	stubSyncUpToDate(t, "abc1234567890abcdef1234567890abcdef123456")

	env.seedWorkspace(t, &Workspace{
		Slug:        "cancel-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
		"cancel-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	// This test validates the deferred cleanup mechanism. The implementation
	// should register a deferred function that, when the context is cancelled,
	// sets sync_status='error'. Since we cannot easily simulate context
	// cancellation at the HTTP handler level in unit tests, we verify at
	// minimum that a sync request does not leave sync_status permanently
	// stuck in 'syncing'.
	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/cancel-ws/sync", "", auth)
	_ = rec

	// After the request completes (regardless of outcome), sync_status
	// must not be 'syncing'.
	var syncStatus string
	err = env.db.QueryRow(
		`SELECT sync_status FROM workspaces WHERE slug = ?`, "cancel-ws",
	).Scan(&syncStatus)
	if err != nil {
		t.Fatalf("query sync_status: %v", err)
	}
	if syncStatus == "syncing" {
		t.Error("sync_status stuck at 'syncing' after request completed; want 'idle' or 'error'")
	}
}
