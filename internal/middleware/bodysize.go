// Package middleware also provides body-size limiting middleware for af-hub.
package middleware

import (
	"github.com/labstack/echo/v4"
	echoMw "github.com/labstack/echo/v4/middleware"
)

// BodySizeLimitMiddleware returns Echo middleware that limits the request body
// size. When a request body exceeds the limit, it returns HTTP 413 immediately
// before auth or handler processing. The middleware is effectively a no-op for
// bodyless requests (GET, HEAD).
//
// The limit parameter follows Echo's size notation (e.g., "1M" for 1 megabyte).
//
// Delegates to Echo's built-in BodyLimit middleware, which produces an
// *echo.HTTPError with status 413 that flows through the custom error handler
// to produce the standard error envelope.
//
// REQ: 01-REQ-13.1, 01-REQ-13.2, 01-REQ-13.3
func BodySizeLimitMiddleware(limit string) echo.MiddlewareFunc {
	return echoMw.BodyLimit(limit)
}
