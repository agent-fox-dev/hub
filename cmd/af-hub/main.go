package main

import (
	"context"
	"flag"
	"log"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitserver"
	"github.com/agent-fox-dev/hub/internal/health"
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

	// Create WORKSPACE_ROOT directory before starting HTTP handlers.
	if err := workspace.EnsureWorkspaceRoot(cfg.Workspace.Path); err != nil {
		log.Fatal(err)
	}

	// Reconcile any workspaces stuck in 'syncing' state from a previous
	// unclean shutdown. Must run before the HTTP server starts accepting
	// requests (13-REQ-5.1). Aborts startup on failure (13-REQ-5.E1).
	if err := workspace.ReconcileStuckSyncs(database.SqlDB); err != nil {
		log.Fatal(err)
	}

	// Initialize the async clone job queue with the configured number of
	// workers. The server context controls worker lifecycle; cancelling it
	// interrupts in-progress clones and discards pending jobs.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspace.InitCloneQueue(ctx, database.SqlDB, cfg.Workspace.Path, cfg.Workspace.Workers)
	workspace.InitSyncFunctions()

	server := apikit.NewServer(cfg, health.NewDBChecker(database))

	// Register the personal org hook before MountWorkspaceHandlers so that
	// the hook is captured when MountHandlers wires up user creation paths
	// (04-REQ-10.1). MountWorkspaceHandlers calls MountHandlers internally.
	server.OnAfterUserCreate(workspace.CreatePersonalOrg)

	// Collect extra permission scopes from all modules.
	var extraPerms []apikit.Permission
	extraPerms = append(extraPerms, gitserver.GitPermissions()...)
	extraPerms = append(extraPerms, secrets.Permissions()...)

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

	// Mount git smart HTTP handlers on the Echo instance. The git server
	// registers routes at /git/:org/:slug.git/* outside the API group,
	// with its own HTTP Basic auth middleware.
	if err := gitserver.MountGitHandlers(server.Echo(), database.SqlDB, cfg.Workspace.Path); err != nil {
		log.Fatal(err)
	}

	if err := server.Start(); err != nil {
		log.Fatal(err)
	}
}
