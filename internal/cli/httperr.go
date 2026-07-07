package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ErrorEnvelope represents the nested error response format used by af-hub.
// The hub returns errors as: {"error": {"code": "<HTTP_STATUS>", "message": "<description>"}}
// per spec 02 REQ-8.1.
type ErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ParseHTTPError reads a non-2xx HTTP response and returns an error with
// a human-readable message. It first attempts to parse the response body as
// the nested error envelope defined by spec 02. If parsing fails, it falls
// back to including the raw HTTP status code and response body.
func ParseHTTPError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("HTTP %d: failed to read response body: %w", resp.StatusCode, err)
	}

	// Try to parse as the nested error envelope.
	var envelope ErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		return fmt.Errorf("%s", envelope.Error.Message)
	}

	// Fallback: return raw status code and body.
	bodyStr := string(body)
	if bodyStr == "" {
		bodyStr = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, bodyStr)
}
