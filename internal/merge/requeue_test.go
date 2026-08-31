package merge

import (
	"net/http"
	"testing"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// TS-NS-4: POST /api/v1/workspaces/:slug/merges/:id/requeue requeues a
// dead-lettered merge job and returns HTTP 200 with the merge job record.
//
// Requirement: NS-REQ-4
// ===========================================================================

func TestRequeueMerge_DeadLetterJob_Returns200(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedMergeJob(t, env.db, "job-dl-requeue", jobqueue.StatusDeadLetter, "ws1", "main", "feature/a", "alice", nil, nil)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges/job-dl-requeue/requeue", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /merges/:id/requeue (dead_letter) status = %d; want %d; body = %s",
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

	// Verify the response contains the job record with status=queued.
	resp := parseMergeJobFullResponse(t, rec)
	if resp.Status != "queued" {
		t.Errorf("expected response status='queued', got %q", resp.Status)
	}
	if resp.ID != "job-dl-requeue" {
		t.Errorf("expected response id='job-dl-requeue', got %q", resp.ID)
	}
}

// ===========================================================================
// TS-NS-5: POST .../requeue returns HTTP 409 when the job is not in
// dead_letter status or when an active job already exists.
//
// Requirement: NS-REQ-5
// ===========================================================================

func TestRequeueMerge_NonDeadLetterJob_Returns409(t *testing.T) {
	statuses := []string{"queued", "running", "completed", "failed", "cancelled"}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			env := newMergeTestEnv(t, nil)

			seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
			seedMergeJob(t, env.db, "job-"+status, status, "ws1", "main", "feature/a", "alice", nil, nil)

			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges/job-"+status+"/requeue", "", mergeUserAuth("alice"))

			if rec.Code != http.StatusConflict {
				t.Fatalf("POST /merges/:id/requeue (%s) status = %d; want %d; body = %s",
					status, rec.Code, http.StatusConflict, rec.Body.String())
			}

			resp := parseMergeErrorEnvelope(t, rec)
			if resp.Error.Message == "" {
				t.Error("expected non-empty error message")
			}
		})
	}
}

func TestRequeueMerge_ActiveJobExists_Returns409(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	// Dead-lettered job.
	seedMergeJob(t, env.db, "job-dl-dup", jobqueue.StatusDeadLetter, "ws1", "main", "feature/a", "alice", nil, nil)
	// Active queued job with the same (type, key).
	seedMergeJob(t, env.db, "job-active-dup", jobqueue.StatusQueued, "ws1", "main", "feature/a", "alice", nil, nil)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges/job-dl-dup/requeue", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /merges/:id/requeue (active sibling) status = %d; want %d; body = %s",
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

func TestRequeueMerge_NonexistentJob_Returns404(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges/nonexistent-id/requeue", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /merges/:id/requeue (nonexistent) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestRequeueMerge_DifferentWorkspaceJob_Returns404(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedTestWorkspace(t, env.db, "ws2", "bob", "active", "ready")

	seedMergeJob(t, env.db, "job-ws2-dl", jobqueue.StatusDeadLetter, "ws2", "main", "feature/a", "bob", nil, nil)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges/job-ws2-dl/requeue", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /merges/:id/requeue (cross-workspace) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestRequeueMerge_PATWithoutWriteScope_Returns403(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedMergeJob(t, env.db, "job-perm-dl", jobqueue.StatusDeadLetter, "ws1", "main", "feature/a", "alice", nil, nil)

	// PAT with merges:read only (no merges:write).
	auth := mergePATAuth("alice", "merges:read")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges/job-perm-dl/requeue", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /merges/:id/requeue (PAT read-only) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}
