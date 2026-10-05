package carrypatch

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression tests for issue #46: statements in the spec 24 documentation
// that contradicted the code. Each test pins one corrected statement to the
// code behaviour it describes.

// norm collapses whitespace so assertions survive Markdown hard-wrapping.
func norm(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// requireAbsent fails for every needle found in the whitespace-normalised doc.
func requireAbsent(t *testing.T, what, doc string, subs ...string) {
	t.Helper()
	doc = norm(doc)
	for _, s := range subs {
		if strings.Contains(doc, s) {
			t.Errorf("%s still contains %q", what, s)
		}
	}
}

// jsonBlockAfter returns the first ```json fenced block after marker.
func jsonBlockAfter(t *testing.T, doc, marker string) string {
	t.Helper()
	i := strings.Index(doc, marker)
	if i < 0 {
		t.Fatalf("marker %q not found", marker)
	}
	rest := doc[i:]
	const open = "```json\n"
	s := strings.Index(rest, open)
	if s < 0 {
		t.Fatalf("no json block after %q", marker)
	}
	rest = rest[s+len(open):]
	e := strings.Index(rest, "```")
	if e < 0 {
		t.Fatalf("unterminated json block after %q", marker)
	}
	return rest[:e]
}

// Finding 1: the sample sync response must give patches_synced the shape the
// code produces (api.go, PatchSyncedElement objects).
func TestDocs_Issue46_SampleSyncResponsePatchesSyncedShape(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	sample := jsonBlockAfter(t, guide, "the sync response is the usual workspace JSON")

	var resp struct {
		PatchesSynced []PatchSyncedElement `json:"patches_synced"`
	}
	if err := json.Unmarshal([]byte(sample), &resp); err != nil {
		t.Fatalf("sample sync response does not decode into []PatchSyncedElement: %v", err)
	}
	if len(resp.PatchesSynced) == 0 {
		t.Fatal("sample sync response has no patches_synced elements")
	}
	for i, e := range resp.PatchesSynced {
		if e.BranchName == "" || e.Action == "" || e.State == "" {
			t.Errorf("patches_synced[%d] = %+v; want branch_name, action and state set", i, e)
		}
	}
}

// Finding 2: a backup ref exists only after a replacement or a reset, never
// for a diverged branch under the report policy.
func TestDocs_Issue46_RecoveryBackupRefOnlyAfterReplacement(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	sec := section(t, guide, "### Recovering a replaced or diverged patch branch", "### ")

	requireAbsent(t, "recovery section", sec,
		"`report`) or a replacement has occurred, the old tip is saved")
	requireContains(t, "recovery section", sec,
		"under the `report` policy the branch is left unchanged and no backup is written",
	)
}

// Finding 3: diverged is recorded only when the hub did not replace the
// branch; a replacement records in_sync with action replaced.
func TestDocs_Issue46_AgentExampleDivergedMeaning(t *testing.T) {
	agent := readDoc(t, "examples/AGENTS_carry_patch.md")

	requireAbsent(t, "agent example", agent,
		"A `diverged` patch means the hub replaced the branch tip")
	requireContains(t, "agent example", agent,
		"A `diverged` patch means the hub left its copy unchanged",
	)
}

// Finding 4: the reset enqueues a rebuild only for active and conflict
// patches (recovery.go, rebuildEligible); eligibility to be reset still
// includes disabled.
func TestDocs_Issue46_Erratum23RebuildRule(t *testing.T) {
	e23 := readDoc(t, "errata/23_patch_divergence_recovery_divergences.md")

	requireAbsent(t, "erratum 23", e23,
		"the patch status is `active`, `conflict` or `disabled`, and is limited to")
	requireContains(t, "erratum 23", e23,
		"enqueues a rebuild when the branch moved and the patch status is `active` or `conflict`",
		"is limited to patches with status `active`, `conflict` or `disabled`",
	)
}

// Finding 5: the Accepted date is the date of the change (commit c06b5cb).
func TestDocs_Issue46_ADR01AcceptedDate(t *testing.T) {
	adr := readDoc(t, "adr/01-choose-the-authority-for-patch-branches.md")

	requireContains(t, "ADR 01", adr, "**Accepted:** 2026-10-04")
	requireAbsent(t, "ADR 01", adr, "2026-10-15")
}

// Finding 7: the code resolves the base and detects a force-push before it
// refreshes the patch branches (sync_handlers.go).
func TestDocs_Issue46_SyncPhaseOrderMatchesCode(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	syncSec := section(t, guide, "### Sync algorithm", "### ")

	keys := []string{
		"Fetch origin (origin mode only)",
		"Resolve the base",
		"Detect force-push",
		"Refresh patch branches",
		"Detect merged patches",
	}
	prev := -1
	for _, k := range keys {
		idx := strings.Index(syncSec, k)
		if idx < 0 {
			t.Errorf("Sync algorithm section missing phase %q", k)
			continue
		}
		if idx <= prev {
			t.Errorf("phase %q is out of order", k)
		}
		prev = idx
	}
	requireContains(t, "Sync algorithm", syncSec, "(phase 6)")
}

// Finding 8: an expired or revoked token surfaces as a failed fetch, not as a
// credential resolution failure (which only a secret-store error causes).
func TestDocs_Issue46_RevokedCredentialsSurfaceAsFetchFailure(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	requireAbsent(t, "guide", guide,
		"If they expire or are revoked, sync fails with `502 failed to resolve origin credentials`")
	requireContains(t, "guide", guide,
		"If they expire or are revoked, the origin fetch fails and sync answers `502 origin fetch failed`",
	)
}

// Finding 9: batch registration is a JSON array body to POST .../patches.
func TestDocs_Issue46_Erratum21BatchRoute(t *testing.T) {
	e21 := readDoc(t, "errata/21_fork_patch_registration_divergences.md")

	requireAbsent(t, "erratum 21", e21, "`POST .../patches/batch`")
	requireContains(t, "erratum 21", e21,
		"Batch registration (a JSON array body to `POST .../patches`)",
	)
}

// Finding 10: --reset-to-upstream is ignored for carry-patch workspaces, so
// the carry-patch agent example must not recommend it.
func TestDocs_Issue46_AgentExampleNoResetToUpstream(t *testing.T) {
	agent := readDoc(t, "examples/AGENTS_carry_patch.md")

	requireAbsent(t, "agent example", agent,
		"afc workspace sync <workspace-slug> --reset-to-upstream",
		"| Reset to upstream (recovery) |")
	requireContains(t, "agent example", agent,
		"force_push_detected",
		"ignored for carry-patch workspaces",
	)
}

// Finding 11: the hub exposes only rerere list and forget; a manual
// recording step has no code behind it.
func TestDocs_Issue46_NoManualRerereRecordingClaim(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	requireAbsent(t, "guide", guide,
		"running `git rerere` there",
		"record a resolution manually")
}

// Finding 12: the agent example names the remote each command talks to.
func TestDocs_Issue46_AgentExampleModeSpecificSteps(t *testing.T) {
	agent := readDoc(t, "examples/AGENTS_carry_patch.md")

	requireContains(t, "agent example", agent,
		"In a clone of the hub, `origin` is the hub; in a clone of the fork, `origin` is the fork.",
		"git remote add upstream <upstream-repo-url>",
		"`REBUILD_PUSH_INTEGRATION_BRANCH=true`",
	)
}

// Minor wording: duplicated clause and the purge routine's callers.
func TestDocs_Issue46_GuideMinorWording(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	requireAbsent(t, "guide", guide,
		"Sync never fetches the integration branch from `origin`; it is never fetched",
		"an operator or external job must call the purge routine")
	requireContains(t, "guide", guide,
		"no CLI command or API route runs the purge; only Go code can call `PurgeExpiredDeletedPatchesWithRefs`",
	)
}

// Spec 24 erratum: the documented phase order follows the code, not the spec.
func TestDocs_Issue46_Erratum24PhaseOrder(t *testing.T) {
	e24 := readDoc(t, "errata/24_patch_authority_docs_divergences.md")

	requireContains(t, "erratum 24", e24,
		"base resolution",
		"force-push detection",
		"patch refresh",
		"sync_handlers.go",
	)
}
