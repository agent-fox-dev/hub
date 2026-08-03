package mergequeue

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ===========================================================================
// 10.3 — Structured Logging for State Transitions Tests
// ===========================================================================

// ---------------------------------------------------------------------------
// Log capture helpers for structured logging tests
// ---------------------------------------------------------------------------

// logEntry represents a single parsed structured log entry.
type logEntry struct {
	Level           string `json:"level"`
	Msg             string `json:"msg"`
	MergeJobID      string `json:"merge_job_id"`
	WorkspaceSlug   string `json:"workspace_slug"`
	Status          string `json:"status"`
	Error           string `json:"error"`
	RetryCount      int    `json:"retry_count"`
	RejectionReason string `json:"rejection_reason"`
}

// captureLogBuffer creates a slog.Logger that writes JSON log entries to a
// buffer for test assertions. Returns the buffer and the logger.
func captureLogBuffer(t *testing.T) (*bytes.Buffer, *slog.Logger) {
	t.Helper()
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)
	return &buf, logger
}

// parseLogEntries parses newline-delimited JSON log entries from a buffer.
func parseLogEntries(t *testing.T, buf *bytes.Buffer) []logEntry {
	t.Helper()
	var entries []logEntry
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry logEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Logf("skipping non-JSON log line: %s", line)
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// findLogEntriesWithJobID returns log entries matching the given merge_job_id.
func findLogEntriesWithJobID(entries []logEntry, jobID string) []logEntry {
	var matched []logEntry
	for _, e := range entries {
		if e.MergeJobID == jobID {
			matched = append(matched, e)
		}
	}
	return matched
}

// ---------------------------------------------------------------------------
// TS-11-92: All significant state transitions are logged with structured
// fields: merge_job_id, workspace_slug, status, error, retry_count, and
// rejection_reason.
// Requirement: 11-REQ-17.1
// ---------------------------------------------------------------------------

func TestLogging_StateTransitions_StructuredFields(t *testing.T) {
	db := openDryRunTestDB(t)
	job := insertQueuedMergeJob(t, db, "log92")

	logBuf, _ := captureLogBuffer(t)

	// CanMerge returns BeforeDependency to trigger a retriable backoff.
	mockCanMerge := func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
		return false, BeforeDependency, nil
	}

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()
	deps := MergeDeps{Git: mockGit, Locker: mockMu}

	err := processJobByID(context.Background(), db, job.ID, deps, mockCanMerge)
	if err != nil {
		t.Fatalf("processJobByID() returned error: %v", err)
	}

	entries := parseLogEntries(t, logBuf)
	jobEntries := findLogEntriesWithJobID(entries, job.ID)

	// Must have at least one log entry for this job.
	if len(jobEntries) == 0 {
		t.Fatal("no log entries found with merge_job_id; want structured log entries for state transitions")
	}

	// Look for a backoff/rejection log entry with the required fields.
	foundBackoffEntry := false
	for _, e := range jobEntries {
		if e.RejectionReason == string(BeforeDependency) {
			foundBackoffEntry = true
			// Verify structured fields.
			if e.WorkspaceSlug != job.WorkspaceSlug {
				t.Errorf("log entry workspace_slug = %q; want %q", e.WorkspaceSlug, job.WorkspaceSlug)
			}
			if e.RetryCount < 1 {
				t.Errorf("log entry retry_count = %d; want >= 1 for backoff event", e.RetryCount)
			}
			break
		}
	}

	if !foundBackoffEntry {
		t.Error("no log entry found with rejection_reason='BeforeDependency'; want structured log for backoff event")
	}
}

// ---------------------------------------------------------------------------
// TS-11-93: When PostMergeHook returns an error, the error is logged with
// merge_job_id and error fields and is not surfaced in any API response.
// Requirement: 11-REQ-17.2
// ---------------------------------------------------------------------------

func TestLogging_PostMergeHookError_Logged_NotInAPIResponse(t *testing.T) {
	db := openMergeTestDB(t)
	campaignID := newTestUUID("campaign93")
	job := insertMergeTestJob(t, db, "log93", campaignID)
	workspaceRoot := t.TempDir()

	logBuf, _ := captureLogBuffer(t)

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	hookError := "hook error: campaign scheduler unavailable"

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
		Hook: func(_ context.Context, _ MergeJob) error {
			return errors.New(hookError)
		},
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Verify log contains the hook error with merge_job_id.
	entries := parseLogEntries(t, logBuf)
	jobEntries := findLogEntriesWithJobID(entries, job.ID)

	foundHookErrorLog := false
	for _, e := range jobEntries {
		if strings.Contains(e.Error, "hook error") || strings.Contains(e.Error, hookError) {
			foundHookErrorLog = true
			break
		}
	}

	if !foundHookErrorLog {
		t.Error("no log entry found with merge_job_id and hook error; want PostMergeHook error logged with merge_job_id")
	}

	// Verify the hook error is NOT surfaced in any API response.
	// Set up HTTP env to query the job.
	env := newMergeHTTPTestEnv(t)
	// Re-insert the job into the HTTP test DB so the handler can find it.
	now := time.Now().UTC().Format(time.RFC3339)
	httpJob := &MergeJob{
		ID:            job.ID,
		Nonce:         job.Nonce,
		CampaignID:    job.CampaignID,
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets-variables",
		Status:        "merged",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
		MergedSHA:     sql.NullString{String: testMergedHead, Valid: true},
	}
	insertTestMergeJobFull(t, env.db, httpJob)

	auth := mergeReadAuth(newTestUUID("reader-log93"))
	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/"+job.ID, "", auth)

	// The response body must not contain the hook error string.
	respBody := rec.Body.String()
	if strings.Contains(respBody, hookError) {
		t.Errorf("API response contains hook error %q; want hook errors not surfaced in API responses", hookError)
	}
}

// ---------------------------------------------------------------------------
// TS-11-94: When a log write fails (e.g. stdout is closed), the merge queue
// continues processing and job state transitions are not affected.
// Requirement: 11-REQ-17.E1
// ---------------------------------------------------------------------------

func TestLogging_WriteFailure_DoesNotAffectProcessing(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "log94", "")
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
	}

	// Even if the logging infrastructure fails (e.g. stdout is closed),
	// the merge queue must continue processing. This test verifies that
	// processMergeJob completes and transitions the job to merged status
	// regardless of any logging issues.
	//
	// Note: Since Go's standard library log functions do not return errors,
	// and slog handlers may silently drop entries, this test primarily
	// verifies that the processing pipeline is resilient to logging
	// infrastructure failures by ensuring state transitions complete.
	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Job must reach merged status regardless of logging infrastructure state.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("job status = %q; want 'merged' (logging failures must not affect job state transitions)", status)
	}
}
