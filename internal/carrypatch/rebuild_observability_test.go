package carrypatch

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// TS-01-66: worktree creation and removal, per-run and startup stale cleanup
// and the _rebuild_temp migration are logged at info with slug and path;
// removal failures are logged at warning.
// Requirement: 01-REQ-10.1
// ===========================================================================

func TestObservability_TS_01_66_InfoLogsCarrySlugAndPath(t *testing.T) {
	e := newWtEnv(t, onePatch())

	// A stale directory for the per-run cleanup.
	stale := filepath.Join(e.rebuildDir(), "crashed-run")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	// A legacy _rebuild_temp branch for the migration. The trunk mock
	// answers for-each-ref with the exact ref name.
	base := e.trunk.RunFunc
	e.trunk.RunFunc = func(ctx context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "for-each-ref" && args[len(args)-1] == "refs/heads/_rebuild_temp" {
			return "refs/heads/_rebuild_temp", nil
		}
		return base(ctx, args...)
	}

	if _, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, "")); err != nil {
		t.Fatalf("HandleRebuildJob: %v", err)
	}
	if len(e.trunk.WorktreeAddCalls) != 1 {
		t.Fatalf("WorktreeAdd calls = %d, want 1", len(e.trunk.WorktreeAddCalls))
	}
	wtPath := e.trunk.WorktreeAddCalls[0].Path

	for _, tc := range []struct {
		name string
		subs []string
	}{
		{"worktree creation", []string{"created", e.slug, wtPath}},
		{"worktree removal", []string{"removed", e.slug, wtPath}},
		{"per-run stale cleanup", []string{"stale", e.slug, stale}},
		{"_rebuild_temp migration", []string{"_rebuild_temp", e.slug, e.trunkPath()}},
	} {
		if !e.logs.has(slog.LevelInfo, tc.subs...) {
			t.Errorf("no info log for %s containing %v; records=%+v", tc.name, tc.subs, e.logs.records)
		}
	}

	// Startup cleanup: a workspace with a stale rebuild dir and no trunk.
	root := t.TempDir()
	startupDir := filepath.Join(root, "ws-startup", "rebuild")
	if err := os.MkdirAll(filepath.Join(startupDir, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	logs := &capturedLog{}
	CleanupStaleRebuildWorktrees(context.Background(), root, slog.New(logs))
	if !logs.has(slog.LevelInfo, "ws-startup", startupDir) {
		t.Errorf("no startup-cleanup info log with slug and path; records=%+v", logs.records)
	}
}

func TestObservability_TS_01_66_RemovalFailureIsWarning(t *testing.T) {
	e := newWtEnv(t, onePatch())
	e.trunk.WorktreeRemoveErr = errors.New("worktree remove failed")
	e.trunk.OnWorktreeRemove = nil

	if _, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, "")); err != nil {
		t.Fatalf("HandleRebuildJob: %v", err)
	}
	path := e.trunk.WorktreeAddCalls[0].Path
	if !e.logs.has(slog.LevelWarn, e.slug, path) {
		t.Errorf("no warning with slug %q and path %q; records=%+v", e.slug, path, e.logs.records)
	}
}

// ===========================================================================
// TS-01-67: hub.rebuild.complete on success, hub.rebuild.fail on every error
// return, with the unchanged event shape.
// Requirement: 01-REQ-10.2
// ===========================================================================

// errListStore fails ListPatches.
type errListStore struct{ PatchStore }

func (errListStore) ListPatches(context.Context, string) ([]Patch, error) {
	return nil, errors.New("list failed")
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestObservability_TS_01_67_AuditEventShape(t *testing.T) {
	type scenario struct {
		name      string
		setup     func(e *wtEnv)
		wantType  string
		wantKeys  []string
		wantMeta  map[string]any
		wantError bool
	}
	scenarios := []scenario{
		{name: "success", wantType: "hub.rebuild.complete", wantKeys: []string{"patches_applied"},
			wantMeta: map[string]any{"patches_applied": 1}},
		{name: "resolve auth", wantError: true, setup: func(e *wtEnv) {
			e.h.ResolveAuth = func(string) (transport.AuthMethod, error) { return nil, errors.New("no creds") }
		}},
		{name: "fetch", wantError: true, setup: func(e *wtEnv) {
			e.h.Fetch = func(context.Context, string, transport.AuthMethod) error { return errors.New("fetch failed") }
		}},
		{name: "trunk runner", wantError: true, setup: func(e *wtEnv) {
			e.h.NewGitRunner = func(string) (GitRunner, error) { return nil, errors.New("no git") }
		}},
		{name: "upstream base", wantError: true, setup: func(e *wtEnv) {
			e.trunk.RunFunc = func(_ context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "rev-parse" {
					return "", errors.New("no base")
				}
				return "", nil
			}
		}},
		{name: "worktree add", wantError: true, setup: func(e *wtEnv) {
			e.trunk.WorktreeAddErr = errors.New("add failed")
		}},
		{name: "list patches", wantError: true, setup: func(e *wtEnv) {
			e.h.PatchStore = errListStore{e.patches}
		}},
		{name: "cherry-pick", wantError: true, setup: func(e *wtEnv) {
			e.wt.CherryPickFunc = func(context.Context, string) error { return errors.New("boom") }
		}},
		{name: "fail-fast conflict", wantError: true, setup: func(e *wtEnv) {
			e.wt.CherryPickFunc = func(context.Context, string) error { return &CherryPickConflictError{} }
			base := e.wt.RunFunc
			e.wt.RunFunc = func(ctx context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "diff" {
					return "conflict.txt", nil
				}
				return base(ctx, args...)
			}
		}},
		{name: "update-ref", wantError: true, setup: func(e *wtEnv) {
			e.trunk.UpdateRefErr = errors.New("update-ref failed")
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			e := newWtEnv(t, onePatch())
			em := newCPAuditEmitter()
			e.h.Audit = em
			if sc.setup != nil {
				sc.setup(e)
			}
			_, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, ""))
			if (err != nil) != sc.wantError {
				t.Fatalf("err = %v, wantError = %v", err, sc.wantError)
			}
			events := em.Events()
			if len(events) != 1 {
				t.Fatalf("emitted %d events, want exactly 1: %+v", len(events), events)
			}
			ev := events[0]
			wantType, wantKeys := sc.wantType, sc.wantKeys
			if sc.wantError {
				wantType, wantKeys = "hub.rebuild.fail", []string{"reason"}
			}
			if ev.EventType != wantType {
				t.Errorf("event type = %q, want %q", ev.EventType, wantType)
			}
			if got := sortedKeys(ev.Metadata); !reflect.DeepEqual(got, wantKeys) {
				t.Errorf("metadata keys = %v, want %v", got, wantKeys)
			}
			if sc.wantMeta != nil && !reflect.DeepEqual(ev.Metadata, sc.wantMeta) {
				t.Errorf("metadata = %v, want %v", ev.Metadata, sc.wantMeta)
			}
			// The envelope is unchanged: patch resource, system actor, the
			// workspace slug.
			if ev.ResourceType != "patch" || ev.ActorType != "system" || ev.Workspace != e.slug {
				t.Errorf("event envelope = {%q %q %q}, want {patch system %s}", ev.ResourceType, ev.ActorType, ev.Workspace, e.slug)
			}
			if sc.wantError {
				if reason, _ := ev.Metadata["reason"].(string); reason == "" {
					t.Error("hub.rebuild.fail carries no reason")
				}
			}
		})
	}
}

// ===========================================================================
// TS-01-68: rebuild API responses and job records are unchanged apart from
// the additive patch_results[].source_sha field.
// Requirement: 01-REQ-10.3
// ===========================================================================

// stripSourceSHA removes patch_results[].source_sha from a decoded document.
func stripSourceSHA(doc map[string]any) {
	if prs, ok := doc["patch_results"].([]any); ok {
		for _, pr := range prs {
			if m, ok := pr.(map[string]any); ok {
				delete(m, "source_sha")
			}
		}
	}
}

func TestObservability_TS_01_68_ResponseShapeUnchangedExceptSourceSHA(t *testing.T) {
	// Golden JSON of a rebuild result as it was before source_sha existed.
	const golden = `{
	  "upstream_head_sha": "u1",
	  "integration_head_sha": "i1",
	  "previous_integration_head_sha": "p0",
	  "strategy": "rebase",
	  "fail_mode": "fail_fast",
	  "patches_applied": 1,
	  "patches_skipped": 1,
	  "patches_conflicted": 0,
	  "patches_removed": 0,
	  "patch_results": [
	    {"patch_id": "a", "branch_name": "feature/a", "position": 1, "status": "success", "new_head_sha": "n1"},
	    {"patch_id": "b", "branch_name": "feature/b", "position": 2, "status": "skipped", "skipped_reason": "disabled", "new_head_sha": null}
	  ],
	  "integration_branch_pushed": true
	}`
	n1 := "n1"
	result := RebuildResult{
		UpstreamHeadSHA: "u1", IntegrationHeadSHA: "i1", PreviousIntegrationHeadSHA: "p0",
		Strategy: "rebase", FailMode: "fail_fast", PatchesApplied: 1, PatchesSkipped: 1,
		PatchResults: []PatchResult{
			{PatchID: "a", BranchName: "feature/a", Position: 1, Status: "success", NewHeadSHA: &n1, SourceSHA: "s1"},
			{PatchID: "b", BranchName: "feature/b", Position: 2, Status: "skipped", SkippedReason: "disabled"},
		},
		IntegrationBranchPushed: true,
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var actual, want map[string]any
	if err := json.Unmarshal(raw, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(golden), &want); err != nil {
		t.Fatal(err)
	}

	// source_sha is additive: present for the snapshotted patch, omitted
	// when empty.
	prs := actual["patch_results"].([]any)
	if got := prs[0].(map[string]any)["source_sha"]; got != "s1" {
		t.Errorf("source_sha = %v, want s1", got)
	}
	if _, present := prs[1].(map[string]any)["source_sha"]; present {
		t.Error("source_sha must be omitted when empty")
	}
	stripSourceSHA(actual)
	if !reflect.DeepEqual(actual, want) {
		t.Errorf("result JSON differs from golden:\n got:  %v\n want: %v", actual, want)
	}

	// The job record keeps its field set; patch_results passes source_sha
	// through and nothing else is added.
	updated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	job := &jobqueue.Job{ID: "j1", Status: jobqueue.StatusCompleted, Payload: json.RawMessage(`{"strategy":"rebase"}`), Result: raw, CreatedAt: updated, UpdatedAt: updated}
	rec := jobToRebuildRecord(job)
	recRaw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var recDoc map[string]any
	if err := json.Unmarshal(recRaw, &recDoc); err != nil {
		t.Fatal(err)
	}
	stripSourceSHA(recDoc)
	var wantKeys []string
	for k := range recDoc {
		wantKeys = append(wantKeys, k)
	}
	sort.Strings(wantKeys)
	goldenKeys := []string{"completed_at", "created_at", "id", "integration_head_sha", "patch_results",
		"previous_integration_head_sha", "status", "strategy"}
	if !reflect.DeepEqual(wantKeys, goldenKeys) {
		t.Errorf("job record keys = %v, want %v", wantKeys, goldenKeys)
	}
	if !reflect.DeepEqual(recDoc["patch_results"], want["patch_results"]) {
		t.Errorf("job record patch_results differ from golden: %v", recDoc["patch_results"])
	}

	// Existing audit event names are unchanged; the only addition is
	// hub.rebuild.followup.
	if audit.EventRebuildFollowup != "hub.rebuild.followup" {
		t.Errorf("follow-up event name = %q", audit.EventRebuildFollowup)
	}
	src, err := os.ReadFile("rebuild_executor.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{`"hub.rebuild.complete"`, `"hub.rebuild.fail"`, `"hub.rebuild.push_failed"`} {
		if !strings.Contains(string(src), name) {
			t.Errorf("rebuild executor no longer emits %s", name)
		}
	}
}
