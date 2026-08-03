package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/campaign"
	"github.com/agent-fox-dev/hub/internal/gitcmd"
	"github.com/agent-fox-dev/hub/internal/gitserver"
	"github.com/agent-fox-dev/hub/internal/health"
	"github.com/agent-fox-dev/hub/internal/mergequeue"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

func main() {
	adminEmail := flag.String("admin-email", "", "admin email for first-boot bootstrap")
	resetToken := flag.Bool("reset-admin-token", false, "rotate the admin token")
	flag.Parse()

	cfg, err := apikit.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	database, err := apikit.OpenDatabase(cfg.Database.Path)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	if err := apikit.Bootstrap(context.Background(), database, apikit.BootstrapOptions{
		AdminEmail: *adminEmail,
		ResetToken: *resetToken,
	}); err != nil {
		log.Fatal(err)
	}

	// Validate git binary meets minimum version requirement (>= 2.38) before
	// any git-dependent operations (clone queue, git smart HTTP). The 5-second
	// deadline is the recommended timeout per the gitcmd package documentation.
	versionCtx, versionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer versionCancel()
	if err := gitcmd.CheckGitVersion(versionCtx); err != nil {
		log.Fatal(err)
	}

	// Create WORKSPACE_ROOT directory before starting HTTP handlers.
	if err := workspace.EnsureWorkspaceRoot(cfg.Workspace.Path); err != nil {
		log.Fatal(err)
	}

	// Initialize the async clone job queue with the configured number of
	// workers. The server context controls worker lifecycle; cancelling it
	// interrupts in-progress clones and discards pending jobs.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspace.InitCloneQueue(ctx, database.SqlDB, cfg.Workspace.Path, cfg.Workspace.Workers)

	server := apikit.NewServer(cfg, health.NewDBChecker(database))

	// Register the personal org hook before MountWorkspaceHandlers so that
	// the hook is captured when MountHandlers wires up user creation paths
	// (04-REQ-10.1). MountWorkspaceHandlers calls MountHandlers internally.
	server.OnAfterUserCreate(workspace.CreatePersonalOrg)

	// Collect extra permission scopes from all modules.
	var extraPerms []apikit.Permission
	extraPerms = append(extraPerms, gitserver.GitPermissions()...)
	extraPerms = append(extraPerms, secrets.Permissions()...)
	extraPerms = append(extraPerms, campaign.Permissions()...)

	// Mount all built-in handlers (OAuth, users, orgs, keys, PATs) and
	// workspace handlers with workspace, git, secrets, and variables
	// permission scopes registered.
	if err := workspace.MountWorkspaceHandlers(server, database, extraPerms...); err != nil {
		log.Fatal(err)
	}

	// Initialise the secrets and variables tables and mount their API
	// routes. Must run after MountWorkspaceHandlers (which creates the
	// API group via MountHandlers) and before Start.
	if err := secrets.InitSchema(database.SqlDB); err != nil {
		log.Fatal(err)
	}
	if err := secrets.RegisterRoutes(server.APIGroup(), database.SqlDB); err != nil {
		log.Fatal(err)
	}

	// Initialise the campaign tables and mount campaign API routes.
	// Must run after MountWorkspaceHandlers and before Start.
	if err := campaign.InitSchema(database.SqlDB); err != nil {
		log.Fatal(err)
	}
	campaignGitOps := campaign.NewGitRunnerAdapter()
	campaignModule := campaign.SetupModule(
		server.APIGroup(), database.SqlDB, campaignGitOps, cfg.Workspace.Path,
	)

	// Recover active campaigns on startup (12-REQ-14).
	if err := campaignModule.RecoverActiveCampaigns(ctx); err != nil {
		log.Fatal(err)
	}

	// Register the campaign's CanMerge check with the merge queue so that
	// cancelled/blocked specs are rejected before merge processing.
	mergequeue.SetCanMergeHook(campaignModule.CheckCanMerge())

	// Mount git smart HTTP handlers on the Echo instance. The git server
	// registers routes at /git/:org/:slug.git/* outside the API group,
	// with its own HTTP Basic auth middleware.
	if err := gitserver.MountGitHandlers(server.Echo(), database.SqlDB, cfg.Workspace.Path); err != nil {
		log.Fatal(err)
	}

	// Register the campaign's PushAuthorizer with the git server to block
	// pushes to conflicted spec branches and integration branches (12-REQ-9).
	gitserver.SetPushAuthorizer(gitserver.PushAuthorizerFunc(campaignModule.PushAuthorizer()))

	if err := server.Start(); err != nil {
		log.Fatal(err)
	}
}
