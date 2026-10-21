package gitserver

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Spec 37 — members clone and fetch like the owner.

// invalidServiceBody is the handler's pkt-line refusal of a missing or
// invalid info/refs service.
var invalidServiceBody = string(encodePktLine("ERR invalid or missing service parameter\n"))

// sameAnswer reports whether two responses have the same status,
// Content-Type and body.
func sameAnswer(a, b *httptest.ResponseRecorder) bool {
	return a.Code == b.Code && a.Header().Get("Content-Type") == b.Header().Get("Content-Type") && a.Body.String() == b.Body.String()
}

// uploadPackWant is an upload-pack request body that wants the head of
// workspace slug's repository.
func uploadPackWant(t *testing.T, env *gitTestEnv, slug string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", filepath.Join(env.workspaceRoot, slug, "trunk"), "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(encodePktLine("want "+strings.TrimSpace(string(out))+"\n")) + "0000" + string(encodePktLine("done\n"))
}

// TS-37-6: upload-pack, and info/refs with any service other than
// git-receive-pack, need read: a member is served, and a malformed service
// gets the handler's refusal. Requirement: 37-REQ-2.2
func TestMemberRead_NeedRead_TS_37_6(t *testing.T) {
	env := newMemberGitEnv(t)
	member := withBasicAuth("x", "af_key_userB")
	for _, r := range []struct{ method, path string }{
		{http.MethodPost, "/git/myorg/myws.git/git-upload-pack"},
		{http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-upload-pack"},
	} {
		if rec := env.doRequest(t, r.method, r.path, "", member); rec.Code != http.StatusOK {
			t.Errorf("%s %s: (%d, %q); want 200", r.method, r.path, rec.Code, rec.Body)
		}
	}
	for _, q := range []string{"?service=foo", "?service=", ""} {
		rec := env.doRequest(t, http.MethodGet, "/git/myorg/myws.git/info/refs"+q, "", member)
		if rec.Code != http.StatusForbidden || rec.Body.String() != invalidServiceBody {
			t.Errorf("info/refs%s: (%d, %q); want 403 %q", q, rec.Code, rec.Body, invalidServiceBody)
		}
	}
}

// TS-37-7: a member's malformed info/refs request gets the owner's answer.
// Requirement: 37-REQ-2.3
func TestMemberRead_MalformedServiceLikeOwner_TS_37_7(t *testing.T) {
	env := newMemberGitEnv(t)
	for _, q := range []string{"", "?service=bogus"} {
		path := "/git/myorg/myws.git/info/refs" + q
		m := env.doRequest(t, http.MethodGet, path, "", withBasicAuth("x", "af_key_userB"))
		o := env.doRequest(t, http.MethodGet, path, "", withBasicAuth("x", "af_key_userA"))
		if m.Code != http.StatusForbidden || m.Header().Get("Content-Type") != "text/plain" || m.Body.String() != invalidServiceBody || !sameAnswer(m, o) {
			t.Errorf("info/refs%s: member (%d, %q), owner (%d, %q); want both 403 text/plain %q", q, m.Code, m.Body, o.Code, o.Body, invalidServiceBody)
		}
	}
}

// TS-37-8 (property): for any git route and service value, a member is never
// given the stranger's 404; a push gets the read-only 403, anything else the
// owner's answer. Requirement: 37-REQ-2.4
func TestMemberRead_PropertyNeverPrivate_TS_37_8(t *testing.T) {
	env := newMemberGitEnv(t)
	rng := rand.New(rand.NewSource(37))
	services := []string{"", "git-upload-pack", "git-receive-pack"}
	for i := 0; i < 8; i++ {
		services = append(services, fmt.Sprintf("svc-%d", rng.Intn(1e6)))
	}
	for _, route := range []struct {
		method, path string
		write        bool
	}{
		{http.MethodGet, "/git/myorg/myws.git/info/refs", false},
		{http.MethodPost, "/git/myorg/myws.git/git-upload-pack", false},
		{http.MethodPost, "/git/myorg/myws.git/git-receive-pack", true},
	} {
		for _, svc := range services {
			path := route.path + "?service=" + svc
			write := route.write || (route.method == http.MethodGet && svc == "git-receive-pack")
			m := env.doRequest(t, route.method, path, "", withBasicAuth("x", "af_key_userB"))
			if m.Code == http.StatusNotFound {
				t.Errorf("%s %s: member got 404 %q", route.method, path, m.Body)
				continue
			}
			if write {
				if m.Code != http.StatusForbidden || m.Body.String() != readOnlyBody {
					t.Errorf("%s %s: (%d, %q); want the read-only 403", route.method, path, m.Code, m.Body)
				}
				continue
			}
			if o := env.doRequest(t, route.method, path, "", withBasicAuth("x", "af_key_userA")); !sameAnswer(m, o) {
				t.Errorf("%s %s: member (%d, %q); want the owner's (%d, %q)", route.method, path, m.Code, m.Body, o.Code, o.Body)
			}
		}
	}
}

// memberReadCreds are user-b's API key and PATs holding git:read and
// git:write.
var memberReadCreds = []string{"af_key_userB", "af_pat_userB_read", "af_pat_userB_write"}

// TS-37-9: a member's API key and git:read / git:write PATs get the owner's
// ref advertisement. Requirement: 37-REQ-3.1
func TestMemberRead_RefAdvertisement_TS_37_9(t *testing.T) {
	env := newMemberGitEnv(t)
	path := "/git/myorg/myws.git/info/refs?service=git-upload-pack"
	owner := env.doRequest(t, http.MethodGet, path, "", withBasicAuth("x", "af_key_userA"))
	if owner.Code != http.StatusOK || !strings.Contains(owner.Body.String(), "refs/heads/") {
		t.Fatalf("owner: (%d, %q); want an advertisement", owner.Code, owner.Body)
	}
	for _, cred := range memberReadCreds {
		if m := env.doRequest(t, http.MethodGet, path, "", withBasicAuth("x", cred)); !sameAnswer(m, owner) {
			t.Errorf("%s: (%d, %q, %q); want the owner's (%d, %q, %q)", cred, m.Code, m.Header().Get("Content-Type"), m.Body, owner.Code, owner.Header().Get("Content-Type"), owner.Body)
		}
	}
}

// TS-37-10: a member's upload-pack POST returns the owner's pack.
// Requirement: 37-REQ-3.2
func TestMemberRead_Pack_TS_37_10(t *testing.T) {
	env := newMemberGitEnv(t)
	body := uploadPackWant(t, env, "myws")
	path := "/git/myorg/myws.git/git-upload-pack"
	owner := env.doRequest(t, http.MethodPost, path, body, withBasicAuth("x", "af_key_userA"))
	if owner.Code != http.StatusOK || !strings.Contains(owner.Body.String(), "PACK") {
		t.Fatalf("owner: (%d, %q); want a pack", owner.Code, owner.Body)
	}
	for _, cred := range memberReadCreds {
		if m := env.doRequest(t, http.MethodPost, path, body, withBasicAuth("x", cred)); !sameAnswer(m, owner) {
			t.Errorf("%s: (%d, %s, %d bytes); want the owner's (%d, %s, %d bytes)", cred, m.Code, m.Header().Get("Content-Type"), m.Body.Len(), owner.Code, owner.Header().Get("Content-Type"), owner.Body.Len())
		}
	}
}

// TS-37-11: a member's PAT without git scopes passes the resolver and is
// refused by the scope check, as the owner's is. Requirement: 37-REQ-3.3
func TestMemberRead_NoScopePAT_TS_37_11(t *testing.T) {
	env := newMemberGitEnv(t)
	seedGitPAT(t, env.db, "memberNoScope", "user-b")
	seedGitPAT(t, env.db, "ownerNoScope", "user-a")
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-upload-pack"},
		{http.MethodPost, "/git/myorg/myws.git/git-upload-pack"},
	} {
		m := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", "af_pat_memberNoScope"))
		o := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", "af_pat_ownerNoScope"))
		if m.Code != http.StatusForbidden || !strings.Contains(m.Body.String(), "insufficient git permissions") || strings.Contains(m.Body.String(), "ERR") || !sameAnswer(m, o) {
			t.Errorf("%s %s: member (%d, %q), owner (%d, %q); want both the JSON 403 insufficient git permissions", r.method, r.path, m.Code, m.Body, o.Code, o.Body)
		}
	}
}

// TS-37-12: a member removed from the org is a stranger on the next
// request. Requirement: 37-REQ-3.4
func TestMemberRead_RemovedMember_TS_37_12(t *testing.T) {
	env := newMemberGitEnv(t)
	path := "/git/myorg/myws.git/info/refs?service=git-upload-pack"
	if rec := env.doRequest(t, http.MethodGet, path, "", withBasicAuth("x", "af_key_userB")); rec.Code != http.StatusOK {
		t.Fatalf("as a member: (%d, %q); want 200", rec.Code, rec.Body)
	}
	if _, err := env.db.Exec(`DELETE FROM org_members WHERE org_id = 'org-1' AND user_id = 'user-b'`); err != nil {
		t.Fatal(err)
	}
	if rec := env.doRequest(t, http.MethodGet, path, "", withBasicAuth("x", "af_key_userB")); rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
		t.Errorf("after removal: (%d, %q); want 404 %q", rec.Code, rec.Body, notFoundBody)
	}
}
