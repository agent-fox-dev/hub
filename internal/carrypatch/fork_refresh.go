package carrypatch

import (
	"context"
	"sort"
	"strings"
)

// PatchSyncOutcome records the outcome of refreshing a single patch branch
// from the origin remote.
type PatchSyncOutcome struct {
	BranchName  string `json:"branch_name"`
	Action      string `json:"action"` // none, created, fast_forwarded, replaced
	State       string `json:"state"`  // in_sync, diverged, missing_on_origin
	LocalSHA    string `json:"local_sha,omitempty"`
	OriginSHA   string `json:"origin_sha,omitempty"`
	ReplacedSHA string `json:"replaced_sha,omitempty"`
}

type patchSyncOutcome = PatchSyncOutcome
type PatchSyncEntry = PatchSyncOutcome

// refreshPatchBranchesFromOrigin brings each registered patch branch to the fork's tip.
// It considers, in position order, every patch of the workspace whose status is
// active, conflict, or disabled (excluding merged_upstream, deleted, and any patch
// whose branch_name equals the integration branch).
func refreshPatchBranchesFromOrigin(
	ctx context.Context,
	git GitRunner,
	patches []Patch,
	integrationBranch string,
	divergencePolicy string,
) ([]patchSyncOutcome, bool) {
	// Sort by position order
	sorted := make([]Patch, len(patches))
	copy(sorted, patches)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Position < sorted[j].Position
	})

	outcomes := make([]patchSyncOutcome, 0)
	patchAdvanced := false

	if divergencePolicy != "report" {
		divergencePolicy = "replace"
	}

	for _, p := range sorted {
		if p.BranchName == integrationBranch {
			continue
		}
		if p.Status != PatchStatusActive && p.Status != PatchStatusConflict && p.Status != PatchStatusDisabled {
			continue
		}

		originRef := "refs/remotes/origin/" + p.BranchName
		originSHA, originErr := git.Run(ctx, "rev-parse", "--verify", originRef)
		originSHA = strings.TrimSpace(originSHA)

		localRef := "refs/heads/" + p.BranchName
		localSHA, localErr := git.Run(ctx, "rev-parse", "--verify", localRef)
		localSHA = strings.TrimSpace(localSHA)

		// 1. Missing on origin:
		if originErr != nil || originSHA == "" {
			var lSHA string
			if localErr == nil {
				lSHA = localSHA
			}
			outcomes = append(outcomes, patchSyncOutcome{
				BranchName: p.BranchName,
				Action:     "none",
				State:      "missing_on_origin",
				LocalSHA:   lSHA,
				OriginSHA:  "",
			})
			continue
		}

		// 2. Missing locally: create at origin tip via update-ref
		if localErr != nil || localSHA == "" {
			_, _ = git.Run(ctx, "update-ref", localRef, originSHA)
			patchAdvanced = true
			if strings.TrimSpace(currentCheckout(ctx, git)) == p.BranchName {
				_ = git.HardReset(ctx, originSHA)
			}
			outcomes = append(outcomes, patchSyncOutcome{
				BranchName: p.BranchName,
				Action:     "created",
				State:      "in_sync",
				LocalSHA:   "",
				OriginSHA:  originSHA,
			})
			continue
		}

		// 3. Both exist and point to the same commit
		if localSHA == originSHA {
			outcomes = append(outcomes, patchSyncOutcome{
				BranchName: p.BranchName,
				Action:     "none",
				State:      "in_sync",
				LocalSHA:   localSHA,
				OriginSHA:  originSHA,
			})
			continue
		}

		// 4. Local tip is ancestor of origin tip (fast-forward)
		isAncestor, ancErr := git.IsAncestor(ctx, localSHA, originSHA)
		if ancErr == nil && isAncestor {
			_, _ = git.Run(ctx, "update-ref", localRef, originSHA)
			patchAdvanced = true
			if strings.TrimSpace(currentCheckout(ctx, git)) == p.BranchName {
				_ = git.HardReset(ctx, originSHA)
			}
			outcomes = append(outcomes, patchSyncOutcome{
				BranchName: p.BranchName,
				Action:     "fast_forwarded",
				State:      "in_sync",
				LocalSHA:   localSHA,
				OriginSHA:  originSHA,
			})
			continue
		}

		// 5. Diverged
		if divergencePolicy == "report" {
			outcomes = append(outcomes, patchSyncOutcome{
				BranchName: p.BranchName,
				Action:     "none",
				State:      "diverged",
				LocalSHA:   localSHA,
				OriginSHA:  originSHA,
			})
			continue
		}

		// divergencePolicy == "replace" (default)
		replacedRef := "refs/hub/replaced/" + p.BranchName
		_, _ = git.Run(ctx, "update-ref", replacedRef, localSHA)
		_, _ = git.Run(ctx, "update-ref", localRef, originSHA)
		patchAdvanced = true
		if strings.TrimSpace(currentCheckout(ctx, git)) == p.BranchName {
			_ = git.HardReset(ctx, originSHA)
		}
		outcomes = append(outcomes, patchSyncOutcome{
			BranchName:  p.BranchName,
			Action:      "replaced",
			State:       "in_sync",
			LocalSHA:    localSHA,
			OriginSHA:   originSHA,
			ReplacedSHA: localSHA,
		})
	}

	return outcomes, patchAdvanced
}
