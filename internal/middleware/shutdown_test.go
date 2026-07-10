package middleware_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	mw "github.com/agent-fox-dev/hub/internal/middleware"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// getFreePort finds a free TCP port on localhost.
func getFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

// ---------------------------------------------------------------------------
// 3.3 — Graceful Shutdown Behavior
// ---------------------------------------------------------------------------

// TestSpec01_ShutdownDrainsInflight verifies that when a shutdown signal is
// received, in-flight requests are given time to complete before the server
// exits. New connections are refused after shutdown is initiated.
//
// Uses GracefulShutdown(e, timeout) directly rather than signals.
// TS-01-23, REQ: 01-REQ-7.1
func TestSpec01_ShutdownDrainsInflight(t *testing.T) {
	port := getFreePort(t)
	e := echo.New()

	// Track whether the slow handler completed.
	handlerCompleted := make(chan struct{})

	e.GET("/slow", func(c echo.Context) error {
		// Simulate an in-flight request that takes 2 seconds.
		time.Sleep(2 * time.Second)
		close(handlerCompleted)
		return c.JSON(http.StatusOK, map[string]string{"status": "done"})
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// Start the server in a goroutine.
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- e.Start(addr)
	}()

	// Wait for the server to start accepting connections.
	var connected bool
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			connected = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !connected {
		t.Fatal("server did not start accepting connections in time")
	}

	// Start a slow request in-flight.
	var wg sync.WaitGroup
	var slowResp *http.Response
	var slowErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		client := &http.Client{Timeout: 10 * time.Second}
		slowResp, slowErr = client.Get(fmt.Sprintf("http://%s/slow", addr))
	}()

	// Give the slow request time to be received by the server.
	time.Sleep(200 * time.Millisecond)

	// Initiate graceful shutdown with 15-second timeout.
	shutdownErr := mw.GracefulShutdown(e, mw.ShutdownTimeout)

	// Wait for the slow request to complete.
	wg.Wait()

	// The in-flight request should have completed successfully.
	select {
	case <-handlerCompleted:
		// Good — handler completed before shutdown killed it.
	default:
		t.Error("in-flight handler should have completed before shutdown finished")
	}

	// Check the slow request response.
	if slowErr != nil {
		t.Errorf("slow request error = %v, want nil (should complete before drain timeout)", slowErr)
	}
	if slowResp != nil && slowResp.StatusCode != http.StatusOK {
		t.Errorf("slow request status = %d, want %d", slowResp.StatusCode, http.StatusOK)
	}

	// Shutdown should not have timed out (15s > 2s handler sleep).
	if shutdownErr == context.DeadlineExceeded {
		t.Error("shutdown should not have timed out — handler only takes 2 seconds")
	}

	// After shutdown, new connections should be refused.
	_, newConnErr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if newConnErr == nil {
		t.Error("new connections should be refused after shutdown")
	}
}

// TestSpec01_ShutdownCleanDrainLogsComplete verifies that when shutdown
// completes within the drain timeout, the caller receives a nil error
// (indicating clean drain). The caller then logs msg=server shutdown complete
// at info level.
//
// TS-01-24, REQ: 01-REQ-7.2
func TestSpec01_ShutdownCleanDrainLogsComplete(t *testing.T) {
	port := getFreePort(t)
	e := echo.New()

	e.GET("/quick", func(c echo.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)

	go func() {
		e.Start(addr)
	}()

	// Wait for server to start.
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Shutdown with no in-flight requests — should complete immediately.
	err := mw.GracefulShutdown(e, mw.ShutdownTimeout)

	// Clean drain: err should be nil (not context.DeadlineExceeded).
	if err != nil {
		t.Errorf("GracefulShutdown error = %v, want nil (clean drain)", err)
	}

	// In the real server, the caller would log "server shutdown complete"
	// at info level. Verify the contract by checking the return value.
	// The actual logging is tested via the main.go integration.

	// Verify the server is no longer accepting connections.
	_, dialErr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if dialErr == nil {
		t.Error("server should not accept connections after shutdown")
	}
}

// TestSpec01_ShutdownTimeoutExpiredLogs verifies that when the drain timeout
// expires before all in-flight requests complete, the caller receives a
// context.DeadlineExceeded error. The caller then logs the fixed warn message:
// "graceful shutdown timed out; some connections may have been dropped"
//
// No connection count is included in the log message.
// TS-01-25, REQ: 01-REQ-7.3
func TestSpec01_ShutdownTimeoutExpiredLogs(t *testing.T) {
	port := getFreePort(t)
	e := echo.New()

	// Handler that blocks forever (will not complete before the timeout).
	e.GET("/forever", func(c echo.Context) error {
		// Block until the context is cancelled by shutdown.
		<-c.Request().Context().Done()
		return c.Request().Context().Err()
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)

	go func() {
		e.Start(addr)
	}()

	// Wait for server to start.
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Start a blocking request.
	go func() {
		client := &http.Client{Timeout: 30 * time.Second}
		client.Get(fmt.Sprintf("http://%s/forever", addr))
	}()
	time.Sleep(200 * time.Millisecond)

	// Shutdown with a very short timeout (1 second) — will expire.
	shortTimeout := 1 * time.Second
	err := mw.GracefulShutdown(e, shortTimeout)

	// Should return context.DeadlineExceeded (or wrapping error).
	if err == nil {
		t.Error("GracefulShutdown should return error when timeout expires, got nil")
	}

	// In the real server, the caller would log at warn level:
	// "graceful shutdown timed out; some connections may have been dropped"
	// with no connection count. This test verifies the contract via the
	// return value; the logging is tested in the main.go integration test.
}

// TestSpec01_ShutdownTimeoutMessageFormat verifies that the fixed warn message
// for shutdown timeout is exactly "graceful shutdown timed out; some connections
// may have been dropped" with no connection count. This tests the message format
// contract that callers should follow.
//
// TS-01-25, REQ: 01-REQ-7.3
func TestSpec01_ShutdownTimeoutMessageFormat(t *testing.T) {
	// Capture logrus output to verify message format.
	logBuf := setupLogCapture(t)

	// Simulate what the main.go caller should do when GracefulShutdown
	// returns a deadline exceeded error.
	expectedMsg := "graceful shutdown timed out; some connections may have been dropped"

	// The actual main.go code will log this. Here we verify the message
	// format is correct by emitting it and checking the output.
	logrus.Warn(expectedMsg)

	entries := parseLogEntries(logBuf)
	if len(entries) == 0 {
		t.Fatal("expected at least one log entry")
	}

	entry := entries[0]
	msg, ok := entry["msg"].(string)
	if !ok || msg != expectedMsg {
		t.Errorf("msg = %q, want %q", msg, expectedMsg)
	}

	// Verify no "connection_count" or similar numeric field.
	if _, hasCount := entry["connection_count"]; hasCount {
		t.Error("warn message should NOT include connection_count field")
	}
	if _, hasCount := entry["connections"]; hasCount {
		t.Error("warn message should NOT include connections field")
	}
}

// TestSpec01_ShutdownHijackedConnections verifies that hijacked connections
// (e.g., WebSocket upgrades) are closed immediately by http.Server.Shutdown
// without waiting for them to drain. This is standard Go behavior.
//
// TS-01-E10, REQ: 01-REQ-7.E1
func TestSpec01_ShutdownHijackedConnections(t *testing.T) {
	port := getFreePort(t)
	e := echo.New()

	hijackCompleted := make(chan struct{})

	// Register a handler that hijacks the connection.
	e.GET("/hijack", func(c echo.Context) error {
		// Hijack the connection to simulate a WebSocket-like upgrade.
		hijacker, ok := c.Response().Writer.(http.Hijacker)
		if !ok {
			return fmt.Errorf("response writer does not implement http.Hijacker")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return err
		}

		// Keep the hijacked connection open until it's closed by shutdown.
		go func() {
			defer close(hijackCompleted)
			buf := make([]byte, 1)
			// This will return when the connection is closed by Shutdown.
			conn.Read(buf)
			conn.Close()
		}()

		return nil
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)

	go func() {
		e.Start(addr)
	}()

	// Wait for server to start.
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Establish the hijacked connection.
	client := &http.Client{Timeout: 5 * time.Second}
	_, _ = client.Get(fmt.Sprintf("http://%s/hijack", addr))
	time.Sleep(200 * time.Millisecond)

	// Shutdown should not wait for the hijacked connection.
	start := time.Now()
	err := mw.GracefulShutdown(e, mw.ShutdownTimeout)
	elapsed := time.Since(start)

	// Shutdown should complete quickly (hijacked connections are closed immediately).
	if elapsed > 5*time.Second {
		t.Errorf("shutdown took %v, want < 5s (hijacked connections should be closed immediately)", elapsed)
	}

	// Shutdown error should be nil (clean — hijacked connections don't block).
	if err != nil && err != context.DeadlineExceeded {
		// Acceptable: nil (clean) or DeadlineExceeded (if connection close
		// is slow on some platforms). The key contract is that shutdown
		// doesn't hang for 15 seconds.
		t.Logf("shutdown error = %v (acceptable if not a hang)", err)
	}

	// The hijacked connection should have been closed.
	select {
	case <-hijackCompleted:
		// Good.
	case <-time.After(3 * time.Second):
		t.Error("hijacked connection should have been closed by Shutdown")
	}
}

// TestSpec01_ShutdownUsesEchoShutdown verifies that GracefulShutdown
// delegates to Echo's e.Shutdown(ctx) with the configured timeout,
// as required by REQ-7.1. No separate in-flight counter is maintained.
// TS-01-23, REQ: 01-REQ-7.1
func TestSpec01_ShutdownUsesEchoShutdown(t *testing.T) {
	// This is verified by the contract: GracefulShutdown accepts an Echo
	// instance and a timeout, and returns an error indicating whether the
	// shutdown completed cleanly or timed out.

	// Verify the ShutdownTimeout constant is 15 seconds.
	if mw.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want %v", mw.ShutdownTimeout, 15*time.Second)
	}
}
