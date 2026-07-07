// Package handler — standardized error response envelope.
package handler

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
)

// ErrorDetail holds the error code and message for the error envelope.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorResponse is the standard error envelope for all API error responses.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// NewErrorResponse creates a standard error response and sends it.
// The caller is responsible for providing a safe message that does not leak
// internal details. For generic 500 errors, callers should pass
// "internal server error" as the message.
func NewErrorResponse(c echo.Context, status int, message string) error {
	return c.JSON(status, ErrorResponse{
		Error: ErrorDetail{
			Code:    fmt.Sprintf("%d", status),
			Message: message,
		},
	})
}

// CustomHTTPErrorHandler is the Echo error handler that formats all errors
// using the standard error envelope. It maps echo.HTTPError to the correct
// status and message, and ensures 5xx errors never expose internal details.
func CustomHTTPErrorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	code := http.StatusInternalServerError
	message := "internal server error"

	if he, ok := err.(*echo.HTTPError); ok {
		code = he.Code
		if code >= 500 {
			// Never expose internal details for 5xx errors from uncaught errors.
			message = "internal server error"
		} else if msg, ok := he.Message.(string); ok {
			message = msg
		} else {
			message = http.StatusText(code)
		}
	}

	// Send the standardized error envelope.
	_ = c.JSON(code, ErrorResponse{
		Error: ErrorDetail{
			Code:    fmt.Sprintf("%d", code),
			Message: message,
		},
	})
}
