// Package middleware also provides graceful shutdown coordination.
//
// Implementation will be added in task group 13.
package middleware

import (
	"context"
	"time"

	"github.com/labstack/echo/v4"
)

// ShutdownTimeout is the maximum duration to wait for in-flight requests
// to complete before forcing shutdown.
const ShutdownTimeout = 15 * time.Second

// GracefulShutdown stops the Echo server gracefully, draining in-flight
// requests via e.Shutdown with the configured timeout.
//
// Returns:
//   - nil if all in-flight requests completed before the deadline
//   - context.DeadlineExceeded if the timeout expired before all requests drained
//
// The caller is responsible for logging the outcome and exiting the process.
//
// Implementation will be added in task group 13.
func GracefulShutdown(e *echo.Echo, timeout time.Duration) error {
	// Stub: returns nil. Implementation in task group 13.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = ctx
	return nil
}
