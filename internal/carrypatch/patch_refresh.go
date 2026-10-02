package carrypatch

import (
	"context"
	"fmt"
	"strings"
)

// Action constants for patch refresh outcomes.
const (
	ActionNone          = "none"
	ActionCreated       = "created"
	ActionFastForwarded = "fast_forwarded"
	ActionReplaced      = "replaced"
)

// State constants for patch refresh outcomes.
const (
	StateInSync          = "in_sync"
	StateDiverged        = "diverged"
	StateMissingOnOrigin = "missing_on_origin"
)

// zeroSHA is the all-zero SHA used as expected-old for ref creates.
const zeroSHA = "0000000000000000000000000000000000000000"

// PatchRefreshOutcome records what happened to a single patch branch during
// the refresh against the fork's tip.
type PatchRefreshOutcome struct {
	PatchID     string
	BranchName  string
	Position    int
	Status      string // the patch's status (active, conflict, disabled)
	Action      string // one of ActionNone, ActionCreated, ActionFastForwarded, ActionReplaced
	State       string // one of StateInSync, StateDiverged, StateMissingOnOrigin
	LocalSHA    string // local tip after refresh (empty when no local branch)
	OriginSHA   string // fork tip (empty when fork has no such branch)
	ReplacedSHA string // the old local tip that was replaced (only for ActionReplaced)
}

// RefWriteError is returned when a ref write fails during the patch refresh.
// It carries the branch name so the caller can report it.
type RefWriteError struct {
	Branch string
	Err    error
}

func (e *RefWriteError) Error() string {
	return fmt.Sprintf("failed to update patch branch %s: %v", e.Branch, e.Err)
}

func (e *RefWriteError) Unwrap() error {
	return e.Err
}

// refreshPatchBranches evaluates each candidate patch against the fork's tip
// and brings local branches to the fork's tip using compare-and-swap ref
// writes. It returns per-branch outcomes, whether any patch counted as
// "advanced" (moved with status active or conflict), and an error if a ref
// write failed (the outcomes produced so far are still returned).
//
// Candidates are patches with status active, conflict or disabled, excluding
// any patch whose name equals the integration branch. They are processed in
// position order (the caller provides them sorted).
//
// The policy parameter is "replace" or "report". Under "replace", diverged
// branches are replaced with the fork tip (with a backup ref). Under "report",
// diverged branches are left unchanged.
func refreshPatchBranches(
	ctx context.Context,
	runner GitRunner,
	trunkDir string,
	patches []Patch,
	integrationBranch string,
	policy string, // "replace" or "report"
) ([]PatchRefreshOutcome, bool, error) {
	// Select candidates: active, conflict or disabled, excluding integration branch.
	var candidates []Patch
	for _, p := range patches {
		if p.Status != PatchStatusActive && p.Status != PatchStatusConflict && p.Status != PatchStatusDisabled {
			continue
		}
		if p.BranchName == integrationBranch {
			continue
		}
		candidates = append(candidates, p)
	}

	outcomes := make([]PatchRefreshOutcome, 0, len(candidates))
	patchAdvanced := false

	for _, patch := range candidates {
		outcome, moved, err := refreshOnePatch(ctx, runner, trunkDir, patch, policy)
		if err != nil {
			return outcomes, patchAdvanced, &RefWriteError{Branch: patch.BranchName, Err: err}
		}
		outcomes = append(outcomes, outcome)

		// A moved branch counts as "patch advanced" only if status is active or conflict.
		if moved && (patch.Status == PatchStatusActive || patch.Status == PatchStatusConflict) {
			patchAdvanced = true
		}
	}

	return outcomes, patchAdvanced, nil
}

// refreshOnePatch processes a single candidate patch branch. It returns the
// outcome, whether the branch was moved, and an error if a ref write failed.
func refreshOnePatch(
	ctx context.Context,
	runner GitRunner,
	trunkDir string,
	patch Patch,
	policy string,
) (PatchRefreshOutcome, bool, error) {
	branchRef := "refs/heads/" + patch.BranchName
	originRef := "refs/remotes/origin/" + patch.BranchName

	outcome := PatchRefreshOutcome{
		PatchID:    patch.ID,
		BranchName: patch.BranchName,
		Position:   patch.Position,
		Status:     patch.Status,
	}

	// Resolve fork tip.
	forkTip, forkErr := resolveRefSHA(ctx, runner, originRef)
	// Resolve local tip.
	localTip, localErr := resolveRefSHA(ctx, runner, branchRef)

	// Rule 1: Fork has no such branch.
	if forkErr != nil {
		outcome.State = StateMissingOnOrigin
		outcome.Action = ActionNone
		if localErr == nil {
			outcome.LocalSHA = localTip
		}
		return outcome, false, nil
	}

	outcome.OriginSHA = forkTip

	// Rule 2: No local branch.
	if localErr != nil {
		// Create refs/heads/<branch> at the fork tip.
		if _, err := runner.Run(ctx, "update-ref", branchRef, forkTip, zeroSHA); err != nil {
			return outcome, false, err
		}
		outcome.Action = ActionCreated
		outcome.State = StateInSync
		outcome.LocalSHA = forkTip
		if err := maybeHardReset(ctx, runner, trunkDir, branchRef); err != nil {
			return outcome, false, err
		}
		return outcome, true, nil
	}

	outcome.LocalSHA = localTip

	// Rule 3: Same commit.
	if localTip == forkTip {
		outcome.Action = ActionNone
		outcome.State = StateInSync
		return outcome, false, nil
	}

	// Rule 4: Local tip is a strict ancestor of the fork tip (fast-forward).
	isAnc, ancErr := runner.IsAncestor(ctx, localTip, forkTip)
	if ancErr != nil {
		// Treat IsAncestor error as a ref-write-level failure.
		return outcome, false, ancErr
	}
	if isAnc {
		// Fast-forward.
		if _, err := runner.Run(ctx, "update-ref", branchRef, forkTip, localTip); err != nil {
			return outcome, false, err
		}
		outcome.Action = ActionFastForwarded
		outcome.State = StateInSync
		outcome.LocalSHA = forkTip
		if err := maybeHardReset(ctx, runner, trunkDir, branchRef); err != nil {
			return outcome, false, err
		}
		return outcome, true, nil
	}

	// Rule 5: Diverged (neither is an ancestor of the other, or fork tip is
	// a strict ancestor of the local tip).
	if policy == "report" {
		outcome.Action = ActionNone
		outcome.State = StateDiverged
		return outcome, false, nil
	}

	// Policy is "replace": save backup, then replace.
	backupRef := "refs/hub/replaced/" + patch.BranchName

	// Resolve current backup ref (may not exist).
	backupOld, backupErr := resolveRefSHA(ctx, runner, backupRef)
	expectedBackupOld := zeroSHA
	if backupErr == nil {
		expectedBackupOld = backupOld
	}

	// Write backup ref.
	if _, err := runner.Run(ctx, "update-ref", backupRef, localTip, expectedBackupOld); err != nil {
		return outcome, false, err
	}

	// Move the branch to the fork tip.
	if _, err := runner.Run(ctx, "update-ref", branchRef, forkTip, localTip); err != nil {
		return outcome, false, err
	}

	outcome.Action = ActionReplaced
	outcome.State = StateInSync
	outcome.ReplacedSHA = localTip
	outcome.LocalSHA = forkTip

	if err := maybeHardReset(ctx, runner, trunkDir, branchRef); err != nil {
		return outcome, false, err
	}

	return outcome, true, nil
}

// resolveRefSHA resolves a ref to its commit SHA. Returns ("", error) if the
// ref does not exist.
func resolveRefSHA(ctx context.Context, runner GitRunner, ref string) (string, error) {
	sha, err := runner.Run(ctx, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// maybeHardReset checks if the trunk's HEAD points at the given branch ref.
// If so, it hard-resets the working tree to match the new tip.
func maybeHardReset(ctx context.Context, runner GitRunner, trunkDir string, branchRef string) error {
	headRef, err := runner.Run(ctx, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		// Detached HEAD or error: no reset needed.
		return nil
	}
	headRef = strings.TrimSpace(headRef)
	if headRef == branchRef {
		return runner.HardReset(ctx, "HEAD")
	}
	return nil
}
