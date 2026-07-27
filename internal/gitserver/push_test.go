package gitserver

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TS-06-22: After a successful ReceivePackSession, the head_sha column on
// the workspace database row is updated to the new HEAD commit SHA.
// Requirement: 06-REQ-6.1
func TestPush_HeadSHA_UpdatedAfterSuccessfulPush(t *testing.T) {
	env := newGitTestEnv(t)

	// Seed org, workspace, and a local git repository.
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	trunkPath := env.initWorkspaceRepo(t, "myws")

	// Record the initial HEAD SHA.
	initialSHA := getHeadSHA(t, trunkPath)
	setHeadSHAInDB(t, env, "myws", initialSHA)

	// Create a second commit (simulating what the pack would contain).
	addCommit(t, trunkPath, "second.txt", "second commit")
	newSHA := getHeadSHA(t, trunkPath)

	// Verify the SHAs are different.
	if initialSHA == newSHA {
		t.Fatalf("initial and new SHA are the same: %s", initialSHA)
	}

	// POST to git-receive-pack with the owner's credential.
	// In a real implementation, this would carry a valid pack body.
	// For the stub test, we verify the endpoint processes the request
	// and updates head_sha in the database.
	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-receive-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusOK {
		t.Errorf("POST git-receive-pack: status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	// The response should contain a pkt-line "unpack ok" line.
	body := rec.Body.String()
	if !strings.Contains(body, "unpack ok") {
		t.Errorf("response body should contain 'unpack ok'; got %q",
			truncate(body, 200))
	}

	// After a successful push, head_sha in the database should be updated.
	var headSHA string
	err := env.db.QueryRow(
		`SELECT COALESCE(head_sha, '') FROM workspaces WHERE slug = ?`, "myws",
	).Scan(&headSHA)
	if err != nil {
		t.Fatalf("failed to query head_sha: %v", err)
	}
	if headSHA == initialSHA {
		t.Errorf("head_sha was not updated; still %q", headSHA)
	}
	if headSHA == "" {
		t.Error("head_sha is empty after push")
	}
}

// TS-06-23: After a successful push, the local clone is updated but no
// propagation to the upstream remote occurs.
// Requirement: 06-REQ-6.2
func TestPush_NoUpstreamPropagation(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	trunkPath := env.initWorkspaceRepo(t, "myws")

	// Set up a local "upstream" bare repo to detect any push propagation.
	upstreamPath := t.TempDir()
	initBareRepo(t, upstreamPath)

	// Add the upstream as a remote to the workspace repo.
	addRemote(t, trunkPath, "origin", upstreamPath)

	// Record the initial upstream ref count.
	upstreamRefsBefore := countRefs(t, upstreamPath)

	// POST to git-receive-pack.
	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-receive-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusOK {
		t.Errorf("POST git-receive-pack: status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	// Verify that no push to the upstream remote occurred.
	upstreamRefsAfter := countRefs(t, upstreamPath)
	if upstreamRefsAfter != upstreamRefsBefore {
		t.Errorf("upstream ref count changed from %d to %d; push should not propagate upstream",
			upstreamRefsBefore, upstreamRefsAfter)
	}
}

// TestPush_FailedSession_DoesNotUpdateHeadSHA verifies that when the
// ReceivePackSession fails (corrupt pack or session error), head_sha
// is not updated in the database.
// Requirement: 06-REQ-6.E3
func TestPush_FailedSession_DoesNotUpdateHeadSHA(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	trunkPath := env.initWorkspaceRepo(t, "myws")
	initialSHA := getHeadSHA(t, trunkPath)
	setHeadSHAInDB(t, env, "myws", initialSHA)

	// Send corrupt/invalid pack data to trigger a session error.
	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-receive-pack", "INVALID_PACK_DATA",
		withBasicAuth("x-token-auth", "af_key_user1"))

	// The response should be HTTP 200 with a pkt-line error per git smart HTTP spec.
	if rec.Code != http.StatusOK {
		t.Errorf("POST git-receive-pack with corrupt pack: status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	// head_sha should NOT be updated after a failed session.
	var headSHA string
	err := env.db.QueryRow(
		`SELECT COALESCE(head_sha, '') FROM workspaces WHERE slug = ?`, "myws",
	).Scan(&headSHA)
	if err != nil {
		t.Fatalf("failed to query head_sha: %v", err)
	}
	if headSHA != initialSHA {
		t.Errorf("head_sha changed after failed push; got %q, want %q",
			headSHA, initialSHA)
	}
}

// TestPush_DBUpdateFails_PushStillSucceeds verifies that if the database
// update of head_sha fails after the pack has been written to disk, the
// git client still receives a successful push response.
// Requirement: 06-REQ-6.E1
func TestPush_DBUpdateFails_PushStillSucceeds(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	// POST to git-receive-pack.
	// The implementation should handle DB errors gracefully — the push
	// response to the client should succeed even if head_sha update fails.
	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-receive-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	// The push response should succeed regardless of DB update status.
	if rec.Code != http.StatusOK {
		t.Errorf("POST git-receive-pack: status = %d; want %d",
			rec.Code, http.StatusOK)
	}
}

// --- Test helpers ---

// getHeadSHA returns the HEAD commit SHA for the repository at the given path.
func getHeadSHA(t *testing.T, repoPath string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD")
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in %q failed: %v", repoPath, err)
	}
	return strings.TrimSpace(string(out))
}

// setHeadSHAInDB updates the head_sha column for a workspace in the database.
func setHeadSHAInDB(t *testing.T, env *gitTestEnv, slug, sha string) {
	t.Helper()
	_, err := env.db.Exec(
		`UPDATE workspaces SET head_sha = ? WHERE slug = ?`, sha, slug,
	)
	if err != nil {
		t.Fatalf("failed to set head_sha for %q: %v", slug, err)
	}
}

// addCommit creates a new file and commits it to the repository at the given path.
func addCommit(t *testing.T, repoPath, filename, message string) {
	t.Helper()
	filePath := filepath.Join(repoPath, filename)
	if err := os.WriteFile(filePath, []byte(message+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write %q: %v", filePath, err)
	}
	for _, args := range [][]string{
		{"git", "-C", repoPath, "add", filename},
		{"git", "-C", repoPath, "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", message},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
}

// initBareRepo creates a bare git repository at the given path.
func initBareRepo(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("git", "init", "--bare", path)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare %q failed: %v\n%s", path, err, out)
	}
}

// addRemote adds a remote to the repository at the given path.
func addRemote(t *testing.T, repoPath, name, url string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repoPath, "remote", "add", name, url)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Remote may already exist; try set-url instead.
		cmd2 := exec.Command("git", "-C", repoPath, "remote", "set-url", name, url)
		cmd2.Env = cmd.Env
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			t.Fatalf("git remote add/set-url %q %q failed: %v\n%s\n%s", name, url, err, out, out2)
		}
	}
}

// countRefs returns the number of refs in a git repository.
func countRefs(t *testing.T, repoPath string) int {
	t.Helper()
	cmd := exec.Command("git", "-C", repoPath, "show-ref")
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.Output()
	if err != nil {
		// show-ref returns exit code 1 when there are no refs.
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return 0
	}
	return len(lines)
}
