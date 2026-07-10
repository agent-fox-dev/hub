// Package apierror provides utilities for handling non-2xx API responses
// from the af-hub server. It parses the standard nested error envelope
// {"error": {"code": N, "message": "..."}} defined by spec 01 and falls
// back to a generic message for non-JSON response bodies.
package apierror

import (
	"encoding/json"
	"fmt"
)

// errorEnvelope matches the spec 01 standard error response format:
// {"error": {"code": <int>, "message": "..."}}
type errorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// HandleResponseBody interprets a non-2xx response body and returns a
// human-readable error message suitable for printing to stderr. It attempts
// to parse the body as the standard nested JSON error envelope first; if
// that fails, it returns the generic message:
// "Error: unexpected response from server (HTTP <status>)."
//
// The raw response body is never included in the returned error message for
// non-JSON bodies, preventing raw HTML/binary from leaking to stderr.
func HandleResponseBody(statusCode int, body []byte) string {
	// Attempt to parse as nested error envelope {"error": {"message": "..."}}.
	var envelope errorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		return envelope.Error.Message
	}

	// Fallback: non-JSON or envelope without message field.
	return fmt.Sprintf("Error: unexpected response from server (HTTP %d).", statusCode)
}

// FormatNonJSONError returns the standard non-JSON error message for the given
// status code.
func FormatNonJSONError(statusCode int) string {
	return fmt.Sprintf("Error: unexpected response from server (HTTP %d).", statusCode)
}

// IsSuccess returns true if the given status code is in the 2xx range.
func IsSuccess(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}
