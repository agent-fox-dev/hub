package workspace

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
)

// TS-38-53 (smoke, 38-PATH-1): The owner pushes a new branch with an escaped
// name to the forge through the API. Real: the apikit server with its auth and
// the binding guard, the SQLite database and secrets store, wslock, a trunk
// cloned by hub's clone function, go-git's push over HTTP to git http-backend,
// and the DuckDB-backed audit emitter.
func TestSmoke_OwnerPushesBranch_TS_38_53(t *testing.T) {
	hub := newRealHub(t)
	if err := secrets.InitSchema(hub.db.SqlDB); err != nil {
		t.Fatal(err)
	}
	duck, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { duck.Close() })
	if err := audit.InitSchema(duck); err != nil {
		t.Fatal(err)
	}
	oldStore, oldEmitter, oldMetrics := defaultAuditStore, defaultAuditEmitter, defaultAuditMetrics
	store := audit.NewStore(duck)
	SetAuditDependencies(store, audit.NewEmitter(store), nil)
	t.Cleanup(func() { SetAuditDependencies(oldStore, oldEmitter, oldMetrics) })
	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = t.TempDir()
	t.Cleanup(func() { defaultWorkspaceRoot = oldRoot })

	// The forge has main; the workspace stores a GIT_PAT and its git_url
	// carries userinfo.
	forge := newStandInForge(t)
	seed := filepath.Join(t.TempDir(), "seed")
	pushGit(t, filepath.Dir(seed), "init", "-q", "-b", "main", seed)
	pushCommit(t, seed, "README.md", "# widgets")
	pushGit(t, seed, "push", "-q", forge.bare, "main")
	gitURL := strings.Replace(forge.repoURL(), "http://", "http://deploy:urlpw@", 1)
	if _, err := secrets.NewStore(hub.db.SqlDB).CreateSecrets("workspace", "smoke38", []secrets.EntryInput{{Key: "GIT_PAT", Value: "tok-smoke-38"}}); err != nil {
		t.Fatal(err)
	}
	auth, err := resolveCloneAuth(secrets.NewStore(hub.db.SqlDB), "smoke38")
	if err != nil {
		t.Fatal(err)
	}
	trunk := filepath.Join(defaultWorkspaceRoot, "smoke38", "trunk")
	if _, err := defaultCloneFn(context.Background(), trunk, gitURL, 0, false, "", auth); err != nil {
		t.Fatalf("cloning the trunk: %v", err)
	}
	pushGit(t, trunk, "checkout", "-q", "-b", "fix/42-nil-map")
	tip := pushCommit(t, trunk, "fix.go", "package fix")
	pushGit(t, trunk, "checkout", "-q", "main")
	if err := insertWorkspace(hub.db.SqlDB, &Workspace{Slug: "smoke38", GitURL: gitURL, OwnerID: "u1", Status: "active", CloneStatus: "ready"}); err != nil {
		t.Fatal(err)
	}

	rec := hub.do(http.MethodPost, "/api/v1/workspaces/smoke38/branches/fix%2F42-nil-map/push", hub.apiKey, "")
	a := pushedOK(t, rec)
	if a.Branch != "fix/42-nil-map" || a.Result != "pushed" || a.SHA != tip || a.Remote != "origin" || a.RemoteURL != forge.repoURL() {
		t.Errorf("answer %+v; want fix/42-nil-map pushed at %s to %s", a, tip, forge.repoURL())
	}
	if got := bareRef(t, forge.bare, "refs/heads/fix/42-nil-map"); got != tip {
		t.Errorf("the forge's fix/42-nil-map = %q; want %s", got, tip)
	}
	auths, updates := forge.received()
	if len(updates) != 1 || len(updates[0]) != 1 || updates[0][0] != "refs/heads/fix/42-nil-map" {
		t.Errorf("ref updates %v", updates)
	}
	if auths[len(auths)-1] != "x-token-auth:tok-smoke-38" {
		t.Errorf("the push carried %q; want the stored GIT_PAT", auths[len(auths)-1])
	}

	rows, err := duck.Query(`SELECT actor_id, resource_id, workspace, metadata FROM hub_audit_events WHERE event_type = 'hub.branch.push'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		n++
		var actor, resource, ws, metadata string
		if err := rows.Scan(&actor, &resource, &ws, &metadata); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(metadata), &m)
		if actor != "u1" || resource != "fix/42-nil-map" || ws != "smoke38" || m["ok"] != true || m["result"] != "pushed" {
			t.Errorf("event actor %s, resource %s, workspace %s, metadata %v", actor, resource, ws, m)
		}
		if strings.Contains(metadata, "urlpw") || strings.Contains(metadata, "tok-smoke-38") {
			t.Errorf("the event holds a secret: %s", metadata)
		}
	}
	if n != 1 {
		t.Errorf("%d hub.branch.push events; want 1", n)
	}
	if strings.Contains(rec.Body.String(), "urlpw") {
		t.Errorf("the answer holds the userinfo: %s", rec.Body)
	}
	assertLockFree(t, "smoke38")
}
