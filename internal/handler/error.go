// Package handler also provides the custom error handler for af-hub.
//
// CustomErrorHandler translates errors on non-health-probe routes into the
// standard JSON error envelope: {"error": {"code": <int>, "message": "<string>"}}.
package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// CustomErrorHandler is the global error handler assigned to e.HTTPErrorHandler.
// It produces a JSON error envelope for all errors on non-health-probe routes.
//
// Behavior:
//   - For *echo.HTTPError: uses Code and Message for the envelope.
//   - For other errors: HTTP 500 with "internal server error".
//   - For health probe routes (/healthz, /readyz): delegates to Echo's default
//     error handler so those routes keep their own response format.
//
// REQ: 01-REQ-12.1, 01-REQ-12.2, 01-REQ-12.3, 01-REQ-12.4, 01-REQ-12.5
func CustomErrorHandler(err error, c echo.Context) {
	// Don't write if the response is already committed.
	if c.Response().Committed {
		return
	}

	// Health probe routes use their own response format, not the error
	// envelope. Delegate to Echo's default error handling for these paths.
	// HTTP 405 on health probes uses Echo's default behavior (REQ-12.5).
	path := c.Request().URL.Path
	if path == "/healthz" || path == "/readyz" {
		c.Echo().DefaultHTTPErrorHandler(err, c)
		return
	}

	// Determine the HTTP status code and message for the envelope.
	code := http.StatusInternalServerError
	message := "internal server error"

	if he, ok := err.(*echo.HTTPError); ok {
		code = he.Code
		if msg, ok := he.Message.(string); ok {
			message = msg
		} else {
			message = http.StatusText(code)
		}
	}

	// Write the standard error envelope:
	// {"error": {"code": <integer>, "message": "<string>"}}
	//nolint:errcheck // Error handler must not return errors.
	c.JSON(code, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}
