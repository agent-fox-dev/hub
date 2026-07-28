package workspace

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"testing"
)

// ========================================================================
// Spec 05 Task 3.1: Archive lifecycle git operations
// (TS-05-18, TS-05-19, TS-05-20, TS-05-21, TS-05-22)
// ========================================================================

// hexSHAPattern matches a valid 40-character lowercase hex SHA string.
var hexSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// TS-05-18: Archiving a workspace with clone_status='ready' records head_sha,
// deletes the workspace directory, and returns HTTP 200 with status='archived'
// and clone_status='archived'. Upstream push is deferred.
// Requirement: 05-REQ-6.1
func TestArchive_Spec05_ReadyRecordAndArchive(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	// Set workspace root for the handler to locate/delete workspace dirs.
	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = oldRoot }()

	// Seed workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:    "ready-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "active",
	})

	// Set clone_status to 'ready' (requires schema columns from group 5).
	if err := updateCloneStatus(env.db, "ready-ws", "ready", nil, nil); err != nil {
		t.Fatalf("updateCloneStatus('ready'): %v", err)
	}

	// Create workspace directory with trunk to simulate a cloned repo.
	trunkDir := filepath.Join(wsRoot, "ready-ws", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("create trunk dir: %v", err)
	}

	// Mock HEAD SHA reader: return a known SHA.
	fakeSHA := "abcdef1234567890abcdef1234567890abcdef12"
	oldHead := archiveHeadFn
	archiveHeadFn = func(repoPath string) (string, error) {
		return fakeSHA, nil
	}
	defer func() { archiveHeadFn = oldHead }()

	// Archive the workspace.
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ready-ws/archive", "",
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

	// Assert status = 'archived'.
	if ws.Status != "archived" {
		t.Errorf("status = %q; want %q", ws.Status, "archived")
	}

	// Assert clone_status = 'archived'.
	if ws.CloneStatus == nil {
		t.Fatal("clone_status is nil; want 'archived'")
	}
	if *ws.CloneStatus != "archived" {
		t.Errorf("clone_status = %q; want %q", *ws.CloneStatus, "archived")
	}

	// Assert head_sha is a valid 40-char hex string.
	if ws.HeadSHA == nil {
		t.Fatal("head_sha is nil; want non-null 40-char hex string")
	}
	if !hexSHAPattern.MatchString(*ws.HeadSHA) {
		t.Errorf("head_sha = %q; want valid 40-char hex string", *ws.HeadSHA)
	}

	// Assert workspace directory was deleted.
	wsDir := filepath.Join(wsRoot, "ready-ws")
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("workspace directory %q should be deleted after archive", wsDir)
	}

	// Verify DB state: clone_status='archived', head_sha recorded.
	cloneStatus, headSHA, _, err := getCloneFields(env.db, "ready-ws")
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "archived" {
		t.Errorf("DB clone_status = %q; want %q", cloneStatus, "archived")
	}
	if headSHA == nil || *headSHA != fakeSHA {
		t.Errorf("DB head_sha = %v; want %q", headSHA, fakeSHA)
	}
}

// TS-05-19: Archiving a workspace with clone_status='pending' or 'failed'
// cleans up any partial directory, sets clone_status='archived' and
// status='archived' without attempting a git push, and returns HTTP 200.
// Requirement: 05-REQ-6.2
func TestArchive_Spec05_PendingOrFailedNoGitPush(t *testing.T) {
	for _, cloneState := range []string{"pending", "failed"} {
		t.Run("clone_status="+cloneState, func(t *testing.T) {
			env := newTestEnv(t)
			wsRoot := t.TempDir()

			oldRoot := defaultWorkspaceRoot
			defaultWorkspaceRoot = wsRoot
			defer func() { defaultWorkspaceRoot = oldRoot }()

			slug := cloneState + "-ws"
			env.seedWorkspace(t, &Workspace{
				Slug:    slug,
				GitURL:  "https://github.com/org/repo",
				OwnerID: "user-1",
				Status:  "active",
			})

			// Set clone_status to the target state.
			if err := updateCloneStatus(env.db, slug, cloneState, nil, nil); err != nil {
				t.Fatalf("updateCloneStatus(%q): %v", cloneState, err)
			}

			// Create a partial workspace directory (simulating partial clone).
			partialDir := filepath.Join(wsRoot, slug)
			if err := os.MkdirAll(partialDir, 0o755); err != nil {
				t.Fatalf("create partial dir: %v", err)
			}

			// Mock archive push: track calls to verify it is NOT called.
			var pushCalled int32
			oldPush := archiveOpenAndPushFn
			archiveOpenAndPushFn = func(_, _ string) error {
				atomic.AddInt32(&pushCalled, 1)
				return nil
			}
			defer func() { archiveOpenAndPushFn = oldPush }()

			// Archive the workspace.
			rec := env.doRequest(t, http.MethodPost,
				"/api/v1/workspaces/"+slug+"/archive", "",
				userAuth("user-1"))

			// Assert HTTP 200.
			if rec.Code != http.StatusOK {
				t.Fatalf("HTTP status = %d; want %d\nbody: %s",
					rec.Code, http.StatusOK, rec.Body.String())
			}

			// Parse response.
			var ws spec05WorkspaceJSON
			if err := json.NewDecoder(rec.Body).Decode(&ws); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			// Assert status = 'archived'.
			if ws.Status != "archived" {
				t.Errorf("status = %q; want %q", ws.Status, "archived")
			}

			// Assert clone_status = 'archived'.
			if ws.CloneStatus == nil {
				t.Fatal("clone_status is nil; want 'archived'")
			}
			if *ws.CloneStatus != "archived" {
				t.Errorf("clone_status = %q; want %q", *ws.CloneStatus, "archived")
			}

			// Assert git push was NOT called.
			if atomic.LoadInt32(&pushCalled) != 0 {
				t.Error("git push was called; should not push for " + cloneState + " workspace")
			}

			// Assert partial directory was cleaned up.
			if _, err := os.Stat(partialDir); !os.IsNotExist(err) {
				t.Errorf("partial directory %q should be removed", partialDir)
			}
		})
	}
}

// TS-05-20: Archiving a workspace with clone_status='cloning' returns
// HTTP 409 with message 'clone in progress; try again after it completes'.
// Requirement: 05-REQ-6.3
func TestArchive_Spec05_CloningReturns409(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "cloning-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "active",
	})

	// Set clone_status to 'cloning'.
	if err := updateCloneStatus(env.db, "cloning-ws", "cloning", nil, nil); err != nil {
		t.Fatalf("updateCloneStatus('cloning'): %v", err)
	}

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cloning-ws/archive", "",
		userAuth("user-1"))

	// Assert HTTP 409 Conflict.
	if rec.Code != http.StatusConflict {
		t.Fatalf("HTTP status = %d; want %d\nbody: %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	// Parse error response.
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusConflict {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusConflict)
	}

	// Assert the error message matches the spec.
	expectedMsg := "clone in progress; try again after it completes"
	if resp.Error.Message != expectedMsg {
		t.Errorf("error.message = %q; want %q", resp.Error.Message, expectedMsg)
	}

	// Verify workspace status and clone_status remain unchanged.
	cloneStatus, _, _, err := getCloneFields(env.db, "cloning-ws")
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "cloning" {
		t.Errorf("clone_status = %q; want %q (unchanged)", cloneStatus, "cloning")
	}

	var status string
	if err := env.db.QueryRow("SELECT status FROM workspaces WHERE slug = ?", "cloning-ws").Scan(&status); err != nil {
		t.Fatalf("query workspace status: %v", err)
	}
	if status != "active" {
		t.Errorf("workspace status = %q; want %q (unchanged)", status, "active")
	}
}

