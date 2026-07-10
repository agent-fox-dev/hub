// Package handler provides HTTP route handlers for af-hub.
// This file implements the health probe endpoints /healthz and /readyz.
package handler

import (
	"context"
	"database/sql"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// readyzFailureCounter tracks consecutive readyz DB check failures.
// Accessed exclusively via sync/atomic for goroutine safety.
var readyzFailureCounter int64

// readyzTimeout is the maximum time allowed for the SELECT 1 DB check.
const readyzTimeout = 2 * time.Second

// HealthzHandler returns the liveness probe handler for GET /healthz.
// Always returns HTTP 200 with {"status": "ok"} without any DB check.
func HealthzHandler() echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	}
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
	return func(c echo.Context) error {
		// Create a context with 2-second timeout for the DB check.
		ctx, cancel := context.WithTimeout(c.Request().Context(), readyzTimeout)
		defer cancel()

		// Execute SELECT 1 to verify DB liveness.
		var one int
		err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one)

		if err != nil {
			// DB check failed. Increment failure counter atomically.
			newCount := atomic.AddInt64(&readyzFailureCounter, 1)

			if newCount == 1 {
				// First consecutive failure: log at error level.
				logrus.WithField("error", err.Error()).Error("readyz DB check failed")
			} else {
				// Subsequent consecutive failures: log at debug level only.
				logrus.WithField("error", err.Error()).Debug("readyz DB check failed")
			}

			return c.JSON(http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		}

		// DB check succeeded. Check if recovering from degraded state.
		previousCount := atomic.LoadInt64(&readyzFailureCounter)
		if previousCount > 0 {
			// Recovery: emit a single info-level log entry and reset counter.
			logrus.Info("readyz DB check recovered after degraded period")
			atomic.StoreInt64(&readyzFailureCounter, 0)
		}

		return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
	}
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
