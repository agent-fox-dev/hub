package gitserver

import (
	"net/http"
	"testing"
)

// TS-37-35: spec 37 supersedes the git clause of TS-35-56 (35-REQ-10.6): a
// member's upload-pack requests are granted and its receive-pack requests
// get the read-only 403. Requirement: 37-REQ-10.2
func TestMemberGitSupersedesTS3556_TS_37_35(t *testing.T) {
	env := newMemberGitEnv(t)
	for _, member := range []string{"af_key_userB", "af_pat_userB_read", "af_pat_userB_write"} {
		for _, r := range gitRequests {
			rec := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", member))
			push := r == gitRequests[1] || r == gitRequests[3]
			if push && (rec.Code != http.StatusForbidden || rec.Body.String() != readOnlyBody) {
				t.Errorf("%s %s as %s: (%d, %q); want 403 %q", r.method, r.path, member, rec.Code, rec.Body, readOnlyBody)
			}
			if !push && rec.Code != http.StatusOK {
				t.Errorf("%s %s as %s: (%d, %q); want 200", r.method, r.path, member, rec.Code, rec.Body)
			}
		}
	}
}
