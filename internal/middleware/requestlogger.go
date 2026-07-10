// Package middleware provides HTTP middleware for af-hub, including
// the request logger with request ID generation and propagation.
package middleware

import (
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// RequestIDContextKey is the Echo context key under which the request ID
// string is stored. Distinct from authctx.AuthContextKey.
const RequestIDContextKey = "request_id"

// requestIDMaxLen is the maximum allowed length for a client-provided
// X-Request-ID header value.
const requestIDMaxLen = 128

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
func RequestLoggerMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()

			// Resolve the request ID: validate the client-provided value
			// or generate a fresh UUID v4.
			requestID := resolveRequestID(c.Request().Header.Get("X-Request-ID"))

			// Attach the request ID to the Echo context.
			c.Set(RequestIDContextKey, requestID)

			// Set the X-Request-ID response header before the handler runs,
			// so it is present even if the handler panics (Recover middleware
			// will still send the response with this header).
			c.Response().Header().Set("X-Request-ID", requestID)

			// Invoke the next handler in the chain.
			err := next(c)

			// Determine the response status for logging. If the response
			// has been committed (handler called c.JSON/c.String/etc.),
			// use the written status. Otherwise, derive it from the error
			// (Echo's error handler runs after middleware returns, so the
			// status may not be written yet).
			status := c.Response().Status
			if err != nil && !c.Response().Committed {
				if he, ok := err.(*echo.HTTPError); ok {
					status = he.Code
				} else {
					status = 500
				}
			}

			// Log the structured request entry after the handler returns.
			duration := time.Since(start)
			logrus.WithFields(logrus.Fields{
				"method":     c.Request().Method,
				"path":       c.Request().URL.Path,
				"status":     status,
				"duration_ms": float64(duration.Nanoseconds()) / 1e6,
				"request_id": requestID,
			}).Info("request completed")

			return err
		}
	}
}

// resolveRequestID validates a client-provided X-Request-ID header value.
// If the value passes all validation checks, it is returned as-is.
// Otherwise, a fresh UUID v4 is generated.
//
// Validation rules (REQ-6.1):
//   - Must be non-empty
//   - Must be at most 128 characters
//   - Must consist entirely of printable ASCII characters (bytes 0x20-0x7E inclusive)
func resolveRequestID(clientID string) string {
	if isValidRequestID(clientID) {
		return clientID
	}
	return uuid.New().String()
}

// isValidRequestID checks whether a client-provided X-Request-ID value
// meets the validation criteria.
func isValidRequestID(id string) bool {
	if len(id) == 0 || len(id) > requestIDMaxLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		if b < 0x20 || b > 0x7E {
			return false
		}
	}
	return true
}
