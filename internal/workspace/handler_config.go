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
}

// RegisterRoutesWithConfig mounts workspace API handlers on the given echo
// group using the provided HandlerConfig. This is the audit-aware variant
// of RegisterRoutes.
func RegisterRoutesWithConfig(api *echo.Group, cfg HandlerConfig) error {
	// TODO(spec-18): Thread cfg.Audit to individual handlers for emission.
	return RegisterRoutes(api, cfg.DB)
}
