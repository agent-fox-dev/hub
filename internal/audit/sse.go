package audit

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/txsvc/apikit"
)

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

	// broadcastChanSize is the capacity of the main broadcast channel.
	broadcastChanSize = 512
)

// connID is a unique identifier for each SSE connection.
type connID uint64

// sseConn represents a single SSE client connection.
type sseConn struct {
	id       connID
	ch       chan HubEvent
	filters  sseFilters
	lastRead time.Time
}

// sseFilters holds per-connection filter criteria for SSE events.
type sseFilters struct {
	workspace string
	runID     string
	category  string // "hub" or "agent"
}

// SSEOption configures the SSE connection manager.
type SSEOption func(*SSEManager)

// WithTickerFactory replaces the default time.NewTicker used for heartbeat
// and stale-connection reaping (18-REQ-8.9). This allows test-time clock
// injection.
func WithTickerFactory(fn func(d time.Duration) *time.Ticker) SSEOption {
	return func(m *SSEManager) {
		if fn != nil {
			m.tickerFactory = fn
		}
	}
}

// SSEManager is the concrete SSE connection manager (18-REQ-9.1).
// It maintains a broadcaster goroutine, per-connection goroutines,
// a heartbeat goroutine, and a mutex-protected connection registry.
type SSEManager struct {
	broadcast     chan HubEvent
	connections   map[connID]*sseConn
	mu            sync.Mutex
	maxConns      int
	tickerFactory func(d time.Duration) *time.Ticker
	nextID        atomic.Uint64
}

// NewSSEManager creates a new SSE connection manager with the given
// maximum connection limit and optional configuration (18-REQ-8.9).
// If maxConns is <= 0, it falls back to DefaultMaxConnections (18-REQ-9.E2).
func NewSSEManager(maxConns int, opts ...SSEOption) *SSEManager {
	if maxConns <= 0 {
		maxConns = DefaultMaxConnections
	}

	m := &SSEManager{
		broadcast:   make(chan HubEvent, broadcastChanSize),
		connections: make(map[connID]*sseConn),
		maxConns:    maxConns,
		tickerFactory: func(d time.Duration) *time.Ticker {
			return time.NewTicker(d)
		},
	}

	for _, opt := range opts {
		opt(m)
	}

	return m
}

// Register adds a new SSE client connection to the manager.
// Returns the sseConn if successful, or nil and an error if the connection
// limit has been reached (18-REQ-8.E1).
func (m *SSEManager) Register(filters sseFilters) (*sseConn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.connections) >= m.maxConns {
		return nil, errTooManyConnections
	}

	id := connID(m.nextID.Add(1))
	conn := &sseConn{
		id:       id,
		ch:       make(chan HubEvent, PerClientBufferSize),
		filters:  filters,
		lastRead: time.Now(),
	}
	m.connections[id] = conn
	return conn, nil
}

// Unregister removes a connection from the manager and closes its channel.
func (m *SSEManager) Unregister(id connID) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if conn, ok := m.connections[id]; ok {
		delete(m.connections, id)
		close(conn.ch)
	}
}

// ConnCount returns the number of currently active SSE connections.
func (m *SSEManager) ConnCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.connections)
}

// Broadcast publishes an event to the broadcast channel non-blocking.
// If the broadcast channel is full, the event is dropped and logged
// at debug level (18-REQ-9.E1, 18-PROP-8).
func (m *SSEManager) Broadcast(event HubEvent) {
	select {
	case m.broadcast <- event:
	default:
		slog.Debug("sse: broadcast channel full, dropping event",
			"event_type", event.EventType,
		)
	}
}

// Run starts the broadcaster goroutine that reads from the broadcast channel
// and fans out to all registered per-connection channels. It also starts
// the heartbeat goroutine and stale-connection reaper. Blocks until ctx is
// done. Typically invoked as go m.Run(ctx).
func (m *SSEManager) Run(done <-chan struct{}) {
	go m.heartbeatLoop(done)
	m.broadcasterLoop(done)
}

// broadcasterLoop reads events from the broadcast channel and fans out to
// all registered connections, respecting per-connection filters and
// backpressure (18-REQ-9.E3).
func (m *SSEManager) broadcasterLoop(done <-chan struct{}) {
	for {
		// Recover from panics in the broadcaster (18-REQ-9.E3).
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("sse: broadcaster panic recovered", "panic", r)
				}
			}()

			for {
				select {
				case <-done:
					return
				case event, ok := <-m.broadcast:
					if !ok {
						return
					}
					m.fanOut(event)
				}
			}
		}()

		// After panic recovery, check if we should stop.
		select {
		case <-done:
			return
		default:
			// Restart the broadcaster after panic recovery.
			slog.Info("sse: restarting broadcaster after panic recovery")
		}
	}
}

// fanOut sends an event to all registered connections that pass their filters.
// When a per-client channel is full, the oldest event is dropped to make room
// (18-REQ-8.5).
func (m *SSEManager) fanOut(event HubEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, conn := range m.connections {
		if !matchesFilters(event, conn.filters) {
			continue
		}

		select {
		case conn.ch <- event:
			// Delivered successfully.
		default:
			// Per-client channel is full — drop the oldest event to make room.
			select {
			case <-conn.ch:
				// Drained oldest event.
				slog.Debug("sse: per-client buffer full, dropped oldest event",
					"conn_id", conn.id,
					"event_type", event.EventType,
				)
			default:
			}
			// Now try to send again. If it still fails (shouldn't happen),
			// drop the new event.
			select {
			case conn.ch <- event:
			default:
				slog.Debug("sse: failed to deliver event after drain",
					"conn_id", conn.id,
					"event_type", event.EventType,
				)
			}
		}
	}
}

// heartbeatLoop ticks every HeartbeatInterval and:
// 1. Broadcasts a heartbeat event to all connections.
// 2. Reaps stale connections (lastRead > StaleConnectionTimeout).
func (m *SSEManager) heartbeatLoop(done <-chan struct{}) {
	ticker := m.tickerFactory(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			m.sendHeartbeat()
			m.reapStaleConnections()
		}
	}
}

// heartbeatEvent is a sentinel HubEvent used for heartbeat frames.
// SSE handlers identify heartbeats by checking EventType == "heartbeat".
func heartbeatEvent() HubEvent {
	return HubEvent{
		EventType: "heartbeat",
		Timestamp: apikit.NowUTC(),
	}
}

// sendHeartbeat broadcasts a heartbeat event to all connections.
func (m *SSEManager) sendHeartbeat() {
	hb := heartbeatEvent()

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, conn := range m.connections {
		select {
		case conn.ch <- hb:
		default:
			// Don't block on heartbeat delivery.
		}
	}
}

// reapStaleConnections removes connections that haven't read for longer
// than StaleConnectionTimeout (18-REQ-9.4).
func (m *SSEManager) reapStaleConnections() {
	m.mu.Lock()
	var stale []connID
	now := time.Now()
	for id, conn := range m.connections {
		if now.Sub(conn.lastRead) > StaleConnectionTimeout {
			stale = append(stale, id)
		}
	}
	m.mu.Unlock()

	for _, id := range stale {
		slog.Debug("sse: reaping stale connection", "conn_id", id)
		m.Unregister(id)
	}
}

// TouchLastRead updates the lastRead timestamp for a connection,
// preventing it from being reaped as stale.
func (m *SSEManager) TouchLastRead(id connID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if conn, ok := m.connections[id]; ok {
		conn.lastRead = time.Now()
	}
}

// matchesFilters checks whether an event passes the connection's filter
// criteria. Empty filter fields match all events.
func matchesFilters(event HubEvent, f sseFilters) bool {
	if f.workspace != "" && event.Workspace != f.workspace {
		return false
	}
	if f.category == "hub" && event.EventType != "" &&
		len(event.EventType) >= 4 && event.EventType[:4] != "hub." &&
		event.EventType != "heartbeat" {
		return false
	}
	if f.category == "agent" && event.EventType != "" &&
		len(event.EventType) >= 4 && event.EventType[:4] == "hub." {
		return false
	}
	// run_id filter: HubEvent doesn't have a RunID field, so this filter
	// only applies if we extend events later. For now, always passes.
	return true
}

// errTooManyConnections is returned when the SSE connection limit is reached.
var errTooManyConnections = errSSEMaxConnections("too many SSE connections")

type errSSEMaxConnections string

func (e errSSEMaxConnections) Error() string { return string(e) }
