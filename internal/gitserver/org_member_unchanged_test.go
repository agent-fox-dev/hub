package gitserver

import (
	"net/http"
	"testing"
)

// TS-35-56 (git): A member of the workspace's org gets the stranger's answer
// on clone/fetch and push: the git server stays owner-only in this spec.
// Requirement: 35-REQ-10.6
func TestMemberGitOwnerOnly_TS_35_56(t *testing.T) {
	env := newGitTestEnv(t)
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedOrgMember(t, "org-1", "user-b")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")

	for _, service := range []string{"git-upload-pack", "git-receive-pack"} {
		path := "/git/myorg/myws.git/info/refs?service=" + service
		stranger := env.doRequest(t, http.MethodGet, path, "", withBasicAuth("user", "af_pat_abc123"))
		for _, member := range []string{"af_key_userB", "af_pat_userB_read", "af_pat_userB_write"} {
			rec := env.doRequest(t, http.MethodGet, path, "", withBasicAuth("user", member))
			if rec.Code != http.StatusNotFound || rec.Code != stranger.Code || rec.Body.String() != stranger.Body.String() {
				t.Errorf("%s as member %s: %d %q; want the stranger's %d %q", service, member, rec.Code, rec.Body, stranger.Code, stranger.Body)
			}
		}
	}
}
