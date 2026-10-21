package gitserver

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// Spec 37 — everyone but members answers as before.

// gitRequests are the three git routes of myws, with both info/refs
// services.
var gitRequests = []struct{ method, path string }{
	{http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-upload-pack"},
	{http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-receive-pack"},
	{http.MethodPost, "/git/myorg/myws.git/git-upload-pack"},
	{http.MethodPost, "/git/myorg/myws.git/git-receive-pack"},
}

// recordGitRefusals records the wsaccess bound-refusal reports for the rest of
// the test.
func recordGitRefusals(t *testing.T) *[]wsaccess.BoundRefusal {
	t.Helper()
	var reports []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { reports = append(reports, r) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })
	return &reports
}

// TS-37-19: a stranger gets the 404 on all three routes, with no binding
// lookup (the table is dropped) and no refusal report.
// Requirement: 37-REQ-6.1
func TestStranger_404NoBindingLookup_TS_37_19(t *testing.T) {
	reports := recordGitRefusals(t)
	env := newMemberGitEnv(t)
	seedGitPAT(t, env.db, "strangerPAT", "user-2", "git:read", "git:write")
	if _, err := env.db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	for _, cred := range []string{"af_key_user2", "af_pat_strangerPAT"} {
		for _, r := range gitRequests {
			rec := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", cred))
			if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "text/plain" || rec.Body.String() != notFoundBody {
				t.Errorf("%s %s as %s: (%d, %q); want text/plain 404 %q", r.method, r.path, cred, rec.Code, rec.Body, notFoundBody)
			}
		}
	}
	if len(*reports) != 0 {
		t.Errorf("%d refusal reports; want 0", len(*reports))
	}
}

// TS-37-20: the owner's credentials and an admin token answer as before on
// every route, the owner's git:read PAT getting the scope 403 on a push.
// Requirement: 37-REQ-6.2
func TestOwnerAndAdmin_Unchanged_TS_37_20(t *testing.T) {
	env := newMemberGitEnv(t)
	// The Content-Type each route answers with (handlers.go), and the body
	// the owner's API key gets, which every granted credential must match.
	contentType := []string{
		"application/x-git-upload-pack-advertisement",
		"application/x-git-receive-pack-advertisement",
		"application/x-git-upload-pack-result",
		"application/x-git-receive-pack-result",
	}
	reference := map[int]string{}
	for i, r := range gitRequests {
		reference[i] = env.doRequest(t, r.method, r.path, "", withBasicAuth("x", "af_key_userA")).Body.String()
	}
	for _, cred := range []string{"af_key_userA", "af_pat_writepat", "af_pat_readpat", "af_admin_test789"} {
		for i, r := range gitRequests {
			rec := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", cred))
			push := strings.Contains(r.path, "receive-pack")
			if cred == "af_pat_readpat" && push {
				if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"insufficient git permissions"`) {
					t.Errorf("%s %s as %s: (%d, %q); want the JSON 403 insufficient git permissions", r.method, r.path, cred, rec.Code, rec.Body)
				}
				continue
			}
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != contentType[i] || rec.Body.String() != reference[i] {
				t.Errorf("%s %s as %s: (%d, %q, %q); want 200 %s with the owner's body %q", r.method, r.path, cred, rec.Code, rec.Header().Get("Content-Type"), rec.Body, contentType[i], reference[i])
			}
		}
	}
}

// TS-37-21: an API key of a user with the admin role is not an admin token:
// as a stranger it gets 404, as a member reads and a refused push.
// Requirement: 37-REQ-6.3
func TestAdminRoleAPIKey_NotAdminToken_TS_37_21(t *testing.T) {
	env := newMemberGitEnv(t)
	if _, err := env.db.Exec(`UPDATE users SET role = 'admin' WHERE id IN ('user-2', 'user-b')`); err != nil {
		t.Fatal(err)
	}
	upload, receive := gitRequests[2], gitRequests[3]
	for _, r := range []struct{ method, path string }{upload, receive} {
		if rec := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", "af_key_user2")); rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Errorf("admin-role stranger %s %s: (%d, %q); want 404 %q", r.method, r.path, rec.Code, rec.Body, notFoundBody)
		}
	}
	if rec := env.doRequest(t, upload.method, upload.path, "", withBasicAuth("x", "af_key_userB")); rec.Code != http.StatusOK {
		t.Errorf("admin-role member upload-pack: (%d, %q); want 200", rec.Code, rec.Body)
	}
	if rec := env.doRequest(t, receive.method, receive.path, "", withBasicAuth("x", "af_key_userB")); rec.Code != http.StatusForbidden || rec.Body.String() != readOnlyBody {
		t.Errorf("admin-role member receive-pack: (%d, %q); want 403 %q", rec.Code, rec.Body, readOnlyBody)
	}
}

// TS-37-22: API key and admin token answers do not depend on the binding
// table. Requirement: 37-REQ-6.4
func TestAPIKeyAndAdmin_NoBindingLookup_TS_37_22(t *testing.T) {
	env := newMemberGitEnv(t)
	run := func() []string {
		var out []string
		for _, cred := range []string{"af_key_userA", "af_key_userB", "af_key_user2", "af_admin_test789"} {
			for _, r := range gitRequests {
				rec := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", cred))
				if rec.Code == http.StatusInternalServerError {
					t.Errorf("%s %s as %s: 500 %q", r.method, r.path, cred, rec.Body)
				}
				out = append(out, fmt.Sprintf("%s %s %s: %d %s", cred, r.method, r.path, rec.Code, rec.Body))
			}
		}
		return out
	}
	with := run()
	if _, err := env.db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	without := run()
	for i := range with {
		if with[i] != without[i] {
			t.Errorf("with the binding table: %s\nwithout it:            %s", with[i], without[i])
		}
	}
}

// TS-37-23: a blocked user's credentials get 401 before the resolver (the
// workspaces table is dropped, so a resolver lookup would be a 500).
// Requirement: 37-REQ-6.5
func TestBlockedUser_401BeforeResolver_TS_37_23(t *testing.T) {
	env := newMemberGitEnv(t)
	if _, err := env.db.Exec(`UPDATE users SET status = 'blocked' WHERE id IN ('user-a', 'user-b')`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`DROP TABLE workspaces`); err != nil {
		t.Fatal(err)
	}
	for _, cred := range []string{"af_key_userA", "af_pat_writepat", "af_key_userB", "af_pat_userB_write"} {
		for _, r := range gitRequests {
			if rec := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", cred)); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s as blocked %s: (%d, %q); want 401", r.method, r.path, cred, rec.Code, rec.Body)
			}
		}
	}
}
