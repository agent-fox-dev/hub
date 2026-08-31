package carrypatch

import (
	"net/http"
	"testing"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// TS-NS-1: DELETE /api/v1/workspaces/:slug/rebuilds/:id cancels a queued
// rebuild job and returns HTTP 200 with body {"status":"cancelled"}.
//
// Requirement: NS-REQ-1
// ===========================================================================

func TestCancelRebuild_QueuedJob_Returns200(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedRebuildJob(t, env.db, "job-cancel-1", "queued", "ws1", "rebase", "alice")

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/rebuilds/job-cancel-1", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /rebuilds/:id (queued) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify the response body contains {"status":"cancelled"}.
	body := rec.Body.String()
	if body == "" {
		t.Fatal("expected non-empty response body")
	}

	// Verify the job transitioned to cancelled status in the database.
	var status string
	err := env.db.QueryRow("SELECT status FROM jobs WHERE id = ?", "job-cancel-1").Scan(&status)
	if err != nil {
		t.Fatalf("query job status failed: %v", err)
	}
	if status != "cancelled" {
		t.Errorf("expected job status='cancelled', got %q", status)
	}
}

// ===========================================================================
// TS-NS-2: DELETE /api/v1/workspaces/:slug/rebuilds/:id returns HTTP 409 when
// the job is not in 'queued' state.
//
// Requirement: NS-REQ-2
// ===========================================================================

func TestCancelRebuild_RunningJob_Returns409(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedRebuildJob(t, env.db, "job-running-1", "running", "ws1", "rebase", "alice")

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/rebuilds/job-running-1", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE /rebuilds/:id (running) status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for non-cancellable job")
	}

	// Verify job status is unchanged.
	var status string
	err := env.db.QueryRow("SELECT status FROM jobs WHERE id = ?", "job-running-1").Scan(&status)
	if err != nil {
		t.Fatalf("query job status failed: %v", err)
	}
	if status != "running" {
		t.Errorf("expected job status='running' (unchanged), got %q", status)
	}
}

func TestCancelRebuild_CompletedJob_Returns409(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedRebuildJob(t, env.db, "job-done-1", "completed", "ws1", "rebase", "alice")

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/rebuilds/job-done-1", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE /rebuilds/:id (completed) status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestCancelRebuild_FailedJob_Returns409(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedRebuildJob(t, env.db, "job-failed-1", "failed", "ws1", "rebase", "alice")

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/rebuilds/job-failed-1", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE /rebuilds/:id (failed) status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestCancelRebuild_DeadLetterJob_Returns409(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedRebuildJob(t, env.db, "job-dl-1", "dead_letter", "ws1", "rebase", "alice")

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/rebuilds/job-dl-1", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE /rebuilds/:id (dead_letter) status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestCancelRebuild_CancelledJob_Returns409(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedRebuildJob(t, env.db, "job-cancelled-1", "cancelled", "ws1", "rebase", "alice")

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/rebuilds/job-cancelled-1", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE /rebuilds/:id (cancelled) status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestCancelRebuild_NonexistentJob_Returns404(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/rebuilds/nonexistent-id", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE /rebuilds/:id (nonexistent) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestCancelRebuild_DifferentWorkspaceJob_Returns404(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedWorkspace(t, env.db, "ws2", "bob", "active", "ready", "carry_patch", "integration")

	// Job belongs to ws2.
	seedRebuildJob(t, env.db, "job-ws2-1", "queued", "ws2", "rebase", "bob")

	// Attempt to cancel via ws1 path.
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/rebuilds/job-ws2-1", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE /rebuilds/:id (cross-workspace) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestCancelRebuild_PATWithoutWriteScope_Returns403(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedRebuildJob(t, env.db, "job-perm-1", "queued", "ws1", "rebase", "alice")

	// PAT with only rebuilds:read scope (no rebuilds:write).
	auth := rebuildPATAuth("alice", "rebuilds:read")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/rebuilds/job-perm-1", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("DELETE /rebuilds/:id (PAT read-only) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// ===========================================================================
// TS-NS-3: POST /api/v1/workspaces/:slug/rebuilds/:id/requeue requeues a
// dead-lettered rebuild and returns HTTP 200 with the job record.
//
// Requirement: NS-REQ-3
// ===========================================================================

func TestRequeueRebuild_DeadLetterJob_Returns200(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedRebuildJob(t, env.db, "job-dl-requeue", jobqueue.StatusDeadLetter, "ws1", "rebase", "alice")

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/rebuilds/job-dl-requeue/requeue", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /rebuilds/:id/requeue (dead_letter) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify the job is now queued with retry_count=0.
	var status string
	var retryCount int
	err := env.db.QueryRow("SELECT status, retry_count FROM jobs WHERE id = ?", "job-dl-requeue").Scan(&status, &retryCount)
	if err != nil {
		t.Fatalf("query job status failed: %v", err)
	}
	if status != "queued" {
		t.Errorf("expected job status='queued', got %q", status)
	}
	if retryCount != 0 {
		t.Errorf("expected retry_count=0, got %d", retryCount)
	}
}

// ===========================================================================
// TS-NS-5: POST .../requeue returns HTTP 409 when the job is not in
// dead_letter status or when an active job already exists.
//
// Requirement: NS-REQ-5
// ===========================================================================

func TestRequeueRebuild_NonDeadLetterJob_Returns409(t *testing.T) {
	statuses := []string{"queued", "running", "completed", "failed", "cancelled"}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			env := newRebuildTestEnv(t)

			seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
			seedRebuildJob(t, env.db, "job-"+status, status, "ws1", "rebase", "alice")

			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/rebuilds/job-"+status+"/requeue", "", rebuildUserAuth("alice"))

			if rec.Code != http.StatusConflict {
				t.Fatalf("POST /rebuilds/:id/requeue (%s) status = %d; want %d; body = %s",
					status, rec.Code, http.StatusConflict, rec.Body.String())
			}

			resp := parseErrorEnvelope(t, rec)
			if resp.Error.Message == "" {
				t.Error("expected non-empty error message")
			}
		})
	}
}

func TestRequeueRebuild_ActiveJobExists_Returns409(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	// Dead-lettered job.
	seedRebuildJob(t, env.db, "job-dl-dup", jobqueue.StatusDeadLetter, "ws1", "rebase", "alice")
	// Active queued job with the same key.
	seedRebuildJob(t, env.db, "job-active-dup", jobqueue.StatusQueued, "ws1", "rebase", "alice")

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/rebuilds/job-dl-dup/requeue", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /rebuilds/:id/requeue (active sibling) status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	// Original dead-lettered job should be unchanged.
	var status string
	err := env.db.QueryRow("SELECT status FROM jobs WHERE id = ?", "job-dl-dup").Scan(&status)
	if err != nil {
		t.Fatalf("query job status failed: %v", err)
	}
	if status != "dead_letter" {
		t.Errorf("expected job status='dead_letter' (unchanged), got %q", status)
	}
}

func TestRequeueRebuild_NonexistentJob_Returns404(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/rebuilds/nonexistent-id/requeue", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /rebuilds/:id/requeue (nonexistent) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestRequeueRebuild_DifferentWorkspaceJob_Returns404(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedWorkspace(t, env.db, "ws2", "bob", "active", "ready", "carry_patch", "integration")

	seedRebuildJob(t, env.db, "job-ws2-dl", jobqueue.StatusDeadLetter, "ws2", "rebase", "bob")

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/rebuilds/job-ws2-dl/requeue", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /rebuilds/:id/requeue (cross-workspace) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestRequeueRebuild_PATWithoutWriteScope_Returns403(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedRebuildJob(t, env.db, "job-perm-dl", jobqueue.StatusDeadLetter, "ws1", "rebase", "alice")

	auth := rebuildPATAuth("alice", "rebuilds:read")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/rebuilds/job-perm-dl/requeue", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /rebuilds/:id/requeue (PAT read-only) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}
