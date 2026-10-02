package carrypatch

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// PostPushMirrorDeps holds the optional dependencies for hub-mode mirroring
// in the post-push hook. When any dependency is nil/empty, mirroring is
// disabled and the rebuild behaviour is unchanged.
type PostPushMirrorDeps struct {
	// ResolveAuth resolves origin credentials for the workspace.
	ResolveAuth ResolveAuthFunc
	// WorkspaceRoot is the filesystem directory under which workspace
	// local clones are stored.
	WorkspaceRoot string
	// Audit is the optional audit event emitter. When nil, mirror failure
	// events are not emitted.
	Audit audit.Emitter
}

// mirrorEnabled returns true when all mirror dependencies are supplied.
func (d PostPushMirrorDeps) mirrorEnabled() bool {
	return d.ResolveAuth != nil && d.WorkspaceRoot != "" && d.Audit != nil
}

// mirrorTimeout is the maximum time allowed for a mirror push to origin.
const mirrorTimeout = 120 * time.Second

// mirrorBranches force-pushes each applicable branch to the fork's origin
// remote. It is called after the rebuild enqueue in the post-push hook
// goroutine. A failure of one branch does not stop the others.
func mirrorBranches(
	db *sql.DB,
	slug string,
	branches []string,
	deps PostPushMirrorDeps,
	getVariable GetVariableFunc,
	logger *slog.Logger,
) {
	// 22-REQ-6.4: Only mirror when all dependencies are supplied.
	if !deps.mirrorEnabled() {
		// When the emitter is nil but other deps are present, mirroring
		// is still disabled per 22-REQ-6.6.
		return
	}

	// 22-REQ-6.4: Only mirror when PUSH_PATCHES_TO_ORIGIN is exactly "true".
	if !pushPatchesEnabled(getVariable, slug) {
		return
	}

	// 22-REQ-6.4: Only mirror when PATCH_BRANCH_SOURCE resolves to "hub".
	source := ParsePatchBranchSource(getVariable, slug)
	if source != "hub" {
		return
	}

	// Load workspace info for applicability checks.
	wsInfo, err := loadWorkspaceInfo(db, slug)
	if err != nil {
		if err != sql.ErrNoRows {
			logger.Warn("mirror: failed to load workspace info",
				"slug", slug,
				"error", err.Error(),
			)
		}
		return
	}

	// 22-REQ-6.4: Only mirror for carry_patch workspaces.
	if wsInfo.mode != "carry_patch" {
		return
	}

	// Resolve origin credentials once for all branches.
	auth, authErr := deps.ResolveAuth(slug)
	if authErr != nil {
		logger.Warn("mirror: failed to resolve origin credentials",
			"slug", slug,
			"error", authErr.Error(),
		)
		return
	}

	for _, branch := range branches {
		// 22-REQ-6.4: Skip the integration branch.
		if branch == wsInfo.integrationBranch {
			continue
		}

		// 22-REQ-6.4: Skip unregistered branches.
		registered, regErr := isRegisteredPatchBranch(db, slug, branch)
		if regErr != nil {
			logger.Warn("mirror: failed to check patch registration",
				"slug", slug,
				"branch", branch,
				"error", regErr.Error(),
			)
			continue
		}
		if !registered {
			continue
		}

		// 22-REQ-6.1: Force-push the branch to origin.
		mirrorErr := mirrorBranchToOrigin(deps.WorkspaceRoot, slug, branch, auth, logger)
		if mirrorErr != nil {
			safeErr := stripUserinfo(mirrorErr.Error())
			logger.Warn("mirror: failed to push branch to origin",
				"slug", slug,
				"branch", branch,
				"error", safeErr,
			)

			// 22-REQ-7.1: Emit hub.patch.mirror_failed event.
			emitMirrorFailedEvent(deps.Audit, slug, branch, safeErr, logger)
		} else {
			logger.Info("mirror: pushed branch to origin",
				"slug", slug,
				"branch", branch,
			)
		}
	}
}

// mirrorBranchToOrigin force-pushes a single branch to the fork's origin
// remote using the refspec +refs/heads/<name>:refs/heads/<name>.
func mirrorBranchToOrigin(
	workspaceRoot, slug, branch string,
	auth transport.AuthMethod,
	logger *slog.Logger,
) error {
	trunkPath := filepath.Join(workspaceRoot, slug, "trunk")
	repo, err := git.PlainOpen(trunkPath)
	if err != nil {
		return fmt.Errorf("open trunk: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), mirrorTimeout)
	defer cancel()

	// 22-REQ-6.1: Force push via the '+' prefix in the refspec.
	refspec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/heads/%s", branch, branch))
	opts := &git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{refspec},
		Auth:       auth,
		Force:      true,
	}

	pushErr := repo.PushContext(ctx, opts)
	if pushErr != nil && pushErr != git.NoErrAlreadyUpToDate {
		return pushErr
	}

	return nil
}

// emitMirrorFailedEvent emits the hub.patch.mirror_failed audit event.
// A nil emitter skips emission. An emit error is logged without affecting
// anything else.
func emitMirrorFailedEvent(
	emitter audit.Emitter,
	slug, branch, safeError string,
	logger *slog.Logger,
) {
	if emitter == nil {
		return
	}

	event := audit.HubEvent{
		EventType:    audit.EventPatchMirrorFailed,
		ResourceType: "patch",
		Action:       "mirror",
		ActorType:    "system",
		Workspace:    slug,
		Metadata: map[string]any{
			"branch_name": branch,
			"error":       safeError,
		},
	}

	if err := emitter.Emit(context.Background(), event); err != nil {
		logger.Warn("mirror: failed to emit mirror_failed event",
			"slug", slug,
			"branch", branch,
			"error", err.Error(),
		)
	}
}
