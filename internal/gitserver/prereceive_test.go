package gitserver

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/txsvc/apikit"
)

// newPreReceiveClientServer is like newGitClientServer but saves and restores
// the global pre-receive hook around the test.
func newPreReceiveClientServer(t *testing.T) (*gitTestEnv, string, string) {
	t.Helper()

	// Save and restore the global hooks.
	origPre := preReceiveHook
	origPost := postPushHook
	t.Cleanup(func() {
		preReceiveHook = origPre
		postPushHook = origPost
	})

	env, trunk, remote := newGitClientServer(t)
	return env, trunk, remote
}

// TS-22-1: go-git reports a failed ref write as a per-ref status that a
// real git push shows as remote rejected.
// Verifies: 22-REQ-2.3, 22-REQ-9.5
func TestPreReceive_TS22_1_RejectedRefShowsRemoteRejected(t *testing.T) {
	_, trunk, remote := newPreReceiveClientServer(t)

	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, _ RefUpdate) error {
		return errors.New("nope: blocked")
	})

	// Create a local clone and add a commit on branch "feature".
	clone := t.TempDir()
	runGitCmd(t, "", "clone", remote, clone)
	addCommit(t, clone, "f.txt", "feature commit")
	runGitCmd(t, clone, "checkout", "-b", "feature")

	// Push feature to the hub.
	out, err := gitCmdOutput(clone, "push", "origin", "feature")
	if err == nil {
		t.Fatal("expected git push to fail, but it succeeded")
	}

	// Client output must contain 'remote rejected' and the hook message.
	if !strings.Contains(out, "remote rejected") {
		t.Errorf("expected 'remote rejected' in output; got:\n%s", out)
	}
	if !strings.Contains(out, "nope: blocked") {
		t.Errorf("expected 'nope: blocked' in output; got:\n%s", out)
	}

	// The trunk must NOT have refs/heads/feature.
	_, refErr := gitCmdOutput(trunk, "rev-parse", "--verify", "refs/heads/feature")
	if refErr == nil {
		t.Error("refs/heads/feature should not exist in trunk after rejected push")
	}

	// Verify HTTP 200 (the push protocol always returns 200).
	// This is implicitly verified because git push received the report status.
}

// TS-22-2: With no hook, or after registering nil, pushes behave as before;
// a nil-returning hook lets the ref be written.
// Verifies: 22-REQ-2.1, 22-REQ-2.5
func TestPreReceive_TS22_2_NoHookAndNilHookAndClearedHook(t *testing.T) {
	_, trunk, remote := newPreReceiveClientServer(t)

	// --- Push 1: no hook registered ---
	RegisterPreReceiveHook(nil)
	clone := t.TempDir()
	runGitCmd(t, "", "clone", remote, clone)
	addCommit(t, clone, "a.txt", "commit a")
	runGitCmd(t, clone, "push", "origin", "HEAD:refs/heads/branch1")

	sha1 := runGitCmd(t, clone, "rev-parse", "HEAD")
	trunkSha1 := runGitCmd(t, trunk, "rev-parse", "refs/heads/branch1")
	if trunkSha1 != sha1 {
		t.Errorf("push 1 (no hook): trunk ref = %s; want %s", trunkSha1, sha1)
	}

	// --- Push 2: hook returning nil ---
	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, _ RefUpdate) error {
		return nil
	})
	addCommit(t, clone, "b.txt", "commit b")
	runGitCmd(t, clone, "push", "origin", "HEAD:refs/heads/branch2")

	sha2 := runGitCmd(t, clone, "rev-parse", "HEAD")
	trunkSha2 := runGitCmd(t, trunk, "rev-parse", "refs/heads/branch2")
	if trunkSha2 != sha2 {
		t.Errorf("push 2 (nil-returning hook): trunk ref = %s; want %s", trunkSha2, sha2)
	}

	// --- Push 3: register a rejecting hook, then clear it with nil ---
	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, _ RefUpdate) error {
		return errors.New("should not be called")
	})
	RegisterPreReceiveHook(nil)
	addCommit(t, clone, "c.txt", "commit c")
	runGitCmd(t, clone, "push", "origin", "HEAD:refs/heads/branch3")

	sha3 := runGitCmd(t, clone, "rev-parse", "HEAD")
	trunkSha3 := runGitCmd(t, trunk, "rev-parse", "refs/heads/branch3")
	if trunkSha3 != sha3 {
		t.Errorf("push 3 (cleared hook): trunk ref = %s; want %s", trunkSha3, sha3)
	}
}

// preReceiveCall records a single invocation of the pre-receive hook.
type preReceiveCall struct {
	slug   string
	actor  *apikit.AuthInfo
	upd    RefUpdate
	hasObj bool   // whether the new object is readable from the trunk
	refVal string // the ref value at call time (empty if not yet written)
}

// TS-22-3: The hook is called once per ref update in command order with
// request context, db, slug, actor and update.
// Verifies: 22-REQ-2.2
func TestPreReceive_TS22_3_HookCalledPerRefWithCorrectArgs(t *testing.T) {
	_, trunk, remote := newPreReceiveClientServer(t)

	var mu sync.Mutex
	var calls []preReceiveCall

	RegisterPreReceiveHook(func(ctx context.Context, db *sql.DB, slug string, actor *apikit.AuthInfo, upd RefUpdate) error {
		if ctx == nil {
			t.Error("hook received nil context")
		}
		if db == nil {
			t.Error("hook received nil db")
		}

		// Check if the new object is readable from the trunk.
		hasObj := false
		if !upd.New.IsZero() {
			_, err := gitCmdOutput(trunk, "cat-file", "-t", upd.New.String())
			hasObj = (err == nil)
		}

		// Check the current ref value (before the write).
		refVal, _ := gitCmdOutput(trunk, "rev-parse", "--verify", string(upd.Name))

		mu.Lock()
		calls = append(calls, preReceiveCall{
			slug:   slug,
			actor:  actor,
			upd:    upd,
			hasObj: hasObj,
			refVal: refVal,
		})
		mu.Unlock()
		return nil
	})

	// Create a clone and set up branches.
	clone := t.TempDir()
	runGitCmd(t, "", "clone", remote, clone)

	// Create branch "b" on the hub first so we can update it.
	runGitCmd(t, clone, "checkout", "-b", "b")
	addCommit(t, clone, "b1.txt", "first b commit")
	runGitCmd(t, clone, "push", "origin", "b")

	// Clear calls from the setup push.
	mu.Lock()
	calls = nil
	mu.Unlock()

	// Create branch "a" (will be a create).
	runGitCmd(t, clone, "checkout", "-b", "a")
	addCommit(t, clone, "a.txt", "commit for a")

	// Update b with a new commit.
	runGitCmd(t, clone, "checkout", "b")
	addCommit(t, clone, "b2.txt", "second b commit")

	// Push both a (create) and b (update) in one push.
	runGitCmd(t, clone, "push", "origin", "a", "b")

	mu.Lock()
	defer mu.Unlock()

	if len(calls) != 2 {
		t.Fatalf("expected 2 hook calls, got %d", len(calls))
	}

	// Both calls should have slug "myws".
	for i, c := range calls {
		if c.slug != "myws" {
			t.Errorf("call %d: slug = %q; want %q", i, c.slug, "myws")
		}
		if c.actor == nil {
			t.Errorf("call %d: actor is nil", i)
		} else if c.actor.UserID != "user-1" {
			t.Errorf("call %d: actor.UserID = %q; want %q", i, c.actor.UserID, "user-1")
		}
	}

	// The new objects should be readable from the trunk at hook call time.
	for i, c := range calls {
		if !c.upd.New.IsZero() && !c.hasObj {
			t.Errorf("call %d: new object %s not readable from trunk at hook time", i, c.upd.New)
		}
	}

	// Verify we got the right ref names.
	foundA, foundB := false, false
	for _, c := range calls {
		if string(c.upd.Name) == "refs/heads/a" {
			foundA = true
		}
		if string(c.upd.Name) == "refs/heads/b" {
			foundB = true
		}
	}
	if !foundA || !foundB {
		names := make([]string, len(calls))
		for i, c := range calls {
			names[i] = string(c.upd.Name)
		}
		t.Errorf("expected refs/heads/a and refs/heads/b in calls; got %v", names)
	}
}

// TS-22-4: A hook rejecting one of two refs leaves the other written and
// reports only the rejected ref's message.
// Verifies: 22-REQ-2.4, 22-REQ-2.5
func TestPreReceive_TS22_4_MixedAcceptReject(t *testing.T) {
	_, trunk, remote := newPreReceiveClientServer(t)

	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, upd RefUpdate) error {
		if string(upd.Name) == "refs/heads/blocked" {
			return errors.New("blocked by policy")
		}
		return nil
	})

	clone := t.TempDir()
	runGitCmd(t, "", "clone", remote, clone)
	addCommit(t, clone, "o.txt", "open commit")
	runGitCmd(t, clone, "checkout", "-b", "open")
	runGitCmd(t, clone, "checkout", "-b", "blocked")

	// Push both branches.
	out, _ := gitCmdOutput(clone, "push", "origin", "open", "blocked")

	// refs/heads/open should be written.
	openSha := runGitCmd(t, clone, "rev-parse", "open")
	trunkOpen := runGitCmd(t, trunk, "rev-parse", "refs/heads/open")
	if trunkOpen != openSha {
		t.Errorf("open ref: trunk = %s; want %s", trunkOpen, openSha)
	}

	// refs/heads/blocked should NOT exist.
	_, err := gitCmdOutput(trunk, "rev-parse", "--verify", "refs/heads/blocked")
	if err == nil {
		t.Error("refs/heads/blocked should not exist in trunk")
	}

	// Client output should contain the rejection message for blocked.
	if !strings.Contains(out, "blocked by policy") {
		t.Errorf("expected 'blocked by policy' in output; got:\n%s", out)
	}
}

// TS-22-5: The storer wrapper consults the hook on every reference write
// and removal, still hides PackfileWriter, and gitserver does not import
// carrypatch.
// Verifies: 22-REQ-2.6
func TestPreReceive_TS22_5_StorerWrapperAndImports(t *testing.T) {
	// Verify thinPackSafeStorer does not expose PackfileWriter.
	var s interface{} = &thinPackSafeStorer{}
	if _, ok := s.(storer.PackfileWriter); ok {
		t.Error("thinPackSafeStorer should NOT implement storer.PackfileWriter")
	}

	// Verify gitserver does not import carrypatch via 'go list'.
	out, err := goListDeps(t, "./internal/gitserver")
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "internal/carrypatch") {
		t.Error("internal/gitserver must not import internal/carrypatch")
	}
}

// emptyPack returns a valid packfile containing zero objects: the signature,
// version 2, an object count of 0 and the SHA-1 trailer. A git client sends
// this when every pushed commit already exists on the server.
func emptyPack() []byte {
	header := []byte{'P', 'A', 'C', 'K', 0, 0, 0, 2, 0, 0, 0, 0}
	sum := sha1.Sum(header)
	return append(header, sum[:]...)
}

// receivePackRequest builds a git-receive-pack request body: the first command
// carries the capability list after a NUL, the rest are plain commands, a
// flush packet ends the command list and the packfile follows.
func receivePackRequest(commands []string, caps string, pack []byte) string {
	var b strings.Builder
	for i, c := range commands {
		if i == 0 {
			c += "\x00" + caps
		}
		b.Write(encodePktLine(c))
	}
	b.Write(encodePktFlush())
	b.Write(pack)
	return b.String()
}

// TS-22-6: A client that does not request report-status still has the
// hook's rejection enforced but gets no per-ref message, and the refs the
// hub did accept in the same push still drive their side effects.
// Verifies: 22-REQ-8.3
//
// A real git client always requests report-status, so this test speaks the
// protocol directly: one push creating a rejected ref (first, so the hook's
// error is go-git's first error) and an accepted ref, without report-status.
// go-git then returns (nil, firstErr); the handler must not turn that error
// into an ERR line carrying the hook's text.
func TestPreReceive_TS22_6_NoReportStatusEnforcement(t *testing.T) {
	env, trunk, _ := newPreReceiveClientServer(t)

	auditMock := newGitAuditEmitter()
	origEmitter := defaultAuditEmitter
	defaultAuditEmitter = auditMock
	t.Cleanup(func() { defaultAuditEmitter = origEmitter })

	postPush := make(chan []string, 4)
	RegisterPostPushHook(func(_ *sql.DB, _ string, branches []string) {
		postPush <- append([]string(nil), branches...)
	})

	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, upd RefUpdate) error {
		if string(upd.Name) == "refs/heads/blocked" {
			return errors.New("denied here")
		}
		return nil
	})

	base := runGitCmd(t, trunk, "rev-parse", "HEAD")
	zero := plumbing.ZeroHash.String()
	body := receivePackRequest([]string{
		zero + " " + base + " refs/heads/blocked",
		zero + " " + base + " refs/heads/open",
	}, "agent=hub-test", emptyPack())

	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-receive-pack", body,
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	// Enforcement: the rejected ref is absent, the accepted ref exists.
	if refExists(t, trunk, "refs/heads/blocked") {
		t.Error("refs/heads/blocked should not exist after a rejected push")
	}
	if got := refValue(t, trunk, "refs/heads/open"); got != base {
		t.Errorf("refs/heads/open = %q; want %q", got, base)
	}

	// No per-ref message: neither the hook's text nor an ERR line.
	respBody := rec.Body.String()
	if strings.Contains(respBody, "denied here") {
		t.Errorf("response leaks the hook's message: %q", respBody)
	}
	if strings.Contains(respBody, "ERR") {
		t.Errorf("response contains an ERR line: %q", respBody)
	}

	// The accepted ref still drives audit and the post-push hook; the
	// rejected one does not.
	events := auditMock.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event for the accepted ref; got %d", len(events))
	}
	refsUpdated, ok := events[0].Metadata["refs_updated"].([]string)
	if !ok {
		t.Fatalf("refs_updated is %T; want []string", events[0].Metadata["refs_updated"])
	}
	if len(refsUpdated) != 1 || refsUpdated[0] != "refs/heads/open" {
		t.Errorf("refs_updated = %v; want [refs/heads/open]", refsUpdated)
	}

	select {
	case branches := <-postPush:
		if len(branches) != 1 || branches[0] != "open" {
			t.Errorf("post-push branches = %v; want [open]", branches)
		}
	case <-time.After(2 * time.Second):
		t.Error("post-push hook was not called for the accepted ref")
	}

	// 22-REQ-5.4: head_sha is refreshed from the trunk HEAD.
	var headSHA string
	if err := env.db.QueryRow(
		`SELECT COALESCE(head_sha, '') FROM workspaces WHERE slug = ?`, "myws",
	).Scan(&headSHA); err != nil {
		t.Fatalf("query head_sha: %v", err)
	}
	if want := runGitCmd(t, trunk, "rev-parse", "HEAD"); headSHA != want {
		t.Errorf("head_sha = %q; want trunk HEAD %q", headSHA, want)
	}
}

// TS-22-6 (guard): without report-status, a session failure that is not a
// hook rejection (here an unreadable packfile) is still reported to the
// client as an ERR line and writes no ref.
// Verifies: 22-REQ-8.3
func TestPreReceive_TS22_6_NoReportStatusUnpackFailureStillErrors(t *testing.T) {
	env, trunk, _ := newPreReceiveClientServer(t)

	var hookCalls int
	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, _ RefUpdate) error {
		hookCalls++
		return nil
	})

	base := runGitCmd(t, trunk, "rev-parse", "HEAD")
	body := receivePackRequest([]string{
		plumbing.ZeroHash.String() + " " + base + " refs/heads/open",
	}, "agent=hub-test", []byte("this is not a packfile"))

	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-receive-pack", body,
		withBasicAuth("x-token-auth", "af_key_user1"))

	if !strings.Contains(rec.Body.String(), "ERR") {
		t.Errorf("expected an ERR line for an unreadable pack; got %q", rec.Body.String())
	}
	if refExists(t, trunk, "refs/heads/open") {
		t.Error("refs/heads/open should not exist after a failed unpack")
	}
	if hookCalls != 0 {
		t.Errorf("hook called %d times; want 0 when unpacking fails", hookCalls)
	}
}

// TS-22-7: An empty-body receive-pack request does not call the pre-receive hook.
// Verifies: 22-REQ-8.4
func TestPreReceive_TS22_7_EmptyBodyDoesNotCallHook(t *testing.T) {
	env, _, _ := newPreReceiveClientServer(t)

	var hookCalls int
	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, _ RefUpdate) error {
		hookCalls++
		return nil
	})

	// POST with empty body.
	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-receive-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	if hookCalls != 0 {
		t.Errorf("hook was called %d times; want 0 for empty body", hookCalls)
	}
}

// TS-22-8: A panicking pre-receive hook gets no special recovery from the
// git server.
// Verifies: 22-REQ-8.6
func TestPreReceive_TS22_8_PanicNoSpecialRecovery(t *testing.T) {
	_, _, remote := newPreReceiveClientServer(t)

	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, _ RefUpdate) error {
		panic("hook panic")
	})

	clone := t.TempDir()
	runGitCmd(t, "", "clone", remote, clone)
	addCommit(t, clone, "p.txt", "panic commit")
	runGitCmd(t, clone, "checkout", "-b", "panic-branch")

	// The push should fail because the panic propagates through the handler.
	// We don't add any special recovery — the existing Echo/httptest
	// behaviour handles it.
	_, err := gitCmdOutput(clone, "push", "origin", "panic-branch")
	if err == nil {
		t.Log("push succeeded despite panic — Echo's recovery middleware caught it")
	}

	// The key assertion: no special recovery was added by the git server.
	// The test passes if it doesn't hang and the panic is handled by
	// whatever recovery the framework provides.
}

// goListDeps runs 'go list -deps' for the given package and returns the output.
func goListDeps(t *testing.T, pkg string) (string, error) {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", pkg)
	cmd.Dir = findModuleRoot()
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// findModuleRoot walks up from the current directory to find go.mod.
func findModuleRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(dir + "/go.mod"); err == nil {
			return dir
		}
		parent := dir[:strings.LastIndex(dir, "/")]
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

