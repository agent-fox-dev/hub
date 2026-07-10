// Package middleware provides HTTP middleware for af-hub, including
// the request logger with request ID generation and propagation.
//
// Implementation will be added in task group 9.
package middleware

import (
	"github.com/labstack/echo/v4"
)

// RequestIDContextKey is the Echo context key under which the request ID
// string is stored. Distinct from authctx.AuthContextKey.
const RequestIDContextKey = "request_id"

// RequestLoggerMiddleware returns Echo middleware that:
//  1. Validates or generates a request ID (X-Request-ID header).
//  2. Attaches the request ID to the Echo context.
//  3. Sets the X-Request-ID response header.
//  4. Logs a structured JSON entry after the handler returns with fields:
//     method (string), path (string), status (int), duration_ms (float),
//     request_id (UUID string).
//
// X-Request-ID validation rules (REQ-6.1):
//   - Must be non-empty
//   - Must consist entirely of printable ASCII characters (0x20-0x7E)
//   - Must be at most 128 characters
//   - If any check fails, the value is silently discarded and a fresh UUID v4
//     is generated
//
// Implementation will be added in task group 9.
func RequestLoggerMiddleware() echo.MiddlewareFunc {
	// Stub: returns a pass-through middleware that does nothing.
	// Implementation in task group 9.
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			return next(c)
		}
	}
}
