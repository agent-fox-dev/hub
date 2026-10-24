package gitserver

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/workspace"
)

// seedGitPAT inserts a PAT in the simple test format af_pat_<tokenID>.
func seedGitPAT(t *testing.T, db *sql.DB, tokenID, userID string, perms ...string) {
	t.Helper()
	if perms == nil {
		perms = []string{}
	}
	permsJSON, _ := json.Marshal(perms)
	if _, err := db.Exec(`INSERT INTO pats (token_id, user_id, name, secret_hash, permissions, expires_days, created_at)
		VALUES (?, ?, ?, ?, ?, 365, ?)`, tokenID, userID, "t-"+tokenID, hashCredential(tokenID), string(permsJSON),
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seeding PAT %s: %v", tokenID, err)
	}
}

// bindGitToken writes a binding row for tokenID naming slug.
func bindGitToken(t *testing.T, db *sql.DB, tokenID, slug string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO token_workspace_bindings (token_id, slug, created_at) VALUES (?, ?, ?)`,
		tokenID, slug, apikit.NowUTC()); err != nil {
		t.Fatalf("binding %s to %s: %v", tokenID, slug, err)
	}
}

// newBindingGitEnv is a git test env with org myorg and two repositories, s
// and x, both owned by user-a.
func newBindingGitEnv(t *testing.T) *gitTestEnv {
	t.Helper()
	env := newGitTestEnv(t)
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	for _, slug := range []string{"s", "x"} {
		env.seedWorkspace(t, slug, "https://github.com/org/"+slug, "user-a", "org-1", "ready")
		env.initWorkspaceRepo(t, slug)
	}
	return env
}

func (env *gitTestEnv) infoRefs(t *testing.T, slug, service, password string) (int, string) {
	t.Helper()
	rec := env.doRequest(t, http.MethodGet, "/git/myorg/"+slug+".git/info/refs?service="+service, "", withBasicAuth("x", password))
	return rec.Code, rec.Body.String()
}

func tableExistsGit(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// TS-33-4: MountGitHandlers creates the binding table so a PAT with a TokenID
// and no binding is not answered with 500. Requirement: 33-REQ-1.4
func TestMountGitHandlers_CreatesBindingTable_TS_33_4(t *testing.T) {
	db := openTestDB(t)
	if tableExistsGit(t, db, "token_workspace_bindings") {
		t.Fatal("binding table exists before MountGitHandlers")
	}
	e := echo.New()
	env := &gitTestEnv{echo: e, db: db, workspaceRoot: t.TempDir()}
	if err := MountGitHandlers(e, db, env.workspaceRoot); err != nil {
		t.Fatalf("MountGitHandlers = %v; want nil", err)
	}
	if !tableExistsGit(t, db, "token_workspace_bindings") {
		t.Fatal("MountGitHandlers did not create token_workspace_bindings")
	}

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "ws", "https://github.com/org/ws", "user-1", "org-1", "ready")
	env.initWorkspaceRepo(t, "ws")
	rec := env.doRequest(t, http.MethodGet, "/git/myorg/ws.git/info/refs?service=git-upload-pack", "", withBasicAuth("x", "af_pat_abc123"))
	if rec.Code != http.StatusOK {
		t.Errorf("fetch by an unbound PAT with a TokenID: status %d; want 200: %q", rec.Code, rec.Body)
	}
}

// TS-33-5: Both mount functions return an error and register nothing further
// when the binding table cannot be created. Requirement: 33-REQ-1.5
func TestMountFunctions_FailOnBindingDDL_TS_33_5(t *testing.T) {
	db := openTestDB(t)
	for _, stmt := range []string{`CREATE TABLE other (x TEXT)`, `CREATE INDEX token_workspace_bindings ON other(x)`} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	e := echo.New()
	before := len(e.Routes())
	if err := MountGitHandlers(e, db, t.TempDir()); err == nil {
		t.Error("MountGitHandlers = nil; want the binding DDL error")
	}
	if after := len(e.Routes()); after != before {
		t.Errorf("routes after the failed MountGitHandlers = %d; want %d", after, before)
	}
	rec := httptestDo(e, http.MethodGet, "/git/myorg/ws.git/info/refs?service=git-upload-pack")
	if rec.Code != http.StatusNotFound {
		t.Errorf("git request after the failed mount: status %d; want the router's 404", rec.Code)
	}

	// MountWorkspaceHandlers on a real apikit database whose workspaces
	// schema succeeds but whose binding DDL fails.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	config := "[server]\nport = 0\n\n[database]\npath = \"" + filepath.Join(dir, "hub.db") + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := apikit.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	database, err := apikit.OpenDatabase(cfg.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, stmt := range []string{`CREATE TABLE other (x TEXT)`, `CREATE INDEX token_workspace_bindings ON other(x)`} {
		if _, err := database.SqlDB.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	server := apikit.NewServer(cfg, func() error { return nil })
	routesBefore := len(server.Echo().Routes())
	if err := workspace.MountWorkspaceHandlers(server, database); err == nil {
		t.Error("MountWorkspaceHandlers = nil; want the binding DDL error")
	}
	if n := len(server.Echo().Routes()); n != routesBefore {
		t.Errorf("routes after the failed MountWorkspaceHandlers = %d; want %d", n, routesBefore)
	}
}

// TS-33-42: A bound token requesting another owned repository gets the
// non-owner pkt-line 404. Requirement: 33-REQ-7.1
func TestGitBinding_OtherRepoIs404_TS_33_42(t *testing.T) {
	env := newBindingGitEnv(t)
	seedGitPAT(t, env.db, "boundread", "user-a", "git:read")
	bindGitToken(t, env.db, "boundread", "s")

	for _, service := range []string{"git-upload-pack", "git-receive-pack"} {
		code, body := env.infoRefs(t, "x", service, "af_pat_boundread")
		ncode, nbody := env.infoRefs(t, "x", service, "af_pat_userB_read")
		if code != http.StatusNotFound || !strings.Contains(body, "ERR repository not found") {
			t.Errorf("%s on x: (%d, %q); want pkt-line 404 repository not found", service, code, body)
		}
		if code != ncode || body != nbody {
			t.Errorf("%s on x: bound (%d, %q); want the non-owner's (%d, %q)", service, code, body, ncode, nbody)
		}
		assertPktLineErrorBody(t, body, "bound token on another repository")
	}
}

// TS-33-43: On S the existing git scope checks decide for a bound token.
// Requirement: 33-REQ-7.2
func TestGitBinding_OwnRepoScopesDecide_TS_33_43(t *testing.T) {
	env := newBindingGitEnv(t)
	seedGitPAT(t, env.db, "bread", "user-a", "git:read")
	seedGitPAT(t, env.db, "uread", "user-a", "git:read")
	seedGitPAT(t, env.db, "bwrite", "user-a", "git:read", "git:write")
	seedGitPAT(t, env.db, "uwrite", "user-a", "git:read", "git:write")
	bindGitToken(t, env.db, "bread", "s")
	bindGitToken(t, env.db, "bwrite", "s")

	for _, pair := range [][2]string{{"bread", "uread"}, {"bwrite", "uwrite"}} {
		for _, service := range []string{"git-upload-pack", "git-receive-pack"} {
			bcode, bbody := env.infoRefs(t, "s", service, "af_pat_"+pair[0])
			ucode, ubody := env.infoRefs(t, "s", service, "af_pat_"+pair[1])
			if bcode != ucode || bbody != ubody {
				t.Errorf("%s %s on s: bound (%d, %q); want the unbound PAT's (%d, %q)", pair[0], service, bcode, bbody, ucode, ubody)
			}
			want := http.StatusOK
			if pair[0] == "bread" && service == "git-receive-pack" {
				want = http.StatusForbidden
			}
			if bcode != want {
				t.Errorf("%s %s on s: status %d; want %d", pair[0], service, bcode, want)
			}
		}
	}
}

// TS-33-44: A git binding lookup failure is a pkt-line 500 and the repository
// is not served. Requirement: 33-REQ-7.3
func TestGitBinding_LookupErrorIs500_TS_33_44(t *testing.T) {
	env := newBindingGitEnv(t)
	if _, err := env.db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	code, body := env.infoRefs(t, "s", "git-upload-pack", "af_pat_readpat")
	if code != http.StatusInternalServerError || !strings.Contains(body, "ERR ") {
		t.Errorf("(%d, %q); want a pkt-line 500", code, body)
	}
	if strings.Contains(body, "refs/heads") || strings.Contains(body, "HEAD") {
		t.Errorf("body %q advertises refs; the repository must not be served", body)
	}
}

// TS-33-45: api_key and admin_token git requests run without a binding query.
// Requirement: 33-REQ-7.4
func TestGitBinding_NoQueryForKeysAndAdmin_TS_33_45(t *testing.T) {
	env := newBindingGitEnv(t)
	if _, err := env.db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cred string
		want int
	}{
		{"af_key_userA", http.StatusOK},
		{"af_admin_test789", http.StatusOK},
		{"af_key_userB", http.StatusNotFound},
	} {
		code, body := env.infoRefs(t, "s", "git-upload-pack", tc.cred)
		if code != tc.want {
			t.Errorf("%s: (%d, %q); want %d without a binding query", tc.cred, code, body, tc.want)
		}
	}
}

// httptestDo sends a request without credentials to e.
func httptestDo(e *echo.Echo, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}
