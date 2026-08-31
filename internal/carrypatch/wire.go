package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
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
	_, err := a.runner.Run(ctx, "cherry-pick", commitSHA)
	if err != nil {
		// Check if CHERRY_PICK_HEAD exists — indicates a conflict in progress.
		_, revErr := a.runner.Run(ctx, "rev-parse", "--verify", "CHERRY_PICK_HEAD")
		if revErr == nil {
			return &CherryPickConflictError{}
		}
		return err
	}
	return nil
}

// MergeNoFF merges a branch with --no-ff. Unlike gitcmd.MergeNoFF, it does NOT
// auto-abort on conflict (same reason as CherryPick).
func (a *GitRunnerAdapter) MergeNoFF(ctx context.Context, branch string) error {
	_, err := a.runner.Run(ctx, "merge", "--no-ff", branch)
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

// ListPatches returns all patches for a workspace ordered by position.
func (s *SQLPatchStore) ListPatches(_ context.Context, workspaceSlug string) ([]Patch, error) {
	rows, err := s.DB.Query(
		`SELECT id, workspace_slug, branch_name, position, status, conflict_files, upstream_pr_url
		 FROM patches WHERE workspace_slug = ? ORDER BY position ASC`,
		workspaceSlug,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var patches []Patch
	for rows.Next() {
		var p Patch
		var conflictFilesJSON string
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.BranchName, &p.Position, &p.Status, &conflictFilesJSON, &p.UpstreamPRURL); err != nil {
			return nil, err
		}
		if conflictFilesJSON != "" {
			_ = json.Unmarshal([]byte(conflictFilesJSON), &p.ConflictFiles)
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

// CompactPositions re-numbers patch positions to be contiguous starting from 1.
func (s *SQLPatchStore) CompactPositions(_ context.Context, workspaceSlug string) error {
	rows, err := s.DB.Query(
		`SELECT id FROM patches WHERE workspace_slug = ? ORDER BY position ASC`,
		workspaceSlug,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	now := apikit.NowUTC()
	for i, id := range ids {
		if _, err := s.DB.Exec(
			`UPDATE patches SET position = ?, updated_at = ? WHERE id = ?`,
			i+1, now, id,
		); err != nil {
			return err
		}
	}
	return nil
}

// ===========================================================================
// DefaultFetchFunc: production fetch using gitcmd
// ===========================================================================

// DefaultFetchFunc returns a FetchFunc that fetches from the 'upstream' remote
// using git CLI. The upstream remote is expected to be pre-configured via
// carry-patch clone setup (spec 15).
func DefaultFetchFunc() FetchFunc {
	return func(ctx context.Context, repoPath string) error {
		runner, err := gitcmd.New(repoPath, nil)
		if err != nil {
			return err
		}
		_, fetchErr := runner.Run(ctx, "fetch", "upstream")
		return fetchErr
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

// DefaultResolveAuthFunc returns a ResolveAuthFunc that validates upstream
// credentials exist for a workspace. It wraps the provided function to
// discard the credential value (only used as a precondition check; the
// actual auth is handled by the git fetch configuration).
func DefaultResolveAuthFunc(resolveFunc func(slug string) error) ResolveAuthFunc {
	return resolveFunc
}

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
