package carrypatch

import (
	"database/sql"
	"encoding/json"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// API configuration
// ===========================================================================

// RebuildAPIConfig holds dependencies for rebuild HTTP endpoints.
type RebuildAPIConfig struct {
	DB          *sql.DB
	Queue       *jobqueue.Queue
	GetVariable GetVariableFunc
}

// RebuildJobResponse is the JSON response body for a rebuild job.
type RebuildJobResponse struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Key      string          `json:"key"`
	GroupKey string          `json:"group_key"`
	Status   string          `json:"status"`
	Payload  json.RawMessage `json:"payload"`
}

// RegisterRebuildRoutes mounts rebuild endpoints on the API group.
// Stub: implementation in later task groups.
func RegisterRebuildRoutes(_ *echo.Group, _ RebuildAPIConfig) {
	// POST /workspaces/:slug/rebuild — to be implemented.
}
