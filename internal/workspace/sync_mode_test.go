package workspace

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// ========================================================================
// Spec 13 Task 1.2: Sync mode configuration
// (TS-13-4, TS-13-5, TS-13-6, 13-REQ-2.E1)
// Requirements: 13-REQ-2
// ========================================================================

// TS-13-4: Verifies that workspace creation with an explicit sync_mode field
// stores and returns the specified value.
// Requirement: 13-REQ-2.1
func TestSyncMode_CreateWithExplicitMode(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	body := `{"slug":"disabled-ws","git_url":"https://github.com/example/repo.git","sync_mode":"disabled"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create returned %d, want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if val, ok := resp["sync_mode"]; !ok {
		t.Error("response missing 'sync_mode' field")
	} else if val != "disabled" {
		t.Errorf("sync_mode = %v; want %q", val, "disabled")
	}
}

// TS-13-5: Verifies that PATCH /workspaces/:slug with sync_mode updates the
// field and returns the new value.
// Requirement: 13-REQ-2.2
func TestSyncMode_PatchSyncMode(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u1-id")

	// Create workspace with default sync_mode='pull_only'.
	createBody := `{"slug":"patch-mode-ws","git_url":"https://github.com/example/repo.git"}`
	createRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", createBody, auth)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create returned %d, want %d", createRec.Code, http.StatusCreated)
	}

	// PATCH sync_mode to 'disabled'.
	patchBody := `{"sync_mode":"disabled"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/patch-mode-ws", patchBody, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH returned %d, want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode PATCH response: %v", err)
	}

	if val, ok := resp["sync_mode"]; !ok {
		t.Error("PATCH response missing 'sync_mode' field")
	} else if val != "disabled" {
		t.Errorf("sync_mode = %v; want %q", val, "disabled")
	}

	// Verify persistence: GET should also return the updated sync_mode.
	getRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/patch-mode-ws", "", auth)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET returned %d, want %d", getRec.Code, http.StatusOK)
	}

	var getResp map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("failed to decode GET response: %v", err)
	}

	if val := getResp["sync_mode"]; val != "disabled" {
		t.Errorf("GET sync_mode = %v; want %q (persisted)", val, "disabled")
	}
}

// TS-13-6: Verifies that creating a workspace with sync_mode='disabled'
// persists the value and it can be read back via GET. This covers the
// API-level equivalent of the CLI 'afc workspace create --sync-mode disabled'
// integration path.
// Requirement: 13-REQ-2.3
func TestSyncMode_CreatePersistsSyncMode(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u2-id")

	// Create workspace with explicit sync_mode.
	body := `{"slug":"cli-equiv-ws","git_url":"https://github.com/example/repo.git","sync_mode":"disabled"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create returned %d, want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Verify the sync_mode is in the create response.
	var createResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}
	if val := createResp["sync_mode"]; val != "disabled" {
		t.Errorf("create sync_mode = %v; want %q", val, "disabled")
	}

	// Verify the sync_mode persisted correctly via GET.
	getRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/cli-equiv-ws", "", auth)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET returned %d, want %d", getRec.Code, http.StatusOK)
	}

	var getResp map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("failed to decode GET response: %v", err)
	}
	if val := getResp["sync_mode"]; val != "disabled" {
		t.Errorf("GET sync_mode = %v; want %q (persisted from create)", val, "disabled")
	}

	// Verify it also persisted in the database.
	var dbSyncMode string
	err := env.db.QueryRow(
		`SELECT sync_mode FROM workspaces WHERE slug = ?`, "cli-equiv-ws",
	).Scan(&dbSyncMode)
	if err != nil {
		t.Fatalf("failed to query sync_mode from DB: %v", err)
	}
	if dbSyncMode != "disabled" {
		t.Errorf("DB sync_mode = %q; want %q", dbSyncMode, "disabled")
	}
}

// 13-REQ-2.E1: Verifies that PATCH /workspaces/:slug with an unrecognized
// sync_mode value is rejected with HTTP 400 and a descriptive error.
func TestSyncMode_InvalidSyncModeRejected(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	// Create workspace first.
	createBody := `{"slug":"invalid-mode-ws","git_url":"https://github.com/example/repo.git"}`
	createRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", createBody, auth)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create returned %d, want %d", createRec.Code, http.StatusCreated)
	}

	// PATCH with an invalid sync_mode value.
	patchBody := `{"sync_mode":"invalid_value"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/invalid-mode-ws", patchBody, auth)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH with invalid sync_mode returned %d; want %d",
			rec.Code, http.StatusBadRequest)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message about invalid sync_mode")
	}
	// The error message should be about the invalid sync_mode value specifically,
	// not a generic "no updatable field" error.
	if strings.Contains(envelope.Error.Message, "at least one updatable field") {
		t.Errorf("error message %q is a generic 'no updatable field' error; want sync_mode-specific validation error",
			envelope.Error.Message)
	}
}

// 13-REQ-2.E1 (create path): Verifies that POST /api/v1/workspaces with an
// unrecognized sync_mode value is rejected with HTTP 400.
func TestSyncMode_CreateInvalidSyncModeRejected(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("u1-id")

	body := `{"slug":"bad-mode-ws","git_url":"https://github.com/example/repo.git","sync_mode":"full_sync"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("create with invalid sync_mode returned %d; want %d",
			rec.Code, http.StatusBadRequest)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message about invalid sync_mode")
	}
}
