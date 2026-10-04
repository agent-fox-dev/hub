package carrypatch

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// ===========================================================================
// TS-23-67 (integration): hub.patch.replace events emitted by sync carry
// no trigger key
// ===========================================================================

func TestSyncAudit_HubPatchReplaceNoTrigger_TS2367(t *testing.T) {
	// Build outcomes that include a replaced branch.
	outcomes := []PatchRefreshOutcome{
		{
			PatchID:     "p1",
			BranchName:  "feature/replaced",
			Action:      ActionReplaced,
			State:       StateInSync,
			LocalSHA:    "aaaa",
			OriginSHA:   "bbbb",
			ReplacedSHA: "aaaa",
		},
	}

	emitter := &recordingAuditEmitter{}
	auth := &apikit.AuthInfo{
		UserID:         "user-1",
		CredentialType: "api_key",
	}

	ctx := context.Background()
	emitSyncAuditEvents(ctx, emitter, auth, "test-slug", outcomes)

	// Find hub.patch.replace events.
	var replaceEvents []audit.HubEvent
	for _, ev := range emitter.events {
		if ev.EventType == audit.EventPatchReplace {
			replaceEvents = append(replaceEvents, ev)
		}
	}

	if len(replaceEvents) != 1 {
		t.Fatalf("hub.patch.replace events = %d; want 1", len(replaceEvents))
	}

	ev := replaceEvents[0]
	meta := ev.Metadata

	// The trigger key must NOT be present.
	if _, hasTrigger := meta["trigger"]; hasTrigger {
		t.Errorf("hub.patch.replace from sync has trigger key = %v; want absent", meta["trigger"])
	}

	// Verify the other metadata fields are present.
	if meta["branch_name"] != "feature/replaced" {
		t.Errorf("branch_name = %v; want %q", meta["branch_name"], "feature/replaced")
	}
	if meta["replaced_sha"] != "aaaa" {
		t.Errorf("replaced_sha = %v; want %q", meta["replaced_sha"], "aaaa")
	}
	if meta["origin_sha"] != "bbbb" {
		t.Errorf("origin_sha = %v; want %q", meta["origin_sha"], "bbbb")
	}
}

// ===========================================================================
// TS-23-70 (integration): A replaced reset and a backup removal log at
// info level with slug, branch and SHAs
// ===========================================================================

func TestRecoveryAudit_ReplacedResetInfoLog_TS2370(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "audit-log-replace"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	branch := "feature/audit-log"

	// Create a diverged scenario.
	forkTip := createBranchOnBare(t, forkDir, branch, "audit-fork.txt", "fork content", "fork commit")
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "audit-local.txt", "local content", "local commit")
	localTip := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "fetch", "origin", branch)

	// Capture log output.
	var logBuf strings.Builder
	handler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
	svc := newTestRecoveryServiceWithStore(workspaceRoot, nil, nil, sf.fetch, nil, nil)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p-audit-log",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "main",
	}

	result, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
	if err != nil {
		t.Fatalf("RunReset failed: %v", err)
	}

	if result.Action != ActionReplaced {
		t.Fatalf("action = %q; want %q", result.Action, ActionReplaced)
	}

	logStr := logBuf.String()

	// Verify an info entry for the replacement carries slug, branch, replaced_sha and origin_sha.
	if !strings.Contains(logStr, "level=INFO") {
		t.Error("expected at least one INFO log entry")
	}
	if !strings.Contains(logStr, slug) {
		t.Errorf("info log should contain slug %q", slug)
	}
	if !strings.Contains(logStr, branch) {
		t.Errorf("info log should contain branch %q", branch)
	}
	if !strings.Contains(logStr, localTip) {
		t.Errorf("info log should contain replaced_sha %q", localTip)
	}
	if !strings.Contains(logStr, forkTip) {
		t.Errorf("info log should contain origin_sha %q", forkTip)
	}
}

func TestRecoveryAudit_BackupRemovalInfoLog_TS2370(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "audit-log-remove"

	_, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	branch := "feature/audit-rm"
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "audit-rm.txt", "rm content", "rm commit")
	commitSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branch, commitSHA)
	runGitCmd(t, trunkDir, "checkout", "main")

	// Capture log output.
	var logBuf strings.Builder
	handler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	svc := &RecoveryService{
		NewGitRunner:  NewGitRunnerFactory(),
		WorkspaceRoot: workspaceRoot,
	}

	ctx := context.Background()
	err := svc.RemoveBackup(ctx, slug, branch)
	if err != nil {
		t.Fatalf("RemoveBackup error: %v", err)
	}

	// Verify the ref is gone.
	after := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
	if after != "" {
		t.Errorf("backup ref should be deleted, got %q", after)
	}

	logStr := logBuf.String()

	// Verify an info entry for the removal carries slug, branch and the removed ref.
	if !strings.Contains(logStr, "level=INFO") {
		t.Error("expected at least one INFO log entry for backup removal")
	}
	if !strings.Contains(logStr, slug) {
		t.Errorf("info log should contain slug %q", slug)
	}
	if !strings.Contains(logStr, branch) {
		t.Errorf("info log should contain branch %q", branch)
	}
}

// ===========================================================================
// recordingAuditEmitter is a test double for audit.Emitter.
// ===========================================================================

type recordingAuditEmitter struct {
	events []audit.HubEvent
}

func (r *recordingAuditEmitter) Emit(_ context.Context, event audit.HubEvent) error {
	r.events = append(r.events, event)
	return nil
}
