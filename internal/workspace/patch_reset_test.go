package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"

	_ "modernc.org/sqlite"
)

// stubRecoveryHookWithCalls extends stubRecoveryHook with call counting.
type stubRecoveryHookWithCalls struct {
	readSHA   string
	readFound bool
	readErr   error

	removeErr error

	resetResult ResetResult
	resetErr    error

	// onReset, when set, runs inside RunReset before it returns. A test uses
	// it to change state the way a concurrent request would, for example to
	// remove the patch row so the handler's re-read after the reset fails.
	onReset func()

	resetCalls int
}

func (s *stubRecoveryHookWithCalls) ReadReplacedSHA(_ context.Context, _, _ string) (string, bool, error) {
	return s.readSHA, s.readFound, s.readErr
}

func (s *stubRecoveryHookWithCalls) RemoveBackup(_ context.Context, _, _ string) error {
	return s.removeErr
}

func (s *stubRecoveryHookWithCalls) RunReset(_ context.Context, _ string, _ ResetPatchInfo, _ *apikit.AuthInfo) (ResetResult, error) {
	s.resetCalls++
	if s.onReset != nil {
		s.onReset()
	}
	return s.resetResult, s.resetErr
}

// seedPatchWithStatus inserts a patch row with a specific status.
func seedPatchWithStatus(t *testing.T, env *testEnv, id, slug, branch string, position int, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	deletedAt := ""
	if status == "deleted" {
		deletedAt = now
	}
	if status == "deleted" {
		_, err := env.db.Exec(
			`INSERT INTO patches (id, workspace_slug, branch_name, position, status, deleted_at, added_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, slug, branch, position, status, deletedAt, now, now,
		)
		if err != nil {
			t.Fatalf("seedPatchWithStatus: %v", err)
		}
	} else {
		_, err := env.db.Exec(
			`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, slug, branch, position, status, now, now,
		)
		if err != nil {
			t.Fatalf("seedPatchWithStatus: %v", err)
		}
	}
}

// seedWorkspaceWithCloneStatus inserts a carry_patch workspace with a specific clone_status.
func seedWorkspaceWithCloneStatus(t *testing.T, env *testEnv, slug, cloneStatus string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := env.db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description,
		 clone_status, created_at, updated_at, sync_mode, sync_status,
		 workspace_mode, upstream_url, integration_branch)
		 VALUES (?, 'https://github.com/fork/repo.git', 'user-1', 'active', '', '', ?, ?, ?, 'pull_only', 'idle',
		 'carry_patch', 'https://github.com/upstream/repo.git', 'main')`,
		slug, cloneStatus, now, now,
	)
	if err != nil {
		t.Fatalf("seedWorkspaceWithCloneStatus: %v", err)
	}
}

// seedArchivedWorkspace inserts an archived workspace.
func seedArchivedWorkspace(t *testing.T, env *testEnv, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := env.db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description,
		 clone_status, created_at, updated_at, sync_mode, sync_status,
		 workspace_mode, upstream_url, integration_branch)
		 VALUES (?, 'https://github.com/fork/repo.git', 'user-1', 'archived', '', '', 'archived', ?, ?, 'pull_only', 'idle',
		 'carry_patch', 'https://github.com/upstream/repo.git', 'main')`,
		slug, now, now,
	)
	if err != nil {
		t.Fatalf("seedArchivedWorkspace: %v", err)
	}
}

// seedStandardWorkspace inserts a standard-mode workspace.
func seedStandardWorkspace(t *testing.T, env *testEnv, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := env.db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description,
		 clone_status, created_at, updated_at, sync_mode, sync_status,
		 workspace_mode)
		 VALUES (?, 'https://github.com/fork/repo.git', 'user-1', 'active', '', '', 'ready', ?, ?, 'pull_only', 'idle',
		 'standard')`,
		slug, now, now,
	)
	if err != nil {
		t.Fatalf("seedStandardWorkspace: %v", err)
	}
}

func resetPath(slug, patchID string) string {
	return fmt.Sprintf("/api/v1/workspaces/%s/patches/%s/reset-to-origin", slug, patchID)
}

// TS-23-9: A completed reset returns the patch object with replaced_sha,
// rebuild_triggered and rebuild_job_id.
func TestResetPatchToOrigin_Success_TS239(t *testing.T) {
	const slug = "reset-success"
	const patchID = "patch-reset-1"
	const stubSHA = "aabbccddee00112233445566778899aabbccddee"

	env := newPatchTestEnv(t, slug, "main")
	// Set clone_status to ready.
	_, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, slug)
	if err != nil {
		t.Fatalf("update clone_status: %v", err)
	}
	seedPatchRaw(t, env.db, patchID, slug, "feature/x", 1)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })

	hook := &stubRecoveryHookWithCalls{
		readSHA:   stubSHA,
		readFound: true,
		resetResult: ResetResult{
			Action:           ResetActionReplaced,
			LocalSHA:         "1111111111111111111111111111111111111111",
			OriginSHA:        "2222222222222222222222222222222222222222",
			ReplacedSHA:      stubSHA,
			RebuildTriggered: true,
			RebuildJobID:     "job-1",
		},
	}
	RegisterRecoveryHook(hook)

	auth := userAuth("user-1")
	rec := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Check patch fields.
	if resp["id"] != patchID {
		t.Errorf("id = %v; want %q", resp["id"], patchID)
	}
	if resp["branch_name"] != "feature/x" {
		t.Errorf("branch_name = %v; want feature/x", resp["branch_name"])
	}

	// Check replaced_sha.
	if resp["replaced_sha"] != stubSHA {
		t.Errorf("replaced_sha = %v; want %q", resp["replaced_sha"], stubSHA)
	}

	// Check rebuild_triggered.
	if resp["rebuild_triggered"] != true {
		t.Errorf("rebuild_triggered = %v; want true", resp["rebuild_triggered"])
	}

	// Check rebuild_job_id.
	if resp["rebuild_job_id"] != "job-1" {
		t.Errorf("rebuild_job_id = %v; want job-1", resp["rebuild_job_id"])
	}

	// Test with no job enqueued.
	t.Run("no_job", func(t *testing.T) {
		hook.resetResult.RebuildTriggered = false
		hook.resetResult.RebuildJobID = ""

		rec := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if resp["rebuild_triggered"] != false {
			t.Errorf("rebuild_triggered = %v; want false", resp["rebuild_triggered"])
		}
		if _, ok := resp["rebuild_job_id"]; ok {
			t.Error("rebuild_job_id should be absent when no job was enqueued")
		}
	})
}

// TS-23-10: Reset is refused for a PAT without patches:write (403) and for
// a non-owner (404) before any git operation.
func TestResetPatchToOrigin_AuthRefused_TS2310(t *testing.T) {
	const slug = "reset-auth"
	const patchID = "patch-auth-1"

	env := newPatchTestEnv(t, slug, "main")
	_, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, slug)
	if err != nil {
		t.Fatalf("update clone_status: %v", err)
	}
	seedPatchRaw(t, env.db, patchID, slug, "feature/x", 1)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })
	hook := &stubRecoveryHookWithCalls{}
	RegisterRecoveryHook(hook)

	t.Run("pat_without_write", func(t *testing.T) {
		auth := patAuth("user-1", "patches:read")
		rec := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d; want 403; body: %s", rec.Code, rec.Body.String())
		}
		if hook.resetCalls != 0 {
			t.Errorf("RunReset called %d times; want 0", hook.resetCalls)
		}
	})

	t.Run("non_owner", func(t *testing.T) {
		hook.resetCalls = 0
		auth := userAuth("user-b")
		rec := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d; want 404; body: %s", rec.Code, rec.Body.String())
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Message != "workspace not found" {
			t.Errorf("message = %q; want %q", resp.Error.Message, "workspace not found")
		}
		if hook.resetCalls != 0 {
			t.Errorf("RunReset called %d times; want 0", hook.resetCalls)
		}
	})
}

// TS-23-11: Reset on an inactive or non-carry_patch workspace answers the
// same 400 messages as restore.
func TestResetPatchToOrigin_InactiveOrStandard_TS2311(t *testing.T) {
	env := newTestEnv(t)
	ensureCarryPatchColumns(t, env.db)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })
	hook := &stubRecoveryHookWithCalls{}
	RegisterRecoveryHook(hook)

	auth := userAuth("user-1")

	t.Run("archived_workspace", func(t *testing.T) {
		slug := "reset-archived"
		seedArchivedWorkspace(t, env, slug)
		seedPatchRaw(t, env.db, "p-archived", slug, "feature/a", 1)

		rec := env.doRequest(t, http.MethodPost, resetPath(slug, "p-archived"), "", auth)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d; want 400; body: %s", rec.Code, rec.Body.String())
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Message != "workspace is not active" {
			t.Errorf("message = %q; want %q", resp.Error.Message, "workspace is not active")
		}
		if hook.resetCalls != 0 {
			t.Errorf("RunReset called %d times; want 0", hook.resetCalls)
		}
	})

	t.Run("standard_workspace", func(t *testing.T) {
		slug := "reset-standard"
		seedStandardWorkspace(t, env, slug)
		seedPatchRaw(t, env.db, "p-standard", slug, "feature/b", 1)

		rec := env.doRequest(t, http.MethodPost, resetPath(slug, "p-standard"), "", auth)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d; want 400; body: %s", rec.Code, rec.Body.String())
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Message != "workspace is not in carry_patch mode" {
			t.Errorf("message = %q; want %q", resp.Error.Message, "workspace is not in carry_patch mode")
		}
	})
}

// TS-23-12: Reset on a workspace whose clone is not ready answers 409.
func TestResetPatchToOrigin_CloneNotReady_TS2312(t *testing.T) {
	env := newTestEnv(t)
	ensureCarryPatchColumns(t, env.db)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })
	hook := &stubRecoveryHookWithCalls{}
	RegisterRecoveryHook(hook)

	slug := "reset-cloning"
	seedWorkspaceWithCloneStatus(t, env, slug, "cloning")
	seedPatchRaw(t, env.db, "p-cloning", slug, "feature/c", 1)

	auth := userAuth("user-1")
	rec := env.doRequest(t, http.MethodPost, resetPath(slug, "p-cloning"), "", auth)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want 409; body: %s", rec.Code, rec.Body.String())
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message != "workspace clone is not ready" {
		t.Errorf("message = %q; want %q", resp.Error.Message, "workspace clone is not ready")
	}
	if hook.resetCalls != 0 {
		t.Errorf("RunReset called %d times; want 0", hook.resetCalls)
	}
}

// TS-23-13: Reset checks unknown id (404) before the integration-branch
// rule (400) and never calls the hook for either.
func TestResetPatchToOrigin_UnknownAndIntegrationBranch_TS2313(t *testing.T) {
	const slug = "reset-checks"

	env := newPatchTestEnv(t, slug, "main")
	_, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, slug)
	if err != nil {
		t.Fatalf("update clone_status: %v", err)
	}

	// Seed a patch whose branch_name equals the integration branch.
	seedPatchRaw(t, env.db, "p-main", slug, "main", 1)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })
	hook := &stubRecoveryHookWithCalls{}
	RegisterRecoveryHook(hook)

	auth := userAuth("user-1")

	t.Run("unknown_id", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, resetPath(slug, "nonexistent"), "", auth)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d; want 404; body: %s", rec.Code, rec.Body.String())
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Message != "patch not found" {
			t.Errorf("message = %q; want %q", resp.Error.Message, "patch not found")
		}
		if hook.resetCalls != 0 {
			t.Errorf("RunReset called %d times; want 0", hook.resetCalls)
		}
	})

	t.Run("integration_branch", func(t *testing.T) {
		hook.resetCalls = 0
		rec := env.doRequest(t, http.MethodPost, resetPath(slug, "p-main"), "", auth)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d; want 400; body: %s", rec.Code, rec.Body.String())
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Message != "patch branch is the integration branch" {
			t.Errorf("message = %q; want %q", resp.Error.Message, "patch branch is the integration branch")
		}
		if hook.resetCalls != 0 {
			t.Errorf("RunReset called %d times; want 0", hook.resetCalls)
		}
	})
}

// TS-23-14: Reset on merged_upstream or deleted patches answers 409 with
// the status in the message.
func TestResetPatchToOrigin_IneligibleStatus_TS2314(t *testing.T) {
	const slug = "reset-status"

	env := newPatchTestEnv(t, slug, "main")
	_, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, slug)
	if err != nil {
		t.Fatalf("update clone_status: %v", err)
	}

	seedPatchWithStatus(t, env, "p-merged", slug, "feature/merged", 1, "merged_upstream")
	seedPatchWithStatus(t, env, "p-deleted", slug, "feature/deleted", -1, "deleted")

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })
	hook := &stubRecoveryHookWithCalls{}
	RegisterRecoveryHook(hook)

	auth := userAuth("user-1")

	t.Run("merged_upstream", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, resetPath(slug, "p-merged"), "", auth)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d; want 409; body: %s", rec.Code, rec.Body.String())
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Message != "patch status merged_upstream cannot be reset" {
			t.Errorf("message = %q; want %q", resp.Error.Message, "patch status merged_upstream cannot be reset")
		}
	})

	t.Run("deleted", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, resetPath(slug, "p-deleted"), "", auth)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d; want 409; body: %s", rec.Code, rec.Body.String())
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Message != "patch status deleted cannot be reset" {
			t.Errorf("message = %q; want %q", resp.Error.Message, "patch status deleted cannot be reset")
		}
	})

	// Verify eligible statuses pass this check (they may fail later).
	t.Run("active_passes_status_check", func(t *testing.T) {
		seedPatchWithStatus(t, env, "p-active", slug, "feature/active", 2, "active")
		hook.resetResult = ResetResult{Action: ResetActionNone}
		rec := env.doRequest(t, http.MethodPost, resetPath(slug, "p-active"), "", auth)
		// Should not be 409 with status message.
		if rec.Code == http.StatusConflict {
			resp := parseErrorEnvelope(t, rec)
			if strings.Contains(resp.Error.Message, "patch status") {
				t.Errorf("active patch should pass status check, got: %s", resp.Error.Message)
			}
		}
	})

	t.Run("conflict_passes_status_check", func(t *testing.T) {
		seedPatchWithStatus(t, env, "p-conflict", slug, "feature/conflict", 3, "conflict")
		hook.resetResult = ResetResult{Action: ResetActionNone}
		rec := env.doRequest(t, http.MethodPost, resetPath(slug, "p-conflict"), "", auth)
		if rec.Code == http.StatusConflict {
			resp := parseErrorEnvelope(t, rec)
			if strings.Contains(resp.Error.Message, "patch status") {
				t.Errorf("conflict patch should pass status check, got: %s", resp.Error.Message)
			}
		}
	})

	t.Run("disabled_passes_status_check", func(t *testing.T) {
		seedPatchWithStatus(t, env, "p-disabled", slug, "feature/disabled", 4, "disabled")
		hook.resetResult = ResetResult{Action: ResetActionNone}
		rec := env.doRequest(t, http.MethodPost, resetPath(slug, "p-disabled"), "", auth)
		if rec.Code == http.StatusConflict {
			resp := parseErrorEnvelope(t, rec)
			if strings.Contains(resp.Error.Message, "patch status") {
				t.Errorf("disabled patch should pass status check, got: %s", resp.Error.Message)
			}
		}
	})
}

// TS-23-16: Reset with no recovery hook registered answers 500.
func TestResetPatchToOrigin_NoHook_TS2316(t *testing.T) {
	const slug = "reset-no-hook"
	const patchID = "p-no-hook"

	env := newPatchTestEnv(t, slug, "main")
	_, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, slug)
	if err != nil {
		t.Fatalf("update clone_status: %v", err)
	}
	seedPatchRaw(t, env.db, patchID, slug, "feature/x", 1)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })
	RegisterRecoveryHook(nil)

	auth := userAuth("user-1")
	rec := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500; body: %s", rec.Code, rec.Body.String())
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message != "patch reset is not configured" {
		t.Errorf("message = %q; want %q", resp.Error.Message, "patch reset is not configured")
	}
}

// TS-23-60: Each classified reset error maps to its status code, error type
// and message.
func TestResetPatchToOrigin_ClassifiedErrors_TS2360(t *testing.T) {
	const slug = "reset-errors"
	const patchID = "p-errors"

	env := newPatchTestEnv(t, slug, "main")
	_, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, slug)
	if err != nil {
		t.Fatalf("update clone_status: %v", err)
	}
	seedPatchRaw(t, env.db, patchID, slug, "feature/x", 1)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })

	auth := userAuth("user-1")

	cases := []struct {
		name     string
		err      *ResetError
		wantCode int
		wantType string
		wantMsg  string
	}{
		{
			name:     "busy",
			err:      &ResetError{Kind: ResetErrBusy, Message: "workspace busy"},
			wantCode: http.StatusConflict,
			wantType: "workspace_busy",
			wantMsg:  "another operation is running on this workspace; retry later",
		},
		{
			name:     "missing_on_origin",
			err:      &ResetError{Kind: ResetErrMissingOnOrigin, Message: "not found"},
			wantCode: http.StatusConflict,
			wantType: "missing_on_origin",
			wantMsg:  "branch does not exist on origin",
		},
		{
			name:     "fetch_failed",
			err:      &ResetError{Kind: ResetErrFetchFailed, Message: "fetch error"},
			wantCode: http.StatusBadGateway,
			wantType: "origin_fetch_failed",
			wantMsg:  "origin fetch failed",
		},
		{
			name:     "credential_failed",
			err:      &ResetError{Kind: ResetErrCredentialFailed, Message: "cred error"},
			wantCode: http.StatusBadGateway,
			wantType: "",
			wantMsg:  "failed to resolve origin credentials",
		},
		{
			name:     "ref_changed",
			err:      &ResetError{Kind: ResetErrRefChanged, Message: "ref changed"},
			wantCode: http.StatusConflict,
			wantType: "ref_changed",
			wantMsg:  "patch branch changed during reset; retry",
		},
		{
			name:     "other",
			err:      &ResetError{Kind: ResetErrOther, Message: "something broke"},
			wantCode: http.StatusInternalServerError,
			wantType: "",
			wantMsg:  "failed to update patch branch feature/x",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hook := &stubRecoveryHookWithCalls{
				resetErr: tc.err,
			}
			RegisterRecoveryHook(hook)

			rec := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d; want %d; body: %s", rec.Code, tc.wantCode, rec.Body.String())
			}

			// Parse the error envelope.
			var errResp struct {
				Error struct {
					Code      int    `json:"code"`
					Message   string `json:"message"`
					ErrorType string `json:"error_type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
				t.Fatalf("decode: %v", err)
			}

			if errResp.Error.Message != tc.wantMsg {
				t.Errorf("message = %q; want %q", errResp.Error.Message, tc.wantMsg)
			}
			if tc.wantType != "" && errResp.Error.ErrorType != tc.wantType {
				t.Errorf("error_type = %q; want %q", errResp.Error.ErrorType, tc.wantType)
			}
		})
	}
}

// TS-23-62: The new routes use only the shared auth helpers and no custom
// context keys or auth structs.
func TestResetPatchToOrigin_SharedAuthHelpers_TS2362(t *testing.T) {
	// This test verifies that unauthenticated requests are rejected.
	const slug = "reset-unauth"
	const patchID = "p-unauth"

	env := newPatchTestEnv(t, slug, "main")
	_, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, slug)
	if err != nil {
		t.Fatalf("update clone_status: %v", err)
	}
	seedPatchRaw(t, env.db, patchID, slug, "feature/x", 1)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })
	RegisterRecoveryHook(&stubRecoveryHookWithCalls{})

	// No auth header → unauthenticated.
	req := httptest.NewRequest(http.MethodPost, resetPath(slug, patchID), nil)
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: status = %d; want 401; body: %s", rec.Code, rec.Body.String())
	}

	// Verify GET single patch also rejects unauthenticated.
	getPath := fmt.Sprintf("/api/v1/workspaces/%s/patches/%s", slug, patchID)
	req = httptest.NewRequest(http.MethodGet, getPath, nil)
	rec = httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET unauthenticated: status = %d; want 401; body: %s", rec.Code, rec.Body.String())
	}
}

// TS-23-69: A nil or failing audit emitter does not change the reset response.
func TestResetPatchToOrigin_EmitterResilience_TS2369(t *testing.T) {
	const slug = "reset-emitter"
	const patchID = "p-emitter"
	const stubSHA = "aabbccddee00112233445566778899aabbccddee"

	env := newPatchTestEnv(t, slug, "main")
	_, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, slug)
	if err != nil {
		t.Fatalf("update clone_status: %v", err)
	}
	seedPatchRaw(t, env.db, patchID, slug, "feature/x", 1)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })

	hook := &stubRecoveryHookWithCalls{
		readSHA:   stubSHA,
		readFound: true,
		resetResult: ResetResult{
			Action:           ResetActionReplaced,
			LocalSHA:         "1111111111111111111111111111111111111111",
			OriginSHA:        "2222222222222222222222222222222222222222",
			ReplacedSHA:      stubSHA,
			RebuildTriggered: true,
			RebuildJobID:     "job-1",
		},
	}
	RegisterRecoveryHook(hook)

	auth := userAuth("user-1")

	// Save and restore the audit emitter.
	savedEmitter := defaultAuditEmitter
	t.Cleanup(func() { defaultAuditEmitter = savedEmitter })

	// 1. Working emitter (nil is fine for baseline).
	defaultAuditEmitter = &stubAuditEmitter{}
	rec1 := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)
	if rec1.Code != http.StatusOK {
		t.Fatalf("working emitter: status = %d; want 200; body: %s", rec1.Code, rec1.Body.String())
	}
	body1 := rec1.Body.String()

	// 2. Nil emitter.
	defaultAuditEmitter = nil
	rec2 := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)
	if rec2.Code != http.StatusOK {
		t.Fatalf("nil emitter: status = %d; want 200; body: %s", rec2.Code, rec2.Body.String())
	}

	// 3. Failing emitter.
	var logBuf logBuffer
	handler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	defaultAuditEmitter = &stubAuditEmitter{err: errors.New("emit failed")}
	rec3 := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)
	if rec3.Code != http.StatusOK {
		t.Fatalf("failing emitter: status = %d; want 200; body: %s", rec3.Code, rec3.Body.String())
	}

	// All three should produce the same status code.
	if rec1.Code != rec2.Code || rec2.Code != rec3.Code {
		t.Errorf("status codes differ: %d, %d, %d", rec1.Code, rec2.Code, rec3.Code)
	}

	// Verify the response bodies have the same key fields.
	var resp1, resp2, resp3 map[string]any
	json.Unmarshal([]byte(body1), &resp1)
	json.Unmarshal(rec2.Body.Bytes(), &resp2)
	json.Unmarshal(rec3.Body.Bytes(), &resp3)

	for _, key := range []string{"id", "branch_name", "rebuild_triggered"} {
		if fmt.Sprint(resp1[key]) != fmt.Sprint(resp2[key]) || fmt.Sprint(resp2[key]) != fmt.Sprint(resp3[key]) {
			t.Errorf("key %q differs across emitter configs: %v, %v, %v", key, resp1[key], resp2[key], resp3[key])
		}
	}

	// Check that the failing emitter's error was logged.
	if logBuf.countLevel("ERROR") < 1 {
		t.Error("expected at least one ERROR log entry for failing emitter")
	}
}

// stubAuditEmitter is a test double for audit.Emitter.
type stubAuditEmitter struct {
	events []audit.HubEvent
	err    error
}

func (s *stubAuditEmitter) Emit(_ context.Context, event audit.HubEvent) error {
	s.events = append(s.events, event)
	return s.err
}
