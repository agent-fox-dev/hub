package carrypatch

import (
	"context"
	"testing"
)

// ===========================================================================
// TS-20-44 (unit): PatchStore gains write and clear methods on SQLPatchStore
// and the doubles without changing existing methods.
//
// Verifies: 20-REQ-7.4
// ===========================================================================

func TestPatchStore_SetAndClearOriginSyncState_SQL_TS2044(t *testing.T) {
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	seedWorkspace(t, db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, db, "p1", "ws1", "feature/a", 1, PatchStatusActive)
	seedPatch(t, db, "p2", "ws1", "feature/b", 2, PatchStatusActive)

	store := NewSQLPatchStore(db)
	ctx := context.Background()

	// Set origin sync state for p1.
	sha := "abc123def456"
	ts := "2024-06-01T12:00:00Z"
	err := store.SetOriginSyncState(ctx, "p1", "in_sync", &sha, ts)
	if err != nil {
		t.Fatalf("SetOriginSyncState: %v", err)
	}

	// Read back via ListPatches and verify.
	patches, err := store.ListPatches(ctx, "ws1")
	if err != nil {
		t.Fatalf("ListPatches: %v", err)
	}

	var p1Found bool
	for _, p := range patches {
		if p.ID == "p1" {
			p1Found = true
			if p.OriginSyncState == nil || *p.OriginSyncState != "in_sync" {
				t.Errorf("expected OriginSyncState='in_sync', got %v", p.OriginSyncState)
			}
			if p.OriginSHA == nil || *p.OriginSHA != sha {
				t.Errorf("expected OriginSHA=%q, got %v", sha, p.OriginSHA)
			}
			if p.OriginSyncedAt == nil || *p.OriginSyncedAt != ts {
				t.Errorf("expected OriginSyncedAt=%q, got %v", ts, p.OriginSyncedAt)
			}
		}
		if p.ID == "p2" {
			// p2 should have nil origin fields.
			if p.OriginSyncState != nil {
				t.Errorf("expected p2 OriginSyncState=nil, got %v", p.OriginSyncState)
			}
		}
	}
	if !p1Found {
		t.Fatal("patch p1 not found in ListPatches result")
	}

	// Set missing_on_origin (nil SHA).
	err = store.SetOriginSyncState(ctx, "p1", "missing_on_origin", nil, ts)
	if err != nil {
		t.Fatalf("SetOriginSyncState (missing): %v", err)
	}

	patches, err = store.ListPatches(ctx, "ws1")
	if err != nil {
		t.Fatalf("ListPatches after missing: %v", err)
	}
	for _, p := range patches {
		if p.ID == "p1" {
			if p.OriginSyncState == nil || *p.OriginSyncState != "missing_on_origin" {
				t.Errorf("expected OriginSyncState='missing_on_origin', got %v", p.OriginSyncState)
			}
			if p.OriginSHA != nil {
				t.Errorf("expected OriginSHA=nil for missing_on_origin, got %v", p.OriginSHA)
			}
		}
	}

	// Clear origin sync state for the whole workspace.
	err = store.ClearOriginSyncState(ctx, "ws1")
	if err != nil {
		t.Fatalf("ClearOriginSyncState: %v", err)
	}

	patches, err = store.ListPatches(ctx, "ws1")
	if err != nil {
		t.Fatalf("ListPatches after clear: %v", err)
	}
	for _, p := range patches {
		if p.OriginSyncState != nil {
			t.Errorf("expected OriginSyncState=nil after clear, got %v for patch %s", p.OriginSyncState, p.ID)
		}
		if p.OriginSHA != nil {
			t.Errorf("expected OriginSHA=nil after clear, got %v for patch %s", p.OriginSHA, p.ID)
		}
		if p.OriginSyncedAt != nil {
			t.Errorf("expected OriginSyncedAt=nil after clear, got %v for patch %s", p.OriginSyncedAt, p.ID)
		}
	}

	// Verify existing methods still work (interface satisfaction).
	var _ PatchStore = store
}

func TestPatchStore_SetAndClearOriginSyncState_Mock_TS2044(t *testing.T) {
	mock := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", WorkspaceID: "ws1", BranchName: "feature/b", Position: 2, Status: PatchStatusActive},
	})
	ctx := context.Background()

	sha := "abc123"
	ts := "2024-06-01T12:00:00Z"

	// Set origin sync state.
	err := mock.SetOriginSyncState(ctx, "p1", "in_sync", &sha, ts)
	if err != nil {
		t.Fatalf("SetOriginSyncState on mock: %v", err)
	}

	// Verify it was recorded.
	if state, ok := mock.OriginSyncStates["p1"]; !ok {
		t.Error("expected origin sync state to be recorded for p1")
	} else {
		if state.State != "in_sync" {
			t.Errorf("expected state='in_sync', got %q", state.State)
		}
		if state.SHA == nil || *state.SHA != sha {
			t.Errorf("expected SHA=%q, got %v", sha, state.SHA)
		}
	}

	// Clear for workspace.
	err = mock.ClearOriginSyncState(ctx, "ws1")
	if err != nil {
		t.Fatalf("ClearOriginSyncState on mock: %v", err)
	}

	if !mock.OriginSyncCleared {
		t.Error("expected OriginSyncCleared to be true after ClearOriginSyncState")
	}

	// Verify mock still satisfies PatchStore interface.
	var _ PatchStore = mock
}

func TestPatchStore_ClearOriginSyncStateForPatch_SQL_TS2044(t *testing.T) {
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	seedWorkspace(t, db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, db, "p1", "ws1", "feature/a", 1, PatchStatusActive)
	seedPatch(t, db, "p2", "ws1", "feature/b", 2, PatchStatusActive)

	store := NewSQLPatchStore(db)
	ctx := context.Background()

	// Set state on both patches.
	sha := "abc123"
	ts := "2024-06-01T12:00:00Z"
	_ = store.SetOriginSyncState(ctx, "p1", "in_sync", &sha, ts)
	_ = store.SetOriginSyncState(ctx, "p2", "diverged", &sha, ts)

	// Clear only p1.
	err := store.ClearOriginSyncStateForPatch(ctx, "p1")
	if err != nil {
		t.Fatalf("ClearOriginSyncStateForPatch: %v", err)
	}

	patches, err := store.ListPatches(ctx, "ws1")
	if err != nil {
		t.Fatalf("ListPatches: %v", err)
	}

	for _, p := range patches {
		if p.ID == "p1" {
			if p.OriginSyncState != nil {
				t.Errorf("expected p1 OriginSyncState=nil after per-row clear, got %v", p.OriginSyncState)
			}
		}
		if p.ID == "p2" {
			if p.OriginSyncState == nil || *p.OriginSyncState != "diverged" {
				t.Errorf("expected p2 OriginSyncState='diverged' (unchanged), got %v", p.OriginSyncState)
			}
		}
	}
}
