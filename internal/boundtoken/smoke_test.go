package boundtoken

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/cli"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// runAFC runs the afc command tree against the running hub at base with the
// API key, and returns stdout, stderr and the exit code afc would use.
func runAFC(t *testing.T, base, apiKey string, args ...string) (string, string, int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ak"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ak", "config.toml"),
		[]byte("endpoint_url = \"\"\nuser_id = \"\"\napi_key = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := cli.BuildRootCommand()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"--endpoint-url", base, "--api-key", apiKey}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), cli.ExitCode(err)
}

// TS-34-64 (smoke, 34-PATH-1): An owner mints a bound token through afc
// against a real apikit server; the binding and hub.token.bind exist, and the
// token reaches its workspace and nothing else.
func TestSmoke_MintThroughAFC_TS_34_64(t *testing.T) {
	h := newRealHub(t)
	h.seed(t, "S", "active")
	h.seed(t, "X", "active")
	srv := httptest.NewServer(h.server.Echo())
	defer srv.Close()

	out, errOut, code := runAFC(t, srv.URL, h.apiKey, "tokens", "create", "--workspace", "S", "--name", "svc", "--permissions", "workspaces:read")
	if code != 0 {
		t.Fatalf("afc exit %d: %s %s", code, out, errOut)
	}
	var minted struct {
		TokenID   string `json:"token_id"`
		Token     string `json:"token"`
		Workspace string `json:"workspace"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&minted); err != nil || minted.Token == "" || minted.Workspace != "S" || dec.More() {
		t.Fatalf("stdout %q; want one JSON document with the token for S", out)
	}
	if lines := strings.Split(strings.TrimSpace(errOut), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "will not be shown again") {
		t.Errorf("stderr %q; want one warning line", errOut)
	}
	var slug string
	if err := h.db.QueryRow(`SELECT b.slug FROM token_workspace_bindings b JOIN pats p ON p.token_id = b.token_id WHERE b.token_id = ?`, minted.TokenID).Scan(&slug); err != nil || slug != "S" {
		t.Errorf("binding of %s: %q (%v); want a pats row bound to S", minted.TokenID, slug, err)
	}
	evs := h.hubEvents(t, "hub.token.bind")
	if len(evs) != 1 || evs[0]["resource_id"] != minted.TokenID || evs[0]["workspace"] != "S" {
		t.Errorf("hub.token.bind events %v; want one for %s on S", evs, minted.TokenID)
	}
	if rec := h.do(http.MethodGet, "/api/v1/workspaces/S", minted.Token, ""); rec.Code != http.StatusOK {
		t.Errorf("the token on S: %d; want 200", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/api/v1/workspaces/X", minted.Token, ""); rec.Code != http.StatusNotFound {
		t.Errorf("the token on X: %d; want 404", rec.Code)
	}
}

// TS-34-65 (smoke, 34-PATH-2): An owner lists the workspace's bound tokens
// through afc, a revoked one included, without any secret.
func TestSmoke_ListThroughAFC_TS_34_65(t *testing.T) {
	h := newRealHub(t)
	h.seed(t, "S", "active")
	srv := httptest.NewServer(h.server.Echo())
	defer srv.Close()

	var tokens, ids []string
	for _, name := range []string{"one", "two"} {
		out, _, code := runAFC(t, srv.URL, h.apiKey, "tokens", "create", "--workspace", "S", "--name", name, "--permissions", "workspaces:read")
		var m struct {
			TokenID string `json:"token_id"`
			Token   string `json:"token"`
		}
		if err := json.Unmarshal([]byte(out), &m); code != 0 || err != nil {
			t.Fatalf("minting %s: exit %d %s", name, code, out)
		}
		tokens, ids = append(tokens, m.Token), append(ids, m.TokenID)
	}
	if rec := h.do(http.MethodDelete, "/api/v1/user/tokens/"+ids[0], h.apiKey, ""); rec.Code != http.StatusOK {
		t.Fatalf("revoking %s through apikit: (%d, %s)", ids[0], rec.Code, rec.Body)
	}

	out, _, code := runAFC(t, srv.URL, h.apiKey, "tokens", "list", "--workspace", "S")
	var list []map[string]any
	if err := json.Unmarshal([]byte(out), &list); code != 0 || err != nil || len(list) != 2 {
		t.Fatalf("afc list: exit %d, %q; want one array of two", code, out)
	}
	for _, e := range list {
		revoked := e["revoked_at"] != nil
		if (e["token_id"] == ids[0]) != revoked {
			t.Errorf("entry %v: revoked_at %v; want it set only for %s", e["token_id"], e["revoked_at"], ids[0])
		}
	}
	for _, secret := range append(tokens, "secret_hash", `"token"`) {
		if strings.Contains(out, secret) {
			t.Errorf("the listing contains %q", secret)
		}
	}
}

// TS-34-66 (smoke, 34-PATH-3): Deleting a workspace revokes its bound token,
// records it in hub.workspace.delete, and the token then gets 401.
func TestSmoke_DeleteRevokes_TS_34_66(t *testing.T) {
	h := newRealHub(t)
	h.seed(t, "S", "active")
	rec := h.do(http.MethodPost, "/api/v1/workspaces/S/tokens", h.apiKey, `{"name":"svc","permissions":["workspaces:read"]}`)
	var m struct {
		TokenID string `json:"token_id"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); rec.Code != http.StatusCreated || err != nil {
		t.Fatalf("mint: (%d, %s)", rec.Code, rec.Body)
	}
	if _, err := h.db.Exec(`UPDATE workspaces SET status = 'archived' WHERE slug = 'S'`); err != nil {
		t.Fatal(err)
	}

	if rec := h.do(http.MethodDelete, "/api/v1/workspaces/S", h.apiKey, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: (%d, %s)", rec.Code, rec.Body)
	}
	var bindings int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM token_workspace_bindings WHERE slug = 'S'`).Scan(&bindings)
	var revoked *string
	_ = h.db.QueryRow(`SELECT revoked_at FROM pats WHERE token_id = ?`, m.TokenID).Scan(&revoked)
	if bindings != 0 || revoked == nil {
		t.Errorf("after the delete: %d bindings of S, revoked_at %v; want 0 and set", bindings, revoked)
	}
	evs := h.hubEvents(t, "hub.workspace.delete")
	if len(evs) != 1 {
		t.Fatalf("hub.workspace.delete events = %d; want 1", len(evs))
	}
	ids, _ := evs[0]["metadata"].(map[string]any)["revoked_tokens"].([]any)
	if len(ids) != 1 || ids[0] != m.TokenID {
		t.Errorf("revoked_tokens = %v; want [%s]", ids, m.TokenID)
	}
	if rec := h.do(http.MethodGet, "/api/v1/user/tokens", m.Token, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("the token after the delete: %d; want 401", rec.Code)
	}
}

// TS-34-67 (smoke, 34-PATH-4): A bound token's refused requests are audited
// once per minute, with the held-back count.
func TestSmoke_RefusalAudited_TS_34_67(t *testing.T) {
	h := newRealHub(t)
	h.seed(t, "S", "active")
	h.seed(t, "X", "active")
	token, id := h.mintBound(t, "S", "workspaces:read", "patches:read")
	clock := &fakeClock{t: t0}
	wsaccess.SetRefusalObserver(NewReporter(h.emitter, clock.now).Report)
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })

	refuse := func() {
		t.Helper()
		if rec := h.do(http.MethodGet, "/api/v1/workspaces/X/patches", token, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("GET X/patches: %d; want 404", rec.Code)
		}
	}
	refuse()
	clock.set(t0.Add(20 * time.Second))
	refuse()
	if evs := h.hubEvents(t, "hub.token.bound_refused"); len(evs) != 1 || evs[0]["metadata"].(map[string]any)["suppressed"] != float64(0) {
		t.Fatalf("events after two refusals within a minute: %v; want one with suppressed 0", evs)
	}
	clock.set(t0.Add(61 * time.Second))
	refuse()
	evs := h.hubEvents(t, "hub.token.bound_refused")
	if len(evs) != 2 {
		t.Fatalf("events = %d; want 2", len(evs))
	}
	md := evs[1]["metadata"].(map[string]any)
	if evs[1]["resource_id"] != id || evs[1]["workspace"] != "S" || evs[1]["actor_type"] != "pat" || evs[1]["actor_id"] != "u1" ||
		md["suppressed"] != float64(1) || md["requested_workspace"] != "X" || md["route"] != "/api/v1/workspaces/:slug/patches" || md["status"] != float64(404) {
		t.Errorf("second event %v; want token %s on S, suppressed 1, requested X", evs[1], id)
	}
}

// TS-34-68 (smoke, 34-PATH-5): The sweep started with go cleans up orphaned
// bindings at once, and returns after cancel.
func TestSmoke_SweepAtBoot_TS_34_68(t *testing.T) {
	h := newRealHub(t)
	h.seed(t, "S", "active")
	_, orphanID := h.mintBound(t, "gone", "workspaces:read")
	_, healthyID := h.mintBound(t, "S", "workspaces:read")
	if _, err := h.db.Exec(`INSERT INTO token_workspace_bindings (token_id, slug, created_at) VALUES ('no-such-token', 'S', 'x')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { StartBindingSweep(ctx, h.db); close(done) }()

	if !eventually(2*time.Second, func() bool { return countBindings(h.db) == 1 }) {
		t.Fatalf("bindings = %d; want only the healthy one", countBindings(h.db))
	}
	var left string
	_ = h.db.QueryRow(`SELECT token_id FROM token_workspace_bindings`).Scan(&left)
	if left != healthyID || !isRevoked(t, h.db, orphanID) || isRevoked(t, h.db, healthyID) {
		t.Errorf("left %s, orphan revoked %v, healthy revoked %v; want the healthy binding and only the orphan revoked",
			left, isRevoked(t, h.db, orphanID), isRevoked(t, h.db, healthyID))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep did not return after cancel")
	}
}
