package secrets

import (
	"database/sql"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// SecretsRouteConfig holds the dependencies for secrets and variables
// handlers, including an optional audit emitter for mutation event emission.
type SecretsRouteConfig struct {
	// DB is the secrets/variables database.
	DB *sql.DB

	// Audit is the optional audit event emitter. When non-nil, secret
	// and variable mutation handlers emit structured audit events.
	// When nil, audit emission is silently skipped.
	Audit audit.Emitter
}

// RegisterRoutesWithAudit mounts secrets and variables API handlers on the
// given echo group using the provided SecretsRouteConfig. This is the
// audit-aware variant of RegisterRoutes.
func RegisterRoutesWithAudit(api *echo.Group, cfg SecretsRouteConfig) error {
	// TODO(spec-18): Thread cfg.Audit to individual handlers for emission.
	return RegisterRoutes(api, cfg.DB)
}
