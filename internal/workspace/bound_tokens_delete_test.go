package workspace

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitserver"
	"github.com/agent-fox-dev/hub/internal/secrets"
)

// createPatsTable creates apikit's pats table in a workspace test database.
func createPatsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS pats (
		token_id TEXT NOT NULL PRIMARY KEY,
		user_id TEXT NOT NULL,
		name TEXT NOT NULL,
		secret_hash TEXT NOT NULL,
		permissions TEXT NOT NULL,
		expires_days INTEGER NOT NULL,
		expires_at TEXT,
		revoked_at TEXT,
		created_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("creating pats: %v", err)
	}
}

// seedPAT inserts a pats row; revokedAt "" leaves the token live.
func seedPAT(t *testing.T, db *sql.DB, tokenID, revokedAt string) {
	t.Helper()
	var revoked any
	if revokedAt != "" {
		revoked = revokedAt
	}
	if _, err := db.Exec(`INSERT INTO pats (token_id, user_id, name, secret_hash, permissions, expires_days, revoked_at, created_at)
		VALUES (?, 'user-1', ?, 'hash', '[]', 90, ?, ?)`, tokenID, "n-"+tokenID, revoked, apikit.NowUTC()); err != nil {
		t.Fatalf("seeding PAT %s: %v", tokenID, err)
	}
}

func revokedAt(t *testing.T, db *sql.DB, tokenID string) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(`SELECT revoked_at FROM pats WHERE token_id = ?`, tokenID).Scan(&v); err != nil {
		t.Fatalf("revoked_at of %s: %v", tokenID, err)
	}
	return v
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TS-34-28: deleteWorkspace revokes the live bound tokens and removes the
// bindings in its transaction, leaving other workspaces' bindings alone.
// Requirement: 34-REQ-5.1
func TestDeleteWorkspace_RevokesBoundTokens_TS_34_28(t *testing.T) {
	db := openTestDB(t)
	createPatsTable(t, db)
	for _, slug := range []string{"s", "t"} {
		if err := insertWorkspace(db, &Workspace{Slug: slug, GitURL: "https://github.com/org/" + slug, OwnerID: "user-1", Status: "archived"}); err != nil {
			t.Fatal(err)
		}
	}
	seedPAT(t, db, "live", "")
	seedPAT(t, db, "old", "2026-01-01T00:00:00Z")
	seedPAT(t, db, "other", "")
	bindToken(t, db, "live", "s")
	bindToken(t, db, "old", "s")
	bindToken(t, db, "other", "t")

	if err := deleteWorkspace(db, "s"); err != nil {
		t.Fatalf("deleteWorkspace = %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM workspaces WHERE slug = 's'`); n != 0 {
		t.Errorf("workspace s still exists")
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM token_workspace_bindings WHERE slug = 's'`); n != 0 {
		t.Errorf("bindings of s = %d; want 0", n)
	}
	if !revokedAt(t, db, "live").Valid {
		t.Error("the live bound token is not revoked")
	}
	if v := revokedAt(t, db, "old"); v.String != "2026-01-01T00:00:00Z" {
		t.Errorf("the already revoked token's revoked_at = %q; want it unchanged", v.String)
	}
	if revokedAt(t, db, "other").Valid || countRows(t, db, `SELECT COUNT(*) FROM token_workspace_bindings WHERE slug = 't'`) != 1 {
		t.Error("workspace t's binding or token changed")
	}
}

// TS-34-29: Deleting a workspace with no bindings issues no statement against
// pats: it succeeds on a database that has no pats table.
// Requirement: 34-REQ-5.2
func TestDeleteWorkspace_NoBindingsNoPats_TS_34_29(t *testing.T) {
	db := openTestDB(t)
	if err := insertWorkspace(db, &Workspace{Slug: "s", GitURL: "https://github.com/org/s", OwnerID: "user-1", Status: "archived"}); err != nil {
		t.Fatal(err)
	}
	if err := deleteWorkspace(db, "s"); err != nil {
		t.Fatalf("deleteWorkspace without a pats table = %v; want nil", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM workspaces WHERE slug = 's'`); n != 0 {
		t.Error("workspace s still exists")
	}
}

// TS-34-30: A failure while reading, revoking or unbinding rolls everything
// back, and DELETE answers 500 failed to delete workspace.
// Requirement: 34-REQ-5.3
func TestDeleteWorkspace_FailureRollsBack_TS_34_30(t *testing.T) {
	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = t.TempDir()
	defer func() { defaultWorkspaceRoot = oldRoot }()

	injectors := map[string]string{
		"bindings unreadable": `DROP TABLE token_workspace_bindings`,
		"pats update fails":   `CREATE TRIGGER block_revoke BEFORE UPDATE ON pats BEGIN SELECT RAISE(ABORT, 'blocked'); END`,
		"unbind fails":        `CREATE TRIGGER block_unbind BEFORE DELETE ON token_workspace_bindings BEGIN SELECT RAISE(ABORT, 'blocked'); END`,
	}
	for name, stmt := range injectors {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t)
			createPatsTable(t, env.db)
			env.seedWorkspace(t, &Workspace{Slug: "s", GitURL: "https://github.com/org/s", OwnerID: "user-1", Status: "archived"})
			seedPAT(t, env.db, "live", "")
			bindToken(t, env.db, "live", "s")
			if _, err := env.db.Exec(stmt); err != nil {
				t.Fatal(err)
			}

			rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/s", "", userAuth("user-1"))
			if rec.Code != http.StatusInternalServerError || parseErrorEnvelope(t, rec).Error.Message != "failed to delete workspace" {
				t.Fatalf("DELETE: status %d; want 500 failed to delete workspace", rec.Code)
			}
			if n := countRows(t, env.db, `SELECT COUNT(*) FROM workspaces WHERE slug = 's'`); n != 1 {
				t.Error("the workspace row is gone; want the transaction rolled back")
			}
			if name != "bindings unreadable" {
				if n := countRows(t, env.db, `SELECT COUNT(*) FROM token_workspace_bindings`); n != 1 {
					t.Errorf("bindings = %d; want 1", n)
				}
			}
			if revokedAt(t, env.db, "live").Valid {
				t.Error("the token is revoked; want it unchanged")
			}
		})
	}
}

// TS-34-31: hub.workspace.delete carries revoked_tokens, the sorted ids this
// deletion revoked, [] when none. Requirement: 34-REQ-5.4
func TestDeleteWorkspace_RevokedTokensMetadata_TS_34_31(t *testing.T) {
	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = t.TempDir()
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env := newAuditTestEnv(t)
	db := env.testEnv.db
	createPatsTable(t, db)
	for _, slug := range []string{"s", "e"} {
		env.testEnv.seedWorkspace(t, &Workspace{Slug: slug, GitURL: "https://github.com/org/" + slug, OwnerID: "user-1", Status: "archived"})
	}
	seedPAT(t, db, "tok_b", "")
	seedPAT(t, db, "tok_a", "")
	seedPAT(t, db, "tok_c", "2026-01-01T00:00:00Z")
	for _, id := range []string{"tok_b", "tok_a", "tok_c"} {
		bindToken(t, db, id, "s")
	}

	for _, slug := range []string{"s", "e"} {
		if rec := env.testEnv.doRequest(t, http.MethodDelete, "/api/v1/workspaces/"+slug, "", userAuth("user-1")); rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s: status %d: %s", slug, rec.Code, rec.Body)
		}
	}
	want := map[string][]string{"s": {"tok_a", "tok_b"}, "e": {}}
	seen := 0
	for _, ev := range env.emitter.Events() {
		if ev.EventType != "hub.workspace.delete" {
			continue
		}
		seen++
		got, ok := ev.Metadata["revoked_tokens"].([]string)
		if !ok || got == nil || !slices.Equal(got, want[ev.Workspace]) {
			t.Errorf("%s: revoked_tokens = %#v; want %#v", ev.Workspace, ev.Metadata["revoked_tokens"], want[ev.Workspace])
		}
	}
	if seen != 2 {
		t.Errorf("hub.workspace.delete events = %d; want 2", seen)
	}
}

// TS-34-32: Archiving and reactivating leave bindings and pats untouched.
// Requirement: 34-REQ-5.5
func TestArchiveReactivate_KeepTokens_TS_34_32(t *testing.T) {
	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = t.TempDir()
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env := newTestEnv(t)
	createPatsTable(t, env.db)
	env.seedWorkspace(t, &Workspace{Slug: "s", GitURL: "https://github.com/org/s", OwnerID: "user-1", Status: "active"})
	seedPAT(t, env.db, "live", "")
	seedPAT(t, env.db, "old", "2026-01-01T00:00:00Z")
	bindToken(t, env.db, "live", "s")
	bindToken(t, env.db, "old", "s")
	dump := func() string {
		rows, err := env.db.Query(`SELECT b.token_id || b.slug || b.created_at || COALESCE(p.revoked_at, '-') FROM token_workspace_bindings b JOIN pats p USING (token_id) ORDER BY b.token_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return strings.Join(out, ";")
	}
	before := dump()
	for _, action := range []string{"archive", "reactivate"} {
		if rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/s/"+action, "", userAuth("user-1")); rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", action, rec.Code, rec.Body)
		}
		if after := dump(); after != before {
			t.Errorf("after %s: %s; want %s", action, after, before)
		}
	}
}

// TS-34-33: After its workspace is deleted, a bound token is refused by
// apikit's REST authentication and by the git server. Requirement: 34-REQ-5.6
func TestDeletedWorkspace_TokenRefused_TS_34_33(t *testing.T) {
	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = t.TempDir()
	defer func() { defaultWorkspaceRoot = oldRoot }()

	h := newRealHub(t)
	// As cmd/af-hub does: deletion also clears the workspace's secrets.
	if err := secrets.InitSchema(h.db.SqlDB); err != nil {
		t.Fatal(err)
	}
	if err := gitserver.MountGitHandlers(h.server.Echo(), h.db.SqlDB, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	h.seed(t, "s", "u1", "archived")
	token, id := h.mintPAT(t, "workspaces:read", "git:read")
	bindToken(t, h.db.SqlDB, id, "s")
	// The git server reads its own af_ prefix; apikit's prefix under go test
	// is apikit.TokenPrefix.
	gitToken := "af_pat_" + strings.TrimPrefix(token, apikit.TokenPrefix+"_pat_")

	gitFetch := func() int {
		req := httptest.NewRequest(http.MethodGet, "/git/u1/s.git/info/refs?service=git-upload-pack", nil)
		req.SetBasicAuth("x", gitToken)
		rec := httptest.NewRecorder()
		h.server.Echo().ServeHTTP(rec, req)
		return rec.Code
	}
	if rec := h.do(http.MethodGet, "/api/v1/user/tokens", token, ""); rec.Code == http.StatusUnauthorized {
		t.Fatalf("before the delete the token is already refused: %s", rec.Body)
	}
	if code := gitFetch(); code == http.StatusUnauthorized {
		t.Fatal("before the delete the git server already refuses the token")
	}

	if rec := h.do(http.MethodDelete, "/api/v1/workspaces/s", h.apiKey, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d: %s", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodGet, "/api/v1/user/tokens", token, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("REST after the delete: status %d; want 401: %s", rec.Code, rec.Body)
	}
	if code := gitFetch(); code != http.StatusUnauthorized {
		t.Errorf("git after the delete: status %d; want 401", code)
	}
}

// TS-34-34: The workspace schema initialisation creates the binding table,
// idempotently. Requirement: 34-REQ-5.7
func TestInitSchema_CreatesBindingTable_TS_34_34(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := initSchema(db); err != nil {
		t.Fatal(err)
	}
	bindToken(t, db, "t1", "s")
	if err := initSchema(db); err != nil {
		t.Fatalf("second initSchema = %v", err)
	}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('token_workspace_bindings') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	rows.Close()
	if !slices.Equal(cols, []string{"token_id", "slug", "created_at"}) {
		t.Errorf("columns = %v; want token_id, slug, created_at", cols)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM token_workspace_bindings`); n != 1 {
		t.Errorf("rows after the second init = %d; want 1", n)
	}
}
