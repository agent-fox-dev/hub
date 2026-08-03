package campaign

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/mergequeue"
)

// Module holds all initialized campaign components needed for cross-package
// wiring. Created by SetupModule.
type Module struct {
	Scheduler *Scheduler
	Authz     *Authz
	store     *Store
	db        *sql.DB
}

// SetupModule initializes all campaign components and registers API routes.
// It returns a Module whose methods provide the entry points that other
// packages need (PostMergeHook, PushAuthorizer, CanMerge, RecoverFromDB).
//
// Call order in main.go:
//  1. campaign.InitSchema(db)
//  2. mod := campaign.SetupModule(apiGroup, db, gitOps, workspaceRoot)
//  3. mod.RecoverActiveCampaigns(ctx)
//  4. mergequeue.SetCanMergeHook(mod.CheckCanMerge())
//  5. gitserver.SetPushAuthorizer(mod.PushAuthorizer())
func SetupModule(g *echo.Group, db *sql.DB, gitOps GitOps, workspaceRoot string) *Module {
	store := NewStore(db)
	authz := NewAuthz()
	rebaseEngine := NewRebaseEngine(store, gitOps, authz)

	scheduler := NewScheduler(store)
	scheduler.gitOps = gitOps
	scheduler.authz = authz
	scheduler.rebaseEngine = rebaseEngine
	scheduler.workspaceRoot = workspaceRoot

	h := &Handler{
		store:         store,
		scheduler:     scheduler,
		db:            db,
		gitOps:        gitOps,
		rebaseEngine:  rebaseEngine,
		authz:         authz,
		workspaceRoot: workspaceRoot,
	}

	campaigns := g.Group("/workspaces/:slug/campaigns")
	campaigns.POST("", h.createCampaign)
	campaigns.GET("", h.listCampaigns)
	campaigns.GET("/:id", h.getCampaign)
	campaigns.DELETE("/:id", h.cancelCampaign)
	campaigns.POST("/:id/specs/:spec_id/resolve", h.resolveSpec)

	return &Module{
		Scheduler: scheduler,
		Authz:     authz,
		store:     store,
		db:        db,
	}
}

// PostMergeHook returns a mergequeue.PostMergeHook that delegates to the
// campaign scheduler's HandlePostMerge method. Register this with
// mergequeue.MergeDeps.Hook.
func (m *Module) PostMergeHook() mergequeue.PostMergeHook {
	return m.Scheduler.HandlePostMerge
}

// PushAuthorizer returns the campaign's push authorization function.
// Register this with the git server to block pushes to conflicted
// spec branches and integration branches.
func (m *Module) PushAuthorizer() func(ctx context.Context, branch string) error {
	return m.Authz.AuthorizePush
}

// RecoverActiveCampaigns re-evaluates all active campaigns from the database.
// Call this at hub startup after InitSchema.
func (m *Module) RecoverActiveCampaigns(ctx context.Context) error {
	return m.Scheduler.RecoverFromDB(ctx)
}

// CheckCanMerge returns a mergequeue.CanMergeFunc that checks campaign spec
// status before allowing a merge to proceed. Use this as the canMerge hook
// registered with mergequeue via SetCanMergeHook.
func (m *Module) CheckCanMerge() mergequeue.CanMergeFunc {
	return func(ctx context.Context, db *sql.DB, job mergequeue.MergeJob) (bool, mergequeue.CantMergeReason, error) {
		reason, err := CheckCanMerge(ctx, db, job)
		if err != nil {
			return false, "", err
		}
		if reason != "" {
			return false, reason, nil
		}
		return true, "", nil
	}
}

// resolveRepoPath derives the git repository path for a given workspace slug.
func resolveRepoPath(workspaceRoot, workspaceSlug string) string {
	return filepath.Join(workspaceRoot, workspaceSlug, "trunk")
}
