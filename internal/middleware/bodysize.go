// Package middleware also provides body-size limiting middleware for af-hub.
//
// Implementation will be added in task group 14.
package middleware

import (
	"github.com/labstack/echo/v4"
)

// BodySizeLimitMiddleware returns Echo middleware that limits the request body
// size. When a request body exceeds the limit, it returns HTTP 413 immediately
// before auth or handler processing. The middleware is effectively a no-op for
// bodyless requests (GET, HEAD).
//
// The limit parameter follows Echo's size notation (e.g., "1M" for 1 megabyte).
//
// Stub: returns a pass-through middleware. Implementation in task group 14.
func BodySizeLimitMiddleware(limit string) echo.MiddlewareFunc {
	// Stub: pass-through. Implementation in task group 14.
	_ = limit
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			return next(c)
		}
	}
}
