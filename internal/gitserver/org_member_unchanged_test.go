package gitserver

import (
	"net/http"
	"testing"
)

// TS-35-56 (git), superseded by spec 37 (37-REQ-10.2, erratum 37 entry 1):
// 35-REQ-10.6 kept the git server owner-only until spec 37. A member of the
// workspace's org now gets the owner's answer on clone and fetch
// (upload-pack) and the read-only 403 on push (receive-pack).
// Requirement: 35-REQ-10.6
func TestMemberGitReadOnly_TS_35_56(t *testing.T) {
	env := newGitTestEnv(t)
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedOrgMember(t, "org-1", "user-b")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")

	readOnly := string(encodePktLine("ERR org members have read-only access to this workspace\n"))
	for _, r := range []struct {
		method, path string
		write        bool
	}{
		{http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-upload-pack", false},
		{http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-receive-pack", true},
		{http.MethodPost, "/git/myorg/myws.git/git-upload-pack", false},
		{http.MethodPost, "/git/myorg/myws.git/git-receive-pack", true},
	} {
		owner := env.doRequest(t, r.method, r.path, "", withBasicAuth("user", "af_key_userA"))
		for _, member := range []string{"af_key_userB", "af_pat_userB_read", "af_pat_userB_write"} {
			rec := env.doRequest(t, r.method, r.path, "", withBasicAuth("user", member))
			switch {
			case r.write && (rec.Code != http.StatusForbidden || rec.Body.String() != readOnly):
				t.Errorf("%s %s as member %s: %d %q; want 403 %q", r.method, r.path, member, rec.Code, rec.Body, readOnly)
			case !r.write && (rec.Code != http.StatusOK || rec.Code != owner.Code || rec.Body.String() != owner.Body.String()):
				t.Errorf("%s %s as member %s: %d %q; want the owner's %d %q", r.method, r.path, member, rec.Code, rec.Body, owner.Code, owner.Body)
			}
		}
	}
}
