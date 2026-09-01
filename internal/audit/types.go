// Package audit provides DuckDB-backed audit event storage, session lifecycle
// tracking, and token usage reporting. The Emitter interface and HubEvent
// struct are used for mutation audit emission.
package audit

import "errors"

// HubEvent represents a hub-internal audit event passed to the Emitter.
type HubEvent struct {
	ID           string         `json:"id"`
	EventType    string         `json:"event_type"`
	ActorID      string         `json:"actor_id"`
	ActorType    string         `json:"actor_type"` // one of: admin_token, api_key, pat, system
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	Action       string         `json:"action"`
	Workspace    string         `json:"workspace"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	Timestamp    string         `json:"timestamp"`
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
	ID                       string        `json:"id"`
	WorkspaceSlug            string        `json:"workspace_slug"`
	RunID                    string        `json:"run_id,omitempty"`
	NodeID                   string        `json:"node_id,omitempty"`
	Archetype                string        `json:"archetype,omitempty"`
	Model                    string        `json:"model,omitempty"`
	Status                   string        `json:"status"`
	CredentialID             string        `json:"credential_id"`
	CredentialType           string        `json:"credential_type"`
	ErrorMessage             string        `json:"error_message,omitempty"`
	StartedAt                string        `json:"started_at"`
	EndedAt                  *string       `json:"ended_at,omitempty"`
	DurationMs               *int64        `json:"duration_ms,omitempty"`
	CacheCreationInputTokens int64         `json:"cache_creation_input_tokens,omitempty"`
	Metadata                 any           `json:"metadata,omitempty"`
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

// CostResponse is the JSON body for GET /api/v1/workspaces/:slug/cost.
type CostResponse struct {
	Workspace string         `json:"workspace"`
	Period    CostPeriod     `json:"period"`
	Totals   CostTotals     `json:"totals"`
	Breakdown []CostBreakdownEntry `json:"breakdown"`
}

// CostPeriod describes the time window of a cost query.
type CostPeriod struct {
	Since string `json:"since"`
	Until string `json:"until"`
}

// CostTotals contains aggregate token usage across all matching records.
type CostTotals struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens"`
	Sessions        int64 `json:"sessions"`
}

// CostBreakdownEntry contains per-dimension token usage aggregates.
// Exactly one of Date, SessionID, or Model will be set, depending on group_by.
type CostBreakdownEntry struct {
	Date            string `json:"date,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	Model           string `json:"model,omitempty"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	CacheReadTokens int64  `json:"cache_read_tokens"`
	Sessions        int64  `json:"sessions"`
}

// CostParams holds parsed parameters for a cost query.
type CostParams struct {
	WorkspaceSlug string
	GroupBy       string // "day", "session", or "model"
	Since         string // RFC3339
	Until         string // RFC3339
}

// ForceCloseResult holds information about a single force-closed session.
type ForceCloseResult struct {
	SessionID string
}

// ---------------------------------------------------------------------------
// Audit ingestion request/response types (spec 17)
// ---------------------------------------------------------------------------

// PostEventRequest is the JSON body for POST /workspaces/:slug/runs/:run_id/events.
type PostEventRequest struct {
	ID        string         `json:"id,omitempty"`
	RunID     string         `json:"run_id,omitempty"`
	EventType string         `json:"event_type"`
	Severity  string         `json:"severity,omitempty"`
	NodeID    string         `json:"node_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Timestamp string         `json:"timestamp,omitempty"`
	Payload   any            `json:"payload,omitempty"`
}

// PostEventResponse is the JSON body returned from POST /workspaces/:slug/runs/:run_id/events.
type PostEventResponse struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	EventType string `json:"event_type"`
	Severity  string `json:"severity"`
	CreatedAt string `json:"created_at"`
}

// PostSessionOutcomeRequest is the JSON body for POST .../sessions/outcomes.
type PostSessionOutcomeRequest struct {
	ID         string `json:"id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	SessionID  string `json:"session_id"`
	NodeID     string `json:"node_id,omitempty"`
	Status     string `json:"status"`
	Timestamp  string `json:"timestamp,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	TokenUsage any    `json:"token_usage,omitempty"`
}

// PostSessionOutcomeResponse is the JSON body returned from POST .../sessions/outcomes.
type PostSessionOutcomeResponse struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	NodeID    string `json:"node_id"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// PostToolCallRequest is the JSON body for POST .../tools/calls.
type PostToolCallRequest struct {
	ID         string `json:"id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	ToolName   string `json:"tool_name"`
	NodeID     string `json:"node_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Timestamp  string `json:"timestamp,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Input      any    `json:"input,omitempty"`
	Output     any    `json:"output,omitempty"`
}

// PostToolCallResponse is the JSON body returned from POST .../tools/calls.
type PostToolCallResponse struct {
	ID       string `json:"id"`
	RunID    string `json:"run_id"`
	ToolName string `json:"tool_name"`
	CalledAt string `json:"called_at"`
}

// PostToolErrorRequest is the JSON body for POST .../tools/errors.
type PostToolErrorRequest struct {
	ID        string `json:"id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	ToolName  string `json:"tool_name"`
	NodeID    string `json:"node_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	ErrorMsg  string `json:"error_msg"`
	Timestamp string `json:"timestamp,omitempty"`
}

// PostToolErrorResponse is the JSON body returned from POST .../tools/errors.
type PostToolErrorResponse struct {
	ID       string `json:"id"`
	RunID    string `json:"run_id"`
	ToolName string `json:"tool_name"`
	FailedAt string `json:"failed_at"`
}

// PostTraceRequest is the JSON body for POST .../traces.
type PostTraceRequest struct {
	ID        string `json:"id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	EventType string `json:"event_type"`
	NodeID    string `json:"node_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Sequence  int    `json:"sequence,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Data      any    `json:"data,omitempty"`
}

// PostTraceResponse is the JSON body returned from POST .../traces.
type PostTraceResponse struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	EventType string `json:"event_type"`
	Timestamp string `json:"timestamp"`
}

// PostPostmortemRequest is the JSON body for POST .../postmortem.
type PostPostmortemRequest struct {
	SchemaVersion  *int   `json:"schema_version,omitempty"`
	RunStatus      string `json:"run_status"`
	StartedAt      string `json:"started_at"`
	CompletedAt    string `json:"completed_at"`
	TaskSummary    any    `json:"task_summary"`
	CostSummary    any    `json:"cost_summary"`
	BlockedTasks   any    `json:"blocked_tasks,omitempty"`
	SessionHistory any    `json:"session_history,omitempty"`
}

// PostPostmortemResponse is the JSON body returned from POST .../postmortem.
type PostPostmortemResponse struct {
	RunID     string `json:"run_id"`
	RunStatus string `json:"run_status"`
	CreatedAt string `json:"created_at"`
}

// BatchIngestResponse is the JSON response body for batch ingestion endpoints.
type BatchIngestResponse struct {
	Accepted   int              `json:"accepted"`
	Duplicates int              `json:"duplicates"`
	Errors     []BatchItemError `json:"errors"`
}

// BatchItemError describes a single invalid item in a batch ingestion request.
type BatchItemError struct {
	Index   int    `json:"index"`
	ID      string `json:"id,omitempty"`
	Message string `json:"message"`
}

// QueryParams is used by all Store query methods. Each query method uses only
// the relevant subset of fields.
type QueryParams struct {
	Since     string
	Until     string
	Order     string
	Cursor    string
	Limit     int
	EventType string
	Severity  string
	NodeID    string
	SessionID string
	ToolName  string
	Status    string
}

// PaginatedCursor is the internal representation of cursor state for
// base64url-encoded pagination tokens.
type PaginatedCursor struct {
	Ts string `json:"ts"`
	ID string `json:"id"`
}

// validRunStatuses is the set of accepted run_status values for postmortems.
var validRunStatuses = map[string]bool{
	"stalled":       true,
	"block_limit":   true,
	"cost_limit":    true,
	"session_limit": true,
}

// WriteTimeoutError indicates a DuckDB write operation timed out under
// sustained write contention. Handlers should check for this error and
// respond with HTTP 503 + Retry-After: 5.
type WriteTimeoutError struct {
	Err error
}

func (e *WriteTimeoutError) Error() string {
	return "audit: write timeout: " + e.Err.Error()
}

func (e *WriteTimeoutError) Unwrap() error {
	return e.Err
}

// ErrSessionNotActive is returned when attempting to complete or report usage
// on a session that is not in the active state.
var ErrSessionNotActive = errors.New("session is not active")

// ErrSessionNotFound is returned when a session with the given ID does not
// exist in the agent_sessions table.
var ErrSessionNotFound = errors.New("session not found")
