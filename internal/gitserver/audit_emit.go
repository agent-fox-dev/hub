package gitserver

import (
	"log/slog"

	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// emitGitPushAudit emits a hub.git.push audit event after a successful
// receive-pack operation. If the package-level emitter is nil, emission
// is silently skipped (18-REQ-5.3, 18-PROP-7). If Emit returns an error,
// the error is logged and the push response is unaffected (18-REQ-5.E1,
// 18-PROP-1).
func emitGitPushAudit(c echo.Context, slug string, commands []*packp.Command) {
	if defaultAuditEmitter == nil {
		return
	}

	// Extract head SHA from the last non-delete command and collect all refs.
	var headSHA string
	refsUpdated := make([]string, 0, len(commands))
	for _, cmd := range commands {
		refName := string(cmd.Name)
		refsUpdated = append(refsUpdated, refName)
		if cmd.New.IsZero() {
			continue
		}
		headSHA = cmd.New.String()
	}

	event := audit.HubEvent{
		EventType:    "hub.git.push",
		ResourceType: "workspace",
		Action:       "push",
		Workspace:    slug,
		Metadata: map[string]any{
			"head_sha":     headSHA,
			"refs_updated": refsUpdated,
		},
	}

	auth := apikit.GetAuthInfo(c)
	if auth != nil {
		event.ActorID = auth.UserID
		event.ActorType = auth.CredentialType
	}

	if err := defaultAuditEmitter.Emit(c.Request().Context(), event); err != nil {
		slog.Error("audit: failed to emit hub.git.push",
			"workspace", slug,
			"error", err,
		)
	}
}
