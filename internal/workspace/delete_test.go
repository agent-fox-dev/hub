package workspace

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// seedPAT inserts a pats row of owner; revokedAt "" leaves the token live.
func seedPAT(t *testing.T, db *sql.DB, tokenID, revokedAt string) {
	t.Helper()
	var revoked any
	if revokedAt != "" {
		revoked = revokedAt
	}
	testutil.Exec(t, db, `INSERT INTO pats (token_id, user_id, name, secret_hash, permissions, expires_days, revoked_at, created_at)
		VALUES (?, ?, ?, 'hash', '[]', 90, ?, ?)`, tokenID, owner, "n-"+tokenID, revoked, apikit.NowUTC())
}

func revokedAt(t *testing.T, db *sql.DB, tokenID string) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(`SELECT revoked_at FROM pats WHERE token_id = ?`, tokenID).Scan(&v); err != nil {
		t.Fatalf("revoked_at of %s: %v", tokenID, err)
	}
	return v
}

// TestDelete_Cascade: deleting an archived workspace removes its row, its
// directory, its patches, secrets and variables, revokes the live tokens bound
// to it and their bindings, force-closes its sessions and records the revoked
// tokens; other workspaces' data is untouched.
func TestDelete_Cascade(t *testing.T) {
	v := newEnv(t)
	v.orgWorkspace(t, "team")
	testutil.Exec(t, v.db, `UPDATE workspaces SET status = 'archived' WHERE slug = 'team'`)
	v.seed(t, &Workspace{Slug: "other", OwnerID: owner})
	v.seedPatch(t, "other", "feature/a", 1)
	marker := markerDir(t, v, "team")
	store := secrets.NewStore(v.db)
	for _, slug := range []string{"team", "other"} {
		storeSecrets(t, v.db, slug, "GIT_PAT", "tok")
		if _, err := store.CreateVariables("workspace", slug, []secrets.EntryInput{{Key: "K", Value: "v"}}); err != nil {
			t.Fatal(err)
		}
	}
	seedPAT(t, v.db, "live", "")
	seedPAT(t, v.db, "old", "2026-01-01T00:00:00Z")
	seedPAT(t, v.db, "elsewhere", "")
	testutil.BindToken(t, v.db, "live", "team")
	testutil.BindToken(t, v.db, "old", "team")
	testutil.BindToken(t, v.db, "elsewhere", "other")
	auditStore := &fakeAuditStore{results: []audit.ForceCloseResult{{SessionID: "s1"}}}
	swap(t, &defaultAuditStore, audit.Store(auditStore))

	rec := v.do(t, http.MethodDelete, "/api/v1/workspaces/team", "", testutil.PAT(owner, "workspaces:write"))
	expectStatus(t, rec, http.StatusNoContent)
	counts := map[string]int{
		`SELECT COUNT(*) FROM workspaces WHERE slug = 'team'`:                0,
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = 'team'`:         0,
		`SELECT COUNT(*) FROM secrets WHERE owner_id = 'team'`:               0,
		`SELECT COUNT(*) FROM variables WHERE owner_id = 'team'`:             0,
		`SELECT COUNT(*) FROM token_workspace_bindings WHERE slug = 'team'`:  0,
		`SELECT COUNT(*) FROM workspaces WHERE slug = 'other'`:               1,
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = 'other'`:        1,
		`SELECT COUNT(*) FROM secrets WHERE owner_id = 'other'`:              1,
		`SELECT COUNT(*) FROM variables WHERE owner_id = 'other'`:            1,
		`SELECT COUNT(*) FROM token_workspace_bindings WHERE slug = 'other'`: 1,
		`SELECT COUNT(*) FROM org_members WHERE org_id = 'org-acme'`:         2,
	}
	for q, want := range counts {
		if n := testutil.Count(t, v.db, q); n != want {
			t.Errorf("%s = %d; want %d", q, n, want)
		}
	}
	if !revokedAt(t, v.db, "live").Valid || revokedAt(t, v.db, "old").String != "2026-01-01T00:00:00Z" || revokedAt(t, v.db, "elsewhere").Valid {
		t.Error("want live revoked, old unchanged and elsewhere untouched")
	}
	if _, err := os.Stat(filepath.Join(v.root, "team")); !os.IsNotExist(err) {
		t.Error("the workspace directory survived")
	}
	_ = marker
	if auditStore.calls[0] != "team|workspace deleted" || len(v.events.ofType("hub.session.force_closed")) != 1 {
		t.Errorf("force-close %v, events %+v", auditStore.calls, v.events.ofType("hub.session.force_closed"))
	}
	ev := v.events.one(t, "hub.workspace.delete")
	if revoked, _ := ev.Metadata["revoked_tokens"].([]string); len(revoked) != 1 || revoked[0] != "live" || ev.Workspace != "team" {
		t.Errorf("event %+v", ev)
	}
}

// TestDelete_Refusals: only an archived workspace is deleted, only by its
// owner or an admin, and a failure leaves everything in place.
func TestDelete_Refusals(t *testing.T) {
	v := newEnv(t)
	v.orgWorkspace(t, "team")
	v.seed(t, &Workspace{Slug: "done", OwnerID: owner, Status: "archived"})
	del := func(slug string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
		return v.do(t, http.MethodDelete, "/api/v1/workspaces/"+slug, "", auth)
	}
	expectError(t, del("team", testutil.User(owner)), http.StatusConflict, "must be archived before deletion", "")
	expectStatus(t, del("done", nil), http.StatusUnauthorized)
	expectError(t, del("done", testutil.PAT(owner, "workspaces:read")), http.StatusNotFound, "workspace not found", "")
	expectError(t, del("done", testutil.User(stranger)), http.StatusNotFound, "workspace not found", "")
	expectError(t, del("nope", testutil.Admin()), http.StatusNotFound, "workspace not found", "")
	testutil.Exec(t, v.db, `UPDATE workspaces SET status = 'archived' WHERE slug = 'team'`)
	expectError(t, del("team", testutil.User(member)), http.StatusForbidden, "read-only access", "forbidden")

	// A failing cascade rolls the whole deletion back.
	testutil.BindToken(t, v.db, "t1", "done")
	seedPAT(t, v.db, "t1", "")
	testutil.Exec(t, v.db, `DROP TABLE secrets`)
	expectError(t, del("done", testutil.User(owner)), http.StatusInternalServerError, "failed to delete workspace", "")
	if testutil.Count(t, v.db, `SELECT COUNT(*) FROM workspaces WHERE slug = 'done'`) != 1 ||
		testutil.Count(t, v.db, `SELECT COUNT(*) FROM token_workspace_bindings WHERE slug = 'done'`) != 1 || revokedAt(t, v.db, "t1").Valid {
		t.Error("the failed deletion was not rolled back")
	}
	if len(v.events.ofType("hub.workspace.delete")) != 0 {
		t.Error("a refused deletion emitted an event")
	}
}

// TestDelete_WithoutBindings: a workspace with no bound tokens is deleted
// without touching the pats table, and the admin may delete it.
func TestDelete_WithoutBindings(t *testing.T) {
	v := newEnv(t)
	v.seed(t, &Workspace{Slug: "done", OwnerID: owner, Status: "archived"})
	testutil.Exec(t, v.db, `DROP TABLE pats`)
	expectStatus(t, v.do(t, http.MethodDelete, "/api/v1/workspaces/done", "", testutil.Admin()), http.StatusNoContent)
	if ev := v.events.one(t, "hub.workspace.delete"); len(ev.Metadata["revoked_tokens"].([]string)) != 0 || ev.ActorType != "admin_token" {
		t.Errorf("event %+v", ev)
	}
}
