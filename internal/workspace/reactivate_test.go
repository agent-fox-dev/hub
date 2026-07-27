package workspace

import (
	"encoding/json"
	"net/http"
	"testing"
)

// ========================================================================
// Spec 05 Task 3.2: Reactivate lifecycle changes
// (TS-05-23)
// ========================================================================

// TS-05-23: POST /api/v1/workspaces/:slug/reactivate sets status='active',
// clone_status='pending', clears clone_error, enqueues a reclone job, and
// returns HTTP 200 immediately.
// Requirement: 05-REQ-7.1
func TestReactivate_Spec05_ArchivedToActive(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "archived-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "archived",
	})

	// Set clone_status to 'archived' and add a stale clone_error to verify
	// it gets cleared on reactivation.
	staleError := "previous clone failure"
	if err := updateCloneStatus(env.db, "archived-ws", "archived", nil, &staleError); err != nil {
		t.Fatalf("updateCloneStatus('archived'): %v", err)
	}

	// Set up a test job queue to capture enqueued reclone jobs.
	q := &JobQueue{
		jobs: make(chan CloneJob, 10),
	}
	oldQueue := defaultQueue
	defaultQueue = q
	defer func() { defaultQueue = oldQueue }()

	// Reactivate the workspace.
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/archived-ws/reactivate", "",
		userAuth("user-1"))

	// Assert HTTP 200.
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d; want %d\nbody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Parse response with clone fields.
	var ws spec05WorkspaceJSON
	if err := json.NewDecoder(rec.Body).Decode(&ws); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Assert status = 'active'.
	if ws.Status != "active" {
		t.Errorf("status = %q; want %q", ws.Status, "active")
	}

	// Assert clone_status = 'pending'.
	if ws.CloneStatus == nil {
		t.Fatal("clone_status is nil; want 'pending'")
	}
	if *ws.CloneStatus != "pending" {
		t.Errorf("clone_status = %q; want %q", *ws.CloneStatus, "pending")
	}

	// Assert clone_error is cleared to null.
	if ws.CloneError != nil {
		t.Errorf("clone_error = %q; want null (cleared on reactivation)", *ws.CloneError)
	}

	// Verify a reclone job was enqueued on the queue.
	select {
	case job := <-q.jobs:
		if job.Slug != "archived-ws" {
			t.Errorf("enqueued job slug = %q; want %q", job.Slug, "archived-ws")
		}
		if job.GitURL != "https://github.com/org/repo" {
			t.Errorf("enqueued job git_url = %q; want %q",
				job.GitURL, "https://github.com/org/repo")
		}
	default:
		t.Error("no reclone job was enqueued on the job queue")
	}

	// Verify DB state: status='active', clone_status='pending', clone_error=null.
	cloneStatus, _, cloneError, err := getCloneFields(env.db, "archived-ws")
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "pending" {
		t.Errorf("DB clone_status = %q; want %q", cloneStatus, "pending")
	}
	if cloneError != nil {
		t.Errorf("DB clone_error = %q; want nil", *cloneError)
	}
}

// TestReactivate_Spec05_NotArchivedReturns409 verifies that reactivating a
// workspace that is not in archived status returns HTTP 409.
// Requirement: 05-REQ-7.E3
func TestReactivate_Spec05_NotArchivedReturns409(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "active-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "active",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/active-ws/reactivate", "",
		userAuth("user-1"))

	// Spec 05-REQ-7.E3 requires HTTP 409.
	// Note: current implementation returns 400; spec 05 changes this to 409.
	if rec.Code != http.StatusConflict {
		t.Errorf("HTTP status = %d; want %d (workspace not archived)",
			rec.Code, http.StatusConflict)
	}
}

// TestReactivate_Spec05_DBFailureReturns500 verifies that a database update
// failure during reactivation returns HTTP 500 without enqueuing a reclone job.
// Requirement: 05-REQ-7.E4
func TestReactivate_Spec05_DBFailureReturns500(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "db-fail-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "archived",
	})

	// Set clone_status to 'archived'.
	if err := updateCloneStatus(env.db, "db-fail-ws", "archived", nil, nil); err != nil {
		t.Fatalf("updateCloneStatus('archived'): %v", err)
	}

	// Set up a test job queue to verify no job is enqueued on failure.
	q := &JobQueue{
		jobs: make(chan CloneJob, 10),
	}
	oldQueue := defaultQueue
	defaultQueue = q
	defer func() { defaultQueue = oldQueue }()

	// Install a trigger that causes UPDATE to fail, simulating a DB error
	// after authorization has passed.
	if _, err := env.db.Exec(`
		CREATE TRIGGER prevent_reactivate_update BEFORE UPDATE ON workspaces
		WHEN NEW.status = 'active' AND OLD.status = 'archived'
		BEGIN
			SELECT RAISE(FAIL, 'simulated database failure');
		END
	`); err != nil {
		t.Fatalf("failed to create trigger: %v", err)
	}

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/db-fail-ws/reactivate", "",
		userAuth("user-1"))

	// Assert HTTP 500.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("HTTP status = %d; want %d\nbody: %s",
			rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	// Verify no reclone job was enqueued.
	select {
	case job := <-q.jobs:
		t.Errorf("reclone job was enqueued (slug=%q) despite DB failure", job.Slug)
	default:
		// Good: no job was enqueued.
	}
}
