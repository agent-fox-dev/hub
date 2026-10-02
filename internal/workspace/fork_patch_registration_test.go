package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// TS-21-7 (unit): With no hook registered registration accepts the branch
// without resolution.
// Verifies: 21-REQ-1.7
func TestTS21_7_NoHookRegisteredAcceptsBranch(t *testing.T) {
	slug := "ts21-7-nohook"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Ensure no hook is registered.
	RegisterBranchCheckHook(nil)
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	body := `{"branch_name": "feature/nonexistent"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Verify a patch row was inserted.
	var count int
	err := env.db.QueryRow(
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, slug,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 patch; got %d", count)
	}
}

// TS-21-18 (integration): A batch names the first failing element index in
// the 400 message.
// Verifies: 21-REQ-4.2
func TestTS21_18_BatchFirstFailingElementIndex(t *testing.T) {
	slug := "ts21-18-batch"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	callCount := 0
	// Hook: element 0 succeeds (local), elements 1 and 2 fail (not found).
	RegisterBranchCheckHook(func(_ context.Context, _ string, branch string) (string, error) {
		callCount++
		if branch == "feature/local" {
			return ResolutionLocal, nil
		}
		return "", NewBranchResolveError(BranchResolveKindNotFound, fmt.Errorf("not found"))
	})
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	body := `[
		{"branch_name":"feature/local"},
		{"branch_name":"feature/missing1"},
		{"branch_name":"feature/missing2"}
	]`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST batch status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var errResp errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	expectedMsg := "patch[1]: branch does not exist in repository or on origin"
	if errResp.Error.Message != expectedMsg {
		t.Errorf("error message = %q; want %q", errResp.Error.Message, expectedMsg)
	}

	// Verify no rows were inserted.
	var count int
	err := env.db.QueryRow(
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, slug,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 patches; got %d", count)
	}
}

// TS-21-22 (integration): A batch resolves all elements in order before any
// insert and rejects wholesale on each failure class.
// Verifies: 21-REQ-6.1, 21-REQ-6.2
func TestTS21_22_BatchResolvesAllBeforeInsert(t *testing.T) {
	type testCase struct {
		name       string
		errKind    string
		wantStatus int
	}

	cases := []testCase{
		{"not_found", BranchResolveKindNotFound, http.StatusBadRequest},
		{"origin_fetch_failed", BranchResolveKindOriginFetchFailed, http.StatusBadGateway},
		{"workspace_busy", BranchResolveKindWorkspaceBusy, http.StatusConflict},
		{"internal", "", http.StatusInternalServerError}, // unclassified error
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slug := "ts21-22-" + tc.name
			env := newPatchTestEnv(t, slug, "deploy")
			auth := userAuth("user-1")

			var callOrder []int
			RegisterBranchCheckHook(func(_ context.Context, _ string, branch string) (string, error) {
				switch branch {
				case "a":
					callOrder = append(callOrder, 0)
					return ResolutionLocal, nil
				case "b":
					callOrder = append(callOrder, 1)
					return ResolutionLocal, nil
				case "c":
					callOrder = append(callOrder, 2)
					if tc.errKind == "" {
						// Unclassified error
						return "", errors.New("boom")
					}
					return "", NewBranchResolveError(tc.errKind, fmt.Errorf("test error"))
				default:
					return ResolutionLocal, nil
				}
			})
			t.Cleanup(func() { RegisterBranchCheckHook(nil) })

			// Count patches before.
			var beforeCount int
			env.db.QueryRow(
				`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, slug,
			).Scan(&beforeCount)

			body := `[{"branch_name":"a"},{"branch_name":"b"},{"branch_name":"c"}]`
			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

			if rec.Code != tc.wantStatus {
				t.Fatalf("POST batch status = %d; want %d; body: %s",
					rec.Code, tc.wantStatus, rec.Body.String())
			}

			// Verify call order: elements 0, 1, 2 in array order.
			if len(callOrder) < 3 {
				// The first failing element stops the loop, so we may have
				// fewer calls. But elements 0 and 1 should be called before 2.
				for i := 0; i < len(callOrder)-1; i++ {
					if callOrder[i] >= callOrder[i+1] {
						t.Errorf("call order not ascending: %v", callOrder)
						break
					}
				}
			}

			// Verify no rows were inserted.
			var afterCount int
			env.db.QueryRow(
				`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, slug,
			).Scan(&afterCount)
			if afterCount != beforeCount {
				t.Errorf("patch count changed from %d to %d; want unchanged", beforeCount, afterCount)
			}
		})
	}
}

// TS-21-31 (unit): With no hook the audit event omits branch_resolution.
// Verifies: 21-REQ-8.3
func TestTS21_31_NoHookAuditOmitsBranchResolution(t *testing.T) {
	env := newAuditTestEnv(t)
	ensureCarryPatchColumns(t, env.testEnv.db)
	slug := "ts21-31-audit"
	seedCarryPatchWorkspaceRaw(t, env.testEnv.db, slug,
		"https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git",
		"deploy")

	// Ensure no hook is registered.
	RegisterBranchCheckHook(nil)
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	// Clear previous events.
	env.emitter.mu.Lock()
	env.emitter.events = nil
	env.emitter.mu.Unlock()

	auth := userAuth("user-1")
	body := `{"branch_name": "feature/audit-test"}`
	rec := env.testEnv.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	events := env.emitter.Events()
	var patchCreateEvents []audit.HubEvent
	for _, ev := range events {
		if ev.EventType == "hub.patch.create" {
			patchCreateEvents = append(patchCreateEvents, ev)
		}
	}

	if len(patchCreateEvents) == 0 {
		t.Fatal("expected hub.patch.create event, got none")
	}

	ev := patchCreateEvents[0]
	// branch_resolution should NOT be present.
	if _, ok := ev.Metadata["branch_resolution"]; ok {
		t.Errorf("branch_resolution should be absent when no hook is registered, got %v",
			ev.Metadata["branch_resolution"])
	}
	// branch_name and position should still be present.
	if _, ok := ev.Metadata["branch_name"]; !ok {
		t.Error("metadata missing 'branch_name' key")
	}
	if _, ok := ev.Metadata["position"]; !ok {
		t.Error("metadata missing 'position' key")
	}
}

// TS-21-32 (integration): An if_not_exists hit returns the existing record
// and emits no event.
// Verifies: 21-REQ-8.4
func TestTS21_32_IfNotExistsHitNoEvent(t *testing.T) {
	env := newAuditTestEnv(t)
	ensureCarryPatchColumns(t, env.testEnv.db)
	slug := "ts21-32-ifne"
	seedCarryPatchWorkspaceRaw(t, env.testEnv.db, slug,
		"https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git",
		"deploy")

	// Seed an existing patch.
	seedPatchRaw(t, env.testEnv.db, "existing-32", slug, "feature/existing", 1)

	// Register a hook that succeeds (simulates branch found).
	RegisterBranchCheckHook(func(_ context.Context, _, _ string) (string, error) {
		return ResolutionLocal, nil
	})
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	// Clear previous events.
	env.emitter.mu.Lock()
	env.emitter.events = nil
	env.emitter.mu.Unlock()

	auth := userAuth("user-1")
	body := `{"branch_name": "feature/existing", "if_not_exists": true}`
	rec := env.testEnv.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify no hub.patch.create event was emitted.
	events := env.emitter.Events()
	for _, ev := range events {
		if ev.EventType == "hub.patch.create" {
			t.Errorf("expected no hub.patch.create event, but got one: %+v", ev)
		}
	}
}

// TS-21-33 (integration): Batch registration emits no hub.patch.create events.
// Verifies: 21-REQ-8.5
func TestTS21_33_BatchNoAuditEvents(t *testing.T) {
	env := newAuditTestEnv(t)
	ensureCarryPatchColumns(t, env.testEnv.db)
	slug := "ts21-33-batch"
	seedCarryPatchWorkspaceRaw(t, env.testEnv.db, slug,
		"https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git",
		"deploy")

	// Register a hook that always succeeds.
	RegisterBranchCheckHook(func(_ context.Context, _, _ string) (string, error) {
		return ResolutionLocal, nil
	})
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	// Clear previous events.
	env.emitter.mu.Lock()
	env.emitter.events = nil
	env.emitter.mu.Unlock()

	auth := userAuth("user-1")
	body := `[{"branch_name":"batch-a"},{"branch_name":"batch-b"}]`
	rec := env.testEnv.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST batch status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Verify response has two rows.
	var resp []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp) != 2 {
		t.Fatalf("response has %d patches; want 2", len(resp))
	}

	// Verify no hub.patch.create event was emitted.
	events := env.emitter.Events()
	for _, ev := range events {
		if ev.EventType == "hub.patch.create" {
			t.Errorf("expected no hub.patch.create event in batch, but got one: %+v", ev)
		}
	}
}

// TS-21-34 (unit): The resolver hook type and error classes map to 400, 502,
// 409 and 500.
// Verifies: 21-REQ-9.1, 21-REQ-9.2
func TestTS21_34_ErrorClassMapping(t *testing.T) {
	type testCase struct {
		name       string
		errKind    string
		wantStatus int
	}

	cases := []testCase{
		{"not_found", BranchResolveKindNotFound, http.StatusBadRequest},
		{"origin_fetch_failed", BranchResolveKindOriginFetchFailed, http.StatusBadGateway},
		{"origin_credentials", BranchResolveKindOriginCredentials, http.StatusBadGateway},
		{"workspace_busy", BranchResolveKindWorkspaceBusy, http.StatusConflict},
		{"other_error", "", http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slug := "ts21-34-" + tc.name
			env := newPatchTestEnv(t, slug, "deploy")
			auth := userAuth("user-1")

			RegisterBranchCheckHook(func(_ context.Context, _, _ string) (string, error) {
				if tc.errKind == "" {
					return "", errors.New("unclassified error")
				}
				return "", NewBranchResolveError(tc.errKind, fmt.Errorf("test"))
			})
			t.Cleanup(func() { RegisterBranchCheckHook(nil) })

			body := `{"branch_name": "feature/test"}`
			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

			if rec.Code != tc.wantStatus {
				t.Errorf("POST status = %d; want %d; body: %s",
					rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// TS-21-37 (unit): An unclassified hook error answers 500 internal server
// error and inserts no row.
// Verifies: 21-REQ-9.5
func TestTS21_37_UnclassifiedErrorReturns500(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		slug := "ts21-37-single"
		env := newPatchTestEnv(t, slug, "deploy")
		auth := userAuth("user-1")

		RegisterBranchCheckHook(func(_ context.Context, _, _ string) (string, error) {
			return "", errors.New("boom")
		})
		t.Cleanup(func() { RegisterBranchCheckHook(nil) })

		body := `{"branch_name": "feature/test"}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("POST status = %d; want %d; body: %s",
				rec.Code, http.StatusInternalServerError, rec.Body.String())
		}

		// Verify the response says "internal server error" and does NOT echo "boom".
		var errResp errorEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Message != "internal server error" {
			t.Errorf("error message = %q; want %q", errResp.Error.Message, "internal server error")
		}
		if strings.Contains(rec.Body.String(), "boom") {
			t.Error("response body should not contain 'boom'")
		}

		// Verify no patch was created.
		var count int
		env.db.QueryRow(
			`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, slug,
		).Scan(&count)
		if count != 0 {
			t.Errorf("expected 0 patches; got %d", count)
		}
	})

	t.Run("batch", func(t *testing.T) {
		slug := "ts21-37-batch"
		env := newPatchTestEnv(t, slug, "deploy")
		auth := userAuth("user-1")

		RegisterBranchCheckHook(func(_ context.Context, _, _ string) (string, error) {
			return "", errors.New("boom")
		})
		t.Cleanup(func() { RegisterBranchCheckHook(nil) })

		body := `[{"branch_name": "feature/test"}]`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("POST batch status = %d; want %d; body: %s",
				rec.Code, http.StatusInternalServerError, rec.Body.String())
		}

		// Verify the response says "internal server error" and does NOT echo "boom".
		var errResp errorEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Message != "internal server error" {
			t.Errorf("error message = %q; want %q", errResp.Error.Message, "internal server error")
		}
		if strings.Contains(rec.Body.String(), "boom") {
			t.Error("response body should not contain 'boom'")
		}

		// Verify no patch was created.
		var count int
		env.db.QueryRow(
			`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, slug,
		).Scan(&count)
		if count != 0 {
			t.Errorf("expected 0 patches; got %d", count)
		}
	})
}
