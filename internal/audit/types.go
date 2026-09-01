// Package audit provides DuckDB-backed audit event storage, session lifecycle
// tracking, and token usage reporting.
package audit

import "errors"


// HubEvent represents a hub-internal audit event passed to the Emitter.
type HubEvent struct {
	EventType    string
	ActorID      string
	ActorType    string // one of: admin_token, api_key, pat, system
	ResourceType string
	ResourceID   string
	Action       string
	Workspace    string
	Metadata     map[string]any
}

// HubEventRow represents a fully-prepared row ready for insertion into
// the hub_audit_events table.
type HubEventRow struct {
	ID           string
	EventType    string
	ActorID      string
	ActorType    string
	ResourceType string
	ResourceID   string
	Action       string
	Workspace    string
	Metadata     string // JSON-encoded
	IngestedAt   string
}

// Session represents an agent session record in the agent_sessions table.
type Session struct {
	ID                       string  `json:"id"`
	WorkspaceSlug            string  `json:"workspace_slug"`
	RunID                    string  `json:"run_id,omitempty"`
	NodeID                   string  `json:"node_id,omitempty"`
	Archetype                string  `json:"archetype,omitempty"`
	Model                    string  `json:"model,omitempty"`
	Status                   string  `json:"status"`
	CredentialID             string  `json:"credential_id"`
	CredentialType           string  `json:"credential_type"`
	ErrorMessage             string  `json:"error_message,omitempty"`
	StartedAt                string  `json:"started_at"`
	EndedAt                  *string `json:"ended_at,omitempty"`
	DurationMs               *int64  `json:"duration_ms,omitempty"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens,omitempty"`
	Metadata                 any     `json:"metadata,omitempty"`
	TokenSummary             *TokenSummary `json:"token_summary,omitempty"`
}

// TokenUsage represents a single incremental token usage record in the
// token_usage table.
type TokenUsage struct {
	ID              string `json:"id"`
	SessionID       string `json:"session_id"`
	WorkspaceSlug   string `json:"workspace_slug"`
	Model           string `json:"model"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	CacheReadTokens int64  `json:"cache_read_tokens"`
	ReportedAt      string `json:"reported_at"`
}

// TokenSummary contains aggregated token usage counts for a session.
type TokenSummary struct {
	TotalInputTokens     int64    `json:"total_input_tokens"`
	TotalOutputTokens    int64    `json:"total_output_tokens"`
	TotalCacheReadTokens int64    `json:"total_cache_read_tokens"`
	ModelsUsed           []string `json:"models_used"`
}

// CreateSessionRequest is the JSON body for POST /api/v1/sessions.
type CreateSessionRequest struct {
	ID            string `json:"id,omitempty"`
	WorkspaceSlug string `json:"workspace_slug"`
	RunID         string `json:"run_id,omitempty"`
	NodeID        string `json:"node_id,omitempty"`
	Archetype     string `json:"archetype,omitempty"`
	Model         string `json:"model,omitempty"`
	Metadata      any    `json:"metadata,omitempty"`
}

// CompleteSessionRequest is the JSON body for POST /api/v1/sessions/:id/complete.
type CompleteSessionRequest struct {
	Status                   string `json:"status"`
	ErrorMessage             string `json:"error_message,omitempty"`
	DurationMs               *int64 `json:"duration_ms,omitempty"`
	CacheCreationInputTokens int64  `json:"cache_creation_input_tokens,omitempty"`
	InputTokens              int64  `json:"input_tokens,omitempty"`
	OutputTokens             int64  `json:"output_tokens,omitempty"`
}

// ReportUsageRequest is the JSON body for POST /api/v1/sessions/:id/usage.
type ReportUsageRequest struct {
	ID              string `json:"id,omitempty"`
	Model           string `json:"model"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	CacheReadTokens int64  `json:"cache_read_tokens"`
}

// SessionListResponse is the JSON body for GET /api/v1/sessions.
type SessionListResponse struct {
	Sessions   []Session `json:"sessions"`
	NextCursor *string   `json:"next_cursor"`
	HasMore    bool      `json:"has_more"`
}

// UsageListResponse is the JSON body for GET /api/v1/sessions/:id/usage.
type UsageListResponse struct {
	SessionID  string       `json:"session_id"`
	Records    []TokenUsage `json:"records"`
	Totals     TokenSummary `json:"totals"`
	NextCursor *string      `json:"next_cursor"`
	HasMore    bool         `json:"has_more"`
}

// SessionListParams holds query parameters for listing sessions.
type SessionListParams struct {
	WorkspaceSlug  string
	RunID          string
	Status         string
	CredentialType string
	Since          string
	Order          string // "asc" or "desc"
	Limit          int
	Cursor         string
}

// UsageListParams holds query parameters for listing token usage records.
type UsageListParams struct {
	Order  string // "asc" or "desc"
	Limit  int
	Cursor string
}

// ErrSessionNotActive is returned when attempting to complete or report usage
// on a session that is not in the active state.
var ErrSessionNotActive = errors.New("session is not active")

// ErrSessionNotFound is returned when a session with the given ID does not
// exist in the agent_sessions table.
var ErrSessionNotFound = errors.New("session not found")
