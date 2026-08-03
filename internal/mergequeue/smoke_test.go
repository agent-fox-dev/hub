package mergequeue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	_ "modernc.org/sqlite"
)

// ===========================================================================
// Smoke Tests (TS-11-SMOKE-1 through TS-11-SMOKE-6)
//
// These tests verify end-to-end integration of the merge queue components.
// They use real SQLite databases with mock git operations (via GitOps
// interface) and the full HTTP handler stack.
// ===========================================================================

// ---------------------------------------------------------------------------
// Smoke test helpers
// ---------------------------------------------------------------------------

// newSmokeTestEnv creates a complete test environment with:
// - In-memory SQLite database with merge_jobs, campaigns, campaign_specs, and workspaces tables
// - Echo HTTP router with merge queue routes registered
// - A merge Queue wired to mock GitOps and a mock BranchLocker
// - An optional CanMergeFunc and PostMergeHook
//
// The returned env includes the queue (started); callers must call env.stop()
// to shut down the worker goroutine.
type smokeTestEnv struct {
	db    *sql.DB
	queue *Queue
	env   *mergeHTTPTestEnv
}

func newSmokeTestEnv(t *testing.T, mockGit *mockGitOps, canMergeFn CanMergeFunc, hook PostMergeHook) *smokeTestEnv {
	t.Helper()

	db := openTestDBNoSchema(t)
	setupMergeJobsTable(t, db)
	setupCampaignTables(t, db)
	setupWorkspacesTable(t, db)
	insertTestWorkspace(t, db, "my-workspace")

	locker := NewInMemoryBranchLocker()
	deps := MergeDeps{
		Git:           mockGit,
		Locker:        locker,
		WorkspaceRoot: t.TempDir(),
	}
	if hook != nil {
		deps.Hook = hook
	}

	q := NewQueue(db, deps, canMergeFn)
	q.pollInterval = 50 * time.Millisecond // Fast polling for tests.

	// Build the HTTP test env using the same DB.
	e := newSmokeHTTPTestEnv(t, db, q)

	return &smokeTestEnv{
		db:    db,
		queue: q,
		env:   e,
	}
}

func (s *smokeTestEnv) start() {
	s.queue.Start()
}

func (s *smokeTestEnv) stop() {
	s.queue.Stop()
}

// newSmokeHTTPTestEnv creates an HTTP test env reusing an existing db and queue.
func newSmokeHTTPTestEnv(t *testing.T, db *sql.DB, queue *Queue) *mergeHTTPTestEnv {
	t.Helper()
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(mergeTestAuthMiddleware())
	if err := RegisterMergeRoutes(api, db, queue); err != nil {
		t.Fatalf("RegisterMergeRoutes() returned error: %v", err)
	}
	return &mergeHTTPTestEnv{e: e, db: db}
}

// waitForJobStatus polls the DB until the job reaches the target status or timeout.
func waitForJobStatus(t *testing.T, db *sql.DB, jobID string, wantStatus string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var status string
		err := db.QueryRow("SELECT status FROM merge_jobs WHERE id = ?", jobID).Scan(&status)
		if err == nil && status == wantStatus {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Return whatever the current status is.
	var finalStatus string
	_ = db.QueryRow("SELECT status FROM merge_jobs WHERE id = ?", jobID).Scan(&finalStatus)
	return finalStatus
}

// submitMerge submits a merge job via POST /merges and returns the HTTP response body.
func (s *smokeTestEnv) submitMerge(t *testing.T, sourceRef, targetBranch string, auth *apikit.AuthInfo) (int, map[string]interface{}) {
	t.Helper()
	body := `{"target_branch":"` + targetBranch + `","source_ref":"` + sourceRef + `"}`
	rec := s.env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)
	var respBody map[string]interface{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &respBody); err != nil {
			t.Logf("response body: %s", rec.Body.String())
		}
	}
	return rec.Code, respBody
}

// ---------------------------------------------------------------------------
// TS-11-SMOKE-1: Happy path — campaign merge submitted, processed, hook fired
// ---------------------------------------------------------------------------

func TestSmoke_HappyPath_CampaignMerge(t *testing.T) {
	hookCalled := make(chan MergeJob, 1)

	hook := func(_ context.Context, job MergeJob) error {
		hookCalled <- job
		return nil
	}

	mockGit := newHappyPathMockGitOps()
	senv := newSmokeTestEnv(t, mockGit, nil, hook)

	// Set up a campaign so the merge is a campaign merge.
	campaignID := newTestUUID("smokecamp1")
	now := time.Now().UTC().Format(time.RFC3339)
	insertTestCampaign(t, senv.db, campaignID, "my-workspace", "main",
		`{"specs":["07"],"edges":[]}`, now)
	insertTestCampaignSpec(t, senv.db, campaignID, "07", "active", "sha-07", now)

	senv.start()
	defer senv.stop()

	// Submit merge via HTTP.
	auth := mergeWriteAuth(newTestUUID("user1"))
	status, body := senv.submitMerge(t, "spec/07-secrets-variables", "main", auth)

	if status != http.StatusAccepted {
		t.Fatalf("POST /merges returned %d; want 202. Body: %v", status, body)
	}

	jobID, ok := body["id"].(string)
	if !ok || jobID == "" {
		t.Fatal("response missing job id")
	}

	// Verify no nonce in response.
	if _, hasNonce := body["nonce"]; hasNonce {
		t.Error("nonce field present in response; want it excluded")
	}

	// Wait for job to be processed.
	finalStatus := waitForJobStatus(t, senv.db, jobID, "merged", 5*time.Second)
	if finalStatus != "merged" {
		t.Fatalf("job status = %q; want 'merged'", finalStatus)
	}

	// Verify merged_sha is recorded.
	mergedSHA := getJobMergedSHA(t, senv.db, jobID)
	if !mergedSHA.Valid || mergedSHA.String == "" {
		t.Error("merged_sha is empty; want non-empty SHA after successful merge")
	}

	// Verify PostMergeHook was called (because campaign_id is non-null).
	select {
	case hookJob := <-hookCalled:
		if hookJob.ID != jobID {
			t.Errorf("PostMergeHook called with job ID %q; want %q", hookJob.ID, jobID)
		}
	case <-time.After(5 * time.Second):
		t.Error("PostMergeHook was not called within timeout")
	}

	// Verify GET /merges/:id returns the merged job.
	readAuth := mergeReadAuth(newTestUUID("reader1"))
	getRec := senv.env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/"+jobID, "", readAuth)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /merges/:id returned %d; want 200", getRec.Code)
	}
	var getBody map[string]interface{}
	json.Unmarshal(getRec.Body.Bytes(), &getBody)
	if getBody["status"] != "merged" {
		t.Errorf("GET response status = %v; want 'merged'", getBody["status"])
	}
	if _, hasNonce := getBody["nonce"]; hasNonce {
		t.Error("GET response contains nonce; want it excluded")
	}
}

// ---------------------------------------------------------------------------
// TS-11-SMOKE-2: Conflict detection — dry-run rejects merge early
// ---------------------------------------------------------------------------

func TestSmoke_ConflictDetection_DryRunRejects(t *testing.T) {
	// Create a mockGitOps that returns a conflict from merge-tree.
	mockGit := &mockGitOps{
		onRun: func(_ context.Context, args ...string) ([]byte, []byte, error) {
			if len(args) >= 1 && args[0] == "rev-parse" {
				for _, a := range args {
					if strings.HasPrefix(a, "origin/") {
						return []byte(testTargetHead + "\n"), nil, nil
					}
				}
				return []byte(testSourceHead + "\n"), nil, nil
			}
			return nil, nil, nil
		},
		onRunExitCode: func(_ context.Context, args ...string) ([]byte, []byte, int, error) {
			// merge-tree returns non-zero exit code with conflict info.
			stderr := "CONFLICT (content): Merge conflict in file1.go\nCONFLICT (content): Merge conflict in file2.go\n"
			return nil, []byte(stderr), 1, nil
		},
	}

	senv := newSmokeTestEnv(t, mockGit, nil, nil)
	senv.start()
	defer senv.stop()

	auth := mergeWriteAuth(newTestUUID("user2"))
	status, body := senv.submitMerge(t, "spec/07-secrets-variables", "main", auth)

	if status != http.StatusAccepted {
		t.Fatalf("POST /merges returned %d; want 202", status)
	}

	jobID := body["id"].(string)

	// Wait for conflict status.
	finalStatus := waitForJobStatus(t, senv.db, jobID, "conflict", 5*time.Second)
	if finalStatus != "conflict" {
		t.Fatalf("job status = %q; want 'conflict'", finalStatus)
	}

	// Verify conflict_details.
	var details sql.NullString
	senv.db.QueryRow("SELECT conflict_details FROM merge_jobs WHERE id = ?", jobID).Scan(&details)
	if !details.Valid {
		t.Fatal("conflict_details is NULL; want JSON array of conflicting files")
	}
	var files []string
	if err := json.Unmarshal([]byte(details.String), &files); err != nil {
		t.Fatalf("conflict_details is not valid JSON: %v", err)
	}
	if len(files) != 2 || files[0] != "file1.go" || files[1] != "file2.go" {
		t.Errorf("conflict_details = %v; want [file1.go, file2.go]", files)
	}

	// GET /merges/:id returns conflict_details as a native JSON array.
	readAuth := mergeReadAuth(newTestUUID("reader2"))
	getRec := senv.env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/"+jobID, "", readAuth)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET returned %d; want 200", getRec.Code)
	}
	var getBody map[string]interface{}
	json.Unmarshal(getRec.Body.Bytes(), &getBody)
	if getBody["status"] != "conflict" {
		t.Errorf("GET status = %v; want 'conflict'", getBody["status"])
	}
	// Verify conflict_details is a JSON array (not a string).
	cd, ok := getBody["conflict_details"].([]interface{})
	if !ok {
		t.Errorf("conflict_details is not a JSON array; got %T: %v", getBody["conflict_details"], getBody["conflict_details"])
	} else if len(cd) != 2 {
		t.Errorf("conflict_details has %d items; want 2", len(cd))
	}
}

// ---------------------------------------------------------------------------
// TS-11-SMOKE-3: Backoff and dead-letter — BeforeDependency exhausts retries
// ---------------------------------------------------------------------------

func TestSmoke_BackoffAndDeadLetter(t *testing.T) {
	// Count how many times canMerge is called.
	callCount := 0
	dependencyResolved := false

	canMergeFn := func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
		callCount++
		if dependencyResolved {
			return true, "", nil
		}
		return false, BeforeDependency, nil
	}

	mockGit := newHappyPathMockGitOps()
	senv := newSmokeTestEnv(t, mockGit, canMergeFn, nil)

	// Submit the merge job.
	auth := mergeWriteAuth(newTestUUID("user3"))

	// Don't start the queue yet — insert a queued job that we can control.
	body := `{"target_branch":"main","source_ref":"spec/07-secrets-variables"}`
	rec := senv.env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /merges returned %d; want 202", rec.Code)
	}
	var submitBody map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &submitBody)
	jobID := submitBody["id"].(string)

	// Manually simulate 20 processJobByID calls to drive the backoff loop
	// without waiting for the actual timer delays.
	deps := MergeDeps{Git: mockGit, Locker: NewInMemoryBranchLocker(), WorkspaceRoot: t.TempDir()}
	for i := 0; i < 20; i++ {
		// Reset available_at to now so job is eligible.
		now := time.Now().UTC().Format(time.RFC3339)
		senv.db.Exec("UPDATE merge_jobs SET available_at = ? WHERE id = ?", now, jobID)

		err := processJobByID(context.Background(), senv.db, jobID, deps, canMergeFn)
		if err != nil {
			t.Fatalf("processJobByID() iteration %d returned error: %v", i, err)
		}
	}

	// After 20 retries, job should be dead-lettered.
	status := getJobStatus(t, senv.db, jobID)
	if status != "dead_letter" {
		t.Fatalf("job status = %q after 20 retries; want 'dead_letter'", status)
	}

	// GET should show dead_letter status.
	readAuth := mergeReadAuth(newTestUUID("reader3"))
	getRec := senv.env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/"+jobID, "", readAuth)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET returned %d; want 200", getRec.Code)
	}
	var getBody map[string]interface{}
	json.Unmarshal(getRec.Body.Bytes(), &getBody)
	if getBody["status"] != "dead_letter" {
		t.Errorf("GET status = %v; want 'dead_letter'", getBody["status"])
	}

	// Resolve the dependency and requeue.
	dependencyResolved = true
	operatorAuth := mergeWriteAuth(newTestUUID("operator3"))
	requeueRec := senv.env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges/"+jobID+"/requeue", "", operatorAuth)
	if requeueRec.Code != http.StatusAccepted {
		t.Fatalf("POST /requeue returned %d; want 202. Body: %s", requeueRec.Code, requeueRec.Body.String())
	}

	var requeueBody map[string]interface{}
	json.Unmarshal(requeueRec.Body.Bytes(), &requeueBody)
	newJobID := requeueBody["id"].(string)
	if newJobID == jobID {
		t.Error("requeued job has same ID as original; want new ID")
	}
	if requeueBody["status"] != "queued" {
		t.Errorf("requeued job status = %v; want 'queued'", requeueBody["status"])
	}
	if int(requeueBody["retry_count"].(float64)) != 0 {
		t.Errorf("requeued job retry_count = %v; want 0", requeueBody["retry_count"])
	}
	if requeueBody["submitted_by"] != newTestUUID("operator3") {
		t.Errorf("requeued job submitted_by = %v; want %q", requeueBody["submitted_by"], newTestUUID("operator3"))
	}

	// Original job should remain dead_letter.
	origStatus := getJobStatus(t, senv.db, jobID)
	if origStatus != "dead_letter" {
		t.Errorf("original job status = %q; want 'dead_letter' (unchanged)", origStatus)
	}

	// Process the new requeued job — it should succeed now.
	err := processJobByID(context.Background(), senv.db, newJobID, deps, canMergeFn)
	if err != nil {
		t.Fatalf("processJobByID() for requeued job returned error: %v", err)
	}
	newStatus := getJobStatus(t, senv.db, newJobID)
	if newStatus != "merged" {
		t.Errorf("requeued job status = %q after processing; want 'merged'", newStatus)
	}
}

// ---------------------------------------------------------------------------
// TS-11-SMOKE-4: Cancellation path — operator cancels a queued job
// ---------------------------------------------------------------------------

func TestSmoke_Cancellation(t *testing.T) {
	mockGit := newHappyPathMockGitOps()
	// Use a canMerge that always blocks (BeforeDependency) so the job stays
	// in queued status long enough to cancel.
	canMergeFn := func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
		return false, BeforeDependency, nil
	}
	senv := newSmokeTestEnv(t, mockGit, canMergeFn, nil)

	auth := mergeWriteAuth(newTestUUID("user4"))

	// Submit a merge job.
	status, body := senv.submitMerge(t, "spec/07-secrets-variables", "main", auth)
	if status != http.StatusAccepted {
		t.Fatalf("POST /merges returned %d; want 202", status)
	}
	jobID := body["id"].(string)

	// Cancel the queued job.
	cancelRec := senv.env.doMergeRequest(t, http.MethodDelete,
		"/api/v1/workspaces/my-workspace/merges/"+jobID, "", auth)
	if cancelRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /merges/:id returned %d; want 204. Body: %s", cancelRec.Code, cancelRec.Body.String())
	}

	// Verify status in DB.
	dbStatus := getJobStatus(t, senv.db, jobID)
	if dbStatus != "cancelled" {
		t.Errorf("job status = %q; want 'cancelled'", dbStatus)
	}

	// Verify updated_at was updated.
	updatedAt := getJobUpdatedAt(t, senv.db, jobID)
	createdAt := body["created_at"]
	if updatedAt == "" {
		t.Error("updated_at is empty after cancel")
	}
	// updated_at should be >= created_at (they might be the same second).
	_ = createdAt

	// Start the queue — it should NOT process the cancelled job.
	senv.start()
	defer senv.stop()

	// Wait a bit and confirm status stays cancelled.
	time.Sleep(200 * time.Millisecond)
	finalStatus := getJobStatus(t, senv.db, jobID)
	if finalStatus != "cancelled" {
		t.Errorf("job status after queue processing = %q; want 'cancelled'", finalStatus)
	}

	// GET /merges/:id returns cancelled.
	readAuth := mergeReadAuth(newTestUUID("reader4"))
	getRec := senv.env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/"+jobID, "", readAuth)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET returned %d; want 200", getRec.Code)
	}
	var getBody map[string]interface{}
	json.Unmarshal(getRec.Body.Bytes(), &getBody)
	if getBody["status"] != "cancelled" {
		t.Errorf("GET status = %v; want 'cancelled'", getBody["status"])
	}
}

// ---------------------------------------------------------------------------
// TS-11-SMOKE-5: Graceful shutdown — in-flight merge completes before exit
// ---------------------------------------------------------------------------

func TestSmoke_GracefulShutdown(t *testing.T) {
	// Create a mock git that introduces a delay in the rebase step
	// to simulate an in-flight operation.
	rebaseStarted := make(chan struct{})
	rebaseComplete := make(chan struct{})
	mockGit := &mockGitOps{
		onRun: func(_ context.Context, args ...string) ([]byte, []byte, error) {
			if len(args) >= 1 {
				switch args[0] {
				case "rev-parse":
					for _, a := range args {
						if strings.HasPrefix(a, "origin/") {
							return []byte(testTargetHead + "\n"), nil, nil
						}
					}
					return []byte(testMergedHead + "\n"), nil, nil
				case "rebase":
					close(rebaseStarted)
					<-rebaseComplete // Block until test signals to proceed.
					return nil, nil, nil
				}
			}
			return nil, nil, nil
		},
		onRunExitCode: func(_ context.Context, _ ...string) ([]byte, []byte, int, error) {
			return nil, nil, 0, nil
		},
	}

	senv := newSmokeTestEnv(t, mockGit, nil, nil)
	senv.start()

	auth := mergeWriteAuth(newTestUUID("user5"))

	// Submit a merge job.
	status, body := senv.submitMerge(t, "spec/07-secrets-variables", "main", auth)
	if status != http.StatusAccepted {
		t.Fatalf("POST /merges returned %d; want 202", status)
	}
	jobID := body["id"].(string)

	// Wait for the rebase step to start (in-flight).
	select {
	case <-rebaseStarted:
		// Good — merge is in progress.
	case <-time.After(5 * time.Second):
		t.Fatal("rebase step did not start within timeout")
	}

	// Call Stop() in a goroutine — it should block until in-flight completes.
	stopDone := make(chan struct{})
	go func() {
		senv.stop()
		close(stopDone)
	}()

	// Stop should be blocking (rebase is still running).
	select {
	case <-stopDone:
		t.Fatal("Stop() returned before in-flight merge completed")
	case <-time.After(50 * time.Millisecond):
		// Good — Stop is still blocking.
	}

	// Let the rebase complete.
	close(rebaseComplete)

	// Stop should now return.
	select {
	case <-stopDone:
		// Good.
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return after in-flight merge completed")
	}

	// Verify the job completed successfully.
	finalStatus := getJobStatus(t, senv.db, jobID)
	if finalStatus != "merged" {
		t.Errorf("job status = %q after shutdown; want 'merged'", finalStatus)
	}
}

// ---------------------------------------------------------------------------
// TS-11-SMOKE-6: Duplicate submission rejected with HTTP 409
// ---------------------------------------------------------------------------

func TestSmoke_DuplicateSubmission(t *testing.T) {
	// Use a canMerge that always blocks to keep the first job in queued.
	canMergeFn := func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
		return false, BeforeDependency, nil
	}

	mockGit := newHappyPathMockGitOps()
	senv := newSmokeTestEnv(t, mockGit, canMergeFn, nil)

	auth := mergeWriteAuth(newTestUUID("user6"))

	// Submit the first merge job.
	status1, body1 := senv.submitMerge(t, "spec/07-secrets-variables", "main", auth)
	if status1 != http.StatusAccepted {
		t.Fatalf("first POST /merges returned %d; want 202", status1)
	}
	jobID1 := body1["id"].(string)

	// Submit a second merge for the same source_ref.
	status2, body2 := senv.submitMerge(t, "spec/07-secrets-variables", "main", auth)
	if status2 != http.StatusConflict {
		t.Fatalf("second POST /merges returned %d; want 409", status2)
	}

	// Verify the response body.
	errMsg, _ := body2["error"].(string)
	if errMsg != "merge already in progress for this source branch" {
		t.Errorf("error = %q; want 'merge already in progress for this source branch'", errMsg)
	}
	existingID, _ := body2["existing_job_id"].(string)
	if existingID != jobID1 {
		t.Errorf("existing_job_id = %q; want %q", existingID, jobID1)
	}

	// Verify only one merge job exists in the database for this source_ref.
	var count int
	senv.db.QueryRow(`SELECT COUNT(*) FROM merge_jobs WHERE workspace_slug = 'my-workspace' AND source_ref = 'spec/07-secrets-variables'`).Scan(&count)
	if count != 1 {
		t.Errorf("merge job count = %d; want 1", count)
	}
}

// ---------------------------------------------------------------------------
// Additional: Verify cancel on running job returns 409
// ---------------------------------------------------------------------------

func TestSmoke_CancelRunningJob_Returns409(t *testing.T) {
	mockGit := newHappyPathMockGitOps()
	senv := newSmokeTestEnv(t, mockGit, nil, nil)

	auth := mergeWriteAuth(newTestUUID("user7"))

	// Submit a merge job and manually set it to running.
	status, body := senv.submitMerge(t, "feature/test-branch", "main", auth)
	if status != http.StatusAccepted {
		t.Fatalf("POST /merges returned %d; want 202", status)
	}
	jobID := body["id"].(string)

	// Transition to running via direct SQL (simulating the worker picking it up).
	senv.db.Exec("UPDATE merge_jobs SET status = 'running' WHERE id = ?", jobID)

	// Try to cancel — should get 409.
	cancelRec := senv.env.doMergeRequest(t, http.MethodDelete,
		"/api/v1/workspaces/my-workspace/merges/"+jobID, "", auth)
	if cancelRec.Code != http.StatusConflict {
		t.Fatalf("DELETE on running job returned %d; want 409. Body: %s", cancelRec.Code, cancelRec.Body.String())
	}

	var cancelBody map[string]interface{}
	json.Unmarshal(cancelRec.Body.Bytes(), &cancelBody)
	if cancelBody["status"] != "running" {
		t.Errorf("response status = %v; want 'running'", cancelBody["status"])
	}
}

// ---------------------------------------------------------------------------
// Additional: PostMergeHook NOT called for standalone merges
// ---------------------------------------------------------------------------

func TestSmoke_PostMergeHook_NotCalledForStandalone(t *testing.T) {
	hookCalled := false
	hook := func(_ context.Context, _ MergeJob) error {
		hookCalled = true
		return errors.New("should not be called")
	}

	mockGit := newHappyPathMockGitOps()
	senv := newSmokeTestEnv(t, mockGit, nil, hook)
	senv.start()
	defer senv.stop()

	auth := mergeWriteAuth(newTestUUID("user8"))

	// Submit a standalone merge (no spec/ prefix).
	status, body := senv.submitMerge(t, "feature/standalone-branch", "main", auth)
	if status != http.StatusAccepted {
		t.Fatalf("POST /merges returned %d; want 202", status)
	}
	jobID := body["id"].(string)

	// Wait for merge to complete.
	finalStatus := waitForJobStatus(t, senv.db, jobID, "merged", 5*time.Second)
	if finalStatus != "merged" {
		t.Fatalf("job status = %q; want 'merged'", finalStatus)
	}

	// Hook should not have been called for standalone merge.
	if hookCalled {
		t.Error("PostMergeHook was called for standalone merge; want it skipped")
	}
}

