package audit

import "time"

// Constants for SSE connection manager (18-REQ-9.4).
// HeartbeatInterval and StaleConnectionTimeout are hardcoded constants,
// NOT read from environment variables.
const (
	// HeartbeatInterval is the interval between SSE heartbeat frames.
	HeartbeatInterval = 30 * time.Second

	// StaleConnectionTimeout is the duration after which an SSE connection
	// with no reads is closed.
	StaleConnectionTimeout = 60 * time.Second

	// DefaultMaxConnections is the default maximum number of concurrent SSE
	// connections when AF_SSE_MAX_CONNECTIONS is absent or unparseable.
	DefaultMaxConnections = 100

	// PerClientBufferSize is the capacity of per-client event channels.
	PerClientBufferSize = 256
)

// SSEOption configures the SSE connection manager.
type SSEOption func(*SSEManager)

// WithTickerFactory replaces the default time.NewTicker used for heartbeat
// and stale-connection reaping (18-REQ-8.9). This allows test-time clock
// injection.
func WithTickerFactory(fn func(d time.Duration) *time.Ticker) SSEOption {
	return func(_ *SSEManager) {
		// Will be implemented in task group 5.
	}
}

// SSEManager is the concrete SSE connection manager (18-REQ-9.1).
// It maintains a broadcaster goroutine, per-connection goroutines,
// a heartbeat goroutine, and a mutex-protected connection registry.
type SSEManager struct {
	maxConns int
}

// NewSSEManager creates a new SSE connection manager with the given
// maximum connection limit and optional configuration (18-REQ-8.9).
// The constructor signature is NewSSEManager(maxConns int, opts ...SSEOption) *SSEManager.
func NewSSEManager(maxConns int, opts ...SSEOption) *SSEManager {
	panic("not implemented: NewSSEManager")
}

// Broadcast publishes an event to all registered SSE subscribers.
func (m *SSEManager) Broadcast(_ HubEvent) {
	panic("not implemented: SSEManager.Broadcast")
}

// ConnCount returns the number of currently active SSE connections.
func (m *SSEManager) ConnCount() int {
	panic("not implemented: SSEManager.ConnCount")
}
