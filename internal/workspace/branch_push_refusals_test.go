package workspace

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wslock"
)

// seedPushWorkspace inserts ws, active and with its clone ready unless ws says
// otherwise, with a trunk under the workspace root holding main and branches,
// and an empty local bare repository as origin and git_url. It returns the bare
// repository's path.
func (e *branchPushEnv) seedPushWorkspace(t *testing.T, ws *Workspace, branches ...string) string {
	t.Helper()
	bare := newPushTrunk(t, filepath.Join(e.root, ws.Slug, "trunk"), branches...)
	ws.GitURL = bare
	if ws.Status == "" {
		ws.Status = "active"
	}
	if ws.Status == "" {
		ws.Status = "ready"
	}
	e.seedWorkspace(t, ws)
	return bare
}

// trunkTip is the commit branch points at in the trunk of slug.
func (e *branchPushEnv) trunkTip(t *testing.T, slug, branch string) string {
	t.Helper()
	return pushGit(t, filepath.Join(e.root, slug, "trunk"), "rev-parse", "refs/heads/"+branch)
}

// bareRefs lists the refs of the bare repository.
func bareRefs(t *testing.T, bare string) []string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "for-each-ref", "--format=%(refname)").Output()
	if err != nil {
		t.Fatalf("listing the refs of %s: %v", bare, err)
	}
	return strings.Fields(string(out))
}

// pushAnswer is the 200 answer of the branch push.
type pushAnswer struct {
	Branch    string `json:"branch"`
	SHA       string `json:"sha"`
	Remote    string `json:"remote"`
	RemoteURL string `json:"remote_url"`
	Result    string `json:"result"`
}

// pushedOK decodes a 200 answer, failing the test on any other status.
func pushedOK(t *testing.T, rec *httptest.ResponseRecorder) pushAnswer {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200: %s", rec.Code, rec.Body)
	}
	var a pushAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("decoding the answer: %v: %s", err, rec.Body)
	}
	return a
}

// refusedWithType checks a refusal's status, error_type and that its message
// starts with the type and holds every one of parts.
func refusedWithType(t *testing.T, rec *httptest.ResponseRecorder, code int, errorType string, parts ...string) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d; want %d: %s", rec.Code, code, rec.Body)
	}
	e := parseErrorEnvelope(t, rec)
	if e.Error.ErrorType != errorType || !strings.HasPrefix(e.Error.Message, errorType+": ") {
		t.Errorf("error %q (type %q); want type %s and the message to start with %q", e.Error.Message, e.Error.ErrorType, errorType, errorType+": ")
	}
	for _, p := range parts {
		if !strings.Contains(e.Error.Message, p) {
			t.Errorf("message %q does not mention %q", e.Error.Message, p)
		}
	}
}

// seedPatchRow inserts a patch row of slug for branch with status.
func seedPatchRow(t *testing.T, env *branchPushEnv, slug, branch, status string, position int) {
	t.Helper()
	now := apikit.NowUTC()
	if _, err := env.db.Exec(`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		slug+"-"+branch, slug, branch, position, status, now, now); err != nil {
		t.Fatalf("seeding patch %s/%s: %v", slug, branch, err)
	}
}

// TS-38-18: On a carry_patch workspace the integration branch (column value or
// default deploy) is refused with 400 integration_branch.
// Requirement: 38-REQ-4.1
func TestBranchPushIntegrationBranch_TS_38_18(t *testing.T) {
	env := newBranchPushEnv(t)
	bare1 := env.seedPushWorkspace(t, &Workspace{Slug: "cp-default", OwnerID: "user-a", WorkspaceMode: "carry_patch"}, "deploy")
	bare2 := env.seedPushWorkspace(t, &Workspace{Slug: "cp-stage", OwnerID: "user-a", WorkspaceMode: "carry_patch", IntegrationBranch: strPtr("stage")}, "stage")
	holdLock(t, "cp-default")
	holdLock(t, "cp-stage")

	for _, tc := range []struct{ slug, branch, bare string }{{"cp-default", "deploy", bare1}, {"cp-stage", "stage", bare2}} {
		for _, body := range []string{"", `{"force":true}`} {
			rec := env.push(t, tc.slug, tc.branch, body, userAuth("user-a"))
			refusedWithType(t, rec, http.StatusBadRequest, "integration_branch", "hub builds", "REBUILD_PUSH_INTEGRATION_BRANCH")
		}
		if refs := bareRefs(t, tc.bare); len(refs) != 0 {
			t.Errorf("origin of %s received %v", tc.slug, refs)
		}
	}
	env.assertNothingHappened(t)
}

// TS-38-19: A registered patch branch is refused with patch_branch while an
// unregistered or deleted-patch branch pushes normally.
// Requirement: 38-REQ-4.2, 38-REQ-4.3
func TestBranchPushPatchBranch_TS_38_19(t *testing.T) {
	env := newBranchPushEnv(t)
	bare := env.seedPushWorkspace(t, &Workspace{Slug: "cp-patch", OwnerID: "user-a", WorkspaceMode: "carry_patch"}, "p1", "p2", "free")
	seedPatchRow(t, env, "cp-patch", "p1", "active", 0)
	seedPatchRow(t, env, "cp-patch", "p2", "deleted", -1)

	refusedWithType(t, env.push(t, "cp-patch", "p1", "", userAuth("user-a")), http.StatusBadRequest, "patch_branch", "PUSH_PATCHES_TO_ORIGIN")
	if refs := bareRefs(t, bare); len(refs) != 0 {
		t.Fatalf("origin received %v for a registered patch", refs)
	}
	if len(*env.pushes) != 0 || len(env.branchEvents()) != 0 {
		t.Errorf("the refusal pushed (%d) or emitted (%d)", len(*env.pushes), len(env.branchEvents()))
	}
	for _, b := range []string{"p2", "free"} {
		a := pushedOK(t, env.push(t, "cp-patch", b, "", userAuth("user-a")))
		if a.Result != "pushed" || a.Branch != b {
			t.Errorf("%s: answer %+v; want pushed", b, a)
		}
		if got, want := bareRef(t, bare, "refs/heads/"+b), env.trunkTip(t, "cp-patch", b); got != want {
			t.Errorf("origin's %s = %q; want %q", b, got, want)
		}
	}
}

// TS-38-20: force on the workspace's own branch is refused 400
// protected_branch in both modes, while a non-forced push of it is a
// fast-forward push.
// Requirement: 38-REQ-4.4, 38-REQ-4.7
func TestBranchPushProtectedBranchColumn_TS_38_20(t *testing.T) {
	env := newBranchPushEnv(t)
	for _, mode := range []string{"standard", "carry_patch"} {
		slug := "own-" + strings.ReplaceAll(mode, "_", "-")
		bare := env.seedPushWorkspace(t, &Workspace{Slug: slug, OwnerID: "user-a", Branch: strPtr("main"), WorkspaceMode: mode})
		release := holdLock(t, slug)
		events := len(env.branchEvents())
		refusedWithType(t, env.push(t, slug, "main", `{"force":true}`, userAuth("user-a")), http.StatusBadRequest, "protected_branch", `"main"`)
		release()
		if refs := bareRefs(t, bare); len(refs) != 0 || len(*env.pushes) != 0 || len(env.branchEvents()) != events {
			t.Fatalf("%s: the refusal reached origin %v, pushed %d times or emitted %d events", mode, refs, len(*env.pushes), len(env.branchEvents())-events)
		}
		a := pushedOK(t, env.push(t, slug, "main", `{"force":false}`, userAuth("user-a")))
		if a.Result != "pushed" {
			t.Errorf("%s: unforced push of main answered %+v; want pushed", mode, a)
		}
		if got := (*env.pushes)[len(*env.pushes)-1]; got.force || got.refSpecs[0] != "refs/heads/main:refs/heads/main" {
			t.Errorf("%s: the push was %+v; want the fast-forward refspec", mode, got)
		}
		*env.pushes = nil
	}
}

// TS-38-21: With a null branch column, force on the HEAD branch is refused
// under the lock; an unreadable HEAD answers 500; the lock is released.
// Requirement: 38-REQ-4.5, 38-REQ-4.6
func TestBranchPushProtectedBranchHead_TS_38_21(t *testing.T) {
	env := newBranchPushEnv(t)
	bare1 := env.seedPushWorkspace(t, &Workspace{Slug: "head-main", OwnerID: "user-a"}, "x")
	bare2 := env.seedPushWorkspace(t, &Workspace{Slug: "head-bad", OwnerID: "user-a"}, "x")
	if err := os.WriteFile(filepath.Join(env.root, "head-bad", "trunk", ".git", "HEAD"), []byte("ref: refs/heads/missing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	refusedWithType(t, env.push(t, "head-main", "main", `{"force":true}`, userAuth("user-a")), http.StatusBadRequest, "protected_branch", `"main"`)
	rec := env.push(t, "head-bad", "x", `{"force":true}`, userAuth("user-a"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("unreadable HEAD: status %d; want 500: %s", rec.Code, rec.Body)
	}
	for slug, bare := range map[string]string{"head-main": bare1, "head-bad": bare2} {
		assertLockFree(t, slug)
		if refs := bareRefs(t, bare); len(refs) != 0 {
			t.Errorf("origin of %s received %v", slug, refs)
		}
	}
	env.assertNothingHappened(t)
}

// TS-38-22: A database failure while deciding integration, patch or
// protected branch answers 500 without contacting the forge.
// Requirement: 38-REQ-4.8
func TestBranchPushRefusalDatabaseFailure_TS_38_22(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedPushWorkspace(t, &Workspace{Slug: "cp-db", OwnerID: "user-a", WorkspaceMode: "carry_patch"}, "x")
	if _, err := env.db.Exec(`DROP TABLE patches`); err != nil {
		t.Fatal(err)
	}
	rec := env.push(t, "cp-db", "x", "", userAuth("user-a"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500: %s", rec.Code, rec.Body)
	}
	assertLockFree(t, "cp-db")
	env.assertNothingHappened(t)
}

// TS-38-23: A held workspace lock answers 409 workspace_busy immediately with
// no event.
// Requirement: 38-REQ-5.1
func TestBranchPushBusy_TS_38_23(t *testing.T) {
	env := newBranchPushEnv(t)
	bare := env.seedPushWorkspace(t, &Workspace{Slug: "busy", OwnerID: "user-a"}, "feat")
	holdLock(t, "busy")
	start := time.Now()
	rec := env.push(t, "busy", "feat", "", userAuth("user-a"))
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the answer took %v; want no wait for the lock", elapsed)
	}
	assertRefusal(t, rec, http.StatusConflict, "another operation is running on this workspace; retry later", "workspace_busy")
	if refs := bareRefs(t, bare); len(refs) != 0 {
		t.Errorf("origin received %v", refs)
	}
	env.assertNothingHappened(t)
}

// TS-38-24: A rebuild between its fetch and final phases does not block the
// push.
// Requirement: 38-REQ-5.2
func TestBranchPushDuringRebuild_TS_38_24(t *testing.T) {
	env := newBranchPushEnv(t)
	bare := env.seedPushWorkspace(t, &Workspace{Slug: "cp-rebuild", OwnerID: "user-a", WorkspaceMode: "carry_patch"}, "feat")
	end, ok := wslock.BeginRebuild("cp-rebuild")
	if !ok {
		t.Fatal("BeginRebuild refused")
	}
	defer end()

	a := pushedOK(t, env.push(t, "cp-rebuild", "feat", "", userAuth("user-a")))
	if a.Result != "pushed" || bareRef(t, bare, "refs/heads/feat") != a.SHA {
		t.Errorf("answer %+v; origin's feat %q", a, bareRef(t, bare, "refs/heads/feat"))
	}
}

// TS-38-25: A valid name missing from the trunk answers 404 branch_not_found
// and releases the lock.
// Requirement: 38-REQ-5.3
func TestBranchPushMissingBranch_TS_38_25(t *testing.T) {
	env := newBranchPushEnv(t)
	bare := env.seedPushWorkspace(t, &Workspace{Slug: "ghosts", OwnerID: "user-a"}, "feat")
	rec := env.push(t, "ghosts", "ghost", "", userAuth("user-a"))
	assertRefusal(t, rec, http.StatusNotFound, `branch_not_found: branch "ghost" does not exist in the workspace`, "branch_not_found")
	assertLockFree(t, "ghosts")
	if refs := bareRefs(t, bare); len(refs) != 0 {
		t.Errorf("origin received %v", refs)
	}
	env.assertNothingHappened(t)
}
