package gitserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// fakeBodyLimit mimics apikit's global body-size-limit middleware: it rejects
// requests whose Content-Length exceeds maxBytes with 413 and wraps the body
// in an http.MaxBytesReader otherwise.
func fakeBodyLimit(maxBytes int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Body != nil && c.Request().ContentLength > maxBytes {
				return c.String(http.StatusRequestEntityTooLarge, "payload too large")
			}
			if c.Request().Body != nil {
				c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, maxBytes)
			}
			return next(c)
		}
	}
}

// A git push whose pack exceeds the instance-wide body limit must still reach
// the receive-pack handler, while non-git routes keep the limit.
func TestGitBodyPassthrough_ExemptsGitRoutesFromBodyLimit(t *testing.T) {
	db := openTestDB(t)
	e := echo.New()
	e.Use(fakeBodyLimit(64))
	e.POST("/api/v1/other", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	wsRoot := t.TempDir()
	if err := MountGitHandlers(e, db, wsRoot); err != nil {
		t.Fatalf("MountGitHandlers: %v", err)
	}
	env := &gitTestEnv{echo: e, db: db, workspaceRoot: wsRoot}
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")

	big := strings.Repeat("x", 4096)

	// Control: a non-git route is still limited.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/other", strings.NewReader(big))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("non-git route: status = %d; want 413", rec.Code)
	}

	// The git route must not be rejected by the limit. The garbage body
	// fails pkt-line decoding, which the handler reports in-band with a
	// 200 and an ERR pkt-line, never with a 413.
	rec = env.doRequest(t, http.MethodPost, "/git/myorg/myws.git/git-receive-pack", big,
		withBasicAuth("x-token-auth", "af_key_user1"))
	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("git route was rejected by the body limit (413)")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("git route: status = %d; want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ERR") {
		t.Fatalf("expected an in-band ERR pkt-line for a garbage pack; got %q", truncate(rec.Body.String(), 200))
	}
}
