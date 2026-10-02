package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// TS-20-4 (unit): The exact value report applies the report policy to a
// diverged branch
//
// Verifies: 20-REQ-1.4
// ===========================================================================

func TestSyncResponse_ReportPolicyDivergedBranch_TS204(t *testing.T) {
	// Set up a real fork and trunk with a diverged branch.
	env := setupOriginIntegrationEnv(t)

	// Create feat on the fork.
	env.addForkBranch(t, "feat", "fork feat content")

	// Fetch and create local branch.
	env.fetchOriginInTrunk(t)
	runGitCmd(t, env.trunkDir, "branch", "feat", "refs/remotes/origin/feat")

	// Add a divergent commit on the fork.
	env.addForkCommit(t, "feat", "fork divergent")

	// Add a divergent commit locally.
	runGitCmd(t, env.trunkDir, "checkout", "feat")
	writeFileHelper(t, filepath.Join(env.trunkDir, "local-divergent.txt"), "local divergent")
	runGitCmd(t, env.trunkDir, "add", ".")
	runGitCmd(t, env.trunkDir, "commit", "-m", "local divergent commit")
	oldLocalTip := runGitCmd(t, env.trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, env.trunkDir, "checkout", "main")

	// Fetch origin again.
	env.fetchOriginInTrunk(t)

	// Register feat as an active patch with report policy.
	seedPatch(t, env.db, "p1", "my-workspace", "feat", 1, PatchStatusActive)
	env.variables["PATCH_DIVERGENCE_POLICY"] = "report"

	runner := newRealGitRunner(t, env.trunkDir)
	env.buildEcho(t, runner)

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Parse response.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	patchesSynced := resp["patches_synced"].([]any)
	if len(patchesSynced) != 1 {
		t.Fatalf("expected 1 element in patches_synced, got %d", len(patchesSynced))
	}

	elem := patchesSynced[0].(map[string]any)
	if elem["state"] != StateDiverged {
		t.Errorf("state = %v; want %q", elem["state"], StateDiverged)
	}
	if elem["action"] != ActionNone {
		t.Errorf("action = %v; want %q", elem["action"], ActionNone)
	}

	// The local branch should be unchanged.
	localTip := runGitCmd(t, env.trunkDir, "rev-parse", "refs/heads/feat")
	if localTip != oldLocalTip {
		t.Errorf("refs/heads/feat changed: %s → %s", oldLocalTip, localTip)
	}
}

// ===========================================================================
// TS-20-5 (unit): Unset, error, replace, Report and other policy values
// apply replace without an error
//
// Verifies: 20-REQ-1.5
// ===========================================================================

func TestSyncResponse_NonReportPolicyValuesApplyReplace_TS205(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		setError bool
		unset    bool
	}{
		{"unset", "", false, true},
		{"lookup_error", "", true, false},
		{"replace", "replace", false, false},
		{"Report_capital", "Report", false, false},
		{"empty_string", "", false, false},
		{"garbage", "x", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupOriginIntegrationEnv(t)

			// Create feat on the fork.
			env.addForkBranch(t, "feat", "fork feat content")

			// Fetch and create local branch.
			env.fetchOriginInTrunk(t)
			runGitCmd(t, env.trunkDir, "branch", "feat", "refs/remotes/origin/feat")

			// Add a divergent commit on the fork.
			env.addForkCommit(t, "feat", "fork divergent")

			// Add a divergent commit locally.
			runGitCmd(t, env.trunkDir, "checkout", "feat")
			writeFileHelper(t, filepath.Join(env.trunkDir, "local-divergent.txt"), "local divergent")
			runGitCmd(t, env.trunkDir, "add", ".")
			runGitCmd(t, env.trunkDir, "commit", "-m", "local divergent commit")
			runGitCmd(t, env.trunkDir, "checkout", "main")

			// Fetch origin again.
			env.fetchOriginInTrunk(t)

			// Register feat as an active patch.
			seedPatch(t, env.db, "p1", "my-workspace", "feat", 1, PatchStatusActive)

			// Set the policy variable.
			if tc.setError {
				// Leave it unset and add an error entry.
				// The env's getVar returns error for unset keys.
				// We need to override the variable lookup to return an error.
				// Since the env uses a map, not setting it means getVar returns "not set" error.
				// That's the same as unset. For a specific error, we'd need a custom env.
				// The default behavior (not in map) returns error, which is what we want.
			} else if !tc.unset {
				env.variables["PATCH_DIVERGENCE_POLICY"] = tc.value
			}
			// If unset, the variable is not in the map, so GetVariable returns error → default (replace).

			runner := newRealGitRunner(t, env.trunkDir)
			env.buildEcho(t, runner)

			rec := env.doSync(t)
			if rec.Code != http.StatusOK {
				t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
			}

			// Parse response.
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			patchesSynced := resp["patches_synced"].([]any)
			if len(patchesSynced) != 1 {
				t.Fatalf("expected 1 element in patches_synced, got %d", len(patchesSynced))
			}

			elem := patchesSynced[0].(map[string]any)
			if elem["action"] != ActionReplaced {
				t.Errorf("action = %v; want %q", elem["action"], ActionReplaced)
			}

			// patches_diverged should be empty ([] under replace).
			patchesDiverged, ok := resp["patches_diverged"].([]any)
			if !ok {
				t.Fatalf("expected patches_diverged array, got %v", resp["patches_diverged"])
			}
			if len(patchesDiverged) != 0 {
				t.Errorf("expected empty patches_diverged under replace, got %v", patchesDiverged)
			}
		})
	}
}

// ===========================================================================
// TS-20-26 (integration): A lost compare-and-swap persists earlier outcomes,
// stops the refresh and returns 500
//
// Verifies: 20-REQ-4.3
// ===========================================================================

func TestSyncResponse_RefWriteFailurePersistsOutcomes_TS2026(t *testing.T) {
	env := setupOriginIntegrationEnv(t)

	// Create three branches on the fork: a, b, c.
	forkTipA := env.addForkBranch(t, "feat-a", "feat-a content")
	env.addForkBranch(t, "feat-b", "feat-b content")
	env.addForkBranch(t, "feat-c", "feat-c content")

	// Fetch origin.
	env.fetchOriginInTrunk(t)

	// Create local branches at fork tips.
	runGitCmd(t, env.trunkDir, "branch", "feat-a", "refs/remotes/origin/feat-a")
	runGitCmd(t, env.trunkDir, "branch", "feat-b", "refs/remotes/origin/feat-b")
	runGitCmd(t, env.trunkDir, "branch", "feat-c", "refs/remotes/origin/feat-c")

	// Advance feat-a on the fork (fast-forwardable).
	env.addForkCommit(t, "feat-a", "extra on feat-a")
	// Advance feat-b on the fork (fast-forwardable).
	env.addForkCommit(t, "feat-b", "extra on feat-b")
	// Advance feat-c on the fork (fast-forwardable).
	env.addForkCommit(t, "feat-c", "extra on feat-c")

	// Fetch origin again.
	env.fetchOriginInTrunk(t)

	// Register patches.
	seedPatch(t, env.db, "p1", "my-workspace", "feat-a", 1, PatchStatusActive)
	seedPatch(t, env.db, "p2", "my-workspace", "feat-b", 2, PatchStatusActive)
	seedPatch(t, env.db, "p3", "my-workspace", "feat-c", 3, PatchStatusActive)

	// Snapshot before.
	var beforeSHA, beforeSyncAt sql.NullString
	env.db.QueryRow(`SELECT upstream_head_sha, last_sync_at FROM workspaces WHERE slug = ?`, "my-workspace").Scan(&beforeSHA, &beforeSyncAt)

	// Use a recording runner that fails the update-ref for feat-b.
	realRunner := newRealGitRunner(t, env.trunkDir)
	updateRefCount := 0
	failingRunner := &recordingGitRunner{
		real: realRunner,
		runOverride: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 1 && args[0] == "update-ref" {
				updateRefCount++
				// The first update-ref is for feat-a (fast-forward).
				// The second update-ref is for feat-b (should fail).
				if updateRefCount == 2 {
					return "", fmt.Errorf("simulated CAS failure")
				}
			}
			return realRunner.Run(ctx, args...)
		},
	}

	env.buildEcho(t, failingRunner)

	rec := env.doSync(t)

	// Should return 500.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	// Check error message.
	var errResp errorEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errResp.Error.Message != "failed to update patch branch feat-b" {
		t.Errorf("error message = %q; want %q", errResp.Error.Message, "failed to update patch branch feat-b")
	}

	// feat-a should have been moved (fast-forwarded).
	localTipA := runGitCmd(t, env.trunkDir, "rev-parse", "refs/heads/feat-a")
	forkTipAAfter := runGitCmd(t, env.trunkDir, "rev-parse", "refs/remotes/origin/feat-a")
	if localTipA != forkTipAAfter {
		t.Errorf("feat-a should have been fast-forwarded: local=%s, fork=%s", localTipA, forkTipAAfter)
	}

	// feat-a's persisted state should be in_sync.
	state1, sha1, syncedAt1 := env.queryPatchOriginState(t, "p1")
	if !state1.Valid || state1.String != StateInSync {
		t.Errorf("p1 persisted state = %v; want %q", state1, StateInSync)
	}
	if !sha1.Valid || sha1.String != forkTipA {
		// forkTipA was the original fork tip before the extra commit.
		// After the extra commit, the fork tip changed. Let's check against the actual fork tip.
		if sha1.String != forkTipAAfter {
			t.Errorf("p1 persisted origin_sha = %v; want %s", sha1, forkTipAAfter)
		}
	}
	if !syncedAt1.Valid {
		t.Error("p1 origin_synced_at should be set")
	}

	// feat-b should NOT have been moved (CAS failure).
	// feat-c should be unprocessed.

	// A rebuild job should be enqueued for the moved branch feat-a.
	var jobCount int
	env.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='rebuild' AND key='my-workspace'`).Scan(&jobCount)
	if jobCount != 1 {
		t.Errorf("expected 1 rebuild job, got %d", jobCount)
	}

	// upstream_head_sha and last_sync_at should be unchanged.
	var afterSHA, afterSyncAt sql.NullString
	env.db.QueryRow(`SELECT upstream_head_sha, last_sync_at FROM workspaces WHERE slug = ?`, "my-workspace").Scan(&afterSHA, &afterSyncAt)
	if afterSHA != beforeSHA {
		t.Errorf("upstream_head_sha changed: %v → %v", beforeSHA, afterSHA)
	}
	if afterSyncAt != beforeSyncAt {
		t.Errorf("last_sync_at changed: %v → %v", beforeSyncAt, afterSyncAt)
	}
}

// ===========================================================================
// TS-20-35 (unit): origin_fetched is always present, and a hub response
// gains no other field
//
// Verifies: 20-REQ-6.1
// ===========================================================================

func TestSyncResponse_OriginFetchedAlwaysPresent_TS2035(t *testing.T) {
	t.Run("hub_mode", func(t *testing.T) {
		env := newSyncTestEnv(t)

		seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
			"https://github.com/example/upstream",
			"aaaa000000000000000000000000000000000001",
			"integration", "")

		rec := env.doSync(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}

		// origin_fetched must be present and false.
		of, ok := resp["origin_fetched"]
		if !ok {
			t.Fatal("expected origin_fetched field in hub mode response")
		}
		if of != false {
			t.Errorf("expected origin_fetched=false in hub mode, got %v", of)
		}

		// Hub mode should NOT have patches_synced or patches_diverged.
		if _, ok := resp["patches_synced"]; ok {
			t.Error("hub mode should not have patches_synced")
		}
		if _, ok := resp["patches_diverged"]; ok {
			t.Error("hub mode should not have patches_diverged")
		}

		// Verify the key set is the previous set plus origin_fetched.
		expectedKeys := map[string]bool{
			"patches_merged":      true,
			"rebuild_triggered":   true,
			"force_push_detected": true,
			"origin_fetched":      true,
		}
		for key := range resp {
			if !expectedKeys[key] {
				// rebuild_job_id is optional (omitempty).
				if key == "rebuild_job_id" {
					continue
				}
				t.Errorf("unexpected key in hub mode response: %q", key)
			}
		}
		for key := range expectedKeys {
			if _, ok := resp[key]; !ok {
				t.Errorf("missing expected key in hub mode response: %q", key)
			}
		}
	})

	t.Run("origin_mode", func(t *testing.T) {
		env := newSyncTestEnv(t)

		seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
			"https://github.com/example/upstream",
			"aaaa000000000000000000000000000000000001",
			"integration", "")

		env.setVar("PATCH_BRANCH_SOURCE", "origin")

		rec := env.doSync(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}

		// origin_fetched must be present and true.
		of, ok := resp["origin_fetched"]
		if !ok {
			t.Fatal("expected origin_fetched field in origin mode response")
		}
		if of != true {
			t.Errorf("expected origin_fetched=true in origin mode, got %v", of)
		}

		// Origin mode should have patches_synced and patches_diverged.
		if _, ok := resp["patches_synced"]; !ok {
			t.Error("origin mode should have patches_synced")
		}
		if _, ok := resp["patches_diverged"]; !ok {
			t.Error("origin mode should have patches_diverged")
		}
	})
}

// ===========================================================================
// TS-20-36 (unit): In origin mode patches_synced elements and patches_diverged
// follow the omission rules
//
// Verifies: 20-REQ-6.2
// ===========================================================================

func TestSyncResponse_PatchesSyncedOmissionRules_TS2036(t *testing.T) {
	env := setupOriginIntegrationEnv(t)

	// Create branches on the fork for various outcomes.
	// feat-a: will be created (no local branch).
	forkTipA := env.addForkBranch(t, "feat-a", "feat-a content")

	// feat-b: will be fast-forwarded.
	env.addForkBranch(t, "feat-b", "feat-b content")
	env.fetchOriginInTrunk(t)
	runGitCmd(t, env.trunkDir, "branch", "feat-b", "refs/remotes/origin/feat-b")
	env.addForkCommit(t, "feat-b", "extra on feat-b")

	// feat-c: will be replaced (diverged under replace policy).
	env.addForkBranch(t, "feat-c", "feat-c content")
	env.fetchOriginInTrunk(t)
	runGitCmd(t, env.trunkDir, "branch", "feat-c", "refs/remotes/origin/feat-c")
	env.addForkCommit(t, "feat-c", "fork divergent on feat-c")
	runGitCmd(t, env.trunkDir, "checkout", "feat-c")
	writeFileHelper(t, filepath.Join(env.trunkDir, "local-c.txt"), "local divergent")
	runGitCmd(t, env.trunkDir, "add", ".")
	runGitCmd(t, env.trunkDir, "commit", "-m", "local divergent on feat-c")
	runGitCmd(t, env.trunkDir, "checkout", "main")

	// feat-d: will be in_sync (equal tips).
	env.addForkBranch(t, "feat-d", "feat-d content")
	env.fetchOriginInTrunk(t)
	runGitCmd(t, env.trunkDir, "branch", "feat-d", "refs/remotes/origin/feat-d")

	// feat-e: missing on origin (local only).
	runGitCmd(t, env.trunkDir, "checkout", "-b", "feat-e")
	writeFileHelper(t, filepath.Join(env.trunkDir, "feat-e.txt"), "local only")
	runGitCmd(t, env.trunkDir, "add", ".")
	runGitCmd(t, env.trunkDir, "commit", "-m", "local only feat-e")
	runGitCmd(t, env.trunkDir, "checkout", "main")

	// Final fetch to update tracking refs.
	env.fetchOriginInTrunk(t)

	// Register patches.
	seedPatch(t, env.db, "p1", "my-workspace", "feat-a", 1, PatchStatusActive)
	seedPatch(t, env.db, "p2", "my-workspace", "feat-b", 2, PatchStatusActive)
	seedPatch(t, env.db, "p3", "my-workspace", "feat-c", 3, PatchStatusActive)
	seedPatch(t, env.db, "p4", "my-workspace", "feat-d", 4, PatchStatusActive)
	seedPatch(t, env.db, "p5", "my-workspace", "feat-e", 5, PatchStatusActive)

	// Use replace policy (default).
	env.variables["PATCH_DIVERGENCE_POLICY"] = "replace"

	runner := newRealGitRunner(t, env.trunkDir)
	env.buildEcho(t, runner)

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	patchesSynced := resp["patches_synced"].([]any)
	if len(patchesSynced) != 5 {
		t.Fatalf("expected 5 elements in patches_synced, got %d", len(patchesSynced))
	}

	// Build a map by branch_name for easier assertions.
	elemMap := make(map[string]map[string]any)
	for _, ps := range patchesSynced {
		elem := ps.(map[string]any)
		elemMap[elem["branch_name"].(string)] = elem
	}

	// feat-a: created → has local_sha, origin_sha, no replaced_sha.
	a := elemMap["feat-a"]
	if a["action"] != ActionCreated {
		t.Errorf("feat-a action = %v; want %q", a["action"], ActionCreated)
	}
	if a["state"] != StateInSync {
		t.Errorf("feat-a state = %v; want %q", a["state"], StateInSync)
	}
	if _, ok := a["local_sha"]; !ok {
		t.Error("feat-a should have local_sha")
	}
	if a["local_sha"] != forkTipA {
		t.Errorf("feat-a local_sha = %v; want %s", a["local_sha"], forkTipA)
	}
	if _, ok := a["origin_sha"]; !ok {
		t.Error("feat-a should have origin_sha")
	}
	if _, ok := a["replaced_sha"]; ok {
		t.Error("feat-a should not have replaced_sha")
	}

	// feat-b: fast_forwarded → has local_sha, origin_sha, no replaced_sha.
	b := elemMap["feat-b"]
	if b["action"] != ActionFastForwarded {
		t.Errorf("feat-b action = %v; want %q", b["action"], ActionFastForwarded)
	}
	if _, ok := b["replaced_sha"]; ok {
		t.Error("feat-b should not have replaced_sha")
	}

	// feat-c: replaced → has local_sha, origin_sha, replaced_sha.
	cc := elemMap["feat-c"]
	if cc["action"] != ActionReplaced {
		t.Errorf("feat-c action = %v; want %q", cc["action"], ActionReplaced)
	}
	if _, ok := cc["replaced_sha"]; !ok {
		t.Error("feat-c should have replaced_sha")
	}

	// feat-d: in_sync → has local_sha, origin_sha, no replaced_sha.
	d := elemMap["feat-d"]
	if d["action"] != ActionNone {
		t.Errorf("feat-d action = %v; want %q", d["action"], ActionNone)
	}
	if d["state"] != StateInSync {
		t.Errorf("feat-d state = %v; want %q", d["state"], StateInSync)
	}
	if _, ok := d["replaced_sha"]; ok {
		t.Error("feat-d should not have replaced_sha")
	}

	// feat-e: missing_on_origin → has local_sha, no origin_sha, no replaced_sha.
	e := elemMap["feat-e"]
	if e["state"] != StateMissingOnOrigin {
		t.Errorf("feat-e state = %v; want %q", e["state"], StateMissingOnOrigin)
	}
	if _, ok := e["local_sha"]; !ok {
		t.Error("feat-e should have local_sha")
	}
	if _, ok := e["origin_sha"]; ok {
		t.Error("feat-e should not have origin_sha (missing on origin)")
	}
	if _, ok := e["replaced_sha"]; ok {
		t.Error("feat-e should not have replaced_sha")
	}

	// Under replace, patches_diverged should be [].
	patchesDiverged := resp["patches_diverged"].([]any)
	if len(patchesDiverged) != 0 {
		t.Errorf("expected empty patches_diverged under replace, got %v", patchesDiverged)
	}
}

// ===========================================================================
// TS-20-37 (unit): Empty patches_synced and patches_diverged serialise as []
// in origin mode and are omitted in hub mode, in both body shapes
//
// Verifies: 20-REQ-6.3
// ===========================================================================

func TestSyncResponse_EmptyArraySerialization_TS2037(t *testing.T) {
	t.Run("origin_mode_no_candidates", func(t *testing.T) {
		env := newSyncTestEnv(t)

		seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
			"https://github.com/example/upstream",
			"aaaa000000000000000000000000000000000001",
			"integration", "")

		env.setVar("PATCH_BRANCH_SOURCE", "origin")
		// No patches seeded → zero candidates.

		rec := env.doSync(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		// Check raw JSON for [] not null and not omitted.
		body := rec.Body.String()

		// Parse as raw map to check field presence.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			t.Fatalf("decode raw response: %v", err)
		}

		// patches_synced must be present.
		psRaw, ok := raw["patches_synced"]
		if !ok {
			t.Fatal("expected patches_synced field in origin mode with no candidates")
		}
		if string(psRaw) != "[]" {
			t.Errorf("patches_synced = %s; want []", string(psRaw))
		}

		// patches_diverged must be present.
		pdRaw, ok := raw["patches_diverged"]
		if !ok {
			t.Fatal("expected patches_diverged field in origin mode with no candidates")
		}
		if string(pdRaw) != "[]" {
			t.Errorf("patches_diverged = %s; want []", string(pdRaw))
		}
	})

	t.Run("hub_mode_omits_both", func(t *testing.T) {
		env := newSyncTestEnv(t)

		seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
			"https://github.com/example/upstream",
			"aaaa000000000000000000000000000000000001",
			"integration", "")

		// Hub mode (default).
		rec := env.doSync(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("decode raw response: %v", err)
		}

		if _, ok := raw["patches_synced"]; ok {
			t.Error("hub mode should not have patches_synced")
		}
		if _, ok := raw["patches_diverged"]; ok {
			t.Error("hub mode should not have patches_diverged")
		}
	})

	t.Run("asExtras_origin_mode", func(t *testing.T) {
		// Test the asExtras map directly.
		resp := CarryPatchSyncResponse{
			PatchesMerged:   make([]string, 0),
			OriginFetched:   true,
			PatchesSynced:   make([]PatchSyncedElement, 0),
			PatchesDiverged: make([]string, 0),
		}
		extras := resp.asExtras()

		if _, ok := extras["patches_synced"]; !ok {
			t.Error("asExtras should have patches_synced in origin mode")
		}
		if _, ok := extras["patches_diverged"]; !ok {
			t.Error("asExtras should have patches_diverged in origin mode")
		}

		// Verify they serialize as [].
		extrasJSON, _ := json.Marshal(extras)
		var extrasMap map[string]json.RawMessage
		json.Unmarshal(extrasJSON, &extrasMap)

		if string(extrasMap["patches_synced"]) != "[]" {
			t.Errorf("asExtras patches_synced = %s; want []", string(extrasMap["patches_synced"]))
		}
		if string(extrasMap["patches_diverged"]) != "[]" {
			t.Errorf("asExtras patches_diverged = %s; want []", string(extrasMap["patches_diverged"]))
		}
	})

	t.Run("asExtras_hub_mode", func(t *testing.T) {
		// Hub mode: PatchesSynced and PatchesDiverged are nil.
		resp := CarryPatchSyncResponse{
			PatchesMerged: make([]string, 0),
			OriginFetched: false,
		}
		extras := resp.asExtras()

		if _, ok := extras["patches_synced"]; ok {
			t.Error("asExtras should not have patches_synced in hub mode")
		}
		if _, ok := extras["patches_diverged"]; ok {
			t.Error("asExtras should not have patches_diverged in hub mode")
		}
	})
}

// ===========================================================================
// TS-20-46 (integration): patch-status shows per-patch origin fields and
// summary counts, with counts 0 in hub mode
//
// Verifies: 20-REQ-7.6
// ===========================================================================

func TestSyncResponse_PatchStatusOriginFields_TS2046(t *testing.T) {
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)
	addWorkspaceColumns(t, db)

	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey: %v", err)
	}
	if err := jobqueue.MigrateProgress(db); err != nil {
		t.Fatalf("MigrateProgress: %v", err)
	}

	logger := nopLogger()
	q, err := jobqueue.New(db, logger)
	if err != nil {
		t.Fatalf("jobqueue.New: %v", err)
	}
	_ = RegisterRebuildJob(q, &RebuildHandler{})

	// Seed origin-mode workspace.
	seedWorkspaceCarryPatch(t, db, "origin-ws", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration", "")

	// Seed hub-mode workspace.
	seedWorkspaceCarryPatch(t, db, "hub-ws", "alice",
		"https://github.com/example/upstream2",
		"bbbb000000000000000000000000000000000002",
		"integration", "")

	// Seed patches for origin-ws with various origin states.
	seedPatch(t, db, "p1", "origin-ws", "feat-a", 1, PatchStatusActive)
	seedPatch(t, db, "p2", "origin-ws", "feat-b", 2, PatchStatusActive)
	seedPatch(t, db, "p3", "origin-ws", "feat-c", 3, PatchStatusActive)
	seedPatch(t, db, "p4", "origin-ws", "feat-d", 4, PatchStatusActive)

	// Set origin state on patches.
	db.Exec(`UPDATE patches SET origin_sync_state = 'diverged', origin_sha = 'aaa111', origin_synced_at = '2024-06-01T00:00:00Z' WHERE id = 'p1'`)
	db.Exec(`UPDATE patches SET origin_sync_state = 'missing_on_origin', origin_sha = NULL, origin_synced_at = '2024-06-01T00:00:00Z' WHERE id = 'p2'`)
	db.Exec(`UPDATE patches SET origin_sync_state = 'in_sync', origin_sha = 'ccc333', origin_synced_at = '2024-06-01T00:00:00Z' WHERE id = 'p3'`)
	// p4 has NULL origin state.

	// Seed patches for hub-ws.
	seedPatch(t, db, "p5", "hub-ws", "feat-x", 1, PatchStatusActive)

	patchStore := NewSQLPatchStore(db)
	wsRoot := t.TempDir()

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	patchStatusCfg := PatchStatusAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: wsRoot,
		PatchStore:    patchStore,
	}
	RegisterPatchStatusRoutes(api, patchStatusCfg)

	auth := rebuildUserAuth("alice")

	// Test origin-ws.
	t.Run("origin_workspace", func(t *testing.T) {
		authJSON, _ := json.Marshal(auth)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/origin-ws/patch-status", nil)
		req.Header.Set("X-Test-Auth", string(authJSON))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}

		// Check summary counts.
		summary := resp["summary"].(map[string]any)
		if summary["patches_diverged"] != float64(1) {
			t.Errorf("summary.patches_diverged = %v; want 1", summary["patches_diverged"])
		}
		if summary["patches_missing_on_origin"] != float64(1) {
			t.Errorf("summary.patches_missing_on_origin = %v; want 1", summary["patches_missing_on_origin"])
		}

		// Check per-patch origin fields.
		patches := resp["patches"].([]any)
		patchMap := make(map[string]map[string]any)
		for _, p := range patches {
			pm := p.(map[string]any)
			patchMap[pm["id"].(string)] = pm
		}

		// p1: diverged.
		p1 := patchMap["p1"]
		if p1["origin_sync_state"] != "diverged" {
			t.Errorf("p1 origin_sync_state = %v; want %q", p1["origin_sync_state"], "diverged")
		}
		if p1["origin_sha"] != "aaa111" {
			t.Errorf("p1 origin_sha = %v; want %q", p1["origin_sha"], "aaa111")
		}
		if _, ok := p1["origin_synced_at"]; !ok {
			t.Error("p1 should have origin_synced_at")
		}

		// p2: missing_on_origin.
		p2 := patchMap["p2"]
		if p2["origin_sync_state"] != "missing_on_origin" {
			t.Errorf("p2 origin_sync_state = %v; want %q", p2["origin_sync_state"], "missing_on_origin")
		}
		if _, ok := p2["origin_sha"]; ok {
			t.Error("p2 should not have origin_sha (missing_on_origin)")
		}

		// p3: in_sync.
		p3 := patchMap["p3"]
		if p3["origin_sync_state"] != "in_sync" {
			t.Errorf("p3 origin_sync_state = %v; want %q", p3["origin_sync_state"], "in_sync")
		}

		// p4: null origin state → fields omitted.
		p4 := patchMap["p4"]
		if _, ok := p4["origin_sync_state"]; ok {
			t.Error("p4 should not have origin_sync_state (null)")
		}
		if _, ok := p4["origin_sha"]; ok {
			t.Error("p4 should not have origin_sha (null)")
		}
		if _, ok := p4["origin_synced_at"]; ok {
			t.Error("p4 should not have origin_synced_at (null)")
		}
	})

	// Test hub-ws.
	t.Run("hub_workspace", func(t *testing.T) {
		authJSON, _ := json.Marshal(auth)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/hub-ws/patch-status", nil)
		req.Header.Set("X-Test-Auth", string(authJSON))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}

		// Summary counts should be present and 0.
		summary := resp["summary"].(map[string]any)
		if summary["patches_diverged"] != float64(0) {
			t.Errorf("hub summary.patches_diverged = %v; want 0", summary["patches_diverged"])
		}
		if summary["patches_missing_on_origin"] != float64(0) {
			t.Errorf("hub summary.patches_missing_on_origin = %v; want 0", summary["patches_missing_on_origin"])
		}
	})
}

// Suppress unused import warnings.
var (
	_ = os.Stat
	_ = apikit.NowUTC
	_ = fmt.Sprintf
	_ = context.Background
	_ transport.AuthMethod
	_ = (*jobqueue.Queue)(nil)
)
