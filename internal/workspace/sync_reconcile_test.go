package workspace

import (
	"testing"

	_ "modernc.org/sqlite"
)

// ========================================================================
// Spec 13 Task 3.1: Stuck-sync startup reconciliation
// (TS-13-17, TS-13-18)
// Requirements: 13-REQ-5
// ========================================================================

// TS-13-17: Verifies that on server startup, all workspaces with
// sync_status='syncing' are reset to sync_status='error' with
// sync_error='sync interrupted by server restart' before HTTP requests
// are accepted.
// Requirement: 13-REQ-5.1
// Property: 13-PROP-1 (sync_status never permanently stuck in 'syncing')
// Property: 13-PROP-9 (startup reconciliation runs before HTTP server)
func TestSyncReconcile_StuckSyncingResetOnStartup(t *testing.T) {
	db := openTestDB(t)

	// Seed two workspaces stuck in 'syncing' state (simulates server crash
	// during sync operations). Uses direct SQL because sync fields may not
	// exist yet — the test will fail at the UPDATE if the schema migration
	// has not been applied.
	for _, slug := range []string{"stuck-ws1", "stuck-ws2"} {
		if err := insertWorkspace(db, &Workspace{
			Slug:        slug,
			GitURL:      "https://github.com/example/repo.git",
			OwnerID:     "alice-id",
			Status:      "active",
			CloneStatus: "ready",
		}); err != nil {
			t.Fatalf("insertWorkspace(%q): %v", slug, err)
		}
	}

	// Set sync_status to 'syncing' for both workspaces.
	// Fails if sync_status column does not exist (expected before migration).
	for _, slug := range []string{"stuck-ws1", "stuck-ws2"} {
		_, err := db.Exec(
			`UPDATE workspaces SET sync_status = 'syncing' WHERE slug = ?`, slug,
		)
		if err != nil {
			t.Fatalf("failed to set sync_status='syncing' for %q: %v", slug, err)
		}
	}

	// ReconcileStuckSyncs should reset all 'syncing' workspaces to 'error'.
	// This function is expected to be called during server startup, before
	// the HTTP server begins accepting requests.
	if err := ReconcileStuckSyncs(db); err != nil {
		t.Fatalf("ReconcileStuckSyncs() returned error: %v", err)
	}

	// Verify both workspaces are now in sync_status='error' with the
	// prescribed sync_error message.
	for _, slug := range []string{"stuck-ws1", "stuck-ws2"} {
		var syncStatus string
		var syncError *string
		err := db.QueryRow(
			`SELECT sync_status, sync_error FROM workspaces WHERE slug = ?`, slug,
		).Scan(&syncStatus, &syncError)
		if err != nil {
			t.Fatalf("failed to query sync fields for %q: %v", slug, err)
		}
		if syncStatus != "error" {
			t.Errorf("%s: sync_status = %q; want %q", slug, syncStatus, "error")
		}
		if syncError == nil || *syncError != "sync interrupted by server restart" {
			got := "<nil>"
			if syncError != nil {
				got = *syncError
			}
			t.Errorf("%s: sync_error = %q; want %q", slug, got, "sync interrupted by server restart")
		}
	}
}

// TS-13-18: Verifies that startup reconciliation completes as a no-op when
// no workspaces have sync_status='syncing'. Existing workspaces with
// sync_status='idle' or 'error' must not be modified.
// Requirement: 13-REQ-5.2
func TestSyncReconcile_NoOpWhenNoneStuck(t *testing.T) {
	db := openTestDB(t)

	// Seed workspaces with non-syncing statuses.
	for _, ws := range []struct {
		slug   string
		status string
	}{
		{"idle-ws", "idle"},
		{"error-ws", "error"},
	} {
		if err := insertWorkspace(db, &Workspace{
			Slug:        ws.slug,
			GitURL:      "https://github.com/example/repo.git",
			OwnerID:     "alice-id",
			Status:      "active",
			CloneStatus: "ready",
		}); err != nil {
			t.Fatalf("insertWorkspace(%q): %v", ws.slug, err)
		}

		_, err := db.Exec(
			`UPDATE workspaces SET sync_status = ? WHERE slug = ?`, ws.status, ws.slug,
		)
		if err != nil {
			t.Fatalf("failed to set sync_status=%q for %q: %v", ws.status, ws.slug, err)
		}
	}

	// Set a specific sync_error on the error-ws to verify it is not overwritten.
	_, err := db.Exec(
		`UPDATE workspaces SET sync_error = 'previous error' WHERE slug = ?`, "error-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync_error: %v", err)
	}

	// Capture pre-reconciliation state.
	type wsState struct {
		syncStatus string
		syncError  *string
	}
	preStates := make(map[string]wsState)
	for _, slug := range []string{"idle-ws", "error-ws"} {
		var st wsState
		err := db.QueryRow(
			`SELECT sync_status, sync_error FROM workspaces WHERE slug = ?`, slug,
		).Scan(&st.syncStatus, &st.syncError)
		if err != nil {
			t.Fatalf("failed to query pre-state for %q: %v", slug, err)
		}
		preStates[slug] = st
	}

	// Reconciliation should be a no-op.
	if err := ReconcileStuckSyncs(db); err != nil {
		t.Fatalf("ReconcileStuckSyncs() returned error: %v", err)
	}

	// Verify no workspace sync_status was changed.
	for _, slug := range []string{"idle-ws", "error-ws"} {
		var syncStatus string
		var syncError *string
		err := db.QueryRow(
			`SELECT sync_status, sync_error FROM workspaces WHERE slug = ?`, slug,
		).Scan(&syncStatus, &syncError)
		if err != nil {
			t.Fatalf("failed to query post-state for %q: %v", slug, err)
		}

		pre := preStates[slug]
		if syncStatus != pre.syncStatus {
			t.Errorf("%s: sync_status changed from %q to %q; want unchanged",
				slug, pre.syncStatus, syncStatus)
		}

		// Verify sync_error was not overwritten.
		if pre.syncError != nil && (syncError == nil || *syncError != *pre.syncError) {
			got := "<nil>"
			if syncError != nil {
				got = *syncError
			}
			t.Errorf("%s: sync_error changed from %q to %q; want unchanged",
				slug, *pre.syncError, got)
		}
	}
}

// 13-REQ-5.E1: Verifies that if the database operation during startup
// reconciliation fails, the function returns an error (which the caller
// should use to abort server startup).
func TestSyncReconcile_DatabaseFailureReturnsError(t *testing.T) {
	db := openTestDB(t)

	// Close the database to force any query to fail.
	db.Close()

	err := ReconcileStuckSyncs(db)
	if err == nil {
		t.Fatal("ReconcileStuckSyncs() returned nil; want error when database is closed")
	}
}

// 13-REQ-5.E2: Verifies that when multiple workspaces are stuck in 'syncing'
// at startup, ALL of them are reset (not just the first one found).
func TestSyncReconcile_MultipleStuckAllReset(t *testing.T) {
	db := openTestDB(t)

	slugs := []string{"multi-stuck-1", "multi-stuck-2", "multi-stuck-3"}
	for _, slug := range slugs {
		if err := insertWorkspace(db, &Workspace{
			Slug:        slug,
			GitURL:      "https://github.com/example/repo.git",
			OwnerID:     "alice-id",
			Status:      "active",
			CloneStatus: "ready",
		}); err != nil {
			t.Fatalf("insertWorkspace(%q): %v", slug, err)
		}

		_, err := db.Exec(
			`UPDATE workspaces SET sync_status = 'syncing' WHERE slug = ?`, slug,
		)
		if err != nil {
			t.Fatalf("failed to set sync_status for %q: %v", slug, err)
		}
	}

	if err := ReconcileStuckSyncs(db); err != nil {
		t.Fatalf("ReconcileStuckSyncs() returned error: %v", err)
	}

	// Verify ALL stuck workspaces were reset.
	for _, slug := range slugs {
		var syncStatus string
		err := db.QueryRow(
			`SELECT sync_status FROM workspaces WHERE slug = ?`, slug,
		).Scan(&syncStatus)
		if err != nil {
			t.Fatalf("failed to query sync_status for %q: %v", slug, err)
		}
		if syncStatus != "error" {
			t.Errorf("%s: sync_status = %q; want %q", slug, syncStatus, "error")
		}
	}
}

// 13-REQ-5.E2 supplementary: Verifies that reconciliation only affects
// workspaces with sync_status='syncing', leaving 'idle' and 'error'
// workspaces untouched even when stuck workspaces exist.
func TestSyncReconcile_OnlyAffectsSyncingWorkspaces(t *testing.T) {
	db := openTestDB(t)

	// Seed a mix: one syncing, one idle, one error.
	workspaces := []struct {
		slug       string
		syncStatus string
	}{
		{"mix-syncing", "syncing"},
		{"mix-idle", "idle"},
		{"mix-error", "error"},
	}

	for _, ws := range workspaces {
		if err := insertWorkspace(db, &Workspace{
			Slug:        ws.slug,
			GitURL:      "https://github.com/example/repo.git",
			OwnerID:     "alice-id",
			Status:      "active",
			CloneStatus: "ready",
		}); err != nil {
			t.Fatalf("insertWorkspace(%q): %v", ws.slug, err)
		}

		_, err := db.Exec(
			`UPDATE workspaces SET sync_status = ? WHERE slug = ?`, ws.syncStatus, ws.slug,
		)
		if err != nil {
			t.Fatalf("failed to set sync_status for %q: %v", ws.slug, err)
		}
	}

	if err := ReconcileStuckSyncs(db); err != nil {
		t.Fatalf("ReconcileStuckSyncs() returned error: %v", err)
	}

	// Verify: syncing -> error, idle -> idle, error -> error.
	expected := map[string]string{
		"mix-syncing": "error",
		"mix-idle":    "idle",
		"mix-error":   "error",
	}

	for slug, want := range expected {
		var syncStatus string
		err := db.QueryRow(
			`SELECT sync_status FROM workspaces WHERE slug = ?`, slug,
		).Scan(&syncStatus)
		if err != nil {
			t.Fatalf("failed to query sync_status for %q: %v", slug, err)
		}
		if syncStatus != want {
			t.Errorf("%s: sync_status = %q; want %q", slug, syncStatus, want)
		}
	}
}
