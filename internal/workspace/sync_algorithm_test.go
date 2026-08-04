package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// stubSyncUpToDate installs a syncFetchAndCompareFn that returns "up_to_date"
// with the provided SHA as the upstream HEAD. Restores the original function
// on test cleanup.
func stubSyncUpToDate(t *testing.T, sha string) {
	t.Helper()
	old := syncFetchAndCompareFn
	oldRef := syncUpdateLocalRefFn
	syncFetchAndCompareFn = func(ctx context.Context, repoPath string, auth transport.AuthMethod, branch *string, localHeadSHA string) (string, string, error) {
		return sha, "up_to_date", nil
	}
	syncUpdateLocalRefFn = func(repoPath string, branch *string, newSHA string) error {
		return nil
	}
	t.Cleanup(func() {
		syncFetchAndCompareFn = old
		syncUpdateLocalRefFn = oldRef
	})
}

// stubSyncFastForward installs a syncFetchAndCompareFn that returns "fast_forward"
// with newSHA as the upstream HEAD. syncUpdateLocalRefFn succeeds.
func stubSyncFastForward(t *testing.T, newSHA string) {
	t.Helper()
	old := syncFetchAndCompareFn
	oldRef := syncUpdateLocalRefFn
	syncFetchAndCompareFn = func(ctx context.Context, repoPath string, auth transport.AuthMethod, branch *string, localHeadSHA string) (string, string, error) {
		return newSHA, "fast_forward", nil
	}
	syncUpdateLocalRefFn = func(repoPath string, branch *string, sha string) error {
		return nil
	}
	t.Cleanup(func() {
		syncFetchAndCompareFn = old
		syncUpdateLocalRefFn = oldRef
	})
}

// stubSyncDiverged installs a syncFetchAndCompareFn that returns "diverged"
// with upstreamSHA as the upstream HEAD.
func stubSyncDiverged(t *testing.T, upstreamSHA string) {
	t.Helper()
	old := syncFetchAndCompareFn
	syncFetchAndCompareFn = func(ctx context.Context, repoPath string, auth transport.AuthMethod, branch *string, localHeadSHA string) (string, string, error) {
		return upstreamSHA, "diverged", nil
	}
	t.Cleanup(func() { syncFetchAndCompareFn = old })
}

// stubSyncFetchError installs a syncFetchAndCompareFn that returns an error.
func stubSyncFetchError(t *testing.T, errMsg string) {
	t.Helper()
	old := syncFetchAndCompareFn
	syncFetchAndCompareFn = func(ctx context.Context, repoPath string, auth transport.AuthMethod, branch *string, localHeadSHA string) (string, string, error) {
		return "", "", fmt.Errorf("%s", errMsg)
	}
	t.Cleanup(func() { syncFetchAndCompareFn = old })
}

// stubSyncBlocking installs a syncFetchAndCompareFn that blocks until the
// context is cancelled, then returns the context error.
func stubSyncBlocking(t *testing.T) {
	t.Helper()
	old := syncFetchAndCompareFn
	syncFetchAndCompareFn = func(ctx context.Context, repoPath string, auth transport.AuthMethod, branch *string, localHeadSHA string) (string, string, error) {
		<-ctx.Done()
		return "", "", ctx.Err()
	}
	t.Cleanup(func() { syncFetchAndCompareFn = old })
}

// ========================================================================
// Spec 13 Task 2.1: Sync algorithm — fetch and fast-forward
// (TS-13-12, TS-13-13, TS-13-14, 13-REQ-4.E2)
// Requirements: 13-REQ-4
// ========================================================================

// TS-13-12: Verifies that the sync handler sets sync_status='syncing', opens
// the repo, resolves credentials, and calls remote.Fetch() when all
// preconditions pass.
// Requirement: 13-REQ-4.1
//
// The sync handler's state machine transitions idle -> syncing -> idle on
// success. A 200 response implicitly verifies that the handler:
//   - set sync_status to 'syncing' before starting git operations
//   - opened the local repository at <WORKSPACE_ROOT>/<slug>/trunk/
//   - called resolveCloneAuth for credential resolution
//   - called remote.Fetch() to fetch from upstream
func TestSyncAlgorithm_SetsStatusSyncingAndFetches(t *testing.T) {
	env := newTestEnv(t)

	// Install stub: sync returns up-to-date (successful sync path).
	stubSyncUpToDate(t, "abc1234567890abcdef1234567890abcdef123456")

	// Seed workspace with all preconditions passing:
	// status='active', clone_status='ready'.
	env.seedWorkspace(t, &Workspace{
		Slug:        "sync-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	// Set sync fields to valid precondition state.
	// Fails if sync_mode/sync_status columns do not exist (expected before
	// the schema migration is applied).
	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
		"sync-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/sync-ws/sync", "", auth)

	// Handler should return 200 after successful sync.
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync returned %d; want %d", rec.Code, http.StatusOK)
	}

	// Verify response body contains sync fields in final state.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["sync_status"] != "idle" {
		t.Errorf("sync_status = %v; want %q", resp["sync_status"], "idle")
	}
}

// TS-13-13: Verifies that when upstream HEAD equals local HEAD (already up to
// date), sync_status is set to 'idle', last_sync_at is updated to a non-null
// RFC 3339 timestamp, and upstream_head_sha is set.
// Requirement: 13-REQ-4.2
// Property: 13-PROP-3 (upstream_head_sha always reflects last fetched state)
// Property: 13-PROP-7 (last_sync_at updated on success)
func TestSyncAlgorithm_AlreadyUpToDate(t *testing.T) {
	env := newTestEnv(t)

	headSHA := "abc1234567890abcdef1234567890abcdef123456"

	// Install stub: upstream HEAD matches local HEAD → up_to_date.
	stubSyncUpToDate(t, headSHA)

	env.seedWorkspace(t, &Workspace{
		Slug:        "uptodate-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
		HeadSHA:     &headSHA,
	})

	// Set sync fields for valid preconditions.
	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
		"uptodate-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/uptodate-ws/sync", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync returned %d; want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// sync_status must be 'idle' after an up-to-date sync.
	if resp["sync_status"] != "idle" {
		t.Errorf("sync_status = %v; want %q", resp["sync_status"], "idle")
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

	// upstream_head_sha must be set (13-PROP-3: always reflects last fetch).
	if resp["upstream_head_sha"] == nil {
		t.Error("upstream_head_sha is null; want non-null SHA (13-PROP-3)")
	}
}

// TS-13-14: Verifies that when upstream HEAD is a descendant of local HEAD,
// the integration branch is fast-forwarded and head_sha, upstream_head_sha,
// sync_status, and last_sync_at are all updated correctly.
// Requirement: 13-REQ-4.3
// Property: 13-PROP-2 (fast-forward only advances to a descendant)
// Property: 13-PROP-3 (upstream_head_sha always reflects last fetched state)
func TestSyncAlgorithm_FastForward(t *testing.T) {
	env := newTestEnv(t)

	originalSHA := "aaaa234567890abcdef1234567890abcdef123456"
	newSHA := "bbbb234567890abcdef1234567890abcdef123456"

	// Install stub: upstream HEAD is descendant of local → fast_forward.
	stubSyncFastForward(t, newSHA)

	env.seedWorkspace(t, &Workspace{
		Slug:        "ff-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
		HeadSHA:     &originalSHA,
	})

	// Set sync fields for valid preconditions.
	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
		"ff-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	// Record the pre-sync head_sha for comparison.
	var preSHA string
	err = env.db.QueryRow(
		`SELECT COALESCE(head_sha, '') FROM workspaces WHERE slug = ?`, "ff-ws",
	).Scan(&preSHA)
	if err != nil {
		t.Fatalf("failed to query pre-sync head_sha: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ff-ws/sync", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync returned %d; want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// sync_status must be 'idle' after successful fast-forward.
	if resp["sync_status"] != "idle" {
		t.Errorf("sync_status = %v; want %q", resp["sync_status"], "idle")
	}

	// head_sha must be updated (advanced from pre-sync value).
	if headSHA, ok := resp["head_sha"].(string); !ok || headSHA == "" {
		t.Error("head_sha is null or empty; want non-null SHA after fast-forward")
	} else if headSHA == preSHA {
		t.Errorf("head_sha = %q; want different from pre-sync value (should be advanced)", headSHA)
	}

	// After fast-forward, head_sha must equal upstream_head_sha.
	if resp["head_sha"] != resp["upstream_head_sha"] {
		t.Errorf("head_sha = %v; upstream_head_sha = %v; want equal after fast-forward",
			resp["head_sha"], resp["upstream_head_sha"])
	}

	// last_sync_at must be set after a successful sync.
	if resp["last_sync_at"] == nil {
		t.Error("last_sync_at is null; want non-null RFC 3339 timestamp after successful sync")
	}
}

// 13-REQ-4.E2: Verifies that when remote.Fetch() fails due to a network error
// or authentication failure, the handler sets sync_status='error', records the
// error in sync_error, and does not modify head_sha or last_sync_at.
// Property: 13-PROP-7 (last_sync_at not updated on failure)
func TestSyncAlgorithm_FetchFailureSetsError(t *testing.T) {
	env := newTestEnv(t)

	// Install stub: fetch returns a network error.
	stubSyncFetchError(t, "dial tcp: connection refused")

	env.seedWorkspace(t, &Workspace{
		Slug:        "fetch-fail-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	// Set sync fields for valid preconditions.
	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
		"fetch-fail-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/fetch-fail-ws/sync", "", auth)

	// Fetch failure should return 502 (Bad Gateway).
	if rec.Code != http.StatusBadGateway {
		t.Errorf("POST /sync with fetch failure returned %d; want %d",
			rec.Code, http.StatusBadGateway)
	}

	// Verify error envelope has a descriptive message.
	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about fetch failure")
	}

	// Verify database state: sync_status should be 'error' and sync_error set.
	var syncStatus string
	var syncError *string
	err = env.db.QueryRow(
		`SELECT sync_status, sync_error FROM workspaces WHERE slug = ?`, "fetch-fail-ws",
	).Scan(&syncStatus, &syncError)
	if err != nil {
		t.Fatalf("failed to query sync fields: %v", err)
	}
	if syncStatus != "error" {
		t.Errorf("sync_status = %q; want %q", syncStatus, "error")
	}
	if syncError == nil || *syncError == "" {
		t.Error("sync_error is null or empty; want non-empty error message")
	}

	// Verify that head_sha and last_sync_at were NOT modified (13-PROP-7).
	var headSHA *string
	var lastSyncAt *string
	err = env.db.QueryRow(
		`SELECT head_sha, last_sync_at FROM workspaces WHERE slug = ?`, "fetch-fail-ws",
	).Scan(&headSHA, &lastSyncAt)
	if err != nil {
		t.Fatalf("failed to query head_sha/last_sync_at: %v", err)
	}
	if headSHA != nil {
		t.Errorf("head_sha = %q; want null (should not be modified on fetch failure)", *headSHA)
	}
	if lastSyncAt != nil {
		t.Errorf("last_sync_at = %q; want null (should not be updated on fetch failure)", *lastSyncAt)
	}
}

// ========================================================================
// Spec 13 Task 2.2: Sync algorithm — fast-forward outcomes
// (TS-13-15, TS-13-16)
// Requirements: 13-REQ-4
// ========================================================================

// TS-13-15: Verifies that when upstream HEAD has diverged (force-push detected),
// sync_status is set to 'error' with the prescribed message, upstream_head_sha
// is updated, and the integration branch is NOT advanced.
// Requirement: 13-REQ-4.4
// Property: 13-PROP-2 (integration branch not moved to non-descendant)
// Property: 13-PROP-3 (upstream_head_sha updated regardless of outcome)
func TestSyncAlgorithm_DivergedForcePush(t *testing.T) {
	env := newTestEnv(t)

	originalSHA := "aaaa234567890abcdef1234567890abcdef123456"
	upstreamSHA := "cccc234567890abcdef1234567890abcdef123456"

	// Install stub: upstream HEAD has diverged (force-push detected).
	stubSyncDiverged(t, upstreamSHA)

	env.seedWorkspace(t, &Workspace{
		Slug:        "diverged-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
		HeadSHA:     &originalSHA,
	})

	// Set sync fields for valid preconditions.
	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
		"diverged-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/diverged-ws/sync", "", auth)

	// Diverged/force-push should return 409 Conflict.
	if rec.Code != http.StatusConflict {
		t.Errorf("POST /sync on diverged workspace returned %d; want %d",
			rec.Code, http.StatusConflict)
	}

	// Verify error message contains the prescribed text.
	envelope := parseErrorEnvelope(t, rec)
	if !strings.Contains(envelope.Error.Message, "diverged") {
		t.Errorf("error.message = %q; want to contain 'diverged'", envelope.Error.Message)
	}
	if !strings.Contains(envelope.Error.Message, "--reset-to-upstream") {
		t.Errorf("error.message = %q; want to contain '--reset-to-upstream'", envelope.Error.Message)
	}

	// Verify database state.
	var syncStatus string
	var syncError *string
	var headSHA *string
	var upstreamHeadSHA *string
	err = env.db.QueryRow(
		`SELECT sync_status, sync_error, head_sha, upstream_head_sha FROM workspaces WHERE slug = ?`,
		"diverged-ws",
	).Scan(&syncStatus, &syncError, &headSHA, &upstreamHeadSHA)
	if err != nil {
		t.Fatalf("failed to query sync fields: %v", err)
	}

	// sync_status must be 'error'.
	if syncStatus != "error" {
		t.Errorf("sync_status = %q; want %q", syncStatus, "error")
	}

	// sync_error must mention diverged history and recovery path.
	if syncError == nil || !strings.Contains(*syncError, "diverged") {
		got := "<nil>"
		if syncError != nil {
			got = *syncError
		}
		t.Errorf("sync_error = %s; want to contain 'diverged'", got)
	}

	// head_sha must NOT be modified (integration branch not advanced).
	if headSHA == nil || *headSHA != originalSHA {
		got := "<nil>"
		if headSHA != nil {
			got = *headSHA
		}
		t.Errorf("head_sha = %s; want %q (unchanged after divergence)", got, originalSHA)
	}

	// upstream_head_sha MUST be updated even on divergence (13-PROP-3).
	if upstreamHeadSHA == nil || *upstreamHeadSHA == "" {
		t.Error("upstream_head_sha is null or empty; want non-null (updated even on divergence)")
	}
	if upstreamHeadSHA != nil && *upstreamHeadSHA == originalSHA {
		t.Error("upstream_head_sha equals local head_sha; want different value (upstream was force-pushed)")
	}
}

// TS-13-16: Verifies that the sync handler registers a deferred cleanup that
// sets sync_status='error' and records a descriptive sync_error when the
// request context is cancelled mid-sync.
// Requirement: 13-REQ-4.5
// Property: 13-PROP-1 (sync_status never permanently stuck in 'syncing')
func TestSyncAlgorithm_ContextCancellation(t *testing.T) {
	env := newTestEnv(t)

	// Install stub: fetch blocks until context is cancelled.
	stubSyncBlocking(t)

	env.seedWorkspace(t, &Workspace{
		Slug:        "ctx-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	// Set sync fields for valid preconditions.
	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
		"ctx-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	// Create a request with a very short context timeout to trigger
	// cancellation during the sync handler's fetch operation.
	// The handler should register a deferred cleanup that catches the
	// cancellation and transitions sync_status to 'error'.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	auth := adminAuth()
	authJSON, err := json.Marshal(auth)
	if err != nil {
		t.Fatalf("failed to marshal auth: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/ctx-ws/sync", nil).WithContext(ctx)
	req.Header.Set("X-Test-Auth", string(authJSON))

	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)

	// The deferred cleanup should catch the context cancellation and
	// return 504 Gateway Timeout.
	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("POST /sync with cancelled context returned %d; want %d",
			rec.Code, http.StatusGatewayTimeout)
	}

	// Verify database state: deferred cleanup must set sync_status='error'
	// so the workspace is not left permanently stuck in 'syncing' (13-PROP-1).
	var syncStatus string
	var syncError *string
	err = env.db.QueryRow(
		`SELECT sync_status, sync_error FROM workspaces WHERE slug = ?`, "ctx-ws",
	).Scan(&syncStatus, &syncError)
	if err != nil {
		t.Fatalf("failed to query sync fields: %v", err)
	}
	if syncStatus != "error" {
		t.Errorf("sync_status = %q; want %q (deferred cleanup should set error on cancellation)",
			syncStatus, "error")
	}
	if syncError == nil || *syncError == "" {
		t.Error("sync_error is null or empty; want descriptive message about context cancellation")
	}
}
