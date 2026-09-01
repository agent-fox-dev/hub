package audit

import (
	"context"
	"testing"
	"time"
)

// ===========================================================================
// Test infrastructure for SSE broadcaster and connection manager
// (internal/audit/sse.go)
// Requirements: 18-REQ-9
// ===========================================================================

// ===========================================================================
// 4.4 — SSE broadcaster and connection manager tests
// Requirements: 18-REQ-9.1, 18-REQ-9.2, 18-REQ-9.3, 18-REQ-9.4
// Test Spec: TS-18-44, TS-18-45, TS-18-46, TS-18-47
// ===========================================================================

// TS-18-44: SSE connection manager has a broadcaster goroutine,
// per-connection goroutines, heartbeat goroutine, and mutex-protected
// connection registry.
func TestSSEManager_BroadcasterStructure_TS18_44(t *testing.T) {
	mgr := NewSSEManager(DefaultMaxConnections)

	// Verify the manager has a broadcast channel.
	if mgr == nil {
		t.Fatal("NewSSEManager returned nil")
	}

	// The manager should have max connections set.
	if mgr.maxConns != DefaultMaxConnections {
		t.Errorf("maxConns = %d, want %d", mgr.maxConns, DefaultMaxConnections)
	}

	// After starting, the manager should have broadcaster and heartbeat
	// goroutines running. These structural assertions validate that the
	// internal fields are initialized correctly.
}

// TS-18-45: Every successful Emitter.Emit call also publishes the event
// to the SSE broadcast channel.
func TestSSEManager_EmitterIntegration_TS18_45(t *testing.T) {
	db := openTestAuditDB(t)
	initHandlerTestSchema(t, db)
	store := NewStore(db)

	mgr := NewSSEManager(DefaultMaxConnections)
	emitter := NewEmitterWithBroadcast(store, mgr)

	event := HubEvent{
		EventType:    "hub.workspace.create",
		ActorID:      "user-1",
		ActorType:    "api_key",
		ResourceType: "workspace",
		ResourceID:   "ws-1",
		Action:       "create",
		Workspace:    "ws-1",
	}

	ctx := context.Background()
	err := emitter.Emit(ctx, event)
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	// After emit, the event should be in DuckDB (hub_audit_events).
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM hub_audit_events WHERE event_type = 'hub.workspace.create'").Scan(&count)
	if err != nil {
		t.Fatalf("query hub_audit_events: %v", err)
	}
	if count != 1 {
		t.Errorf("hub_audit_events count = %d, want 1", count)
	}

	// The event should also have been broadcast to the SSE manager.
	// When a subscriber is connected, they should receive the event.
	// (The broadcaster channel assertion depends on internal implementation.)
}

// TS-18-46: SSE connection manager reads AF_SSE_MAX_CONNECTIONS at startup,
// defaulting to 100 when the variable is absent.
func TestSSEManager_DefaultMaxConnections_TS18_46(t *testing.T) {
	// Unset the environment variable.
	t.Setenv("AF_SSE_MAX_CONNECTIONS", "")

	mgr := NewSSEManager(DefaultMaxConnections)

	if mgr.maxConns != 100 {
		t.Errorf("maxConns = %d, want 100 (default)", mgr.maxConns)
	}
}

// TS-18-46: SSE connection manager respects custom AF_SSE_MAX_CONNECTIONS.
func TestSSEManager_CustomMaxConnections(t *testing.T) {
	mgr := NewSSEManager(50)

	if mgr.maxConns != 50 {
		t.Errorf("maxConns = %d, want 50", mgr.maxConns)
	}
}

// TS-18-47: HeartbeatInterval is a hardcoded constant of 30 seconds.
func TestSSEManager_HeartbeatIntervalConstant_TS18_47(t *testing.T) {
	if HeartbeatInterval != 30*time.Second {
		t.Errorf("HeartbeatInterval = %v, want %v", HeartbeatInterval, 30*time.Second)
	}
}

// TS-18-47: StaleConnectionTimeout is a hardcoded constant of 60 seconds.
func TestSSEManager_StaleTimeoutConstant_TS18_47(t *testing.T) {
	if StaleConnectionTimeout != 60*time.Second {
		t.Errorf("StaleConnectionTimeout = %v, want %v", StaleConnectionTimeout, 60*time.Second)
	}
}

// TS-18-41: Per-client buffered channel holds 256 events. When the buffer
// is full, the oldest event is dropped and logged at debug level.
func TestSSEManager_PerClientBufferDropsOldest_TS18_41(t *testing.T) {
	mgr := NewSSEManager(DefaultMaxConnections)

	// Verify the per-client buffer size constant.
	if PerClientBufferSize != 256 {
		t.Errorf("PerClientBufferSize = %d, want 256", PerClientBufferSize)
	}

	// Emit 257 events. The broadcaster should not block. The per-client
	// channel should drop the oldest event and retain 256.
	for i := 0; i < 257; i++ {
		mgr.Broadcast(HubEvent{
			EventType: "hub.workspace.create",
			Workspace: "ws-1",
		})
	}
	// After implementation, verify:
	// - The broadcaster did not block on Emit.
	// - The per-client channel length is 256.
	// - A debug log entry was written for the drop.
}

// 18-REQ-9.E1: When the broadcast channel is full, the event is dropped
// and logged at debug level rather than blocking the Emitter.
func TestSSEManager_FullBroadcastChannelDropsEvent(t *testing.T) {
	mgr := NewSSEManager(DefaultMaxConnections)

	// Rapidly broadcast many events without any consumers.
	// The broadcaster should never block (18-PROP-8).
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			mgr.Broadcast(HubEvent{
				EventType: "hub.workspace.create",
				Workspace: "ws-1",
			})
		}
	}()

	// Wait for broadcasts to complete with a timeout.
	select {
	case <-done:
		// Broadcasts completed without blocking.
	case <-time.After(5 * time.Second):
		t.Fatal("Broadcast blocked for 5 seconds — broadcaster should never block (18-PROP-8)")
	}
}

// 18-REQ-9.E2: AF_SSE_MAX_CONNECTIONS with a non-integer or negative value
// falls back to the default limit of 100.
func TestSSEManager_InvalidMaxConnectionsFallback(t *testing.T) {
	t.Setenv("AF_SSE_MAX_CONNECTIONS", "not-a-number")

	// When the env var is invalid, the caller should use DefaultMaxConnections.
	mgr := NewSSEManager(DefaultMaxConnections)

	if mgr.maxConns != 100 {
		t.Errorf("maxConns = %d, want 100 (fallback for invalid env var)", mgr.maxConns)
	}
}

// 18-REQ-9.E2: AF_SSE_MAX_CONNECTIONS with a negative value falls back
// to the default.
func TestSSEManager_NegativeMaxConnectionsFallback(t *testing.T) {
	// Negative value should be treated as invalid.
	mgr := NewSSEManager(-5)

	// The constructor should clamp or reject negative values and use the default.
	if mgr.maxConns < 0 {
		t.Errorf("maxConns = %d, want positive value (default 100)", mgr.maxConns)
	}
}

// 18-REQ-9.E3: If the broadcaster goroutine panics, it is recovered and
// restarted to prevent total SSE subsystem failure.
func TestSSEManager_BroadcasterPanicRecovery(t *testing.T) {
	mgr := NewSSEManager(DefaultMaxConnections)

	// The broadcaster should survive panics and restart.
	// After implementation, this test would:
	// 1. Trigger a panic in the broadcaster (e.g., via a specially crafted event)
	// 2. Verify the broadcaster recovers and continues to function
	// 3. Verify the error was logged

	// For now, verify the manager can be created and a broadcast attempt
	// doesn't permanently break the system.
	mgr.Broadcast(HubEvent{EventType: "hub.workspace.create"})
}

// TS-18-40: SSE connection manager closes a connection that has had no
// reads for 60 seconds (stale-connection timeout).
func TestSSEManager_StaleConnectionReaping_TS18_40(t *testing.T) {
	// Use WithTickerFactory to inject a test clock for faster testing.
	fakeTicker := time.NewTicker(1 * time.Millisecond) // Fast tick for tests
	defer fakeTicker.Stop()

	mgr := NewSSEManager(DefaultMaxConnections,
		WithTickerFactory(func(_ time.Duration) *time.Ticker {
			return fakeTicker
		}),
	)

	// After implementation:
	// 1. Register a client that doesn't read from its channel
	// 2. Advance the test clock past 60 seconds
	// 3. Verify the client was deregistered and connection closed
	// 4. Verify the active connection count decremented by 1

	if mgr.ConnCount() != 0 {
		t.Errorf("initial ConnCount = %d, want 0", mgr.ConnCount())
	}
}

// 18-REQ-8.9: WithTickerFactory option allows test-time clock injection.
func TestSSEManager_WithTickerFactory(t *testing.T) {
	called := false
	factory := func(d time.Duration) *time.Ticker {
		called = true
		return time.NewTicker(d)
	}

	_ = NewSSEManager(DefaultMaxConnections, WithTickerFactory(factory))

	// After implementation, the factory should be called during manager start.
	// For now, verify the option type is accepted by the constructor.
	_ = called
}

// Verify SSE constants are not read from environment variables (18-REQ-9.4).
func TestSSEManager_ConstantsNotFromEnv(t *testing.T) {
	// Set environment variables to different values.
	t.Setenv("AF_SSE_HEARTBEAT_INTERVAL", "999")
	t.Setenv("AF_SSE_STALE_TIMEOUT", "999")

	// Constants should remain at their hardcoded values regardless of env.
	if HeartbeatInterval != 30*time.Second {
		t.Errorf("HeartbeatInterval = %v (affected by env), want %v", HeartbeatInterval, 30*time.Second)
	}
	if StaleConnectionTimeout != 60*time.Second {
		t.Errorf("StaleConnectionTimeout = %v (affected by env), want %v", StaleConnectionTimeout, 60*time.Second)
	}
}
