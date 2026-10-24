package gitserver

import (
	"net/http"
	"testing"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// Spec 37 — bound tokens keep spec 33 and 34 behaviour.

// infoRefsUpload is GET info/refs?service=git-upload-pack of slug in myorg.
func infoRefsUpload(slug string) string {
	return "/git/myorg/" + slug + ".git/info/refs?service=git-upload-pack"
}

// TS-37-24: a PAT bound to another workspace is refused on a workspace its
// user owns or reads as a member, and each refusal is reported once with
// the git site. Requirement: 37-REQ-7.1
func TestBoundPAT_OtherWorkspaceReported_TS_37_24(t *testing.T) {
	reports := recordGitRefusals(t)
	env := newBindingGitEnv(t) // user-a owns s and x in myorg
	env.seedOrgMember(t, "org-1", "user-b")
	env.seedWorkspace(t, "y", "https://github.com/org/y", "user-b", "org-1", "ready")
	seedGitPAT(t, env.db, "tokOwnerS", "user-a", "git:read")
	seedGitPAT(t, env.db, "tokMemberY", "user-b", "git:read")
	bindGitToken(t, env.db, "tokOwnerS", "s")
	bindGitToken(t, env.db, "tokMemberY", "y")
	for _, c := range []struct{ token, user, bound string }{
		{"tokOwnerS", "user-a", "s"},
		{"tokMemberY", "user-b", "y"},
	} {
		n := len(*reports)
		rec := env.doRequest(t, http.MethodGet, infoRefsUpload("x"), "", withBasicAuth("x", "af_pat_"+c.token))
		if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Errorf("%s on x: (%d, %q); want 404 %q", c.token, rec.Code, rec.Body, notFoundBody)
		}
		want := wsaccess.BoundRefusal{TokenID: c.token, UserID: c.user, BoundSlug: c.bound, RequestedSlug: "x",
			Method: http.MethodGet, Route: infoRefsRoute, Status: http.StatusNotFound, Surface: wsaccess.SurfaceGit}
		if got := (*reports)[n:]; len(got) != 1 || got[0] != want {
			t.Errorf("%s on x: reports %+v; want exactly %+v", c.token, got, want)
		}
	}
}

// TS-37-25: a failed binding lookup is a pkt-line 500 and nothing is
// served. Requirement: 37-REQ-7.2
func TestBoundPAT_LookupError500_TS_37_25(t *testing.T) {
	env := newMemberGitEnv(t)
	seedGitPAT(t, env.db, "tokOwner", "user-a", "git:read")
	if _, err := env.db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	rec := env.doRequest(t, http.MethodGet, infoRefsUpload("myws"), "", withBasicAuth("x", "af_pat_tokOwner"))
	want := string(encodePktLine("ERR internal server error\n"))
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != want || rec.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("(%d, %q, %q); want text/plain 500 %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body, want)
	}
}

// TS-37-26: a member's PAT bound to the member workspace answers exactly as
// an unbound PAT of the same user and scopes. Requirement: 37-REQ-7.3
func TestBoundPAT_MemberBoundToW_TS_37_26(t *testing.T) {
	env := newMemberGitEnv(t)
	seedGitPAT(t, env.db, "tokBoundW", "user-b", "git:write")
	seedGitPAT(t, env.db, "tokUnbound", "user-b", "git:write")
	bindGitToken(t, env.db, "tokBoundW", "myws")
	for _, r := range gitRequests {
		b := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", "af_pat_tokBoundW"))
		u := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", "af_pat_tokUnbound"))
		if !sameAnswer(b, u) {
			t.Errorf("%s %s: bound (%d, %q), unbound (%d, %q); want the same", r.method, r.path, b.Code, b.Body, u.Code, u.Body)
		}
		push := r == gitRequests[1] || r == gitRequests[3]
		if push && (b.Code != http.StatusForbidden || b.Body.String() != readOnlyBody) {
			t.Errorf("%s %s: (%d, %q); want 403 %q", r.method, r.path, b.Code, b.Body, readOnlyBody)
		}
		if !push && b.Code != http.StatusOK {
			t.Errorf("%s %s: (%d, %q); want 200", r.method, r.path, b.Code, b.Body)
		}
	}
}

// TS-37-27: a stranger's bound PAT is refused without a report.
// Requirement: 37-REQ-7.4
func TestBoundPAT_StrangerNoReport_TS_37_27(t *testing.T) {
	reports := recordGitRefusals(t)
	env := newMemberGitEnv(t)
	seedGitPAT(t, env.db, "tokStranger", "user-2", "git:read", "git:write")
	bindGitToken(t, env.db, "tokStranger", "s")
	for _, r := range []struct{ method, path string }{gitRequests[0], gitRequests[2], gitRequests[3]} {
		rec := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", "af_pat_tokStranger"))
		if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Errorf("%s %s: (%d, %q); want 404 %q", r.method, r.path, rec.Code, rec.Body, notFoundBody)
		}
	}
	if len(*reports) != 0 {
		t.Errorf("%d refusal reports %+v; want 0", len(*reports), *reports)
	}
}
