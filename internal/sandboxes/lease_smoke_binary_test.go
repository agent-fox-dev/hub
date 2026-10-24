package sandboxes

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// callHub sends a request to a running af-hub with the admin token and returns
// the status and the body.
func callHub(t *testing.T, env *afHubEnv, method, path, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", env.port, path), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// openHubDB opens the SQLite database of a running af-hub next to it.
func openHubDB(t *testing.T, env *afHubEnv) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(env.dataDir, "afhub.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("opening the hub database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// The real af-hub with sandboxes enabled serves the lease routes: the schema is
// created at start, a lease is issued, listed, read and ended over HTTP with the
// admin token behind apikit's real auth middleware, and the database ends up
// with the destroyed lease and no token hash.
// Path: 26-PATH-1 through cmd/af-hub.
func TestAfHubBinary_Leases_PATH_1(t *testing.T) {
	bin := buildAfHub(t)
	engine, _ := writeFakeEngine(t)
	env := newAfHubEnv(t, fmt.Sprintf(`[sandbox]
provider      = "podman"
engine_binary = %q
max_sandboxes = 3

[[sandbox.profiles]]
name    = "tools"
default = true
image   = "registry.example/tools:1"
ttl     = "30m"
`, engine))
	token := bootstrapAfHub(t, bin, env)
	startAfHub(t, bin, env)

	db := openHubDB(t, env)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, status, created_at, updated_at)
		VALUES ('widgets', 'https://example.com/widgets.git', 'user-1', 'ready', ?, ?)`, now, now); err != nil {
		t.Fatalf("inserting a workspace: %v", err)
	}

	status, body := callHub(t, env, http.MethodPost, "/api/v1/workspaces/widgets/sandboxes", token, `{"purpose":"binary"}`)
	if status != http.StatusCreated {
		t.Fatalf("POST: status %d: %s", status, body)
	}
	var posted map[string]any
	if err := json.Unmarshal([]byte(body), &posted); err != nil {
		t.Fatalf("POST body is not JSON: %v: %s", err, body)
	}
	id, _ := posted["id"].(string)
	leaseToken, _ := posted["token"].(string)
	if posted["state"] != "leased" || posted["profile"] != "tools" || posted["created_by"] != "admin" || !strings.HasPrefix(leaseToken, "sbl_") {
		t.Fatalf("POST body = %s; want a leased lease of profile tools issued by admin with an sbl_ token", body)
	}

	for _, path := range []string{"/api/v1/workspaces/widgets/sandboxes", "/api/v1/workspaces/widgets/sandboxes/" + id, "/api/v1/sandboxes"} {
		status, body := callHub(t, env, http.MethodGet, path, token, "")
		if status != http.StatusOK || !strings.Contains(body, id) || strings.Contains(body, leaseToken) {
			t.Errorf("GET %s: status %d, body %s; want 200 with the lease and without its token", path, status, body)
		}
	}
	if status, body := callHub(t, env, http.MethodPost, "/api/v1/workspaces/nosuch/sandboxes", token, `{}`); status != http.StatusNotFound {
		t.Errorf("POST on a missing workspace: status %d: %s; want 404", status, body)
	}

	if status, body := callHub(t, env, http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, token, ""); status != http.StatusNoContent {
		t.Fatalf("DELETE: status %d: %s", status, body)
	}
	var state string
	var hash sql.NullString
	if err := db.QueryRow(`SELECT state, token_hash FROM sandbox_leases WHERE id = ?`, id).Scan(&state, &hash); err != nil {
		t.Fatalf("reading the lease row: %v", err)
	}
	if state != "destroyed" || hash.Valid {
		t.Errorf("row after DELETE: state %q, hash %v; want destroyed and a NULL hash", state, hash)
	}
}

// The real af-hub without a [sandbox] section still creates the lease table at
// start and answers every lease route with 404 sandboxes_disabled.
// Path: 26-REQ-1.1, 26-REQ-2.2 through cmd/af-hub.
func TestAfHubBinary_LeasesDisabled(t *testing.T) {
	bin := buildAfHub(t)
	env := newAfHubEnv(t, "")
	token := bootstrapAfHub(t, bin, env)
	startAfHub(t, bin, env)

	var table string
	if err := openHubDB(t, env).QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'sandbox_leases'`).Scan(&table); err != nil {
		t.Errorf("the sandbox_leases table does not exist although sandboxes are off: %v", err)
	}

	for _, r := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/workspaces/widgets/sandboxes"},
		{http.MethodGet, "/api/v1/workspaces/widgets/sandboxes"},
		{http.MethodGet, "/api/v1/workspaces/widgets/sandboxes/sbx_0123456789abcdef0123456789abcdef"},
		{http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/sbx_0123456789abcdef0123456789abcdef"},
		{http.MethodGet, "/api/v1/sandboxes"},
	} {
		status, body := callHub(t, env, r.method, r.path, token, `{}`)
		if status != http.StatusNotFound || !strings.Contains(body, `"error_type":"sandboxes_disabled"`) {
			t.Errorf("%s %s: status %d, body %s; want 404 sandboxes_disabled", r.method, r.path, status, body)
		}
	}
}

// A max_sandboxes that is not a whole number of at least 1 stops af-hub at start
// with a message naming the key, before any queue or listener.
// Requirement: 26-REQ-5.2
func TestAfHubBinary_InvalidMaxSandboxes(t *testing.T) {
	bin := buildAfHub(t)
	for _, value := range []string{"0", "-1", "2.5"} {
		t.Run(value, func(t *testing.T) {
			env := newAfHubEnv(t, fmt.Sprintf(`[sandbox]
provider      = "podman"
max_sandboxes = %s

[[sandbox.profiles]]
name  = "tools"
image = "registry.example/tools:1"
ttl   = "30m"
`, value))
			bootstrapAfHub(t, bin, env)

			output, exit := env.run(t, bin, 30*time.Second)
			if exit <= 0 {
				t.Fatalf("af-hub exited with status %d; want a refusal\n%s", exit, output)
			}
			if !strings.Contains(output, "sandbox.max_sandboxes") {
				t.Errorf("output does not name sandbox.max_sandboxes:\n%s", output)
			}
			if strings.Contains(output, "queue started") || env.listening() {
				t.Errorf("af-hub started a queue or a listener before refusing:\n%s", output)
			}
		})
	}
}
