package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/aflib/jobqueue"
	"github.com/agent-fox-dev/carrypatch"
	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/boundtoken"
	"github.com/agent-fox-dev/hub/internal/cphost"
	"github.com/agent-fox-dev/hub/internal/forge"
	"github.com/agent-fox-dev/hub/internal/gitserver"
	"github.com/agent-fox-dev/hub/internal/merge"
	"github.com/agent-fox-dev/hub/internal/sandboxes"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/workspace"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
	"github.com/agent-fox-dev/hub/internal/wslock"
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

	// Load, validate and probe the [sandbox] section of config.toml (spec 25).
	// This runs after apikit.Bootstrap, so that first-boot admin-token
	// generation never depends on the sandbox configuration, and before the
	// workspace root, the queues, the workers and the routes exist, so that an
	// invalid section stops the start before any of them is set up. The result
	// is nil when sandboxes are not configured; the routes then answer
	// sandboxes_disabled.
	sandboxSvc, err := sandboxes.Init(slog.Default(), apikit.ConfigPath(), sandboxes.PodmanFactory,
		sandboxes.WithGatewayFactory(sandboxes.DialGateway))
	if err != nil {
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

	// The two git credentials of a workspace, resolved from its secrets on
	// every use: the one of its own repository (origin) and, on a carry-patch
	// fork, the one of the upstream it tracks.
	resolveOriginAuth := func(slug string) (transport.AuthMethod, error) {
		return workspace.ResolveCloneAuth(store, slug)
	}
	resolveUpstreamAuth := func(slug string) (transport.AuthMethod, error) {
		return workspace.ResolveUpstreamAuthMethod(store, slug)
	}

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
		ResolveAuth:   resolveOriginAuth,
		GetVariable:   store.GetVariableValue,
		Executor:      &merge.ShellExecutor{},
		Rollback:      merge.DefaultRollbackFunc(),
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
		DB:              database.SqlDB,
		Queue:           mergeQueue,
		Logger:          slog.Default(),
		WorkspaceRoot:   cfg.Workspace.Path,
		NewGitRunner:    cpGitRunnerFactory,
		Fetch:           carrypatch.DefaultFetchFunc(),
		ResolveAuth:     resolveUpstreamAuth,
		GetVariable:     store.GetVariableValue,
		PatchStore:      cpPatchStore,
		PushIntegration: carrypatch.DefaultPushIntegrationFunc(resolveOriginAuth),
	}

	if err := carrypatch.RegisterRebuildJob(mergeQueue, rebuildHandler); err != nil {
		log.Fatal(err)
	}

	// Remove rebuild worktrees left behind by a crash. This runs before
	// the queue starts, so no rebuild can be using one of those directories;
	// it logs its errors and never prevents startup.
	carrypatch.CleanupStaleRebuildWorktrees(ctx, cfg.Workspace.Path, slog.Default())

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
	// SSE connection gauge and audit event counter are recorded through the
	// package-level sink.
	audit.SetMetrics(metrics)

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
	// Read AF_SSE_MAX_CONNECTIONS from environment, default to 100 (18-REQ-9.3).
	sseMaxConns := audit.DefaultMaxConnections
	if envMax := os.Getenv("AF_SSE_MAX_CONNECTIONS"); envMax != "" {
		if n, parseErr := strconv.Atoi(envMax); parseErr != nil || n <= 0 {
			slog.Warn("AF_SSE_MAX_CONNECTIONS is invalid, using default",
				"value", envMax,
				"default", audit.DefaultMaxConnections,
			)
		} else {
			sseMaxConns = n
		}
	}
	sseMgr := audit.NewSSEManager(sseMaxConns)
	go sseMgr.Run(ctx.Done())
	auditEmitter := audit.NewEmitter(auditStore, sseMgr)

	server := apikit.NewServer(cfg, func() error { return database.Ping(context.Background()) })

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
	// Registered whether or not sandboxes are configured, so PATs can be issued
	// with the sandbox scopes ahead of time (25-REQ-6.1).
	extraPerms = append(extraPerms, sandboxes.Permissions()...)
	// Likewise the forge scopes (30-REQ-2.1): forge:write is registered although no
	// route needs it yet, so that PATs are issued once.
	extraPerms = append(extraPerms, forge.Permissions()...)

	// Mount all built-in handlers (OAuth, users, orgs, keys, PATs) and
	// workspace handlers with workspace, git, secrets, variables, and
	// merge permission scopes registered.
	if err := workspace.MountWorkspaceHandlers(server, database, extraPerms...); err != nil {
		log.Fatal(err)
	}

	// Mount the routes that mint and list workspace-bound tokens (spec 34).
	// A mint is served by apikit's own POST /user/tokens through server.Echo(),
	// in-process, and hub adds the binding.
	boundtoken.RegisterRoutes(server.APIGroup(), boundtoken.Config{
		DB:     database.SqlDB,
		APIKit: server.Echo(),
		Audit:  auditEmitter,
	})
	// Record every request refused because of a token binding as
	// hub.token.bound_refused, at most once per token per minute.
	boundtoken.Install(auditEmitter)

	// Wire audit dependencies into workspace package for force-close on
	// archive/delete (19-REQ-8, 19-REQ-9).
	workspace.SetAuditDependencies(auditStore, auditEmitter, metrics)

	// Wire audit emitter into merge and rebuild job handlers so that
	// complete/fail events are emitted (18-REQ-2, 18-REQ-3).
	mergeHandler.Audit = auditEmitter
	rebuildHandler.Audit = cphost.Emitter(auditEmitter)

	// ---------------------------------------------------------------------------
	// Audit routes (specs 17-19)
	// ---------------------------------------------------------------------------

	// Register session lifecycle, token usage, and cost routes (spec 19).
	auditAPI := server.APIGroup()
	audit.RegisterSessionRoutes(auditAPI, auditStore, database.SqlDB, metrics)

	// Register audit ingestion and per-run query routes (spec 17).
	audit.RegisterRoutes(auditAPI, auditStore, auditEmitter, database.SqlDB)

	// Register unified audit query, transcript, and SSE streaming routes (spec 18).
	audit.RegisterAuditQueryRoutes(auditAPI, auditStore, sseMgr, database.SqlDB)

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
	if err := secrets.RegisterRoutesWithAudit(server.APIGroup(), secrets.SecretsRouteConfig{
		DB:    database.SqlDB,
		Audit: auditEmitter,
	}); err != nil {
		log.Fatal(err)
	}

	// Create the table of the forge deliveries at every start (spec 32); the
	// maintenance loop below keeps it small.
	if err := forge.InitSchema(database.SqlDB); err != nil {
		log.Fatal(err)
	}

	// Create the sandbox lease table at every start, whether or not sandboxes
	// are enabled, so that turning them off and on again keeps the history
	// (spec 26).
	if err := sandboxes.InitSchema(database.SqlDB); err != nil {
		log.Fatal(err)
	}
	// Add lease_expires_at to a lease table that predates it (spec 28). This too
	// runs at every start, whether or not sandboxes are enabled.
	if err := sandboxes.MigrateLeaseExpiry(database.SqlDB); err != nil {
		log.Fatal(err)
	}

	// Housekeeping: at once and then every hour, until the hub stops. Every
	// table the tasks touch exists by now. The audit retention applies the
	// AF_AUDIT_* and AF_SESSION_* limits to the audit database, the delivery
	// pruner keeps the forge delivery log to seven days, and the binding sweep
	// revokes and removes token bindings whose workspace or token is gone.
	retention := audit.LoadRetentionConfigFromEnv()
	go runMaintenance(ctx, maintenanceInterval, []maintenanceTask{
		{name: "audit retention", run: func(ctx context.Context) error {
			return audit.RunRetention(ctx, auditDB, database.SqlDB, retention, metrics)
		}},
		{name: "forge delivery log", run: func(ctx context.Context) error {
			_, err := forge.PruneDeliveries(ctx, database.SqlDB)
			return err
		}},
		{name: "token binding sweep", run: func(ctx context.Context) error {
			return wsaccess.SweepBindings(ctx, database.SqlDB)
		}},
	})

	// Mount merge REST API routes (POST/GET/DELETE /merges, POST /rebase).
	// Uses NewMergeAPIConfig to wire BatchRebase to the real GitRunner-backed
	// implementation (12-REQ-12).
	mergeCfg := merge.NewMergeAPIConfig(
		database.SqlDB,
		mergeQueue,
		merge.DefaultBranchChecker(cfg.Workspace.Path),
		cfg.Workspace.Path,
	)
	mergeCfg.Audit = auditEmitter
	merge.RegisterMergeRoutes(server.APIGroup(), mergeCfg)

	// Mount the sandbox profile listing. The route exists even when sandboxes
	// are not configured (a nil service) and then answers sandboxes_disabled.
	sandboxes.RegisterRoutes(server.APIGroup(), sandboxSvc)

	// Mount the lease routes (spec 26). The lease service needs the validated
	// profiles, the database, the secrets store (for secret names only), the audit
	// emitter and the address of the gateway to show on every lease. A hub without
	// sandboxes passes a nil service, and the routes answer sandboxes_disabled.
	//
	// With sandboxes on, the sandbox gateway (spec 27) is built first, on its own
	// listener and before the HTTP server serves anything: a lease is worth
	// nothing unless a gateway serves it. A key pair that cannot be loaded or an
	// address that cannot be bound stops the start.
	//
	// Run (spec 28) then ends the leases a crashed process left active, does one
	// sweep, starts the reaper, and only then lets the gateway accept connections.
	// It needs the lease table and its migration, the audit emitter and the
	// metrics, which all exist by now. What fails in it is logged and never stops
	// hub from starting.
	gateway, leaseSvc := startSandboxes(ctx, sandboxSvc, database.SqlDB, store, auditEmitter, metrics)
	sandboxes.RegisterLeaseRoutes(server.APIGroup(), leaseSvc)

	// Mount the forge routes (specs 30, 31 and 32): a workspace's repository, issues and pull
	// requests on its forge, read and written with the credential hub holds for the
	// workspace, and the webhook secret of the workspace. Every write is recorded as an audit
	// event. The same configuration serves the ingress below, which receives the forge's
	// deliveries.
	forgeConfig := forge.Config{
		DB:                  database.SqlDB,
		ResolveOriginAuth:   resolveOriginAuth,
		ResolveUpstreamAuth: resolveUpstreamAuth,
		GetVariable:         store.GetVariableValue,
		Audit:               auditEmitter,
		Secrets:             store,
		ExternalURL:         server.ExternalURL(),
	}
	forge.RegisterRoutes(server.APIGroup(), forgeConfig)

	// ---------------------------------------------------------------------------
	// Carry-patch module
	// ---------------------------------------------------------------------------

	// The carry-patch module (github.com/agent-fox-dev/carrypatch) knows the
	// hub only through internal/cphost: the per-workspace locks and the
	// workspace access rule it is bound to here, the audit emitter each
	// configuration carries, and the git hooks the git server is handed below.
	cphost.Bind()
	cpAudit := cphost.Emitter(auditEmitter)
	cpResolveBranch := carrypatch.NewBranchResolverHook(
		cpGitRunnerFactory,
		cfg.Workspace.Path,
		store.GetVariableValue,
		resolveOriginAuth,
		carrypatch.DefaultSingleBranchFetch(),
	)
	cpRecovery := &carrypatch.RecoveryService{
		NewGitRunner:  cpGitRunnerFactory,
		WorkspaceRoot: cfg.Workspace.Path,
		GetVariable:   store.GetVariableValue,
		ResolveAuth:   resolveOriginAuth,
		Fetch:         carrypatch.DefaultSingleBranchFetch(),
		LockFunc:      wslock.TryLock,
		PatchStore:    cpPatchStore,
		Queue:         mergeQueue,
	}

	// The patch list, rebuild submission and history, rollback, rebuild
	// preview, rerere and the patch-status dashboard.
	carrypatch.RegisterRoutes(server.APIGroup(), carrypatch.APIConfig{
		DB:            database.SqlDB,
		Queue:         mergeQueue,
		WorkspaceRoot: cfg.Workspace.Path,
		NewGitRunner:  cpGitRunnerFactory,
		PatchStore:    cpPatchStore,
		GetVariable:   store.GetVariableValue,
		ResolveBranch: cpResolveBranch,
		Recovery:      cpRecovery,
		Audit:         cpAudit,
	})

	// POST /sync on a carry_patch workspace runs the module's sync: upstream
	// fetch, merge detection and the auto-rebuild.
	workspace.RegisterSyncExtension(carrypatch.NewCarryPatchSyncHook(
		carrypatch.SyncAPIConfig{
			DB:                database.SqlDB,
			Queue:             mergeQueue,
			WorkspaceRoot:     cfg.Workspace.Path,
			NewGitRunner:      cpGitRunnerFactory,
			Fetch:             carrypatch.DefaultFetchFunc(),
			ResolveAuth:       resolveUpstreamAuth,
			GetVariable:       store.GetVariableValue,
			PatchStore:        cpPatchStore,
			FetchOrigin:       carrypatch.DefaultFetchOriginFunc(),
			ResolveOriginAuth: resolveOriginAuth,
			Audit:             cpAudit,
		},
	))

	// Push control: in origin mode the pre-receive hook rejects or forwards
	// hub pushes to registered patch branches; in hub mode it lets them
	// through. After a push the post-push hook enqueues a rebuild for a
	// registered patch branch (unless AUTO_REBUILD_AFTER_PUSH is "false")
	// and, in hub mode with PUSH_PATCHES_TO_ORIGIN, mirrors it to the fork.
	gitserver.RegisterPreReceiveHook(cphost.PreReceive(carrypatch.NewPreReceiveHook(carrypatch.PreReceiveHookDeps{
		GetVariable:   store.GetVariableValue,
		ResolveAuth:   resolveOriginAuth,
		WorkspaceRoot: cfg.Workspace.Path,
	})))
	gitserver.RegisterPostPushHook(cphost.PostPush(carrypatch.NewPostPushRebuildHook(
		mergeQueue,
		store.GetVariableValue,
		carrypatch.PostPushMirrorDeps{
			ResolveAuth:   resolveOriginAuth,
			WorkspaceRoot: cfg.Workspace.Path,
			Audit:         cpAudit,
		},
	)))

	// Set the audit emitter for git server push events (18-REQ-5.2).
	gitserver.SetAuditEmitter(auditEmitter)

	// Mount git smart HTTP handlers on the Echo instance. The git server
	// registers routes at /git/:org/:slug.git/* outside the API group,
	// with its own HTTP Basic auth middleware.
	if err := gitserver.MountGitHandlers(server.Echo(), database.SqlDB, cfg.Workspace.Path); err != nil {
		log.Fatal(err)
	}

	// Mount the webhook ingress of the forges (spec 32) the same way: POST /hooks/forge/:slug
	// is outside the API group and its authentication, and a delivery proves who it is with the
	// signature of the workspace's webhook secret.
	forge.MountWebhookIngress(server.Echo(), forgeConfig)

	// Mount Prometheus middleware on the Echo instance for HTTP request
	// metrics and expose GET /metrics outside the API auth group (19-REQ-10).
	server.Echo().Use(metrics.PrometheusMiddleware())
	server.Echo().GET("/metrics", echo.WrapHandler(metrics.MetricsHandler()))

	// When Start returns, the sandbox shutdown runs while both databases are still
	// open: they close in the deferred calls above, which run after this. A hub
	// without sandboxes has no gateway, and the shutdown does nothing.
	if err := runServer(server.Start, func() {
		if err := gateway.Shutdown(context.Background()); err != nil {
			slog.Error("the sandbox shutdown did not finish; the leases still active are ended at the next start", "error", err)
		}
	}); err != nil {
		log.Fatal(err)
	}
}

// startSandboxes builds the sandbox gateway, lets it do its start-up work and
// builds the lease service over it. A hub without sandboxes (a nil service) gets a
// nil gateway and a nil lease service, and nothing is bound or run. A gateway that
// cannot be built stops the start; what fails in its start-up work is logged.
func startSandboxes(ctx context.Context, svc *sandboxes.Service, db *sql.DB, store *secrets.Store, emitter audit.Emitter, metrics *audit.Metrics) (*sandboxes.Gateway, *sandboxes.LeaseService) {
	if svc == nil {
		return nil, nil
	}
	gateway, err := sandboxes.NewGateway(svc, sandboxes.GatewayDeps{DB: db, Secrets: store, Audit: emitter, Metrics: metrics})
	if err != nil {
		log.Fatal(err)
	}
	if err := gateway.Run(ctx); err != nil {
		slog.Warn("the sandbox gateway starts all the same, but its start-up work reported failures", "error", err)
	}
	return gateway, sandboxes.NewLeaseService(sandboxes.LeaseConfig{
		Service:    svc,
		DB:         db,
		Secrets:    store,
		Audit:      emitter,
		GatewayURL: gateway.URL(),
		Boxes:      gateway,
		Metrics:    metrics,
	})
}

// runServer runs the HTTP server until its start function returns, then the
// sandbox shutdown, and returns what the server returned. The shutdown runs when
// the server failed as well, since a log.Fatal after it would skip the deferred
// closes of the databases and the shutdown with them.
func runServer(start func() error, shutdown func()) error {
	err := start()
	shutdown()
	return err
}
