package workspace

import (
	"net/http"
	"testing"
)

// ========================================================================
// Spec 13 Task 3.5: Sync state integrity
// (TS-13-28, 13-REQ-9.E1)
// Requirements: 13-REQ-9
//
// A sync in progress is not persisted: it is the in-memory workspace lock.
// The outcome of the last sync is sync_error (NULL after a success).
// ========================================================================

// TS-13-28: Verifies that a workspace whose last sync failed (sync_error set)
// allows a new sync attempt to proceed without manual database intervention,
// and that a successful retry clears sync_error.
// Requirement: 13-REQ-9.2
func TestSyncStateMachine_ErrorAllowsRetry(t *testing.T) {
	env := newTestEnv(t)

	// Install stub: sync returns up-to-date on retry.
	stubSyncUpToDate(t, "abc1234567890abcdef1234567890abcdef123456")

	env.seedWorkspace(t, &Workspace{
		Slug:    "error-retry-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "ready",
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_error = 'previous sync failed' WHERE slug = ?`,
		"error-retry-ws",
	)
	if err != nil {
		t.Fatalf("failed to set error state: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/error-retry-ws/sync", "", auth)

	// The request must NOT be rejected with 409 (which would mean the
	// error state blocks retries).
	if rec.Code == http.StatusConflict {
		t.Error("POST /sync from error state returned 409; error state should allow retry")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync from error state returned %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var syncError *string
	err = env.db.QueryRow(
		`SELECT sync_error FROM workspaces WHERE slug = ?`, "error-retry-ws",
	).Scan(&syncError)
	if err != nil {
		t.Fatalf("query sync_error: %v", err)
	}
	if syncError != nil {
		t.Errorf("after successful retry: sync_error = %q; want NULL", *syncError)
	}
}

// 13-REQ-9.E1: Verifies that concurrent sync requests are rejected while a
// sync holds the workspace lock. The lock guard must return HTTP 409 with
// error_type workspace_busy.
// Property: 13-PROP-4 (at most one sync at a time per workspace)
func TestSyncStateMachine_ConcurrentSyncRejected(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "concurrent-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "ready",
	})

	// Simulate a sync in progress: hold the workspace lock.
	holdLock(t, "concurrent-ws")

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/concurrent-ws/sync", "", auth)

	if rec.Code != http.StatusConflict {
		t.Errorf("POST /sync while syncing returned %d; want %d",
			rec.Code, http.StatusConflict)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.ErrorType != "workspace_busy" {
		t.Errorf("error.error_type = %q; want %q", envelope.Error.ErrorType, "workspace_busy")
	}
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about sync in progress")
	}
}
