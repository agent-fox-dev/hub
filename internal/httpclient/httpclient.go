// Package httpclient provides a shared HTTP client for the afc CLI with
// a fixed 30-second timeout and Authorization: Bearer header support.
package httpclient

import (
	"net/http"
	"time"
)

// DefaultTimeout is the fixed HTTP client timeout for all outbound requests.
const DefaultTimeout = 30 * time.Second

// NewClient creates an HTTP client with the default 30-second timeout.
func NewClient() *http.Client {
	// Stub: not implemented yet.
	return &http.Client{}
}
