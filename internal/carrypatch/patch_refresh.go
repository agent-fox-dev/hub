package carrypatch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
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

// RefStage names the step of the patch refresh that failed for a branch.
// The zero value is a failed ref write, the case 20-REQ-4.3 specifies.
type RefStage int

const (
	// RefStageWrite: an update-ref (including a lost compare-and-swap) failed.
	// The branch did not move.
	RefStageWrite RefStage = iota
	// RefStageCompare: the ancestry check between the local and fork tips
	// failed. No ref was written for the branch.
	RefStageCompare
	// RefStageReset: the ref was written, but resetting the trunk work tree
	// to the new tip failed. The branch has moved.
	RefStageReset
)

// RefWriteError is returned when the patch refresh fails for a branch. It
// carries the branch name so the caller can report it, and the Stage that
// failed so the caller does not claim a ref write failed when it did not (a
// failed hard reset happens after the ref moved).
type RefWriteError struct {
	Branch string
	Err    error
	Stage  RefStage
}

func (e *RefWriteError) Error() string {
	switch e.Stage {
	case RefStageCompare:
		return fmt.Sprintf("failed to compare patch branch %s with origin: %v", e.Branch, e.Err)
	case RefStageReset:
		return fmt.Sprintf("failed to reset working tree for patch branch %s: %v", e.Branch, e.Err)
	default:
		return fmt.Sprintf("failed to update patch branch %s: %v", e.Branch, e.Err)
	}
}

func (e *RefWriteError) Unwrap() error {
	return e.Err
}

// refreshPatchBranches evaluates each candidate patch against the fork's tip
// and brings local branches to the fork's tip using compare-and-swap ref
// writes. It returns per-branch outcomes, whether any patch counted as
// "advanced" (moved with status active or conflict), and a *RefWriteError if
// the refresh failed for a branch (the outcomes produced so far are still
// returned).
//
// A branch whose ref moved but whose work-tree reset then failed (RefStageReset)
// keeps its outcome and counts as advanced, because the ref did move and the
// rebuild decision must see it. A branch whose ref did not move produces no
// outcome when it fails.
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
		outcome, moved, refErr := refreshOnePatch(ctx, runner, trunkDir, patch, policy)

		// A branch whose ref moved is recorded even when a later step for it
		// failed: the move is real, so it must be persisted, audited and seen
		// by the rebuild decision. A branch that did not move and failed
		// produces no outcome.
		if refErr == nil || moved {
			outcomes = append(outcomes, outcome)

			// A moved branch counts as "patch advanced" only if status is active or conflict.
			if moved && (patch.Status == PatchStatusActive || patch.Status == PatchStatusConflict) {
				patchAdvanced = true
			}
		}
		if refErr != nil {
			return outcomes, patchAdvanced, refErr
		}
	}

	return outcomes, patchAdvanced, nil
}

// refreshOnePatch processes a single candidate patch branch. It returns the
// outcome, whether the branch was moved, and a *RefWriteError if a step
// failed. moved is true together with an error when the ref was written but
// the work-tree reset that follows failed (RefStageReset).
func refreshOnePatch(
	ctx context.Context,
	runner GitRunner,
	trunkDir string,
	patch Patch,
	policy string,
) (PatchRefreshOutcome, bool, *RefWriteError) {
	fail := func(stage RefStage, err error) *RefWriteError {
		return &RefWriteError{Branch: patch.BranchName, Err: err, Stage: stage}
	}

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

	// move brings the branch to the fork tip with the shared compare-and-swap
	// move and records the outcome. A failed ref write leaves the outcome
	// untouched and reports the branch as not moved; a failed work-tree reset
	// keeps the outcome and reports the branch as moved (RefStageReset).
	move := func(action, expectedOld string) (PatchRefreshOutcome, bool, *RefWriteError) {
		moved, stage, err := casMoveRef(ctx, runner, trunkDir, branchRef, forkTip, expectedOld)
		if !moved {
			return outcome, false, fail(stage, err)
		}
		outcome.Action = action
		outcome.State = StateInSync
		outcome.LocalSHA = forkTip
		if action == ActionReplaced {
			outcome.ReplacedSHA = expectedOld
		}
		if err != nil {
			return outcome, true, fail(stage, err)
		}
		return outcome, true, nil
	}

	// Rule 2: No local branch.
	if localErr != nil {
		// Create refs/heads/<branch> at the fork tip.
		return move(ActionCreated, zeroSHA)
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
		// An ancestry-check failure stops the refresh like a ref-write
		// failure, but nothing was written and it is reported as such.
		return outcome, false, fail(RefStageCompare, ancErr)
	}
	if isAnc {
		// Fast-forward.
		return move(ActionFastForwarded, localTip)
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
	if err := casUpdateRef(ctx, runner, backupRef, localTip, expectedBackupOld); err != nil {
		return outcome, false, fail(RefStageWrite, err)
	}

	// Move the branch to the fork tip.
	return move(ActionReplaced, localTip)
}

// resolveRefSHA resolves a ref to its commit SHA. Returns ("", error) if the
// ref does not exist, and also for every other failure: it cannot tell the
// two apart. Use lookupRefSHA where a lookup failure must not be read as a
// missing ref.
func resolveRefSHA(ctx context.Context, runner GitRunner, ref string) (string, error) {
	sha, err := runner.Run(ctx, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// lookupRefSHA resolves a ref to its commit SHA and tells a missing ref from
// a failed lookup. found is false with a nil error when the ref does not
// exist: git rev-parse --verify --quiet exits 1 for that case and only that
// case, so the exit code of the typed *gitcmd.GitError decides, never its
// text. Every other failure (a runner that cannot start git, a corrupt
// repository, a cancelled context) is returned.
func lookupRefSHA(ctx context.Context, runner GitRunner, ref string) (sha string, found bool, err error) {
	out, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		var gitErr *gitcmd.GitError
		if errors.As(err, &gitErr) && gitErr.ExitCode == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	sha = strings.TrimSpace(out)
	if sha == "" {
		return "", false, nil
	}
	return sha, true, nil
}

// casUpdateRef writes ref with a compare-and-swap: git update-ref <ref>
// <newSHA> <oldSHA> fails unless ref currently holds oldSHA. zeroSHA as oldSHA
// means create: it fails if ref already exists. This is the only place that
// runs the three-argument update-ref; the patch refresh (spec 20) and the
// reset-to-origin (spec 23) both write refs through it.
func casUpdateRef(ctx context.Context, runner GitRunner, ref, newSHA, oldSHA string) error {
	_, err := runner.Run(ctx, "update-ref", ref, newSHA, oldSHA)
	return err
}

// casMoveRef moves a branch ref from oldSHA to newSHA with casUpdateRef and
// then brings the trunk work tree along with maybeHardReset.
//
// The two outcomes of a failure are told apart so callers do not claim a ref
// write failed when it did not:
//   - the write failed: moved is false, stage is RefStageWrite, and the
//     branch is unchanged;
//   - the write succeeded but the work-tree reset failed: moved is true,
//     stage is RefStageReset, and the branch has moved.
//
// stage is meaningful only when err is non-nil.
func casMoveRef(ctx context.Context, runner GitRunner, trunkDir, ref, newSHA, oldSHA string) (moved bool, stage RefStage, err error) {
	if err := casUpdateRef(ctx, runner, ref, newSHA, oldSHA); err != nil {
		return false, RefStageWrite, err
	}
	if err := maybeHardReset(ctx, runner, trunkDir, ref); err != nil {
		return true, RefStageReset, err
	}
	return true, RefStageWrite, nil
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
