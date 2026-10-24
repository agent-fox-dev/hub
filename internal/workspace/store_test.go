package workspace

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// TestStore_InsertAndRead: insertWorkspace fills the defaults and the
// timestamps, a second insert of the slug is the unique constraint, a missing
// slug reads as nil, and every field round-trips.
func TestStore_InsertAndRead(t *testing.T) {
	v := newEnv(t)
	settings := DefaultSettings()
	settings.RebuildStrategy = "merge"
	full := &Workspace{Slug: "full", GitURL: "git@h:p", Branch: strPtr("dev"), OwnerID: owner, OrgID: strPtr("o"), Status: "active",
		DisplayName: "Full", Description: "d", CloneStatus: "ready", HeadSHA: strPtr(headSHA), CloneError: strPtr("e"), SyncMode: "disabled",
		SyncStatus: "error", UpstreamHeadSHA: strPtr(upstreamSHA), LastSyncAt: strPtr("2026-01-01T00:00:00Z"), SyncError: strPtr("se"),
		WorkspaceMode: "carry_patch", UpstreamURL: strPtr("https://u/p"), IntegrationBranch: strPtr("deploy"), Settings: settings}
	if err := insertWorkspace(v.db, full); err != nil {
		t.Fatal(err)
	}
	got, err := getWorkspaceBySlug(v.db, "full")
	if err != nil {
		t.Fatal(err)
	}
	if *got.Branch != "dev" || *got.OrgID != "o" || *got.HeadSHA != headSHA || *got.CloneError != "e" || got.SyncMode != "disabled" || got.SyncStatus != "error" ||
		*got.UpstreamHeadSHA != upstreamSHA || *got.LastSyncAt != "2026-01-01T00:00:00Z" || *got.SyncError != "se" || got.WorkspaceMode != "carry_patch" ||
		*got.UpstreamURL != "https://u/p" || *got.IntegrationBranch != "deploy" || got.Settings.RebuildStrategy != "merge" || got.DisplayName != "Full" {
		t.Errorf("round trip %+v", got)
	}
	if _, err := time.Parse(timestampFormat, got.CreatedAt); err != nil || got.CreatedAt != got.UpdatedAt || len(got.CreatedAt) != len("2006-01-02T15:04:05.000000Z") {
		t.Errorf("timestamps %q %q: %v", got.CreatedAt, got.UpdatedAt, err)
	}

	minimal := &Workspace{Slug: "min", GitURL: "https://h/p", OwnerID: owner}
	if err := insertWorkspace(v.db, minimal); err != nil {
		t.Fatal(err)
	}
	if got, _ := getWorkspaceBySlug(v.db, "min"); got.CloneStatus != "pending" || got.SyncMode != "pull_only" || got.SyncStatus != "idle" || got.WorkspaceMode != "standard" || got.Settings != DefaultSettings() {
		t.Errorf("defaults %+v", got)
	}
	err = insertWorkspace(v.db, minimal)
	if !isUniqueConstraintError(err) || isUniqueConstraintError(errors.New("other")) || isUniqueConstraintError(nil) {
		t.Errorf("duplicate insert: %v", err)
	}
	if got, err := getWorkspaceBySlug(v.db, "nope"); got != nil || err != nil {
		t.Errorf("missing slug: %v %v", got, err)
	}
	testutil.Exec(t, v.db, `DROP TABLE workspaces`)
	if _, err := getWorkspaceBySlug(v.db, "min"); err == nil {
		t.Error("read without the table succeeded")
	}
	if _, err := listAllWorkspaces(v.db, true); err == nil {
		t.Error("list without the table succeeded")
	}
	if _, err := listWorkspacesBySlugs(v.db, []string{"min"}, true); err == nil {
		t.Error("list by slugs without the table succeeded")
	}
}

// TestStore_Transitions: the row updates of the handlers write what they say
// and refresh updated_at.
func TestStore_Transitions(t *testing.T) {
	v := newEnv(t)
	v.seed(t, &Workspace{Slug: "ws", OwnerID: owner, CloneStatus: "failed", CloneError: strPtr("boom")})
	before := v.column(t, "ws", "updated_at")
	time.Sleep(time.Millisecond)

	ws, err := updateWorkspaceRow(v.db, "ws", "Name", "desc", strPtr("org"), "disabled", DefaultSettings())
	if err != nil || ws.DisplayName != "Name" || ws.Description != "desc" || *ws.OrgID != "org" || ws.SyncMode != "disabled" || ws.UpdatedAt <= before {
		t.Errorf("update: %+v %v", ws, err)
	}
	if err := updateCloneStatus(v.db, "ws", "ready", strPtr(headSHA), nil); err != nil {
		t.Fatal(err)
	}
	if status, sha, e, _ := getCloneFields(v.db, "ws"); status != "ready" || *sha != headSHA || e != nil {
		t.Errorf("clone fields %s %v %v", status, sha, e)
	}
	ws, err = archiveWorkspaceDB(v.db, "ws", strPtr(upstreamSHA))
	if err != nil || ws.Status != "archived" || ws.CloneStatus != "archived" || *ws.HeadSHA != upstreamSHA || ws.CloneError != nil {
		t.Errorf("archive: %+v %v", ws, err)
	}
	ws, err = reactivateWorkspaceDB(v.db, "ws")
	if err != nil || ws.Status != "active" || ws.CloneStatus != "pending" || ws.CloneError != nil || *ws.HeadSHA != upstreamSHA {
		t.Errorf("reactivate: %+v %v", ws, err)
	}
	now := "2026-02-02T00:00:00Z"
	for i, set := range []func() error{
		func() error { return setSyncStatus(v.db, "ws", "idle", strPtr(upstreamSHA), nil, strPtr(now)) },
		func() error { return setSyncStatus(v.db, "ws", "idle", strPtr(headSHA), nil, nil) },
		func() error { return setSyncStatus(v.db, "ws", "error", nil, strPtr("e"), strPtr(now)) },
		func() error { return setSyncStatus(v.db, "ws", "error", nil, strPtr("f"), nil) },
	} {
		if err := set(); err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
	}
	if got, _ := getWorkspaceBySlug(v.db, "ws"); got.SyncStatus != "error" || *got.SyncError != "f" || *got.UpstreamHeadSHA != headSHA || *got.LastSyncAt != now {
		t.Errorf("after the sync status writes %+v", got)
	}
	if err := setSyncStatusDiverged(v.db, "ws", strPtr(upstreamSHA), strPtr("diverged")); err != nil {
		t.Fatal(err)
	}
	if got, _ := getWorkspaceBySlug(v.db, "ws"); *got.UpstreamHeadSHA != upstreamSHA || *got.SyncError != "diverged" || *got.LastSyncAt != now || *got.HeadSHA != upstreamSHA {
		t.Errorf("after the divergence %+v", got)
	}
	if err := setSyncStatusWithHeadSHA(v.db, "ws", "idle", strPtr(headSHA), strPtr(headSHA), nil, strPtr(now)); err != nil {
		t.Fatal(err)
	}
	if got, _ := getWorkspaceBySlug(v.db, "ws"); *got.HeadSHA != headSHA || got.SyncError != nil {
		t.Errorf("after the fast-forward write %+v", got)
	}
	testutil.Exec(t, v.db, `DROP TABLE workspaces`)
	for name, err := range map[string]error{
		"update":     func() error { _, err := updateWorkspaceRow(v.db, "ws", "", "", nil, "", DefaultSettings()); return err }(),
		"archive":    func() error { _, err := archiveWorkspaceDB(v.db, "ws", nil); return err }(),
		"reactivate": func() error { _, err := reactivateWorkspaceDB(v.db, "ws"); return err }(),
		"clone":      updateCloneStatus(v.db, "ws", "ready", nil, nil),
		"fields":     func() error { _, _, _, err := getCloneFields(v.db, "ws"); return err }(),
		"delete":     deleteWorkspace(v.db, "ws"),
	} {
		if err == nil {
			t.Errorf("%s without the table succeeded", name)
		}
	}
}

// TestStore_DeleteUnknown: deleting a slug that is not there is an error, and
// the transaction's work is rolled back.
func TestStore_DeleteUnknown(t *testing.T) {
	v := newEnv(t)
	testutil.BindToken(t, v.db, "t", "ghost")
	seedPAT(t, v.db, "t", "")
	if err := deleteWorkspace(v.db, "ghost"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("delete of a missing slug: %v", err)
	}
	if testutil.Count(t, v.db, `SELECT COUNT(*) FROM token_workspace_bindings`) != 1 || revokedAt(t, v.db, "t").Valid {
		t.Error("the rolled back deletion revoked or unbound the token")
	}
}
