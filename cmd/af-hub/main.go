package main

import (
	"context"
	"flag"
	"log"
	"log/slog"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitserver"
	"github.com/agent-fox-dev/hub/internal/health"
	"github.com/agent-fox-dev/hub/internal/jobqueue"
	"github.com/agent-fox-dev/hub/internal/merge"
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

	// Initialize the async clone job queue with the configured number of
	// workers. The server context controls worker lifecycle; cancelling it
	// interrupts in-progress clones and discards pending jobs.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspace.InitCloneQueue(ctx, database.SqlDB, cfg.Workspace.Path, cfg.Workspace.Workers)
	workspace.InitSyncFunctions()

	// ---------------------------------------------------------------------------
	// Durable job queue: merge operations (spec 12)
	// ---------------------------------------------------------------------------

	// Initialise the durable job queue schema and apply the group_key
	// migration for merge job serialization (12-REQ-1.1).
	if err := jobqueue.InitSchema(database.SqlDB); err != nil {
		log.Fatal(err)
	}
	if err := jobqueue.MigrateGroupKey(database.SqlDB); err != nil {
		log.Fatal(err)
	}

	// Create the durable job queue for merge operations.
	mergeQueue, err := jobqueue.New(database.SqlDB, slog.Default())
	if err != nil {
		log.Fatal(err)
	}

	// Create a secrets store for resolving clone credentials and workspace
	// variables (CHECK_COMMAND) in the merge handler.
	store := secrets.NewStore(database.SqlDB)

	// Create and register the merge handler with all cross-spec dependencies
	// wired:
	//   - GitRunner (spec 11): created on-demand via runnerForWorkspace
	//   - resolveCloneAuth (spec 05): injected via ResolveAuth field
	//   - workspace variables (spec 07): injected via GetVariable field
	//   - CHECK_COMMAND execution: injected via Executor field
	//   - Rollback: injected via DefaultRollbackFunc
	mergeHandler := &merge.Handler{
		WorkspaceRoot: cfg.Workspace.Path,
		Fetch:         merge.DefaultFetchFunc(),
		ResolveAuth: func(slug string) (transport.AuthMethod, error) {
			return workspace.ResolveCloneAuth(store, slug)
		},
		GetVariable: store.GetVariableValue,
		Executor:    &merge.ShellExecutor{},
		Rollback:    merge.DefaultRollbackFunc(),
	}

	if err := merge.RegisterHandler(mergeQueue, mergeHandler); err != nil {
		log.Fatal(err)
	}

	// Start the durable job queue workers. Workers dispatch merge jobs
	// using group_key serialization (12-REQ-1.4) so at most one merge
	// per target branch runs at a time.
	if err := mergeQueue.Start(); err != nil {
		log.Fatal(err)
	}
	defer mergeQueue.Stop()

	// ---------------------------------------------------------------------------
	// HTTP server
	// ---------------------------------------------------------------------------

	server := apikit.NewServer(cfg, health.NewDBChecker(database))

	// Register the personal org hook before MountWorkspaceHandlers so that
	// the hook is captured when MountHandlers wires up user creation paths
	// (04-REQ-10.1). MountWorkspaceHandlers calls MountHandlers internally.
	server.OnAfterUserCreate(workspace.CreatePersonalOrg)

	// Collect extra permission scopes from all modules.
	var extraPerms []apikit.Permission
	extraPerms = append(extraPerms, gitserver.GitPermissions()...)
	extraPerms = append(extraPerms, secrets.Permissions()...)
	extraPerms = append(extraPerms, merge.MergePermissions()...)

	// Mount all built-in handlers (OAuth, users, orgs, keys, PATs) and
	// workspace handlers with workspace, git, secrets, variables, and
	// merge permission scopes registered.
	if err := workspace.MountWorkspaceHandlers(server, database, extraPerms...); err != nil {
		log.Fatal(err)
	}

	// Reconcile any workspaces stuck in 'syncing' state from a previous
	// unclean shutdown. Must run after schema init and before the HTTP server
	// starts accepting requests (13-REQ-5.1). Aborts startup on failure
	// (13-REQ-5.E1).
	if err := workspace.ReconcileStuckSyncs(database.SqlDB); err != nil {
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

	// Mount merge REST API routes (POST/GET/DELETE /merges, POST /rebase).
	// Uses NewMergeAPIConfig to wire BatchRebase to the real GitRunner-backed
	// implementation (12-REQ-12).
	mergeCfg := merge.NewMergeAPIConfig(
		database.SqlDB,
		mergeQueue,
		merge.DefaultBranchChecker(cfg.Workspace.Path),
		cfg.Workspace.Path,
	)
	merge.RegisterMergeRoutes(server.APIGroup(), mergeCfg)

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
