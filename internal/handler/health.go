// Package handler provides HTTP route handlers for af-hub.
// This file implements the health probe endpoints /healthz and /readyz.
//
// Implementation will be added in task group 10.
package handler

import (
	"database/sql"
	"sync/atomic"

	"github.com/labstack/echo/v4"
)

// readyzFailureCounter tracks consecutive readyz DB check failures.
// Accessed exclusively via sync/atomic for goroutine safety.
// Exported for testing (via accessor functions) only.
var readyzFailureCounter int64

// HealthzHandler returns the liveness probe handler for GET /healthz.
// Always returns HTTP 200 with {"status": "ok"} without any DB check.
func HealthzHandler() echo.HandlerFunc {
	// Stub: returns nil handler.
	// Implementation will be added in task group 10.
	return nil
}

// ReadyzHandler returns the readiness probe handler for GET /readyz.
// Executes SELECT 1 against the DB with a 2-second timeout.
// On success: HTTP 200 with {"status": "ready"}.
// On failure: HTTP 503 with {"status": "not ready"}.
//
// Uses the package-level readyzFailureCounter (sync/atomic) for
// logging cadence: first failure at error, subsequent at debug,
// recovery at info.
func ReadyzHandler(db *sql.DB) echo.HandlerFunc {
	// Stub: returns nil handler.
	// Implementation will be added in task group 10.
	return nil
}

// GetReadyzFailureCounter returns the current value of the readyz
// failure counter. Exported for test assertions.
func GetReadyzFailureCounter() int64 {
	return atomic.LoadInt64(&readyzFailureCounter)
}

// ResetReadyzFailureCounter resets the readyz failure counter to 0.
// Exported for test setup/teardown.
func ResetReadyzFailureCounter() {
	atomic.StoreInt64(&readyzFailureCounter, 0)
}
