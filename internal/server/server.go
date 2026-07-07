// Package server sets up the Echo HTTP server with health probe routes.
package server

import (
	"database/sql"

	"github.com/agent-fox/af-hub/internal/config"
)

// NewServer is a placeholder stub. It will create and configure the Echo
// server instance with health routes and middleware.
func NewServer(_ *config.Config, _ *sql.DB) error {
	// Stub — implementation in a later task group.
	return nil
}
