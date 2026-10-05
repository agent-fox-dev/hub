package carrypatch

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/txsvc/apikit"
)

// PurgeExpiredDeletedPatchesWithRefs permanently removes soft-deleted patches
// that have been in status='deleted' for longer than 7 days, cleaning up
// their backup refs (refs/hub/replaced/<branch>) first.
//
// For each expired row the routine removes the backup ref in the workspace's
// trunk (removeBackupRef, the same removal patch deletion uses), then deletes
// the row by id only while status='deleted'. A row whose ref removal fails is
// kept for the next purge (with a warn log). A missing ref or a missing trunk
// counts as success. Returns the number of rows actually purged: a row that
// was restored between the listing and the delete is not counted (its backup
// ref is already removed by then; the restore itself is not blocked).
//
// If listing the expired rows fails, the error is returned immediately and
// no row or ref is touched.
func PurgeExpiredDeletedPatchesWithRefs(
	ctx context.Context,
	store PatchStore,
	workspaceRoot string,
	newGitRunner func(repoPath string) (GitRunner, error),
) (int64, error) {
	cutoff := apikit.FormatUTC(time.Now().Add(-7 * 24 * time.Hour))

	rows, err := store.ListExpiredDeletedPatches(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("list expired deleted patches: %w", err)
	}

	var purged int64
	for _, row := range rows {
		// Attempt to remove the backup ref.
		if err := removeBackupRef(ctx, workspaceRoot, row.Slug, row.Branch, newGitRunner); err != nil {
			slog.Warn("purge: failed to remove backup ref, keeping row for next purge",
				"slug", row.Slug,
				"branch", row.Branch,
				"patch_id", row.ID,
				"error", err,
			)
			continue
		}

		// Delete the row only while status='deleted'.
		deleted, err := store.DeletePatchByIDIfDeleted(ctx, row.ID)
		if err != nil {
			slog.Warn("purge: failed to delete patch row",
				"patch_id", row.ID,
				"slug", row.Slug,
				"branch", row.Branch,
				"error", err,
			)
			continue
		}
		if !deleted {
			// The row is gone or is no longer soft-deleted, for example because
			// it was restored after it was listed. It was not purged.
			slog.Info("purge: patch row is no longer soft-deleted, left in place",
				"patch_id", row.ID,
				"slug", row.Slug,
				"branch", row.Branch,
			)
			continue
		}

		purged++
	}

	return purged, nil
}
