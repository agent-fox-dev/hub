package gitserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// insufficientScope is the JSON 403 of requireGitScope.
const insufficientScope = `"insufficient git permissions"`

// recordRefusals records the bound-token refusals wsaccess reports for the
// rest of the test.
func recordRefusals(t *testing.T) *[]wsaccess.BoundRefusal {
	t.Helper()
	var reports []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { reports = append(reports, r) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })
	return &reports
}

// Who may do what on a workspace's repository: the owner and admin tokens
// read and push, org members read but are refused pushes with the read-only
// 403 whatever their scopes, strangers get the 404; a PAT needs git:read to
// read and git:write (which implies read) to push, else the scope 403. A
// granted credential gets exactly the owner's answer.
func TestAccess_Matrix(t *testing.T) {
	v := newEnv(t)
	testutil.Exec(t, v.db, `UPDATE users SET role = 'admin' WHERE id = 'stranger'`)
	owner := v.key(t, "owner")
	var reference []*httptest.ResponseRecorder
	for _, r := range gitRoutes {
		rec := v.do(t, r.method, r.path, "", owner)
		if rec.Code != http.StatusOK {
			t.Fatalf("owner %s %s: (%d, %q)", r.method, r.path, rec.Code, rec.Body)
		}
		reference = append(reference, rec)
	}
	const ok, readOnly, scope, notFound = "ok", "read-only", "scope", "not-found"
	for name, c := range map[string]struct {
		cred       string
		read, push string
	}{
		"owner key":            {owner, ok, ok},
		"admin token":          {v.admin(t), ok, ok},
		"owner PAT git:write":  {v.pat(t, "owner", "git:write"), ok, ok},
		"owner PAT git:read":   {v.pat(t, "owner", "git:read"), ok, scope},
		"owner PAT no scopes":  {v.pat(t, "owner"), scope, scope},
		"member key":           {v.key(t, "member"), ok, readOnly},
		"member PAT git:write": {v.pat(t, "member", "git:write"), ok, readOnly},
		"member PAT no scopes": {v.pat(t, "member"), scope, readOnly},
		"admin-role stranger":  {v.key(t, "stranger"), notFound, notFound},
		"stranger PAT":         {v.pat(t, "stranger", "git:read", "git:write"), notFound, notFound},
	} {
		for i, r := range gitRoutes {
			want := c.read
			if isPush(i) {
				want = c.push
			}
			rec := v.do(t, r.method, r.path, "", c.cred)
			var good bool
			switch want {
			case ok:
				good = rec.Code == http.StatusOK && rec.Body.String() == reference[i].Body.String() &&
					rec.Header().Get("Content-Type") == reference[i].Header().Get("Content-Type")
			case readOnly:
				good = rec.Code == http.StatusForbidden && rec.Body.String() == readOnlyBody && rec.Header().Get("Content-Type") == "text/plain"
			case scope:
				good = rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), insufficientScope)
			case notFound:
				good = rec.Code == http.StatusNotFound && rec.Body.String() == notFoundBody && rec.Header().Get("Content-Type") == "text/plain"
			}
			if !good {
				t.Errorf("%s %s %s: (%d, %q); want %s", name, r.method, r.path, rec.Code, rec.Body, want)
			}
		}
	}
}

// The first service value of a ref discovery decides whether it is a push.
func TestAccess_FirstServiceValueDecides(t *testing.T) {
	v := newEnv(t)
	member := v.key(t, "member")
	if rec := v.do(t, http.MethodGet, infoRefs+"git-upload-pack&service=git-receive-pack", "", member); rec.Code != http.StatusOK {
		t.Errorf("upload-pack first: (%d, %q); want 200", rec.Code, rec.Body)
	}
	if rec := v.do(t, http.MethodGet, infoRefs+"git-receive-pack&service=git-upload-pack", "", member); rec.Code != http.StatusForbidden || rec.Body.String() != readOnlyBody {
		t.Errorf("receive-pack first: (%d, %q); want 403 %q", rec.Code, rec.Body, readOnlyBody)
	}
}

// A member removed from the org is a stranger on the next request.
func TestAccess_RemovedMemberIsStranger(t *testing.T) {
	v := newEnv(t)
	member := v.key(t, "member")
	if rec := v.do(t, http.MethodGet, infoRefs+"git-upload-pack", "", member); rec.Code != http.StatusOK {
		t.Fatalf("member before removal: %d", rec.Code)
	}
	testutil.Exec(t, v.db, `DELETE FROM org_members WHERE user_id = 'member'`)
	if rec := v.do(t, http.MethodGet, infoRefs+"git-upload-pack", "", member); rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
		t.Errorf("member after removal: (%d, %q); want 404 %q", rec.Code, rec.Body, notFoundBody)
	}
}

// A PAT bound to another workspace is refused with the 404 on a workspace its
// user owns or reads as a member, and the refusal is reported once with the
// git site; on its own workspace the scopes decide; a stranger's bound PAT
// is refused without a report.
func TestAccess_BoundTokens(t *testing.T) {
	reports := recordRefusals(t)
	v := newEnv(t)
	v.workspace(t, "other", "owner", orgID, "ready")
	v.workspace(t, "theirs", "member", orgID, "ready")
	initRepo(t, v.root, "other")
	ownerBound := v.pat(t, "owner", "git:read")
	memberBound := v.pat(t, "member", "git:write")
	memberUnbound := v.pat(t, "member", "git:write")
	strangerBound := v.pat(t, "stranger", "git:read", "git:write")
	for cred, slug := range map[string]string{ownerBound: "other", memberBound: "theirs", strangerBound: "other"} {
		testutil.BindToken(t, v.db, strings.TrimPrefix(cred, "af_pat_"), slug)
	}
	for _, c := range []struct{ cred, user, bound string }{{ownerBound, "owner", "other"}, {memberBound, "member", "theirs"}} {
		n := len(*reports)
		rec := v.do(t, http.MethodGet, infoRefs+"git-upload-pack", "", c.cred)
		if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Errorf("%s on ws: (%d, %q); want 404 %q", c.user, rec.Code, rec.Body, notFoundBody)
		}
		want := wsaccess.BoundRefusal{TokenID: strings.TrimPrefix(c.cred, "af_pat_"), UserID: c.user, BoundSlug: c.bound, RequestedSlug: "ws",
			Method: http.MethodGet, Route: "/git/:org/:slug.git/info/refs", Status: http.StatusNotFound, Surface: wsaccess.SurfaceGit}
		if got := (*reports)[n:]; len(got) != 1 || got[0] != want {
			t.Errorf("%s on ws: reports %+v; want exactly %+v", c.user, got, want)
		}
	}
	n := len(*reports)
	if rec := v.do(t, http.MethodGet, "/git/myorg/other.git/info/refs?service=git-upload-pack", "", ownerBound); rec.Code != http.StatusOK {
		t.Errorf("owner's bound PAT on its workspace: (%d, %q); want 200", rec.Code, rec.Body)
	}
	if rec := v.do(t, http.MethodGet, "/git/myorg/other.git/info/refs?service=git-receive-pack", "", ownerBound); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), insufficientScope) {
		t.Errorf("owner's git:read bound PAT pushing to its workspace: (%d, %q); want the scope 403", rec.Code, rec.Body)
	}
	for _, r := range gitRoutes {
		if rec := v.do(t, r.method, r.path, "", strangerBound); rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Errorf("stranger's bound PAT %s %s: (%d, %q); want 404", r.method, r.path, rec.Code, rec.Body)
		}
	}
	if len(*reports) != n {
		t.Errorf("reports after the stranger: %+v; want none", (*reports)[n:])
	}
	// Bound to the workspace it reads as a member, a PAT answers as an
	// unbound one of the same user and scopes.
	testutil.Exec(t, v.db, `UPDATE token_workspace_bindings SET slug = 'ws' WHERE token_id = ?`, strings.TrimPrefix(memberBound, "af_pat_"))
	for _, r := range gitRoutes {
		b, u := v.do(t, r.method, r.path, "", memberBound), v.do(t, r.method, r.path, "", memberUnbound)
		if b.Code != u.Code || b.Body.String() != u.Body.String() {
			t.Errorf("member bound %s %s: (%d, %q); unbound (%d, %q)", r.method, r.path, b.Code, b.Body, u.Code, u.Body)
		}
	}
}

// A failed binding or membership lookup is a pkt-line 500 and nothing is
// served; API keys and admin tokens never consult the binding table.
func TestAccess_LookupErrorsAre500(t *testing.T) {
	v := newEnv(t)
	owner, member, admin := v.key(t, "owner"), v.key(t, "member"), v.admin(t)
	pat := v.pat(t, "owner", "git:read")
	testutil.Exec(t, v.db, `DROP TABLE token_workspace_bindings`)
	rec := v.do(t, http.MethodGet, infoRefs+"git-upload-pack", "", pat)
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != internalErrorBody || rec.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("PAT without the binding table: (%d, %q); want 500 %q", rec.Code, rec.Body, internalErrorBody)
	}
	for name, cred := range map[string]string{"owner key": owner, "member key": member, "admin token": admin} {
		if rec := v.do(t, http.MethodGet, infoRefs+"git-upload-pack", "", cred); rec.Code != http.StatusOK {
			t.Errorf("%s without the binding table: (%d, %q); want 200", name, rec.Code, rec.Body)
		}
	}
	testutil.Exec(t, v.db, `DROP TABLE org_members`)
	rec = v.do(t, http.MethodGet, infoRefs+"git-upload-pack", "", member)
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != internalErrorBody {
		t.Errorf("member without the membership table: (%d, %q); want 500 %q", rec.Code, rec.Body, internalErrorBody)
	}
}

// Without an identity the access check answers 401 before touching the
// database, and writes nothing itself.
func TestAuthorizeGitAccess_NoIdentityIs401(t *testing.T) {
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, infoRefs+"git-upload-pack", nil), rec)
	c.SetPath("/git/:org/:slug.git/info/refs")
	err := authorizeGitAccess(c, nil, &workspaceInfo{Slug: "ws", OwnerID: "owner", OrgID: orgID, CloneStatus: "ready"})
	var he *echo.HTTPError
	if !errors.As(err, &he) || he.Code != http.StatusUnauthorized || rec.Body.Len() != 0 || c.Response().Committed {
		t.Errorf("err %v, body %q; want a 401 HTTPError and nothing written", err, rec.Body)
	}
}

// git:write implies git:read; nothing else grants either.
func TestHasGitScope(t *testing.T) {
	for _, c := range []struct {
		perms    []string
		required string
		want     bool
	}{
		{[]string{"git:read"}, "git:read", true},
		{[]string{"git:write"}, "git:read", true},
		{[]string{"git:write"}, "git:write", true},
		{[]string{"git:read"}, "git:write", false},
		{[]string{"workspaces:write"}, "git:read", false},
		{nil, "git:read", false},
	} {
		if got := hasGitScope(c.perms, c.required); got != c.want {
			t.Errorf("hasGitScope(%v, %s) = %v; want %v", c.perms, c.required, got, c.want)
		}
	}
	if perms := GitPermissions(); len(perms) != 2 || perms[0] != (apikit.Permission{Resource: "git", Action: "read"}) || perms[1] != (apikit.Permission{Resource: "git", Action: "write"}) {
		t.Errorf("GitPermissions() = %v; want git:read and git:write", perms)
	}
}
