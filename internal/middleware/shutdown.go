// Package middleware also provides graceful shutdown coordination.
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
// Delegates in-flight tracking entirely to Go's net/http connection state
// machine without maintaining a separate counter (REQ-7.1).
//
// Returns:
//   - nil if all in-flight requests completed before the deadline
//   - context.DeadlineExceeded if the timeout expired before all requests drained
//
// The caller is responsible for logging the outcome and exiting the process:
//   - nil → logrus.Info("server shutdown complete")
//   - DeadlineExceeded → logrus.Warn("graceful shutdown timed out; some connections may have been dropped")
//
// REQ: 01-REQ-7.1, 01-REQ-7.2, 01-REQ-7.3, 01-REQ-7.E1
func GracefulShutdown(e *echo.Echo, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return e.Shutdown(ctx)
}
