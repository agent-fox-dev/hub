package gitserver

import "github.com/agent-fox-dev/hub/internal/audit"

// GitServerConfig holds optional configuration for the git server,
// including an audit emitter for post-push event emission.
type GitServerConfig struct {
	// Audit is the optional audit event emitter. When non-nil, the
	// post-push hook emits a hub.git.push event with metadata containing
	// head_sha and refs_updated. When nil, audit emission is silently
	// skipped without panicking.
	Audit audit.Emitter
}
