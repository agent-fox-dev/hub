package gitserver

import (
	"net/http"
	"strings"
	"testing"
)

// Spec 37 — the order of the git server's checks.

// newOrderGitEnv is newMemberGitEnv with org otherorg, workspace notready
// (cloning) in myorg owned by user-a, and user-a's PAT tokOwnerS bound to
// another workspace.
func newOrderGitEnv(t *testing.T) *gitTestEnv {
	t.Helper()
	env := newMemberGitEnv(t)
	env.seedOrg(t, "org-2", "Other Org", "otherorg")
	env.seedWorkspace(t, "notready", "https://github.com/org/notready", "user-a", "org-1", "active")
	env.setCloneStatus(t, "notready", "cloning")
	env.seedWorkspace(t, "s", "https://github.com/org/s", "user-a", "org-1", "active")
	seedGitPAT(t, env.db, "tokOwnerS", "user-a", "git:read", "git:write")
	bindGitToken(t, env.db, "tokOwnerS", "s")
	seedGitPAT(t, env.db, "memberNoScope", "user-b")
	seedGitPAT(t, env.db, "ownerNoScope", "user-a")
	return env
}

// TS-37-28: a request tripping several checks gets the answer of the first
// in the documented order. Requirement: 37-REQ-8.1
func TestCheckOrder_FirstRefusalWins_TS_37_28(t *testing.T) {
	env := newOrderGitEnv(t)
	seedGitPAT(t, env.db, "blockedPAT", "user-1", "git:read")
	if _, err := env.db.Exec(`UPDATE users SET status = 'blocked' WHERE id = 'user-1'`); err != nil {
		t.Fatal(err)
	}
	notAvailable := string(encodePktLine("ERR repository not available\n"))
	for _, c := range []struct {
		name, method, path, cred string
		status                   int
		body                     string
	}{
		{"blocked user on a not-ready clone", http.MethodGet, infoRefsUpload("notready"), "af_pat_blockedPAT", http.StatusUnauthorized, ""},
		{"no .git suffix", http.MethodGet, "/git/myorg/myws/info/refs?service=git-upload-pack", "af_key_user2", http.StatusNotFound, `"message":"Not Found"`},
		{"stranger on a not-ready clone", http.MethodGet, infoRefsUpload("notready"), "af_key_user2", http.StatusNotFound, notAvailable},
		{"stranger with the wrong org", http.MethodGet, "/git/otherorg/myws.git/info/refs?service=git-upload-pack", "af_key_user2", http.StatusNotFound, notFoundBody},
		{"member push", http.MethodPost, "/git/myorg/myws.git/git-receive-pack", "af_key_userB", http.StatusForbidden, readOnlyBody},
		{"member push with a scope-less PAT", http.MethodPost, "/git/myorg/myws.git/git-receive-pack", "af_pat_memberNoScope", http.StatusForbidden, readOnlyBody},
		{"owner's scope-less PAT", http.MethodPost, "/git/myorg/myws.git/git-receive-pack", "af_pat_ownerNoScope", http.StatusForbidden, `"insufficient git permissions"`},
	} {
		rec := env.doRequest(t, c.method, c.path, "", withBasicAuth("x", c.cred))
		// A pkt-line body must match exactly; a JSON one must carry the message.
		pkt := strings.Contains(c.body, "ERR ")
		if rec.Code != c.status || (pkt && rec.Body.String() != c.body) || (!pkt && c.body != "" && !strings.Contains(rec.Body.String(), c.body)) {
			t.Errorf("%s: (%d, %q); want %d with %q", c.name, rec.Code, rec.Body, c.status, c.body)
		}
	}

	// A database error in the resolver's own lookup is a plain 500.
	if _, err := env.db.Exec(`DROP TABLE orgs`); err != nil {
		t.Fatal(err)
	}
	if rec := env.doRequest(t, http.MethodGet, infoRefsUpload("myws"), "", withBasicAuth("x", "af_key_userA")); rec.Code != http.StatusInternalServerError || rec.Body.Len() != 0 {
		t.Errorf("resolver database error: (%d, %q); want a plain 500 with no body", rec.Code, rec.Body)
	}
}

// TS-37-29: a clone that is not ready is refused before the access
// decision for every caller, with no refusal report.
// Requirement: 37-REQ-8.2
func TestCheckOrder_NotReadyFirst_TS_37_29(t *testing.T) {
	reports := recordGitRefusals(t)
	env := newOrderGitEnv(t)
	want := string(encodePktLine("ERR repository not available\n"))
	for _, cred := range []string{"af_pat_tokOwnerS", "af_key_user2", "af_key_userB", "af_key_userA"} {
		for _, r := range []struct{ method, path string }{
			{http.MethodGet, infoRefsUpload("notready")},
			{http.MethodGet, "/git/myorg/notready.git/info/refs?service=git-receive-pack"},
			{http.MethodPost, "/git/myorg/notready.git/git-upload-pack"},
			{http.MethodPost, "/git/myorg/notready.git/git-receive-pack"},
		} {
			if rec := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", cred)); rec.Code != http.StatusNotFound || rec.Body.String() != want {
				t.Errorf("%s %s as %s: (%d, %q); want 404 %q", r.method, r.path, cred, rec.Code, rec.Body, want)
			}
		}
	}
	if len(*reports) != 0 {
		t.Errorf("%d refusal reports %+v; want 0", len(*reports), *reports)
	}
}

// TS-37-30: a URL naming the wrong org is refused before the access
// decision, with no refusal report. Requirement: 37-REQ-8.3
func TestCheckOrder_WrongOrgFirst_TS_37_30(t *testing.T) {
	reports := recordGitRefusals(t)
	env := newOrderGitEnv(t)
	for _, cred := range []string{"af_pat_tokOwnerS", "af_key_userB"} {
		rec := env.doRequest(t, http.MethodGet, "/git/otherorg/myws.git/info/refs?service=git-upload-pack", "", withBasicAuth("x", cred))
		if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Errorf("%s: (%d, %q); want 404 %q", cred, rec.Code, rec.Body, notFoundBody)
		}
	}
	if len(*reports) != 0 {
		t.Errorf("%d refusal reports %+v; want 0", len(*reports), *reports)
	}
}
