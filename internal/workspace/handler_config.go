package workspace

import (
	"database/sql"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// HandlerConfig holds the dependencies for workspace mutation handlers.
// This config struct enables audit emission injection without breaking
// existing handler signatures.
type HandlerConfig struct {
	// DB is the workspace database.
	DB *sql.DB

	// Audit is the optional audit event emitter. When non-nil, workspace
	// mutation handlers emit structured audit events. When nil, audit
	// emission is silently skipped.
	Audit audit.Emitter

	// AuditStore is the optional DuckDB-backed audit store used for
	// force-closing active sessions on workspace archive/delete.
	AuditStore audit.Store

	// AuditMetrics is the optional Prometheus metrics instance for
	// decrementing the afhub_agent_sessions_active gauge on force-close.
	AuditMetrics *audit.Metrics
}

// RegisterRoutesWithConfig mounts workspace API handlers on the given echo
// group using the provided HandlerConfig. This is the audit-aware variant
// of RegisterRoutes that threads audit dependencies to workspace lifecycle
// handlers.
func RegisterRoutesWithConfig(api *echo.Group, cfg HandlerConfig) error {
	// Thread audit dependencies to package-level vars so archive/delete
	// handlers can call force-close without changing their constructor
	// signatures.
	defaultAuditStore = cfg.AuditStore
	defaultAuditEmitter = cfg.Audit
	defaultAuditMetrics = cfg.AuditMetrics

	return RegisterRoutes(api, cfg.DB)
}
