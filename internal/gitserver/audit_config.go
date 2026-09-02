package gitserver

import "github.com/agent-fox-dev/hub/internal/audit"

// defaultAuditEmitter is the package-level audit emitter set during server
// initialization. When nil, audit emission is silently skipped in the
// post-push hook (18-REQ-5.3, 18-PROP-7).
var defaultAuditEmitter audit.Emitter

// GitServerConfig holds optional configuration for the git server,
// including an audit emitter for post-push event emission.
type GitServerConfig struct {
	// Audit is the optional audit event emitter. When non-nil, the
	// post-push hook emits a hub.git.push event with metadata containing
	// head_sha and refs_updated. When nil, audit emission is silently
	// skipped without panicking.
	Audit audit.Emitter
}

// SetAuditEmitter sets the package-level audit emitter for git server
// operations. Called during server initialization with the emitter from
// GitServerConfig.Audit (18-REQ-5.2).
func SetAuditEmitter(emitter audit.Emitter) {
	defaultAuditEmitter = emitter
}
