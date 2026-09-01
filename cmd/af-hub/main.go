package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/carrypatch"
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
	if err := jobqueue.MigrateProgress(database.SqlDB); err != nil {
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

	// ---------------------------------------------------------------------------
	// Carry-patch job registration (must happen before queue start)
	// ---------------------------------------------------------------------------

	cpPatchStore := carrypatch.NewSQLPatchStore(database.SqlDB)
	cpGitRunnerFactory := carrypatch.NewGitRunnerFactory()

	rebuildHandler := &carrypatch.RebuildHandler{
		DB:            database.SqlDB,
		Queue:         mergeQueue,
		Logger:        slog.Default(),
		WorkspaceRoot: cfg.Workspace.Path,
		NewGitRunner:  cpGitRunnerFactory,
		Fetch:         carrypatch.DefaultFetchFunc(),
		ResolveAuth: func(slug string) error {
			return workspace.ResolveUpstreamAuth(store, slug)
		},
		GetVariable: store.GetVariableValue,
		PatchStore:  cpPatchStore,
	}

	if err := carrypatch.RegisterRebuildJob(mergeQueue, rebuildHandler); err != nil {
		log.Fatal(err)
	}

	// Start the durable job queue workers. Workers dispatch merge and
	// rebuild jobs using group_key serialization so at most one job per
	// target runs at a time. All job types must be registered above.
	if err := mergeQueue.Start(); err != nil {
		log.Fatal(err)
	}
	defer mergeQueue.Stop()

	// ---------------------------------------------------------------------------
	// HTTP server
	// ---------------------------------------------------------------------------

	// ---------------------------------------------------------------------------
	// Prometheus metrics (spec 19)
	// ---------------------------------------------------------------------------

	metrics := audit.NewMetrics()

	// ---------------------------------------------------------------------------
	// Audit DuckDB database (specs 17-19)
	// ---------------------------------------------------------------------------

	// Resolve audit DuckDB path: AF_AUDIT_DB_PATH env or default to
	// <data_dir>/audit.duckdb alongside the SQLite database.
	auditDBPath := os.Getenv("AF_AUDIT_DB_PATH")
	if auditDBPath == "" {
		auditDBPath = filepath.Join(filepath.Dir(cfg.Database.Path), "audit.duckdb")
	}

	auditDB, err := audit.OpenDB(auditDBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer auditDB.Close()

	if err := audit.InitSchema(auditDB); err != nil {
		log.Fatal(err)
	}

	auditStore := audit.NewStore(auditDB)

	// Create SSE connection manager and audit emitter for hub event
	// broadcasting (spec 18) and session force-close audit events (spec 19).
	sseMgr := audit.NewSSEManager(100)
	go sseMgr.Run(ctx.Done())
	auditEmitter := audit.NewEmitterWithBroadcast(auditStore, sseMgr)

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
	extraPerms = append(extraPerms, carrypatch.CarryPatchPermissions()...)
	extraPerms = append(extraPerms, audit.Permissions()...)

	// Mount all built-in handlers (OAuth, users, orgs, keys, PATs) and
	// workspace handlers with workspace, git, secrets, variables, and
	// merge permission scopes registered.
	if err := workspace.MountWorkspaceHandlers(server, database, extraPerms...); err != nil {
		log.Fatal(err)
	}

	// Wire audit dependencies into workspace package for force-close on
	// archive/delete (19-REQ-8, 19-REQ-9).
	workspace.SetAuditDependencies(auditStore, auditEmitter, metrics)

	// ---------------------------------------------------------------------------
	// Audit routes (specs 17-19)
	// ---------------------------------------------------------------------------

	// Register session lifecycle, token usage, and cost routes (spec 19).
	auditAPI := server.APIGroup()
	audit.RegisterSessionRoutes(auditAPI, auditStore, database.SqlDB, metrics)

	// Register audit ingestion and per-run query routes (spec 17).
	audit.RegisterRoutes(auditAPI, auditStore, auditEmitter, database.SqlDB)

	// Register unified audit query, transcript, and SSE streaming routes (spec 18).
	audit.RegisterAuditQueryRoutes(auditAPI, auditStore, sseMgr)

	// Start the retention worker goroutine. Runs an immediate retention
	// pass then repeats hourly (19-REQ-12.1). Exits cleanly on ctx cancel.
	go audit.StartRetentionWorker(ctx, auditStore, database.SqlDB, metrics)

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

	// ---------------------------------------------------------------------------
	// Carry-patch HTTP routes (spec 16)
	// ---------------------------------------------------------------------------

	// Mount carry-patch REST API routes.
	cpAPI := server.APIGroup()

	carrypatch.RegisterRebuildRoutes(cpAPI, carrypatch.RebuildAPIConfig{
		DB:          database.SqlDB,
		Queue:       mergeQueue,
		GetVariable: store.GetVariableValue,
	})

	carrypatch.RegisterRerereRoutes(cpAPI, carrypatch.RerereAPIConfig{
		DB:            database.SqlDB,
		WorkspaceRoot: cfg.Workspace.Path,
		NewGitRunner:  cpGitRunnerFactory,
	})

	carrypatch.RegisterRebuildPreviewRoutes(cpAPI, carrypatch.RebuildPreviewAPIConfig{
		DB:            database.SqlDB,
		WorkspaceRoot: cfg.Workspace.Path,
		NewGitRunner:  cpGitRunnerFactory,
		PatchStore:    cpPatchStore,
	})

	carrypatch.RegisterPatchStatusRoutes(cpAPI, carrypatch.PatchStatusAPIConfig{
		DB:            database.SqlDB,
		Queue:         mergeQueue,
		WorkspaceRoot: cfg.Workspace.Path,
		PatchStore:    cpPatchStore,
	})

	// Register the branch-check hook so that POST /patches validates that the
	// branch exists in the workspace git repository before inserting.
	workspace.RegisterBranchCheckHook(func(slug, branchName string) error {
		runner, err := cpGitRunnerFactory(filepath.Join(cfg.Workspace.Path, slug, "trunk"))
		if err != nil {
			return err
		}
		_, err = runner.Run(context.Background(), "rev-parse", "--verify", branchName)
		return err
	})

	// Register the carry-patch sync hook so that POST /sync for carry_patch
	// workspaces delegates to the carry-patch sync extension (16-REQ-5).
	workspace.RegisterCarryPatchSyncHook(carrypatch.NewCarryPatchSyncHook(
		carrypatch.SyncAPIConfig{
			DB:            database.SqlDB,
			Queue:         mergeQueue,
			WorkspaceRoot: cfg.Workspace.Path,
			NewGitRunner:  cpGitRunnerFactory,
			Fetch:         carrypatch.DefaultFetchFunc(),
			ResolveAuth: func(slug string) error {
				return workspace.ResolveUpstreamAuth(store, slug)
			},
			GetVariable: store.GetVariableValue,
			PatchStore:  cpPatchStore,
		},
	))

	// Register the post-push hook for auto-rebuild on patch branch push (issue #14).
	// When a push targets a branch registered as a patch in a carry_patch workspace,
	// and AUTO_REBUILD_AFTER_PUSH is not "false", enqueue a rebuild job.
	gitserver.RegisterPostPushHook(carrypatch.NewPostPushRebuildHook(
		mergeQueue,
		store.GetVariableValue,
	))

	// Mount git smart HTTP handlers on the Echo instance. The git server
	// registers routes at /git/:org/:slug.git/* outside the API group,
	// with its own HTTP Basic auth middleware.
	if err := gitserver.MountGitHandlers(server.Echo(), database.SqlDB, cfg.Workspace.Path); err != nil {
		log.Fatal(err)
	}

	// Mount Prometheus middleware on the Echo instance for HTTP request
	// metrics and expose GET /metrics outside the API auth group (19-REQ-10).
	server.Echo().Use(metrics.PrometheusMiddleware())
	server.Echo().GET("/metrics", echo.WrapHandler(metrics.MetricsHandler()))

	if err := server.Start(); err != nil {
		log.Fatal(err)
	}
}
