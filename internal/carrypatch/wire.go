package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
	"github.com/agent-fox-dev/hub/internal/upstream"
)

// ===========================================================================
// GitRunnerAdapter: bridges gitcmd.GitRunner → carrypatch.GitRunner
// ===========================================================================

// GitRunnerAdapter wraps a *gitcmd.GitRunner to satisfy the carrypatch.GitRunner
// interface. The key difference from using gitcmd.GitRunner directly:
//
//   - CherryPick and MergeNoFF do NOT auto-abort on conflict. This allows the
//     rebuild executor to run git rerere before deciding whether to abort or
//     continue (16-REQ-3.1).
//   - Return signatures are adapted: CherryPick/MergeNoFF/HardReset return
//     only error (no string).
type GitRunnerAdapter struct {
	runner *gitcmd.GitRunner
}

// NewGitRunnerAdapter creates a GitRunnerAdapter from a gitcmd.GitRunner.
func NewGitRunnerAdapter(runner *gitcmd.GitRunner) *GitRunnerAdapter {
	return &GitRunnerAdapter{runner: runner}
}

// Run delegates to the underlying gitcmd.GitRunner.Run.
func (a *GitRunnerAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return a.runner.Run(ctx, args...)
}

// CherryPick applies a single commit via cherry-pick. Unlike gitcmd.CherryPick,
// it does NOT auto-abort on conflict — the repository is left in a conflicted
// state so that the rebuild executor can invoke rerere (16-REQ-1.5, 16-REQ-3.1).
func (a *GitRunnerAdapter) CherryPick(ctx context.Context, commitSHA string) error {
	_, err := a.runner.Run(ctx, "cherry-pick", "--end-of-options", commitSHA)
	if err != nil {
		// Check if CHERRY_PICK_HEAD exists — indicates a conflict in progress.
		_, revErr := a.runner.Run(ctx, "rev-parse", "--verify", "CHERRY_PICK_HEAD")
		if revErr == nil {
			// A cherry-pick whose changes are already upstream stops with
			// CHERRY_PICK_HEAD set but no unmerged paths and nothing staged.
			// That is not a conflict: skip the commit and carry on.
			unmerged, _ := a.runner.Run(ctx, "diff", "--name-only", "--diff-filter=U")
			if strings.TrimSpace(unmerged) == "" {
				if _, stagedErr := a.runner.Run(ctx, "diff", "--cached", "--quiet"); stagedErr == nil {
					if _, skipErr := a.runner.Run(ctx, "cherry-pick", "--skip"); skipErr == nil {
						return nil
					}
				}
			}
			return &CherryPickConflictError{}
		}
		return err
	}
	return nil
}

// MergeNoFF merges a branch with --no-ff. Unlike gitcmd.MergeNoFF, it does NOT
// auto-abort on conflict (same reason as CherryPick).
func (a *GitRunnerAdapter) MergeNoFF(ctx context.Context, branch string) error {
	_, err := a.runner.Run(ctx, "merge", "--no-ff", "--no-edit", "--end-of-options", branch)
	if err != nil {
		// Check if MERGE_HEAD exists — indicates a merge conflict in progress.
		_, revErr := a.runner.Run(ctx, "rev-parse", "--verify", "MERGE_HEAD")
		if revErr == nil {
			return &MergeNoFFConflictError{}
		}
		return err
	}
	return nil
}

// MergeTree delegates to the underlying gitcmd.GitRunner.MergeTree for
// read-only conflict detection (git merge-tree --write-tree).
func (a *GitRunnerAdapter) MergeTree(ctx context.Context, base, head string) (string, error) {
	return a.runner.MergeTree(ctx, base, head)
}

// IsAncestor delegates to the underlying gitcmd.GitRunner.IsAncestor.
func (a *GitRunnerAdapter) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	return a.runner.IsAncestor(ctx, ancestor, descendant)
}

// Cherry delegates to the underlying gitcmd.GitRunner.Cherry.
func (a *GitRunnerAdapter) Cherry(ctx context.Context, upstream, head string) ([]string, []string, error) {
	return a.runner.Cherry(ctx, upstream, head)
}

// HardReset runs 'git reset --hard <ref>'.
func (a *GitRunnerAdapter) HardReset(ctx context.Context, ref string) error {
	_, err := a.runner.Run(ctx, "reset", "--hard", ref)
	return err
}

// NewGitRunnerFactory returns a factory function suitable for
// RebuildHandler.NewGitRunner and the various API config types.
func NewGitRunnerFactory() func(repoPath string) (GitRunner, error) {
	return func(repoPath string) (GitRunner, error) {
		runner, err := gitcmd.New(repoPath, nil)
		if err != nil {
			return nil, err
		}
		return NewGitRunnerAdapter(runner), nil
	}
}

// ===========================================================================
// SQLPatchStore: production PatchStore backed by SQLite
// ===========================================================================

// SQLPatchStore implements PatchStore against the patches table in SQLite.
type SQLPatchStore struct {
	DB *sql.DB
}

// NewSQLPatchStore creates a new SQLPatchStore.
func NewSQLPatchStore(db *sql.DB) *SQLPatchStore {
	return &SQLPatchStore{DB: db}
}

// ListPatches returns all non-deleted patches for a workspace ordered by position.
func (s *SQLPatchStore) ListPatches(_ context.Context, workspaceSlug string) ([]Patch, error) {
	rows, err := s.DB.Query(
		`SELECT id, workspace_slug, branch_name, position, status, conflict_files, upstream_pr_url
		 FROM patches WHERE workspace_slug = ? AND (status != 'deleted' OR status IS NULL) ORDER BY position ASC`,
		workspaceSlug,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var patches []Patch
	for rows.Next() {
		var p Patch
		var conflictFilesJSON sql.NullString
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.BranchName, &p.Position, &p.Status, &conflictFilesJSON, &p.UpstreamPRURL); err != nil {
			return nil, err
		}
		if conflictFilesJSON.Valid && conflictFilesJSON.String != "" {
			_ = json.Unmarshal([]byte(conflictFilesJSON.String), &p.ConflictFiles)
		}
		patches = append(patches, p)
	}
	return patches, rows.Err()
}

// UpdatePatchStatus updates a patch's status and conflict_files.
func (s *SQLPatchStore) UpdatePatchStatus(_ context.Context, patchID, status string, conflictFiles []string) error {
	conflictJSON := "[]"
	if len(conflictFiles) > 0 {
		b, err := json.Marshal(conflictFiles)
		if err != nil {
			return err
		}
		conflictJSON = string(b)
	}
	now := apikit.NowUTC()
	_, err := s.DB.Exec(
		`UPDATE patches SET status = ?, conflict_files = ?, updated_at = ? WHERE id = ?`,
		status, conflictJSON, now, patchID,
	)
	return err
}

// DeletePatch removes a patch from the patches table.
func (s *SQLPatchStore) DeletePatch(_ context.Context, patchID string) error {
	_, err := s.DB.Exec(`DELETE FROM patches WHERE id = ?`, patchID)
	return err
}

// SoftDeletePatch transitions a patch to status='deleted' and sets deleted_at.
//
// The row keeps its identity but gives up its slot in the 1-based position
// sequence: it is parked at a unique negative position (-rowid) so that
// compaction of the remaining patches can never collide with it on the
// UNIQUE(workspace_slug, position) constraint.
func (s *SQLPatchStore) SoftDeletePatch(_ context.Context, patchID string) error {
	now := apikit.NowUTC()
	_, err := s.DB.Exec(
		`UPDATE patches SET status = 'deleted', deleted_at = ?, updated_at = ?, position = -rowid WHERE id = ?`,
		now, now, patchID,
	)
	return err
}

// RestorePatch transitions a soft-deleted patch back to status='active' and clears deleted_at.
func (s *SQLPatchStore) RestorePatch(_ context.Context, patchID string) error {
	now := apikit.NowUTC()
	_, err := s.DB.Exec(
		`UPDATE patches SET status = 'active', deleted_at = NULL, updated_at = ?,
		        position = (SELECT COALESCE(MAX(position), 0) + 1 FROM patches p2
		                    WHERE p2.workspace_slug = patches.workspace_slug AND p2.status != 'deleted')
		 WHERE id = ? AND status = 'deleted'`,
		now, patchID,
	)
	return err
}

// PurgeDeletedPatches permanently removes patches with status='deleted' whose
// deleted_at is older than the provided cutoff time (RFC3339 string).
func (s *SQLPatchStore) PurgeDeletedPatches(_ context.Context, olderThan string) (int64, error) {
	result, err := s.DB.Exec(
		`DELETE FROM patches WHERE status = 'deleted' AND deleted_at IS NOT NULL AND deleted_at < ?`,
		olderThan,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CompactPositions re-numbers the non-deleted patches of a workspace to
// contiguous 1-based positions in their current order. Soft-deleted patches
// keep a unique negative position (-rowid) and are excluded.
//
// All rows of the workspace are first parked at -rowid (unique per table)
// within one transaction so that no intermediate state violates
// UNIQUE(workspace_slug, position).
func (s *SQLPatchStore) CompactPositions(_ context.Context, workspaceSlug string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(
		`SELECT id FROM patches WHERE workspace_slug = ? AND (status != 'deleted' OR status IS NULL) ORDER BY position ASC`,
		workspaceSlug,
	)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if _, err := tx.Exec(`UPDATE patches SET position = -rowid WHERE workspace_slug = ?`, workspaceSlug); err != nil {
		return err
	}
	now := apikit.NowUTC()
	for i, id := range ids {
		if _, err := tx.Exec(
			`UPDATE patches SET position = ?, updated_at = ? WHERE id = ?`,
			i+1, now, id,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ===========================================================================
// PurgeExpiredDeletedPatches: cleanup routine for soft-deleted patches
// ===========================================================================

// PurgeExpiredDeletedPatches permanently removes patches that have been in
// status='deleted' for longer than the specified retention period (7 days).
// Returns the number of patches purged.
func PurgeExpiredDeletedPatches(ctx context.Context, store PatchStore) (int64, error) {
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	return store.PurgeDeletedPatches(ctx, cutoff)
}

// ===========================================================================
// DefaultFetchFunc: production fetch using gitcmd
// ===========================================================================

// DefaultFetchFunc returns a FetchFunc that fetches the 'upstream' remote via
// go-git with the resolved credentials and records the upstream default
// branch under refs/remotes/upstream/HEAD (see package upstream). The
// upstream remote is expected to be pre-configured via carry-patch clone
// setup (spec 15).
func DefaultFetchFunc() FetchFunc {
	return upstream.Fetch
}

// DefaultPushIntegrationFunc returns a PushIntegrationFunc that force-pushes
// the integration branch to the origin remote via go-git, using the origin
// credentials resolved by resolveOriginAuth.
func DefaultPushIntegrationFunc(resolveOriginAuth func(slug string) (transport.AuthMethod, error)) PushIntegrationFunc {
	return func(ctx context.Context, workspaceSlug, repoPath, branch string) error {
		auth, err := resolveOriginAuth(workspaceSlug)
		if err != nil {
			return fmt.Errorf("resolve origin credentials: %w", err)
		}
		repo, err := git.PlainOpen(repoPath)
		if err != nil {
			return fmt.Errorf("open repository: %w", err)
		}
		spec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/heads/%s", branch, branch))
		err = repo.PushContext(ctx, &git.PushOptions{
			RemoteName: "origin",
			RefSpecs:   []config.RefSpec{spec},
			Auth:       auth,
			Force:      true,
		})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return err
		}
		return nil
	}
}

// resolveUpstreamBase returns the commit the rebuild, sync, and preview
// operations treat as "upstream HEAD" for the workspace.
//
// Resolution order:
//  1. refs/remotes/upstream/HEAD — the upstream default branch, recorded by
//     the fetch (package upstream). This is the intended base.
//  2. refs/remotes/upstream/<branch> — the workspace branch, when the
//     tracking ref for it exists (fetched by an older hub version).
//  3. FETCH_HEAD — legacy fallback; only correct for single-branch upstreams.
func resolveUpstreamBase(ctx context.Context, git GitRunner, workspaceBranch string) (string, error) {
	candidates := []string{upstream.HeadRef}
	if workspaceBranch != "" {
		candidates = append(candidates, "refs/remotes/upstream/"+workspaceBranch)
	}
	for _, ref := range candidates {
		if sha, err := git.Run(ctx, "rev-parse", "--verify", ref+"^{commit}"); err == nil && sha != "" {
			return sha, nil
		}
	}
	sha, err := git.Run(ctx, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve upstream base: %w", err)
	}
	return sha, nil
}

// workspaceBranch returns the workspace's configured branch ("" when unset
// or when db is nil).
func workspaceBranch(db *sql.DB, slug string) string {
	if db == nil {
		return ""
	}
	var branch sql.NullString
	if err := db.QueryRow(`SELECT branch FROM workspaces WHERE slug = ?`, slug).Scan(&branch); err != nil {
		return ""
	}
	return branch.String
}

// BuildRebuildPayload assembles the rebuild job payload, capturing the
// rebuild strategy and fail mode at enqueue time (16-PROP-3). Explicit
// overrides win over the REBUILD_STRATEGY / REBUILD_FAIL_MODE workspace
// variables; unset or invalid variables fall back to the defaults.
func BuildRebuildPayload(slug, integrationBranch, submittedBy string, getVariable GetVariableFunc, strategyOverride, failModeOverride string) RebuildPayload {
	strategy := strategyOverride
	if strategy == "" {
		strategy = StrategyRebase
		if getVariable != nil {
			if val, err := getVariable("workspace", slug, "REBUILD_STRATEGY"); err == nil && (val == StrategyRebase || val == StrategyMerge) {
				strategy = val
			}
		}
	}
	failMode := failModeOverride
	if failMode == "" && getVariable != nil {
		if val, err := getVariable("workspace", slug, "REBUILD_FAIL_MODE"); err == nil && (val == FailModeFailFast || val == FailModeContinue) {
			failMode = val
		}
	}
	return RebuildPayload{
		WorkspaceSlug:     slug,
		Strategy:          strategy,
		SubmittedBy:       submittedBy,
		IntegrationBranch: integrationBranch,
		FailMode:          failMode,
	}
}

// ===========================================================================
// Permission exports
// ===========================================================================

// CarryPatchPermissions returns the permission scopes used by carry-patch
// endpoints. The caller passes these to apikit.Server.MountHandlers so that
// PATs can be scoped to carry-patch operations.
func CarryPatchPermissions() []apikit.Permission {
	return []apikit.Permission{
		{Resource: "rebuilds", Action: "read"},
		{Resource: "rebuilds", Action: "write"},
	}
}

// ===========================================================================
// DefaultResolveAuthFunc: wraps workspace.ResolveUpstreamAuth
// ===========================================================================

// ===========================================================================
// FormatGroupKey constructs the job queue group_key for rebuild jobs.
// ===========================================================================

// FormatGroupKey returns the group_key for rebuild job serialization.
func FormatGroupKey(workspaceSlug, integrationBranch string) string {
	return workspaceSlug + ":" + integrationBranch
}

// SplitGroupKey parses a group_key into workspace_slug and integration_branch.
func SplitGroupKey(groupKey string) (workspaceSlug, integrationBranch string) {
	parts := strings.SplitN(groupKey, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return groupKey, ""
}
