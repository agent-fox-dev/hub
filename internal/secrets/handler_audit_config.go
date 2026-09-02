package secrets

import (
	"database/sql"
	"log/slog"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// defaultAuditEmitter is the package-level audit emitter set during route
// registration. When nil, audit emission is silently skipped in all CRUD
// handlers (18-REQ-4.E2, 18-PROP-7).
var defaultAuditEmitter audit.Emitter

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
// audit-aware variant of RegisterRoutes (18-REQ-4.7).
func RegisterRoutesWithAudit(api *echo.Group, cfg SecretsRouteConfig) error {
	defaultAuditEmitter = cfg.Audit
	return RegisterRoutes(api, cfg.DB)
}

// emitSecretAudit emits a hub-internal audit event for a secret mutation.
// If the emitter is nil, emission is silently skipped (18-REQ-4.E2).
// If Emit returns an error, the error is logged and the caller is
// unaffected (18-REQ-4.E1, 18-PROP-1).
func emitSecretAudit(c echo.Context, eventType, scope, key string) {
	if defaultAuditEmitter == nil {
		return
	}
	auth := apikit.GetAuthInfo(c)
	event := audit.HubEvent{
		EventType:    eventType,
		ResourceType: "secret",
		Action:       eventType[len("hub.secret."):],
		Metadata: map[string]any{
			"scope": scope,
			"key":   key,
		},
	}
	if auth != nil {
		event.ActorID = auth.UserID
		event.ActorType = auth.CredentialType
	}
	if err := defaultAuditEmitter.Emit(c.Request().Context(), event); err != nil {
		slog.Error("audit: failed to emit "+eventType,
			"scope", scope,
			"key", key,
			"error", err,
		)
	}
}

// emitVarAudit emits a hub-internal audit event for a variable mutation.
// If the emitter is nil, emission is silently skipped (18-REQ-4.E2).
// If Emit returns an error, the error is logged and the caller is
// unaffected (18-REQ-4.E1, 18-PROP-1).
func emitVarAudit(c echo.Context, eventType, scope, key string) {
	if defaultAuditEmitter == nil {
		return
	}
	auth := apikit.GetAuthInfo(c)
	event := audit.HubEvent{
		EventType:    eventType,
		ResourceType: "variable",
		Action:       eventType[len("hub.variable."):],
		Metadata: map[string]any{
			"scope": scope,
			"key":   key,
		},
	}
	if auth != nil {
		event.ActorID = auth.UserID
		event.ActorType = auth.CredentialType
	}
	if err := defaultAuditEmitter.Emit(c.Request().Context(), event); err != nil {
		slog.Error("audit: failed to emit "+eventType,
			"scope", scope,
			"key", key,
			"error", err,
		)
	}
}
