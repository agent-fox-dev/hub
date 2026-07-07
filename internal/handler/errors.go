// Package handler — standardized error response envelope.
package handler

import "github.com/labstack/echo/v4"

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
func NewErrorResponse(c echo.Context, status int, message string) error {
	panic("not implemented")
}

// CustomHTTPErrorHandler is the Echo error handler that formats all errors
// using the standard error envelope.
func CustomHTTPErrorHandler(err error, c echo.Context) {
	panic("not implemented")
}
