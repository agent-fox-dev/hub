package workspace

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestStateMachine_IntegrationWithDB verifies the state machine transitions
// work end-to-end with database updates.
// Requirement: 05-REQ-9.1
func TestStateMachine_IntegrationWithDB(t *testing.T) {
	db := openTestDB(t)

	ws := &Workspace{
		Slug:    "sm-test-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Walk through the full lifecycle: pending -> cloning -> ready -> archived.
	transitions := []struct {
		to       string
		headSHA  *string
		cloneErr *string
	}{
		{to: "cloning", headSHA: nil, cloneErr: nil},
		{to: "ready", headSHA: strPtr("a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"), cloneErr: nil},
		{to: "archived", headSHA: strPtr("a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"), cloneErr: nil},
	}

	for _, tr := range transitions {
		if err := updateCloneStatus(db, ws.Slug, tr.to, tr.headSHA, tr.cloneErr); err != nil {
			t.Fatalf("updateCloneStatus(%q -> %q): %v", ws.Slug, tr.to, err)
		}
		cloneStatus, _, _, err := getCloneFields(db, ws.Slug)
		if err != nil {
			t.Fatalf("getCloneFields after %q: %v", tr.to, err)
		}
		if cloneStatus != tr.to {
			t.Errorf("clone_status = %q after transition; want %q", cloneStatus, tr.to)
		}
	}
}

// strPtr returns a pointer to the given string. Helper for test data setup.
func strPtr(s string) *string {
	return &s
}

// TS-05-26: The only way to transition clone_status from 'archived' to
// 'pending' is via the reactivate endpoint.
// Requirement: 05-REQ-9.2
func TestStateMachine_ArchivedToPendingOnlyViaReactivate(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "archived-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "archived",
	})

	// Set clone_status to 'archived'.
	if err := updateCloneStatus(env.db, "archived-ws", "archived", nil, nil); err != nil {
		t.Fatalf("updateCloneStatus('archived'): %v", err)
	}

	// Reactivate the workspace via the HTTP endpoint.
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/archived-ws/reactivate", "",
		userAuth("user-1"))

	// Assert HTTP 200.
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d; want %d\nbody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Parse response.
	var ws spec05WorkspaceJSON
	if err := json.NewDecoder(rec.Body).Decode(&ws); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Assert clone_status transitioned to 'pending'.
	if ws.CloneStatus == nil {
		t.Fatal("clone_status is nil; want 'pending'")
	}
	if *ws.CloneStatus != "pending" {
		t.Errorf("clone_status = %q; want %q", *ws.CloneStatus, "pending")
	}

	// Verify in the database as well.
	cloneStatus, _, _, err := getCloneFields(env.db, "archived-ws")
	if err != nil {
		t.Fatalf("getCloneFields: %v", err)
	}
	if cloneStatus != "pending" {
		t.Errorf("DB clone_status = %q; want %q", cloneStatus, "pending")
	}
}
