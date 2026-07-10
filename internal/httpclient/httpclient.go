// Package httpclient provides a shared HTTP client for the afc CLI with
// a fixed 30-second timeout and Authorization: Bearer header support.
package httpclient

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultTimeout is the fixed HTTP client timeout for all outbound requests.
const DefaultTimeout = 30 * time.Second

// NewClient creates an HTTP client with the default 30-second timeout.
func NewClient() *http.Client {
	return &http.Client{
		Timeout: DefaultTimeout,
	}
}

// DoRequest performs an authenticated HTTP request with the given method, URL,
// API key, and optional body. It sets the Authorization: Bearer header and
// Content-Type: application/json header when a body is provided. Returns the
// HTTP response or an error for network/timeout failures.
func DoRequest(client *http.Client, method, url, apiKey string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return client.Do(req)
}
