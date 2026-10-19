package sandboxes

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// TS-26-57 (smoke): A caller issues a lease through the running router, reads
// it and ends it, with the token revoked. The router is the one cmd/af-hub
// builds (RegisterLeaseRoutes), over a real lease service and store, SQLite,
// the real secrets store, wsaccess.AuthorizeWorkspace and the real DuckDB-backed
// audit emitter.
// Path: 26-PATH-1. Requirements: 26-REQ-3.1, 26-REQ-8.1, 26-REQ-6.4
func TestSmoke_IssueReadEndLease_TS_26_57(t *testing.T) {
	auditDB, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatalf("audit.OpenDB: %v", err)
	}
	t.Cleanup(func() { auditDB.Close() })
	if err := audit.InitSchema(auditDB); err != nil {
		t.Fatalf("audit.InitSchema: %v", err)
	}
	env := newLeaseEnv(t, leaseEnvOptions{emitter: audit.NewEmitter(audit.NewStore(auditDB))})

	rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: status %d; want 201: %s", rec.Code, rec.Body)
	}
	posted := jsonObject(t, rec)
	id, token := posted["id"].(string), posted["token"].(string)
	if posted["state"] != "leased" || len(token) != 48 || !strings.HasPrefix(token, "sbl_") {
		t.Fatalf("POST body: state %v, token %q; want leased and a 48-character sbl_ token", posted["state"], token)
	}
	created, _ := apikit.ParseUTC(posted["created_at"].(string))
	expires, _ := apikit.ParseUTC(posted["expires_at"].(string))
	if expires.Sub(created).String() != "1h0m0s" {
		t.Errorf("expires_at - created_at = %v; want the profile's 1h", expires.Sub(created))
	}

	// The database holds only the token's SHA-256.
	sum := sha256.Sum256([]byte(token))
	var hash string
	if err := env.db.QueryRow(`SELECT token_hash FROM sandbox_leases WHERE id = ?`, id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != hex.EncodeToString(sum[:]) {
		t.Errorf("stored hash %q is not the SHA-256 of the token", hash)
	}
	for i, col := range env.rowText(id) {
		if strings.Contains(col, token) || strings.Contains(col, plantedSecret) {
			t.Errorf("column %d of the row holds the token or the planted secret", i)
		}
	}

	// Reads never carry the token, and the lease is found by its token.
	for name, body := range map[string]string{
		"GET list": env.get("/api/v1/workspaces/widgets/sandboxes", apiKeyAuth()).Body.String(),
		"GET one":  env.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth()).Body.String(),
	} {
		if strings.Contains(body, token) || !strings.Contains(body, id) {
			t.Errorf("%s: want the lease and not the token: %s", name, body)
		}
	}
	if found, err := env.store.LookupByToken(token); err != nil || found.ID != id {
		t.Fatalf("LookupByToken before DELETE = (%v, %v); want the lease", found, err)
	}

	// Exactly one lease event, carrying the workspace, was written to the audit database.
	var events int
	var workspace, resource, action string
	if err := auditDB.QueryRow(`SELECT COUNT(*), MAX(workspace), MAX(resource_id), MAX(action) FROM hub_audit_events WHERE event_type = 'hub.sandbox.lease'`).
		Scan(&events, &workspace, &resource, &action); err != nil {
		t.Fatalf("reading the audit database: %v", err)
	}
	if events != 1 || workspace != "widgets" || resource != id || action != "lease" {
		t.Errorf("audit: %d events, workspace %q, resource %q, action %q; want one for widgets and %s", events, workspace, resource, action, id)
	}

	// DELETE ends it and revokes the token.
	if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d; want 204", rec.Code)
	}
	after := jsonObject(t, env.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth()))
	if after["state"] != "destroyed" || after["end_reason"] != "api" {
		t.Errorf("after DELETE: state %v, end_reason %v; want destroyed and api", after["state"], after["end_reason"])
	}
	if found, err := env.store.LookupByToken(token); found != nil || err != ErrLeaseNotFound {
		t.Errorf("LookupByToken after DELETE = (%v, %v); want (nil, ErrLeaseNotFound)", found, err)
	}
	if err := auditDB.QueryRow(`SELECT COUNT(*) FROM hub_audit_events WHERE event_type = 'hub.sandbox.lease'`).Scan(&events); err != nil || events != 1 {
		t.Errorf("after DELETE: %d lease events (%v); want still one", events, err)
	}
}
