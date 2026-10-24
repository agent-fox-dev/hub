package workspace

import (
	"encoding/json"
	"net/http"
	"testing"
)

// ========================================================================
// Spec 13 Task 1.2: Sync mode configuration, now the sync_enabled setting
// (TS-13-4, TS-13-5, TS-13-6, 13-REQ-2.E1)
// Requirements: 13-REQ-2
// ========================================================================

// syncEnabledOf returns settings.sync_enabled of a workspace JSON response.
func syncEnabledOf(t *testing.T, body []byte) any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	settings, ok := resp["settings"].(map[string]any)
	if !ok {
		t.Fatalf("response settings = %v; want an object", resp["settings"])
	}
	val, ok := settings["sync_enabled"]
	if !ok {
		t.Fatal("response missing 'settings.sync_enabled' field")
	}
	return val
}

// TS-13-4: Verifies that workspace creation with an explicit sync_enabled
// setting stores and returns the specified value.
// Requirement: 13-REQ-2.1
func TestSyncEnabled_CreateDisabled(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"disabled-ws","git_url":"https://github.com/example/repo.git","settings":{"sync_enabled":false}}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create returned %d, want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	if val := syncEnabledOf(t, rec.Body.Bytes()); val != false {
		t.Errorf("settings.sync_enabled = %v; want false", val)
	}
}

// TS-13-5: Verifies that PATCH /workspaces/:slug with the sync_enabled
// setting updates it and returns the new value.
// Requirement: 13-REQ-2.2
func TestSyncEnabled_PatchDisables(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u1-id")

	// Create workspace with the default sync_enabled=true.
	createBody := `{"slug":"patch-mode-ws","git_url":"https://github.com/example/repo.git"}`
	createRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", createBody, auth)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create returned %d, want %d", createRec.Code, http.StatusCreated)
	}
	if val := syncEnabledOf(t, createRec.Body.Bytes()); val != true {
		t.Errorf("create settings.sync_enabled = %v; want true (default)", val)
	}

	// PATCH sync_enabled to false.
	patchBody := `{"settings":{"sync_enabled":false}}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/patch-mode-ws", patchBody, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH returned %d, want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
	if val := syncEnabledOf(t, rec.Body.Bytes()); val != false {
		t.Errorf("PATCH settings.sync_enabled = %v; want false", val)
	}

	// Verify persistence: GET should also return the updated setting.
	getRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/patch-mode-ws", "", auth)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET returned %d, want %d", getRec.Code, http.StatusOK)
	}
	if val := syncEnabledOf(t, getRec.Body.Bytes()); val != false {
		t.Errorf("GET settings.sync_enabled = %v; want false (persisted)", val)
	}
}

// TS-13-6: Verifies that creating a workspace with sync_enabled=false
// persists the value and it can be read back via GET. This covers the
// API-level equivalent of the CLI 'afc workspace create --setting
// sync_enabled=false' integration path.
// Requirement: 13-REQ-2.3
func TestSyncEnabled_CreatePersists(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u2-id")

	// Create workspace with sync disabled.
	body := `{"slug":"cli-equiv-ws","git_url":"https://github.com/example/repo.git","settings":{"sync_enabled":false}}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create returned %d, want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
	if val := syncEnabledOf(t, rec.Body.Bytes()); val != false {
		t.Errorf("create settings.sync_enabled = %v; want false", val)
	}

	// Verify the setting persisted correctly via GET.
	getRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/cli-equiv-ws", "", auth)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET returned %d, want %d", getRec.Code, http.StatusOK)
	}
	if val := syncEnabledOf(t, getRec.Body.Bytes()); val != false {
		t.Errorf("GET settings.sync_enabled = %v; want false (persisted from create)", val)
	}

	// Verify it also persisted in the settings document.
	var dbSyncEnabled int
	err := env.db.QueryRow(
		`SELECT json_extract(settings, '$.sync_enabled') FROM workspaces WHERE slug = ?`, "cli-equiv-ws",
	).Scan(&dbSyncEnabled)
	if err != nil {
		t.Fatalf("failed to query sync_enabled from DB: %v", err)
	}
	if dbSyncEnabled != 0 {
		t.Errorf("DB settings.sync_enabled = %d; want 0 (false)", dbSyncEnabled)
	}
}

// 13-REQ-2.E1: Verifies that PATCH /workspaces/:slug with the removed
// sync_mode field is rejected with HTTP 400 and an error naming the setting
// to use instead.
func TestSyncEnabled_SyncModeFieldRejected(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	// Create workspace first.
	createBody := `{"slug":"invalid-mode-ws","git_url":"https://github.com/example/repo.git"}`
	createRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", createBody, auth)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create returned %d, want %d", createRec.Code, http.StatusCreated)
	}

	// PATCH with the old sync_mode field.
	patchBody := `{"sync_mode":"disabled"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/invalid-mode-ws", patchBody, auth)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH with sync_mode returned %d; want %d",
			rec.Code, http.StatusBadRequest)
	}

	envelope := parseErrorEnvelope(t, rec)
	want := "sync_mode is no longer a field; set the sync_enabled setting instead"
	if envelope.Error.Message != want {
		t.Errorf("error.message = %q; want %q", envelope.Error.Message, want)
	}

	// The workspace keeps its setting.
	getRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/invalid-mode-ws", "", auth)
	if val := syncEnabledOf(t, getRec.Body.Bytes()); val != true {
		t.Errorf("GET settings.sync_enabled = %v; want true (unchanged)", val)
	}
}

// 13-REQ-2.E1 (create path): Verifies that POST /api/v1/workspaces ignores
// the removed sync_mode field: the workspace is created with the sync_enabled
// setting at its default.
func TestSyncEnabled_CreateIgnoresSyncMode(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u1-id")

	body := `{"slug":"bad-mode-ws","git_url":"https://github.com/example/repo.git","sync_mode":"disabled"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with sync_mode returned %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	if val := syncEnabledOf(t, rec.Body.Bytes()); val != true {
		t.Errorf("settings.sync_enabled = %v; want true (sync_mode ignored)", val)
	}
}
