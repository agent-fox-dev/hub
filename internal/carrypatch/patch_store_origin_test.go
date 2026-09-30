package carrypatch

import (
	"context"
	"testing"
	"time"
)

func TestSQLPatchStore_OriginSyncState(t *testing.T) {
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)
	addUpstreamPRURLColumn(t, db)

	seedWorkspace(t, db, "ws1", "user-1", "active", "ready", "carry_patch", "integration")
	seedPatch(t, db, "p1", "ws1", "feature/one", 1, PatchStatusActive)
	seedPatch(t, db, "p2", "ws1", "feature/two", 2, PatchStatusActive)

	store := NewSQLPatchStore(db)
	ctx := context.Background()

	// Initial list: origin fields should be nil
	patches, err := store.ListPatches(ctx, "ws1")
	if err != nil {
		t.Fatalf("ListPatches returned error: %v", err)
	}
	if len(patches) != 2 {
		t.Fatalf("expected 2 patches, got %d", len(patches))
	}
	if patches[0].OriginSyncState != nil || patches[0].OriginSHA != nil || patches[0].OriginSyncedAt != nil {
		t.Errorf("expected initial origin fields to be nil, got state=%v, sha=%v, synced_at=%v",
			patches[0].OriginSyncState, patches[0].OriginSHA, patches[0].OriginSyncedAt)
	}

	// Update p1 with origin sync state
	stateInSync := "in_sync"
	sha1 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	syncedAt := time.Now().UTC().Format(time.RFC3339)
	if err := store.SetOriginSyncState(ctx, "p1", &stateInSync, &sha1, syncedAt); err != nil {
		t.Fatalf("SetOriginSyncState returned error: %v", err)
	}

	// List again and verify p1 has origin fields populated, p2 still nil
	patches, err = store.ListPatches(ctx, "ws1")
	if err != nil {
		t.Fatalf("ListPatches returned error: %v", err)
	}
	if patches[0].OriginSyncState == nil || *patches[0].OriginSyncState != "in_sync" {
		t.Errorf("p1 origin_sync_state = %v, want 'in_sync'", patches[0].OriginSyncState)
	}
	if patches[0].OriginSHA == nil || *patches[0].OriginSHA != sha1 {
		t.Errorf("p1 origin_sha = %v, want %s", patches[0].OriginSHA, sha1)
	}
	if patches[0].OriginSyncedAt == nil || *patches[0].OriginSyncedAt != syncedAt {
		t.Errorf("p1 origin_synced_at = %v, want %s", patches[0].OriginSyncedAt, syncedAt)
	}
	if patches[1].OriginSyncState != nil {
		t.Errorf("p2 origin_sync_state = %v, want nil", patches[1].OriginSyncState)
	}

	// Clear origin sync state for workspace
	if err := store.ClearOriginSyncState(ctx, "ws1"); err != nil {
		t.Fatalf("ClearOriginSyncState returned error: %v", err)
	}

	// Verify p1's origin fields are now cleared
	patches, err = store.ListPatches(ctx, "ws1")
	if err != nil {
		t.Fatalf("ListPatches returned error: %v", err)
	}
	if patches[0].OriginSyncState != nil || patches[0].OriginSHA != nil || patches[0].OriginSyncedAt != nil {
		t.Errorf("expected cleared origin fields on p1, got state=%v, sha=%v, synced_at=%v",
			patches[0].OriginSyncState, patches[0].OriginSHA, patches[0].OriginSyncedAt)
	}
}

func TestMockPatchStore_OriginSyncState(t *testing.T) {
	state := "diverged"
	sha := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	syncedAt := "2025-01-01T00:00:00Z"

	mock := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/one", Position: 1, Status: PatchStatusActive},
	})
	ctx := context.Background()

	if err := mock.SetOriginSyncState(ctx, "p1", &state, &sha, syncedAt); err != nil {
		t.Fatalf("SetOriginSyncState error: %v", err)
	}
	call, ok := mock.OriginSyncStates["p1"]
	if !ok {
		t.Fatal("expected OriginSyncStates to record p1 call")
	}
	if call.State == nil || *call.State != "diverged" || call.SHA == nil || *call.SHA != sha || call.SyncedAt != syncedAt {
		t.Errorf("unexpected call recorded: %+v", call)
	}

	// Check updated in patch
	patches, _ := mock.ListPatches(ctx, "ws1")
	if patches[0].OriginSyncState == nil || *patches[0].OriginSyncState != "diverged" {
		t.Errorf("patch origin_sync_state = %v, want diverged", patches[0].OriginSyncState)
	}

	// Clear
	if err := mock.ClearOriginSyncState(ctx, "ws1"); err != nil {
		t.Fatalf("ClearOriginSyncState error: %v", err)
	}
	if len(mock.ClearedOriginSync) != 1 || mock.ClearedOriginSync[0] != "ws1" {
		t.Errorf("ClearedOriginSync = %v, want ['ws1']", mock.ClearedOriginSync)
	}
	patches, _ = mock.ListPatches(ctx, "ws1")
	if patches[0].OriginSyncState != nil {
		t.Errorf("patch origin_sync_state = %v, want nil", patches[0].OriginSyncState)
	}
}
