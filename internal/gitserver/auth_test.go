package gitserver

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// authenticate runs GitAuthMiddleware on a request carrying header as
// Authorization and returns the identity it attached and the response.
func authenticate(t *testing.T, v *env, header string) (*apikit.AuthInfo, *httptest.ResponseRecorder) {
	t.Helper()
	var info *apikit.AuthInfo
	next := func(c echo.Context) error {
		info = apikit.GetAuthInfo(c)
		return c.NoContent(http.StatusOK)
	}
	req := httptest.NewRequest(http.MethodGet, infoRefs+"git-upload-pack", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	if err := GitAuthMiddleware(v.db)(next)(echo.New().NewContext(req, rec)); err != nil {
		t.Fatalf("middleware: %v", err)
	}
	return info, rec
}

// Every hub credential format in the Basic password is resolved to its
// identity, whatever the username; the username is never consulted.
func TestGitAuth_ResolvesCredentialFormats(t *testing.T) {
	v := newEnv(t)
	seedPAT(t, v.db, "split", "s3cret", "owner", "git:write")
	seedKey(t, v.db, "k2", "ksecret", "member")
	seedKey(t, v.db, "lk", "lsecret", "stranger")
	simple := v.pat(t, "owner", "git:read")
	for name, c := range map[string]struct{ cred, user, typ, role string }{
		"simple PAT":     {simple, "owner", "pat", "user"},
		"apikit PAT":     {"af_pat_split_s3cret", "owner", "pat", "user"},
		"simple key":     {v.key(t, "member"), "member", "api_key", "user"},
		"split key":      {"af_key_k2_ksecret", "member", "api_key", "user"},
		"login key":      {"af_lk_lsecret", "stranger", "api_key", "user"},
		"admin token":    {v.admin(t), "", "admin_token", "admin"},
		"empty username": {"", "owner", "pat", "user"},
	} {
		header := basic(c.cred)
		if name == "empty username" {
			header = "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+simple))
		}
		info, rec := authenticate(t, v, header)
		if info == nil || rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, no identity", name, rec.Code)
		}
		if info.CredentialType != c.typ || info.UserID != c.user || info.Role != c.role {
			t.Errorf("%s: %s %q role %q; want %s %q role %q", name, info.CredentialType, info.UserID, info.Role, c.typ, c.user, c.role)
		}
	}
	info, _ := authenticate(t, v, basic(simple))
	if len(info.Permissions) != 1 || info.Permissions[0] != "git:read" || info.TokenID == "" {
		t.Errorf("PAT identity %+v; want its scopes and token id", info)
	}
}

// A request without credentials is challenged with the af-hub realm; a
// request with bad ones is refused without a challenge. Neither reaches the
// handler.
func TestGitAuth_RefusesMissingAndInvalidCredentials(t *testing.T) {
	v := newEnv(t)
	seedPAT(t, v.db, "revoked", "revoked", "owner", "git:read")
	testutil.Exec(t, v.db, `UPDATE pats SET revoked_at = ? WHERE token_id = 'revoked'`, apikit.NowUTC())
	seedPAT(t, v.db, "expired", "expired", "owner", "git:read")
	testutil.Exec(t, v.db, `UPDATE pats SET expires_at = '2000-01-01T00:00:00Z' WHERE token_id = 'expired'`)
	blockedKey, blockedPAT := v.key(t, "stranger"), v.pat(t, "stranger", "git:read")
	testutil.Exec(t, v.db, `UPDATE users SET status = 'blocked' WHERE id = 'stranger'`)
	for name, header := range map[string]string{
		"no header":        "",
		"not Basic":        "Bearer " + v.key(t, "owner"),
		"not base64":       "Basic !!!",
		"no colon":         "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon")),
		"unknown PAT":      basic("af_pat_nobody"),
		"wrong PAT secret": basic("af_pat_split_wrong"),
		"unknown key":      basic("af_key_nobody"),
		"wrong login key":  basic("af_abc_wrong"),
		"unknown admin":    basic("af_admin_wrong"),
		"no prefix":        basic("password"),
		"revoked PAT":      basic("af_pat_revoked"),
		"expired PAT":      basic("af_pat_expired"),
		"blocked user key": basic(blockedKey),
		"blocked user PAT": basic(blockedPAT),
	} {
		info, rec := authenticate(t, v, header)
		if info != nil || rec.Code != http.StatusUnauthorized || rec.Body.Len() != 0 {
			t.Errorf("%s: identity %v, (%d, %q); want none and an empty 401", name, info, rec.Code, rec.Body)
		}
		challenge := rec.Header().Get("WWW-Authenticate")
		if want := `Basic realm="af-hub"`; (header == "" && challenge != want) || (header != "" && challenge != "") {
			t.Errorf("%s: WWW-Authenticate %q", name, challenge)
		}
	}
}

// Authentication runs before the resolver: a blocked user is answered 401
// even when the workspace lookup could not run.
func TestGitAuth_RunsBeforeResolver(t *testing.T) {
	v := newEnv(t)
	key := v.key(t, "owner")
	testutil.Exec(t, v.db, `UPDATE users SET status = 'blocked' WHERE id = 'owner'`)
	testutil.Exec(t, v.db, `DROP TABLE workspaces`)
	for _, r := range gitRoutes {
		if rec := v.do(t, r.method, r.path, "", key); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: %d; want 401", r.method, r.path, rec.Code)
		}
	}
}
