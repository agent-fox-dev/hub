package gitserver

import (
	"bytes"
	"compress/gzip"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// Each route answers with its git smart HTTP Content-Type.
func TestRoutes_ContentTypes(t *testing.T) {
	v := newEnv(t)
	owner := v.key(t, "owner")
	for i, want := range []string{
		"application/x-git-upload-pack-advertisement",
		"application/x-git-receive-pack-advertisement",
		"application/x-git-upload-pack-result",
		"application/x-git-receive-pack-result",
	} {
		r := gitRoutes[i]
		if rec := v.do(t, r.method, r.path, "", owner); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != want {
			t.Errorf("%s %s: (%d, %q); want 200 %s", r.method, r.path, rec.Code, rec.Header().Get("Content-Type"), want)
		}
	}
}

// Ref discovery announces the service, then advertises the refs with the
// capabilities of the implementation that serves the following POST: the git
// CLI for fetches, go-git for pushes.
func TestInfoRefs_Advertisement(t *testing.T) {
	v := newEnv(t)
	owner := v.key(t, "owner")
	trunk := v.trunk("ws")
	head, branch := gitRun(t, trunk, "rev-parse", "HEAD"), gitRun(t, trunk, "symbolic-ref", "HEAD")
	for service, capability := range map[string]string{"git-upload-pack": "multi_ack_detailed", "git-receive-pack": "report-status"} {
		body := v.do(t, http.MethodGet, infoRefs+service, "", owner).Body.String()
		prefix := string(encodePktLine("# service="+service+"\n")) + "0000"
		if !strings.HasPrefix(body, prefix) || !strings.HasSuffix(body, "0000") || !strings.Contains(body, head+" "+branch) || !strings.Contains(body, capability) {
			t.Errorf("%s advertisement %q; want %q, %s %s, %s and a final flush", service, body, prefix, head, branch, capability)
		}
	}
}

// A missing or unknown service is refused with a pkt-line 403.
func TestInfoRefs_InvalidService(t *testing.T) {
	v := newEnv(t)
	owner := v.key(t, "owner")
	for _, q := range []string{"", "?service=", "?service=git-foo"} {
		rec := v.do(t, http.MethodGet, wsPath+"/info/refs"+q, "", owner)
		if rec.Code != http.StatusForbidden || rec.Body.String() != invalidServiceBody || rec.Header().Get("Content-Type") != "text/plain" {
			t.Errorf("info/refs%s: (%d, %q); want 403 %q", q, rec.Code, rec.Body, invalidServiceBody)
		}
	}
}

// A URL without the .git suffix matches no git route.
func TestRoutes_RequireDotGitSuffix(t *testing.T) {
	v := newEnv(t)
	if rec := v.do(t, http.MethodGet, "/git/myorg/ws/info/refs?service=git-upload-pack", "", v.key(t, "owner")); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"message":"Not Found"`) {
		t.Errorf("(%d, %q); want the router's 404", rec.Code, rec.Body)
	}
}

// upload-pack streams the pack for a plain or gzip-encoded request and
// answers an in-band ERR for an empty, malformed or badly compressed one.
func TestUploadPack_Requests(t *testing.T) {
	v := newEnv(t)
	owner := v.key(t, "owner")
	want := uploadPackWant(t, v.trunk("ws"))
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write([]byte(want))
	_ = w.Close()
	for name, c := range map[string]struct{ body, encoding, expect string }{
		"want and done": {want, "", "PACK"},
		"gzip":          {gz.String(), "gzip", "PACK"},
		"empty":         {"", "", "ERR empty upload-pack request"},
		"malformed":     {"not pkt-lines", "", "ERR "},
		"bad gzip":      {"not gzip", "gzip", "ERR invalid gzip request body"},
	} {
		req := httptest.NewRequest(http.MethodPost, wsPath+"/git-upload-pack", strings.NewReader(c.body))
		req.Header.Set("Authorization", basic(owner))
		if c.encoding != "" {
			req.Header.Set("Content-Encoding", c.encoding)
		}
		rec := v.serve(req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), c.expect) {
			t.Errorf("%s: (%d, %.80q); want 200 with %q", name, rec.Code, rec.Body, c.expect)
		}
	}
}

// Without a git executable, fetches get an in-band ERR instead of a pack.
func TestUploadPack_MissingGitIsInBandError(t *testing.T) {
	orig := gitBinary
	gitBinary = func() (string, error) { return "", errors.New("absent") }
	t.Cleanup(func() { gitBinary = orig })
	v := newEnv(t)
	owner := v.key(t, "owner")
	for _, r := range []struct{ method, path, body string }{
		{http.MethodGet, infoRefs + "git-upload-pack", ""},
		{http.MethodPost, wsPath + "/git-upload-pack", uploadPackWant(t, v.trunk("ws"))},
	} {
		if rec := v.do(t, r.method, r.path, r.body, owner); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ERR git executable not found") {
			t.Errorf("%s %s: (%d, %q); want an in-band ERR", r.method, r.path, rec.Code, rec.Body)
		}
	}
}

// bodyLimit stands for apikit's instance-wide body limit: a 413 for a body
// declared longer than max, a MaxBytesReader otherwise.
func bodyLimit(max int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().ContentLength > max {
				return c.NoContent(http.StatusRequestEntityTooLarge)
			}
			c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, max)
			return next(c)
		}
	}
}

// Git routes are exempt from the instance-wide body limit, which other routes
// keep: a push larger than the limit reaches the handler.
func TestRoutes_ExemptFromBodyLimit(t *testing.T) {
	e := echo.New()
	e.Use(bodyLimit(64))
	e.POST("/api/v1/other", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	v := newEnvOn(t, e)
	big := strings.Repeat("x", 4096)
	if rec := v.do(t, http.MethodPost, "/api/v1/other", big, ""); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("other route: %d; want 413", rec.Code)
	}
	rec := v.do(t, http.MethodPost, wsPath+"/git-receive-pack", big, v.key(t, "owner"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ERR") {
		t.Errorf("git route: (%d, %.60q); want 200 with the pack's in-band ERR, not a 413", rec.Code, rec.Body)
	}
}

// When the binding table cannot be created, MountGitHandlers fails and
// registers no route.
func TestMountGitHandlers_FailsWithoutBindingTable(t *testing.T) {
	db := testutil.OpenDB(t)
	testutil.Exec(t, db, `CREATE TABLE other (x TEXT)`)
	testutil.Exec(t, db, `CREATE INDEX token_workspace_bindings ON other(x)`)
	e := echo.New()
	if err := MountGitHandlers(e, db, t.TempDir()); err == nil {
		t.Error("MountGitHandlers = nil; want the DDL error")
	}
	if n := len(e.Routes()); n != 0 {
		t.Errorf("%d routes registered; want none", n)
	}
}
