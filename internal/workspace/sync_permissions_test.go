package workspace

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/txsvc/apikit"
)

// ========================================================================
// Spec 13 Task 3.4: Sync permission enforcement
// (TS-13-25, TS-13-26)
// Requirements: 13-REQ-8
// ========================================================================

// TS-13-25: Verifies that admin tokens, API keys, and PATs with
// workspaces:sync scope can all successfully call the sync endpoint.
// Admin tokens and API keys have implicit access; PATs require explicit
// workspaces:sync scope grant.
// Requirement: 13-REQ-8.1
func TestSyncPermissions_AllCredentialTypesSync(t *testing.T) {
	credentials := []struct {
		name string
		slug string
		auth func() *apikit.AuthInfo
	}{
		{
			name: "admin_token",
			slug: "perm-admin-ws",
			auth: func() *apikit.AuthInfo { return adminAuth() },
		},
		{
			name: "api_key",
			slug: "perm-apikey-ws",
			auth: func() *apikit.AuthInfo { return userAuth("alice-id") },
		},
		{
			name: "pat_with_sync_scope",
			slug: "perm-pat-ws",
			auth: func() *apikit.AuthInfo { return patAuth("alice-id", "workspaces:sync") },
		},
	}

	for _, cred := range credentials {
		t.Run(cred.name, func(t *testing.T) {
			env := newTestEnv(t)

			env.seedWorkspace(t, &Workspace{
				Slug:        cred.slug,
				GitURL:      "https://github.com/example/repo.git",
				OwnerID:     "alice-id",
				Status:      "active",
				CloneStatus: "ready",
			})

			_, err := env.db.Exec(
				`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
				cred.slug,
			)
			if err != nil {
				t.Fatalf("failed to set sync fields: %v", err)
			}

			auth := cred.auth()
			rec := env.doRequest(t, http.MethodPost,
				"/api/v1/workspaces/"+cred.slug+"/sync", "", auth)

			// All three credential types should not be rejected for auth reasons.
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Errorf("%s: POST /sync returned %d; want successful auth (not 401/403)",
					cred.name, rec.Code)
			}
		})
	}
}

// TS-13-25 supplementary: Verifies that admin tokens, API keys, and PATs
// with workspaces:sync scope can call the reclone endpoint.
func TestSyncPermissions_AllCredentialTypesReclone(t *testing.T) {
	credentials := []struct {
		name string
		slug string
		auth func() *apikit.AuthInfo
	}{
		{
			name: "admin_token",
			slug: "reclone-perm-admin-ws",
			auth: func() *apikit.AuthInfo { return adminAuth() },
		},
		{
			name: "api_key",
			slug: "reclone-perm-apikey-ws",
			auth: func() *apikit.AuthInfo { return userAuth("alice-id") },
		},
		{
			name: "pat_with_sync_scope",
			slug: "reclone-perm-pat-ws",
			auth: func() *apikit.AuthInfo { return patAuth("alice-id", "workspaces:sync") },
		},
	}

	for _, cred := range credentials {
		t.Run(cred.name, func(t *testing.T) {
			env := newTestEnv(t)

			env.seedWorkspace(t, &Workspace{
				Slug:        cred.slug,
				GitURL:      "https://github.com/example/repo.git",
				OwnerID:     "alice-id",
				Status:      "active",
				CloneStatus: "ready",
			})

			auth := cred.auth()
			rec := env.doRequest(t, http.MethodPost,
				"/api/v1/workspaces/"+cred.slug+"/reclone", "", auth)

			if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Errorf("%s: POST /reclone returned %d; want successful auth",
					cred.name, rec.Code)
			}
		})
	}
}

// 13-REQ-8.E1: Verifies that a PAT with only workspaces:read scope is
// rejected when calling POST /sync.
func TestSyncPermissions_ReadOnlyPATRejectedSync(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:        "perm-readonly-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	auth := patAuth("alice-id", "workspaces:read")
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/perm-readonly-ws/sync", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /sync with read-only PAT returned %d; want %d",
			rec.Code, http.StatusForbidden)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about missing scope")
	}
}

// 13-REQ-8.E1 (reclone path): Verifies that a PAT with only workspaces:read
// scope is rejected when calling POST /reclone.
func TestSyncPermissions_ReadOnlyPATRejectedReclone(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:        "perm-readonly-reclone-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	auth := patAuth("alice-id", "workspaces:read")
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/perm-readonly-reclone-ws/reclone", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /reclone with read-only PAT returned %d; want %d",
			rec.Code, http.StatusForbidden)
	}
}

// 13-REQ-8.E2: Verifies that unauthenticated requests to POST /sync
// are rejected.
func TestSyncPermissions_UnauthenticatedSyncRejected(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:        "perm-noauth-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/perm-noauth-ws/sync", "", nil)

	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Errorf("POST /sync unauthenticated returned %d; want %d or %d",
			rec.Code, http.StatusUnauthorized, http.StatusForbidden)
	}
}

// 13-REQ-8.E2 (reclone path): Verifies that unauthenticated requests to
// POST /reclone are rejected.
func TestSyncPermissions_UnauthenticatedRecloneRejected(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:        "perm-noauth-reclone-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/perm-noauth-reclone-ws/reclone", "", nil)

	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Errorf("POST /reclone unauthenticated returned %d; want %d or %d",
			rec.Code, http.StatusUnauthorized, http.StatusForbidden)
	}
}

// TS-13-26: Verifies that sync status fields are included in workspace
// responses accessible under the workspaces:read permission.
// Requirement: 13-REQ-8.2
func TestSyncPermissions_SyncFieldsVisibleWithReadScope(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:        "read-fields-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'error',
		 sync_error = 'test error', upstream_head_sha = 'abc123',
		 last_sync_at = '2024-01-01T00:00:00Z'
		 WHERE slug = ?`,
		"read-fields-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	auth := patAuth("alice-id", "workspaces:read")
	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/read-fields-ws", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET workspace returned %d; want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	syncFields := []string{"sync_mode", "sync_status", "upstream_head_sha", "last_sync_at", "sync_error"}
	for _, field := range syncFields {
		if _, ok := resp[field]; !ok {
			t.Errorf("response missing %q field", field)
		}
	}

	if resp["sync_mode"] != "pull_only" {
		t.Errorf("sync_mode = %v; want %q", resp["sync_mode"], "pull_only")
	}
	if resp["sync_status"] != "error" {
		t.Errorf("sync_status = %v; want %q", resp["sync_status"], "error")
	}
	if resp["sync_error"] != "test error" {
		t.Errorf("sync_error = %v; want %q", resp["sync_error"], "test error")
	}
}
