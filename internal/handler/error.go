// Package handler also provides the custom error handler for af-hub.
//
// CustomErrorHandler translates errors on non-health-probe routes into the
// standard JSON error envelope: {"error": {"code": <int>, "message": "<string>"}}.
//
// Implementation will be added in task group 13.
package handler

import (
	"github.com/labstack/echo/v4"
)

// CustomErrorHandler is the global error handler assigned to e.HTTPErrorHandler.
// It produces a JSON error envelope for all errors on non-health-probe routes.
//
// Behavior:
//   - For *echo.HTTPError: uses Code and Message for the envelope.
//   - For other errors: HTTP 500 with "internal server error".
//   - For health probe routes (/healthz, /readyz): does not apply the envelope;
//     those routes handle their own response format.
//
// Stub: implementation will be added in task group 13.
func CustomErrorHandler(err error, c echo.Context) {
	// Stub: does nothing. The default Echo error handler will be used instead.
	// Implementation in task group 13.
	_ = err
	_ = c
}
