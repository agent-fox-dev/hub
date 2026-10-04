package carrypatch

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// PurgeExpiredDeletedPatchesWithRefs permanently removes soft-deleted patches
// that have been in status='deleted' for longer than 7 days, cleaning up
// their backup refs (refs/hub/replaced/<branch>) first.
//
// For each expired row the routine removes the backup ref in the workspace's
// trunk, then deletes the row by id only while status='deleted'. A row whose
// ref removal fails is kept for the next purge (with a warn log). A missing
// ref or a missing trunk counts as success. Returns the number of rows purged.
//
// If listing the expired rows fails, the error is returned immediately and
// no row or ref is touched.
func PurgeExpiredDeletedPatchesWithRefs(
	ctx context.Context,
	store PatchStore,
	workspaceRoot string,
	newGitRunner func(repoPath string) (GitRunner, error),
) (int64, error) {
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)

	rows, err := store.ListExpiredDeletedPatches(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("list expired deleted patches: %w", err)
	}

	var purged int64
	for _, row := range rows {
		// Attempt to remove the backup ref.
		if err := removeBackupRefForPurge(ctx, workspaceRoot, row.Slug, row.Branch, newGitRunner); err != nil {
			slog.Warn("purge: failed to remove backup ref, keeping row for next purge",
				"slug", row.Slug,
				"branch", row.Branch,
				"patch_id", row.ID,
				"error", err,
			)
			continue
		}

		// Delete the row only while status='deleted'.
		if err := store.DeletePatchByIDIfDeleted(ctx, row.ID); err != nil {
			slog.Warn("purge: failed to delete patch row",
				"patch_id", row.ID,
				"slug", row.Slug,
				"branch", row.Branch,
				"error", err,
			)
			continue
		}

		purged++
	}

	return purged, nil
}

// removeBackupRefForPurge removes refs/hub/replaced/<branch> from the
// workspace's trunk. A missing ref or a missing trunk counts as success.
// Any other failure is returned as an error.
func removeBackupRefForPurge(
	ctx context.Context,
	workspaceRoot, slug, branch string,
	newGitRunner func(repoPath string) (GitRunner, error),
) error {
	repoPath := filepath.Join(workspaceRoot, slug, "trunk")

	// Check if the trunk directory exists.
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		slog.Info("purge: trunk not on disk, treating as success",
			"slug", slug,
			"branch", branch,
		)
		return nil
	}

	runner, err := newGitRunner(repoPath)
	if err != nil {
		return fmt.Errorf("open runner for %s: %w", slug, err)
	}

	ref := "refs/hub/replaced/" + branch
	_, err = runner.Run(ctx, "update-ref", "-d", ref)
	if err != nil {
		// Check if the ref simply doesn't exist (which is fine).
		if _, resolveErr := resolveRefSHA(ctx, runner, ref); resolveErr != nil {
			// Ref doesn't exist — that's success.
			return nil
		}
		return fmt.Errorf("delete ref %s in %s: %w", ref, slug, err)
	}

	return nil
}
