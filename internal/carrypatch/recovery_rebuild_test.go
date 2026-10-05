package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// TS-23-25: In origin mode every completed reset records in_sync state
// ===========================================================================

func TestRecovery_OriginModeRecordsInSyncState_TS2325(t *testing.T) {
	getVar := func(scope, s, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		return "", nil
	}

	branch := "feature/state"
	patchID := "p-state"

	actions := []struct {
		name  string
		setup func(t *testing.T, forkDir, trunkDir string) string // returns expected fork tip
	}{
		{
			name: "created",
			setup: func(t *testing.T, forkDir, trunkDir string) string {
				t.Helper()
				tip := createBranchOnBare(t, forkDir, branch, "state-create.txt", "create", "create commit")
				return tip
			},
		},
		{
			name: "none",
			setup: func(t *testing.T, forkDir, trunkDir string) string {
				t.Helper()
				// Create branch on fork and set local to the same commit.
				tip := createBranchOnBare(t, forkDir, branch, "state-none.txt", "none", "none commit")
				runGitCmd(t, trunkDir, "fetch", "origin", branch)
				runGitCmd(t, trunkDir, "branch", branch, "refs/remotes/origin/"+branch)
				return tip
			},
		},
		{
			name: "fast_forwarded",
			setup: func(t *testing.T, forkDir, trunkDir string) string {
				t.Helper()
				// Create branch on fork with two commits; set local at first.
				tmpDir := filepath.Join(t.TempDir(), "ff-setup")
				cloneRepo(t, forkDir, tmpDir)
				runGitCmd(t, tmpDir, "checkout", "-b", branch)
				commitFile(t, tmpDir, "state-ff1.txt", "ff1", "ff commit 1")
				firstCommit := runGitCmd(t, tmpDir, "rev-parse", "HEAD")
				commitFile(t, tmpDir, "state-ff2.txt", "ff2", "ff commit 2")
				forkTip := runGitCmd(t, tmpDir, "rev-parse", "HEAD")
				runGitCmd(t, tmpDir, "push", "origin", branch)

				runGitCmd(t, trunkDir, "fetch", "origin", branch)
				runGitCmd(t, trunkDir, "branch", branch, firstCommit)
				return forkTip
			},
		},
		{
			name: "replaced",
			setup: func(t *testing.T, forkDir, trunkDir string) string {
				t.Helper()
				// Create branch on fork.
				tip := createBranchOnBare(t, forkDir, branch, "state-replace.txt", "replace", "replace commit")
				// Create a diverged local branch.
				runGitCmd(t, trunkDir, "checkout", "-b", branch)
				commitFile(t, trunkDir, "state-diverge.txt", "diverge", "diverge commit")
				runGitCmd(t, trunkDir, "checkout", "main")
				runGitCmd(t, trunkDir, "fetch", "origin", branch)
				return tip
			},
		},
	}

	for _, tc := range actions {
		t.Run(tc.name, func(t *testing.T) {
			// Each subtest gets its own fork and trunk.
			workspaceRoot := t.TempDir()
			slug := "origin-state-" + tc.name
			forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

			// Set up a real PatchStore on a test database.
			db := openTestDB(t)
			createPatchesTable(t, db)
			patchStore := NewSQLPatchStore(db)

			// Seed a patch with origin_sync_state = diverged.
			now := time.Now().UTC().Format(time.RFC3339Nano)
			_, err := db.Exec(
				`INSERT INTO patches (id, workspace_slug, branch_name, position, status, origin_sync_state, added_at, updated_at)
				 VALUES (?, ?, ?, 1, 'active', 'diverged', ?, ?)`,
				patchID, slug, branch, now, now,
			)
			if err != nil {
				t.Fatalf("seed patch: %v", err)
			}

			forkTip := tc.setup(t, forkDir, trunkDir)

			sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
			svc := newTestRecoveryServiceWithStore(workspaceRoot, getVar, nil, sf.fetch, patchStore, nil)

			ctx := context.Background()
			patchInfo := ResetPatchInfo{
				ID:                patchID,
				BranchName:        branch,
				Status:            PatchStatusActive,
				IntegrationBranch: "main",
			}

			before := time.Now().UTC()
			_, resetErr := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
			if resetErr != nil {
				t.Fatalf("RunReset failed: %v", resetErr)
			}

			// Read the patch row.
			var syncState, originSHA, syncedAt sql.NullString
			err = db.QueryRow(
				`SELECT origin_sync_state, origin_sha, origin_synced_at FROM patches WHERE id = ?`,
				patchID,
			).Scan(&syncState, &originSHA, &syncedAt)
			if err != nil {
				t.Fatalf("query patch: %v", err)
			}

			if !syncState.Valid || syncState.String != StateInSync {
				t.Errorf("origin_sync_state = %v; want %q", syncState, StateInSync)
			}
			if !originSHA.Valid || originSHA.String != forkTip {
				t.Errorf("origin_sha = %v; want %q", originSHA, forkTip)
			}
			if !syncedAt.Valid {
				t.Fatal("origin_synced_at is NULL")
			}
			// Parse the synced_at time and check it's within 5 seconds of now.
			parsed, parseErr := time.Parse(time.RFC3339Nano, syncedAt.String)
			if parseErr != nil {
				parsed, parseErr = time.Parse(time.RFC3339, syncedAt.String)
				if parseErr != nil {
					t.Fatalf("parse origin_synced_at %q: %v", syncedAt.String, parseErr)
				}
			}
			if time.Since(parsed) > 5*time.Second || parsed.Before(before.Add(-1*time.Second)) {
				t.Errorf("origin_synced_at = %v; want within 5s of now", parsed)
			}
		})
	}
}

// ===========================================================================
// TS-23-26: In hub mode a reset writes no origin sync state
// ===========================================================================

func TestRecovery_HubModeNoOriginState_TS2326(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "hub-state"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Set up a real PatchStore on a test database.
	db := openTestDB(t)
	createPatchesTable(t, db)

	branch := "feature/hub-state"
	patchID := "p-hub-state"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES (?, ?, ?, 1, 'active', ?, ?)`,
		patchID, slug, branch, now, now,
	)
	if err != nil {
		t.Fatalf("seed patch: %v", err)
	}

	// Create a diverged scenario so we get a "replaced" action.
	createBranchOnBare(t, forkDir, branch, "hub-fork.txt", "fork", "fork commit")
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "hub-local.txt", "local", "local commit")
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "fetch", "origin", branch)

	getVar := func(scope, s, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "hub", nil
		}
		return "", nil
	}

	// Use a recording mock to verify SetOriginSyncState is never called.
	mockStore := newMockPatchStore(nil)
	sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
	svc := newTestRecoveryServiceWithStore(workspaceRoot, getVar, nil, sf.fetch, mockStore, nil)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                patchID,
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "main",
	}

	result, resetErr := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
	if resetErr != nil {
		t.Fatalf("RunReset failed: %v", resetErr)
	}

	if result.Action != ActionReplaced {
		t.Errorf("action = %q; want %q", result.Action, ActionReplaced)
	}

	// Verify SetOriginSyncState was never called.
	if len(mockStore.OriginSyncStates) != 0 {
		t.Errorf("SetOriginSyncState called %d times; want 0", len(mockStore.OriginSyncStates))
	}

	// Also verify the real DB row is unchanged (null origin columns).
	var syncState, originSHA, syncedAt sql.NullString
	err = db.QueryRow(
		`SELECT origin_sync_state, origin_sha, origin_synced_at FROM patches WHERE id = ?`,
		patchID,
	).Scan(&syncState, &originSHA, &syncedAt)
	if err != nil {
		t.Fatalf("query patch: %v", err)
	}
	if syncState.Valid {
		t.Errorf("origin_sync_state should be NULL, got %q", syncState.String)
	}
	if originSHA.Valid {
		t.Errorf("origin_sha should be NULL, got %q", originSHA.String)
	}
	if syncedAt.Valid {
		t.Errorf("origin_synced_at should be NULL, got %q", syncedAt.String)
	}
}

// ===========================================================================
// TS-23-27: A moved active or conflict patch enqueues a rebuild
// ===========================================================================

func TestRecovery_RebuildEnqueuedForActiveConflict_TS2327(t *testing.T) {
	integrationBranch := "deploy"

	getVar := func(scope, s, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "hub", nil
		}
		return "", nil
	}

	statuses := []string{PatchStatusActive, PatchStatusConflict}
	actionSetups := []struct {
		name  string
		setup func(t *testing.T, forkDir, trunkDir, branch string)
	}{
		{
			name: "created",
			setup: func(t *testing.T, forkDir, trunkDir, branch string) {
				t.Helper()
				createBranchOnBare(t, forkDir, branch, "rebuild-create.txt", "create", "create commit")
			},
		},
		{
			name: "fast_forwarded",
			setup: func(t *testing.T, forkDir, trunkDir, branch string) {
				t.Helper()
				tmpDir := filepath.Join(t.TempDir(), "ff-setup")
				cloneRepo(t, forkDir, tmpDir)
				runGitCmd(t, tmpDir, "checkout", "-b", branch)
				commitFile(t, tmpDir, "rebuild-ff1.txt", "ff1", "ff commit 1")
				firstCommit := runGitCmd(t, tmpDir, "rev-parse", "HEAD")
				commitFile(t, tmpDir, "rebuild-ff2.txt", "ff2", "ff commit 2")
				runGitCmd(t, tmpDir, "push", "origin", branch)

				runGitCmd(t, trunkDir, "fetch", "origin", branch)
				runGitCmd(t, trunkDir, "branch", branch, firstCommit)
			},
		},
		{
			name: "replaced",
			setup: func(t *testing.T, forkDir, trunkDir, branch string) {
				t.Helper()
				createBranchOnBare(t, forkDir, branch, "rebuild-replace.txt", "replace", "replace commit")
				runGitCmd(t, trunkDir, "checkout", "-b", branch)
				commitFile(t, trunkDir, "rebuild-diverge.txt", "diverge", "diverge commit")
				runGitCmd(t, trunkDir, "checkout", "main")
				runGitCmd(t, trunkDir, "fetch", "origin", branch)
			},
		},
	}

	for _, status := range statuses {
		for _, as := range actionSetups {
			t.Run(fmt.Sprintf("%s_%s", status, as.name), func(t *testing.T) {
				workspaceRoot := t.TempDir()
				slug := fmt.Sprintf("rebuild-%s-%s", status, as.name)
				branch := "feature/rebuild"

				forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

				q, _ := newTestQueue(t)
				_ = RegisterRebuildJob(q, &RebuildHandler{})
				if err := q.Start(); err != nil {
					t.Fatalf("queue start: %v", err)
				}
				defer q.Stop()

				as.setup(t, forkDir, trunkDir, branch)

				sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
				svc := newTestRecoveryServiceWithStore(workspaceRoot, getVar, nil, sf.fetch, nil, q)

				ctx := context.Background()
				patchInfo := ResetPatchInfo{
					ID:                "p-rebuild",
					BranchName:        branch,
					Status:            status,
					IntegrationBranch: integrationBranch,
				}

				result, resetErr := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
				if resetErr != nil {
					t.Fatalf("RunReset failed: %v", resetErr)
				}

				if !result.RebuildTriggered {
					t.Error("rebuild_triggered = false; want true")
				}
				if result.RebuildJobID == "" {
					t.Error("rebuild_job_id is empty; want non-empty")
				}

				// Verify the job exists and has the right properties.
				job, jobErr := q.GetByID(result.RebuildJobID)
				if jobErr != nil {
					t.Fatalf("get job: %v", jobErr)
				}
				if job.Type != "rebuild" {
					t.Errorf("job type = %q; want %q", job.Type, "rebuild")
				}
				if job.Key != slug {
					t.Errorf("job key = %q; want %q", job.Key, slug)
				}
				expectedGroup := slug + ":" + integrationBranch
				if job.GroupKey != expectedGroup {
					t.Errorf("job group = %q; want %q", job.GroupKey, expectedGroup)
				}
				if job.SubmittedBy != "user-1" {
					t.Errorf("submitted_by = %q; want %q", job.SubmittedBy, "user-1")
				}
			})
		}
	}
}

// ===========================================================================
// TS-23-28: AUTO_REBUILD_AFTER_SYNC=false suppresses the rebuild
// ===========================================================================

func TestRecovery_AutoRebuildFalseSuppressesRebuild_TS2328(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "no-rebuild"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	q, _ := newTestQueue(t)
	_ = RegisterRebuildJob(q, &RebuildHandler{})
	if err := q.Start(); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	defer q.Stop()

	branch := "feature/no-rebuild"

	// Create a diverged scenario.
	createBranchOnBare(t, forkDir, branch, "no-rebuild-fork.txt", "fork", "fork commit")
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "no-rebuild-local.txt", "local", "local commit")
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "fetch", "origin", branch)

	getVar := func(scope, s, key string) (string, error) {
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "false", nil
		}
		return "", nil
	}

	sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
	svc := newTestRecoveryServiceWithStore(workspaceRoot, getVar, nil, sf.fetch, nil, q)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p-no-rebuild",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "deploy",
	}

	result, resetErr := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
	if resetErr != nil {
		t.Fatalf("RunReset failed: %v", resetErr)
	}

	if result.Action != ActionReplaced {
		t.Errorf("action = %q; want %q", result.Action, ActionReplaced)
	}

	// Branch should have moved.
	forkTip := revParse(t, trunkDir, "refs/remotes/origin/"+branch)
	localTip := revParse(t, trunkDir, "refs/heads/"+branch)
	if localTip != forkTip {
		t.Errorf("local tip = %q; want %q (fork tip)", localTip, forkTip)
	}

	if result.RebuildTriggered {
		t.Error("rebuild_triggered = true; want false")
	}
	if result.RebuildJobID != "" {
		t.Errorf("rebuild_job_id = %q; want empty", result.RebuildJobID)
	}

	// Verify no jobs in the queue.
	jobs, _ := q.ListByKey("rebuild", slug)
	if len(jobs) != 0 {
		t.Errorf("queue has %d jobs; want 0", len(jobs))
	}
}

// ===========================================================================
// TS-23-29: A moved disabled patch triggers no rebuild
// ===========================================================================

func TestRecovery_DisabledPatchNoRebuild_TS2329(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "disabled-rebuild"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	q, _ := newTestQueue(t)
	_ = RegisterRebuildJob(q, &RebuildHandler{})
	if err := q.Start(); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	defer q.Stop()

	branch := "feature/disabled"

	// Create a diverged scenario.
	forkTip := createBranchOnBare(t, forkDir, branch, "disabled-fork.txt", "fork", "fork commit")
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "disabled-local.txt", "local", "local commit")
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "fetch", "origin", branch)

	getVar := func(scope, s, key string) (string, error) {
		return "", nil
	}

	sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
	svc := newTestRecoveryServiceWithStore(workspaceRoot, getVar, nil, sf.fetch, nil, q)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p-disabled",
		BranchName:        branch,
		Status:            PatchStatusDisabled,
		IntegrationBranch: "deploy",
	}

	result, resetErr := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
	if resetErr != nil {
		t.Fatalf("RunReset failed: %v", resetErr)
	}

	if result.Action != ActionReplaced {
		t.Errorf("action = %q; want %q", result.Action, ActionReplaced)
	}

	// Branch should have moved to the fork tip.
	localTip := revParse(t, trunkDir, "refs/heads/"+branch)
	if localTip != forkTip {
		t.Errorf("local tip = %q; want %q (fork tip)", localTip, forkTip)
	}

	if result.RebuildTriggered {
		t.Error("rebuild_triggered = true; want false")
	}
	if result.RebuildJobID != "" {
		t.Errorf("rebuild_job_id = %q; want empty", result.RebuildJobID)
	}

	// Verify no jobs in the queue.
	jobs, _ := q.ListByKey("rebuild", slug)
	if len(jobs) != 0 {
		t.Errorf("queue has %d jobs; want 0", len(jobs))
	}
}

// ===========================================================================
// TS-23-30: A reset with action none enqueues no rebuild
// ===========================================================================

func TestRecovery_ActionNoneNoRebuild_TS2330(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "none-rebuild"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	q, _ := newTestQueue(t)
	_ = RegisterRebuildJob(q, &RebuildHandler{})
	if err := q.Start(); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	defer q.Stop()

	branch := "feature/none"

	// Create a branch on the fork and set local to the same commit.
	createBranchOnBare(t, forkDir, branch, "none.txt", "none", "none commit")
	runGitCmd(t, trunkDir, "fetch", "origin", branch)
	runGitCmd(t, trunkDir, "branch", branch, "refs/remotes/origin/"+branch)

	getVar := func(scope, s, key string) (string, error) {
		return "", nil
	}

	sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
	svc := newTestRecoveryServiceWithStore(workspaceRoot, getVar, nil, sf.fetch, nil, q)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p-none",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "deploy",
	}

	result, resetErr := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
	if resetErr != nil {
		t.Fatalf("RunReset failed: %v", resetErr)
	}

	if result.Action != ActionNone {
		t.Errorf("action = %q; want %q", result.Action, ActionNone)
	}

	if result.RebuildTriggered {
		t.Error("rebuild_triggered = true; want false")
	}

	// Verify no jobs in the queue.
	jobs, _ := q.ListByKey("rebuild", slug)
	if len(jobs) != 0 {
		t.Errorf("queue has %d jobs; want 0", len(jobs))
	}
}

// ===========================================================================
// TS-23-31: A deduplicated rebuild enqueue leaves rebuild_triggered false
// ===========================================================================

func TestRecovery_DeduplicatedRebuild_TS2331(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "dedup-rebuild"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	q, qDB := newTestQueue(t)
	_ = RegisterRebuildJob(q, &RebuildHandler{})
	// Do NOT start the queue workers so the pre-existing job stays queued.

	branch := "feature/dedup"
	integrationBranch := "deploy"

	// Create a diverged scenario.
	createBranchOnBare(t, forkDir, branch, "dedup-fork.txt", "fork", "fork commit")
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "dedup-local.txt", "local", "local commit")
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "fetch", "origin", branch)

	// Pre-enqueue a rebuild job with the same key and group.
	groupKey := slug + ":" + integrationBranch
	payload := BuildRebuildPayload(slug, integrationBranch, "user-1", nil, "", "")
	payloadJSON, _ := json.Marshal(payload)
	_, _, enqErr := q.Enqueue(jobqueue.EnqueueParams{
		Type:        "rebuild",
		Key:         slug,
		Nonce:       "pre-existing",
		Payload:     payloadJSON,
		SubmittedBy: "user-1",
		Group:       groupKey,
	})
	if enqErr != nil {
		t.Fatalf("pre-enqueue: %v", enqErr)
	}

	// Count jobs before.
	jobsBefore := countJobs(t, qDB, slug)

	getVar := func(scope, s, key string) (string, error) {
		return "", nil
	}

	sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
	svc := newTestRecoveryServiceWithStore(workspaceRoot, getVar, nil, sf.fetch, nil, q)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p-dedup",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: integrationBranch,
	}

	result, resetErr := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
	if resetErr != nil {
		t.Fatalf("RunReset failed: %v", resetErr)
	}

	if result.RebuildTriggered {
		t.Error("rebuild_triggered = true; want false (deduplicated)")
	}
	if result.RebuildJobID != "" {
		t.Errorf("rebuild_job_id = %q; want empty", result.RebuildJobID)
	}

	// Verify the queue still holds the same number of jobs.
	jobsAfter := countJobs(t, qDB, slug)
	if jobsAfter != jobsBefore {
		t.Errorf("job count changed: %d -> %d", jobsBefore, jobsAfter)
	}
}

// ===========================================================================
// TS-23-32: A rebuild enqueue error is logged and the reset still completes
// ===========================================================================

func TestRecovery_RebuildEnqueueErrorLogged_TS2332(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "enqueue-error"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	branch := "feature/enqueue-err"

	// Create a diverged scenario.
	forkTip := createBranchOnBare(t, forkDir, branch, "enqueue-err-fork.txt", "fork", "fork commit")
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "enqueue-err-local.txt", "local", "local commit")
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "fetch", "origin", branch)

	getVar := func(scope, s, key string) (string, error) {
		return "", nil
	}

	// Use a failing queue.
	failingQueue := &failingJobQueue{}

	sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
	svc := newTestRecoveryServiceWithStore(workspaceRoot, getVar, nil, sf.fetch, nil, failingQueue)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p-enqueue-err",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "deploy",
	}

	result, resetErr := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
	if resetErr != nil {
		t.Fatalf("RunReset should succeed despite enqueue error, got: %v", resetErr)
	}

	if result.RebuildTriggered {
		t.Error("rebuild_triggered = true; want false")
	}

	// Branch should have moved to the fork tip.
	localTip := revParse(t, trunkDir, "refs/heads/"+branch)
	if localTip != forkTip {
		t.Errorf("local tip = %q; want %q (fork tip)", localTip, forkTip)
	}
}

// ===========================================================================
// Issue #45 finding 2: the reset and the sync enqueue rebuilds through one
// helper
// ===========================================================================

func TestAutoRebuildEnabledFor_Issue45(t *testing.T) {
	variable := func(val string, err error) GetVariableFunc {
		return func(scope, slug, key string) (string, error) {
			if scope != "workspace" || slug != "ws" || key != "AUTO_REBUILD_AFTER_SYNC" {
				t.Errorf("unexpected lookup (%q, %q, %q)", scope, slug, key)
			}
			return val, err
		}
	}

	cases := []struct {
		name string
		get  GetVariableFunc
		want bool
	}{
		{"no_lookup", nil, true},
		{"unset", variable("", nil), true},
		{"true", variable("true", nil), true},
		{"false", variable("false", nil), false},
		{"other_value_leaves_it_on", variable("FALSE", nil), true},
		{"lookup_error_leaves_it_on", variable("", fmt.Errorf("store down")), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoRebuildEnabledFor(tc.get, "ws"); got != tc.want {
				t.Errorf("autoRebuildEnabledFor = %v; want %v", got, tc.want)
			}
			// The sync wrapper must agree.
			if got := autoRebuildEnabled(SyncAPIConfig{GetVariable: tc.get}, "ws"); got != tc.want {
				t.Errorf("autoRebuildEnabled = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestEnqueueRebuildJob_Issue45(t *testing.T) {
	getVar := func(_, _, key string) (string, error) {
		if key == "REBUILD_STRATEGY" {
			return StrategyMerge, nil
		}
		return "", nil
	}

	t.Run("job_parameters", func(t *testing.T) {
		q := &recordingEnqueuer{}
		jobID, enqueued, err := enqueueRebuildJob(q, getVar, "ws", "deploy", "user-1")
		if err != nil || !enqueued || jobID != "job-recorded" {
			t.Fatalf("got (%q, %v, %v); want (\"job-recorded\", true, nil)", jobID, enqueued, err)
		}
		if len(q.calls) != 1 {
			t.Fatalf("enqueue calls = %d; want 1", len(q.calls))
		}
		p := q.calls[0]
		if p.Type != "rebuild" || p.Key != "ws" || p.Group != "ws:deploy" || p.SubmittedBy != "user-1" || p.Nonce == "" {
			t.Errorf("params = %+v; want type rebuild, key ws, group ws:deploy, submitter user-1 and a nonce", p)
		}
		var payload RebuildPayload
		if err := json.Unmarshal(p.Payload, &payload); err != nil {
			t.Fatalf("payload is not a RebuildPayload: %v", err)
		}
		if payload.WorkspaceSlug != "ws" || payload.IntegrationBranch != "deploy" ||
			payload.SubmittedBy != "user-1" || payload.Strategy != StrategyMerge {
			t.Errorf("payload = %+v; want slug ws, branch deploy, submitter user-1, strategy merge", payload)
		}

		// Every enqueue uses a fresh nonce.
		if _, _, err := enqueueRebuildJob(q, getVar, "ws", "deploy", "user-1"); err != nil {
			t.Fatalf("second enqueue: %v", err)
		}
		if q.calls[0].Nonce == q.calls[1].Nonce {
			t.Error("two enqueues share a nonce")
		}
	})

	t.Run("enqueued_and_duplicate", func(t *testing.T) {
		q, _ := newTestQueue(t)
		_ = RegisterRebuildJob(q, &RebuildHandler{})

		jobID, enqueued, err := enqueueRebuildJob(q, nil, "dup-ws", "deploy", "user-1")
		if err != nil || !enqueued || jobID == "" {
			t.Fatalf("first enqueue = (%q, %v, %v); want a job", jobID, enqueued, err)
		}
		// The same key is already queued: a duplicate, not an error.
		jobID, enqueued, err = enqueueRebuildJob(q, nil, "dup-ws", "deploy", "user-1")
		if err != nil || enqueued || jobID != "" {
			t.Errorf("second enqueue = (%q, %v, %v); want (\"\", false, nil)", jobID, enqueued, err)
		}
	})

	t.Run("queue_error_is_returned", func(t *testing.T) {
		_, enqueued, err := enqueueRebuildJob(&failingJobQueue{}, nil, "ws", "deploy", "user-1")
		if err == nil || enqueued {
			t.Errorf("got (enqueued=%v, err=%v); want the queue error", enqueued, err)
		}
	})
}

// ===========================================================================
// Test helpers
// ===========================================================================

// newTestRecoveryServiceWithStore creates a RecoveryService with PatchStore and Queue.
func newTestRecoveryServiceWithStore(
	workspaceRoot string,
	getVariable GetVariableFunc,
	resolveAuth ResolveAuthFunc,
	fetch SingleBranchFetchFunc,
	patchStore PatchStore,
	queue RebuildEnqueuer,
) *RecoveryService {
	return &RecoveryService{
		NewGitRunner:  NewGitRunnerFactory(),
		WorkspaceRoot: workspaceRoot,
		GetVariable:   getVariable,
		ResolveAuth:   resolveAuth,
		Fetch:         fetch,
		LockFunc:      func(slug string) (func(), bool) { return func() {}, true },
		PatchStore:    patchStore,
		Queue:         queue,
	}
}

// failingJobQueue is a stub that always returns an error from Enqueue.
type failingJobQueue struct{}

func (f *failingJobQueue) Enqueue(params jobqueue.EnqueueParams) (string, bool, error) {
	return "", false, fmt.Errorf("queue is broken")
}

// drainJobs cancels all queued rebuild jobs for a slug.
func drainJobs(t *testing.T, q *jobqueue.Queue, slug string) {
	t.Helper()
	jobs, _ := q.ListByKey("rebuild", slug)
	for _, j := range jobs {
		if j.Status == "queued" {
			_ = q.CancelJob(j.ID)
		}
	}
}

// countJobs counts rebuild jobs for a slug in the database.
func countJobs(t *testing.T, db *sql.DB, slug string) int {
	t.Helper()
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type = 'rebuild' AND key = ?`, slug).Scan(&count)
	if err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	return count
}
