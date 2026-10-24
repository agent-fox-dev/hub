package gitserver

import (
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// The resolver serves a ready workspace of the URL's org and refuses, with
// the git server's pkt-line answers, a slug it does not know, a slug under
// the wrong or an unknown org, a workspace outside every org, a clone that
// is not ready, and a URL without the .git suffix; a repository missing on
// disk and a failed lookup are plain 500s.
func TestResolver_Answers(t *testing.T) {
	v := newEnv(t)
	owner := v.key(t, "owner")
	testutil.SeedOrg(t, v.db, "org-2", "otherorg", "owner")
	v.workspace(t, "noorg", "owner", "", "ready")
	v.workspace(t, "nodisk", "owner", orgID, "ready")
	for _, status := range []string{"cloning", "failed", "archived"} {
		v.workspace(t, status, "owner", orgID, status)
	}
	for _, c := range []struct {
		path   string
		status int
		body   string
	}{
		{wsPath, http.StatusOK, ""},
		{"/git/myorg/unknown.git", http.StatusNotFound, notFoundBody},
		{"/git/otherorg/ws.git", http.StatusNotFound, notFoundBody},
		{"/git/nowhere/ws.git", http.StatusNotFound, notFoundBody},
		{"/git/myorg/noorg.git", http.StatusNotFound, notFoundBody},
		{"/git/myorg/cloning.git", http.StatusNotFound, notAvailableBody},
		{"/git/myorg/failed.git", http.StatusNotFound, notAvailableBody},
		{"/git/myorg/archived.git", http.StatusNotFound, notAvailableBody},
		{"/git/myorg/nodisk.git", http.StatusInternalServerError, ""},
		{"/git/myorg/ws", http.StatusNotFound, `{"message":"Not Found"}`},
	} {
		rec := v.do(t, http.MethodGet, c.path+"/info/refs?service=git-upload-pack", "", owner)
		body := rec.Body.String()
		if rec.Code != c.status || (c.body != "" && !strings.Contains(body, c.body)) || (c.body == "" && c.status != http.StatusOK && body != "") {
			t.Errorf("%s: (%d, %q); want %d %q", c.path, rec.Code, body, c.status, c.body)
		}
		if c.body != "" && strings.HasPrefix(c.body, "0") && rec.Header().Get("Content-Type") != "text/plain" {
			t.Errorf("%s: Content-Type %q; want text/plain", c.path, rec.Header().Get("Content-Type"))
		}
	}
	testutil.Exec(t, v.db, `DROP TABLE orgs`)
	if rec := v.do(t, http.MethodGet, infoRefs+"git-upload-pack", "", owner); rec.Code != http.StatusInternalServerError || rec.Body.Len() != 0 {
		t.Errorf("lookup failure: (%d, %q); want a bodiless 500", rec.Code, rec.Body)
	}
}

// A clone that is not ready and a URL naming the wrong org are refused before
// the access decision: every caller gets the resolver's answer and no bound
// refusal is reported.
func TestResolver_RefusesBeforeAccessDecision(t *testing.T) {
	reports := recordRefusals(t)
	v := newEnv(t)
	testutil.SeedOrg(t, v.db, "org-2", "otherorg", "owner")
	v.workspace(t, "notready", "owner", orgID, "cloning")
	v.workspace(t, "other", "owner", orgID, "ready")
	bound := v.pat(t, "owner", "git:read", "git:write")
	testutil.BindToken(t, v.db, strings.TrimPrefix(bound, "af_pat_"), "other")
	for _, cred := range []string{bound, v.key(t, "stranger"), v.key(t, "member"), v.key(t, "owner")} {
		for _, r := range gitRoutes {
			path := strings.Replace(r.path, "/ws.git", "/notready.git", 1)
			if rec := v.do(t, r.method, path, "", cred); rec.Code != http.StatusNotFound || rec.Body.String() != notAvailableBody {
				t.Errorf("%s %s as %s: (%d, %q); want 404 %q", r.method, path, cred, rec.Code, rec.Body, notAvailableBody)
			}
		}
		path := "/git/otherorg/ws.git/info/refs?service=git-upload-pack"
		if rec := v.do(t, http.MethodGet, path, "", cred); rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Errorf("%s as %s: (%d, %q); want 404 %q", path, cred, rec.Code, rec.Body, notFoundBody)
		}
	}
	if len(*reports) != 0 {
		t.Errorf("refusal reports %+v; want none", *reports)
	}
}

// parseEndpointPath reads the org and slug of both endpoint path forms and
// rejects paths without them.
func TestParseEndpointPath(t *testing.T) {
	for path, want := range map[string][2]string{
		"/git/myorg/ws.git": {"myorg", "ws"},
		"/myorg/ws.git":     {"myorg", "ws"},
		"myorg/ws":          {"myorg", "ws"},
		"/git/myorg/ws":     {"myorg", "ws"},
		"":                  {},
		"/":                 {},
		"/ws.git":           {},
		"/myorg/.git":       {},
	} {
		org, slug, err := parseEndpointPath(path)
		if (err != nil) != (want[0] == "") || org != want[0] || slug != want[1] {
			t.Errorf("parseEndpointPath(%q) = %q, %q, %v; want %q, %q", path, org, slug, err, want[0], want[1])
		}
	}
}
