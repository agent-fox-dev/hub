package audit

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// ---------------------------------------------------------------------------
// Unified audit query handler — GET /api/v1/audit
// Requirements: 18-REQ-6
// ---------------------------------------------------------------------------

// UnifiedAuditEvent represents a single event in the unified query response.
// Hub-specific fields are null for agent events and vice versa.
type UnifiedAuditEvent struct {
	ID           string  `json:"id"`
	EventType    string  `json:"event_type"`
	Source       string  `json:"source"` // "hub" or "agent"
	Timestamp    string  `json:"timestamp"`
	Severity     string  `json:"severity"`
	Workspace    string  `json:"workspace"`
	ActorID      *string `json:"actor_id"`
	ActorType    *string `json:"actor_type"`
	ResourceType *string `json:"resource_type"`
	ResourceID   *string `json:"resource_id"`
	Action       *string `json:"action"`
	RunID        *string `json:"run_id"`
	NodeID       *string `json:"node_id"`
	SessionID    *string `json:"session_id"`
	Archetype    *string `json:"archetype"`
}

// unifiedQueryResponse is the JSON response body for GET /api/v1/audit.
type unifiedQueryResponse struct {
	Events     []UnifiedAuditEvent `json:"events"`
	NextCursor *string             `json:"next_cursor"`
	HasMore    bool                `json:"has_more"`
}

// unifiedQueryParams holds parsed parameters for the unified audit query.
type unifiedQueryParams struct {
	source          string
	runID           string
	actorID         string
	actorType       string
	resourceType    string
	action          string
	eventType       string
	eventTypePrefix string
	severity        string
	workspace       string
	since           string
	until           string
	limit           int
	cursor          string
}

const (
	defaultAuditQueryLimit = 100
	maxAuditQueryLimit     = 1000
)

// handleAuditQuery implements GET /api/v1/audit — the unified audit event
// query handler (18-REQ-6).
func handleAuditQuery(store Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Auth check: audit:read required (18-REQ-6.E3, 18-REQ-6.E4).
		auth := requireAuditRead(c)
		if auth == nil {
			return nil
		}

		// Parse query parameters (18-REQ-6.2).
		params, err := parseUnifiedQueryParams(c)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}

		// Execute unified query against DuckDB.
		ds, ok := store.(*duckDBStore)
		if !ok {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		events, nextCursor, hasMore, queryErr := executeUnifiedQuery(ds.db, params)
		if queryErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		resp := unifiedQueryResponse{
			Events:  events,
			HasMore: hasMore,
		}
		if nextCursor != "" {
			resp.NextCursor = &nextCursor
		}

		return c.JSON(http.StatusOK, resp)
	}
}

// parseUnifiedQueryParams extracts and validates query parameters for the
// unified audit query (18-REQ-6.2, 18-REQ-6.3, 18-REQ-6.E1, 18-REQ-6.E5,
// 18-REQ-6.E7, 18-REQ-6.E8).
func parseUnifiedQueryParams(c echo.Context) (unifiedQueryParams, error) {
	params := unifiedQueryParams{
		source:          c.QueryParam("source"),
		runID:           c.QueryParam("run_id"),
		actorID:         c.QueryParam("actor_id"),
		actorType:       c.QueryParam("actor_type"),
		resourceType:    c.QueryParam("resource_type"),
		action:          c.QueryParam("action"),
		eventType:       c.QueryParam("event_type"),
		eventTypePrefix: c.QueryParam("event_type_prefix"),
		severity:        c.QueryParam("severity"),
		workspace:       c.QueryParam("workspace"),
		since:           c.QueryParam("since"),
		until:           c.QueryParam("until"),
		cursor:          c.QueryParam("cursor"),
	}

	// Parse and clamp limit (18-REQ-6.3, 18-REQ-6.E5).
	params.limit = defaultAuditQueryLimit
	if limitStr := c.QueryParam("limit"); limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err == nil && l > 0 {
			params.limit = l
		}
	}
	if params.limit > maxAuditQueryLimit {
		params.limit = maxAuditQueryLimit
	}

	// Validate cursor (18-REQ-6.E1).
	if params.cursor != "" {
		if _, _, err := decodeCursor(params.cursor); err != nil {
			return unifiedQueryParams{}, fmt.Errorf("invalid cursor")
		}
	}

	// Validate since/until timestamps (18-REQ-6.E8).
	var sinceTime, untilTime time.Time
	if params.since != "" {
		t, err := time.Parse(time.RFC3339, params.since)
		if err != nil {
			t, err = time.Parse(time.RFC3339Nano, params.since)
			if err != nil {
				return unifiedQueryParams{}, fmt.Errorf("invalid query parameter: since/until must be RFC 3339")
			}
		}
		sinceTime = t
	}
	if params.until != "" {
		t, err := time.Parse(time.RFC3339, params.until)
		if err != nil {
			t, err = time.Parse(time.RFC3339Nano, params.until)
			if err != nil {
				return unifiedQueryParams{}, fmt.Errorf("invalid query parameter: since/until must be RFC 3339")
			}
		}
		untilTime = t
	}

	// Validate since < until (18-REQ-6.E7).
	if params.since != "" && params.until != "" {
		if !sinceTime.Before(untilTime) {
			return unifiedQueryParams{}, fmt.Errorf("since must be before until")
		}
	}

	return params, nil
}

// executeUnifiedQuery builds and executes the UNION ALL query over
// hub_audit_events and agent_audit_events (18-REQ-6.1).
func executeUnifiedQuery(db *sql.DB, params unifiedQueryParams) ([]UnifiedAuditEvent, string, bool, error) {
	// Determine which sources to include based on filters.
	includeHub := params.source == "" || params.source == "hub"
	includeAgent := params.source == "" || params.source == "agent"

	// Hub-specific filters exclude agent events.
	if params.actorID != "" || params.actorType != "" || params.resourceType != "" || params.action != "" {
		includeAgent = false
	}
	// Agent-specific filters exclude hub events.
	if params.runID != "" {
		includeHub = false
	}

	var subQueries []string
	var args []any

	if includeHub {
		q, a := buildHubSubQuery(params)
		subQueries = append(subQueries, q)
		args = append(args, a...)
	}

	if includeAgent {
		q, a := buildAgentSubQuery(params)
		subQueries = append(subQueries, q)
		args = append(args, a...)
	}

	if len(subQueries) == 0 {
		return []UnifiedAuditEvent{}, "", false, nil
	}

	// Build the full UNION ALL query with ORDER BY and LIMIT.
	query := strings.Join(subQueries, " UNION ALL ")
	query = "SELECT * FROM (" + query + ") unified ORDER BY timestamp DESC, id DESC LIMIT ?"
	args = append(args, params.limit+1)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, "", false, fmt.Errorf("unified query: %w", err)
	}
	defer rows.Close()

	var events []UnifiedAuditEvent
	for rows.Next() {
		var ev UnifiedAuditEvent
		var actorID, actorType, resourceType, resourceID, action sql.NullString
		var runID, nodeID, sessionID, archetype sql.NullString

		if err := rows.Scan(
			&ev.ID, &ev.EventType, &ev.Source, &ev.Timestamp,
			&ev.Severity, &ev.Workspace,
			&actorID, &actorType, &resourceType, &resourceID, &action,
			&runID, &nodeID, &sessionID, &archetype,
		); err != nil {
			return nil, "", false, fmt.Errorf("unified query scan: %w", err)
		}

		ev.ActorID = nullableString(actorID)
		ev.ActorType = nullableString(actorType)
		ev.ResourceType = nullableString(resourceType)
		ev.ResourceID = nullableString(resourceID)
		ev.Action = nullableString(action)
		ev.RunID = nullableString(runID)
		ev.NodeID = nullableString(nodeID)
		ev.SessionID = nullableString(sessionID)
		ev.Archetype = nullableString(archetype)

		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, fmt.Errorf("unified query iterate: %w", err)
	}

	// Determine pagination (18-REQ-6.3).
	hasMore := len(events) > params.limit
	if hasMore {
		events = events[:params.limit]
	}

	var nextCursor string
	if hasMore && len(events) > 0 {
		last := events[len(events)-1]
		nextCursor = encodeCursor(last.Timestamp, last.ID)
	}

	if events == nil {
		events = []UnifiedAuditEvent{}
	}

	return events, nextCursor, hasMore, nil
}

// buildHubSubQuery builds the SELECT ... FROM hub_audit_events sub-query
// with applicable WHERE clauses.
func buildHubSubQuery(params unifiedQueryParams) (string, []any) {
	var conditions []string
	var args []any

	// Common filters.
	if params.workspace != "" {
		conditions = append(conditions, "workspace = ?")
		args = append(args, params.workspace)
	}
	if params.eventType != "" {
		conditions = append(conditions, "event_type = ?")
		args = append(args, params.eventType)
	}
	if params.eventTypePrefix != "" {
		conditions = append(conditions, "event_type LIKE ?")
		args = append(args, params.eventTypePrefix+"%")
	}
	if params.severity != "" {
		conditions = append(conditions, "severity = ?")
		args = append(args, params.severity)
	}
	if params.since != "" {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, params.since)
	}
	if params.until != "" {
		conditions = append(conditions, "timestamp < ?")
		args = append(args, params.until)
	}

	// Hub-specific filters.
	if params.actorID != "" {
		conditions = append(conditions, "actor_id = ?")
		args = append(args, params.actorID)
	}
	if params.actorType != "" {
		conditions = append(conditions, "actor_type = ?")
		args = append(args, params.actorType)
	}
	if params.resourceType != "" {
		conditions = append(conditions, "resource_type = ?")
		args = append(args, params.resourceType)
	}
	if params.action != "" {
		conditions = append(conditions, "action = ?")
		args = append(args, params.action)
	}

	// Cursor-based pagination (18-REQ-6.3).
	if params.cursor != "" {
		cursorTS, cursorID, _ := decodeCursor(params.cursor)
		conditions = append(conditions,
			"(timestamp < ? OR (timestamp = ? AND id < ?))")
		args = append(args, cursorTS, cursorTS, cursorID)
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	q := `SELECT id, event_type, 'hub' AS source, timestamp, severity, workspace,
		actor_id, actor_type, resource_type, resource_id, action,
		NULL AS run_id, NULL AS node_id, NULL AS session_id, NULL AS archetype
		FROM hub_audit_events` + where

	return q, args
}

// buildAgentSubQuery builds the SELECT ... FROM agent_audit_events sub-query
// with applicable WHERE clauses.
func buildAgentSubQuery(params unifiedQueryParams) (string, []any) {
	var conditions []string
	var args []any

	// Common filters.
	if params.workspace != "" {
		conditions = append(conditions, "workspace = ?")
		args = append(args, params.workspace)
	}
	if params.eventType != "" {
		conditions = append(conditions, "event_type = ?")
		args = append(args, params.eventType)
	}
	if params.eventTypePrefix != "" {
		conditions = append(conditions, "event_type LIKE ?")
		args = append(args, params.eventTypePrefix+"%")
	}
	if params.severity != "" {
		conditions = append(conditions, "severity = ?")
		args = append(args, params.severity)
	}
	if params.since != "" {
		conditions = append(conditions, "CAST(timestamp AS VARCHAR) >= ?")
		args = append(args, params.since)
	}
	if params.until != "" {
		conditions = append(conditions, "CAST(timestamp AS VARCHAR) < ?")
		args = append(args, params.until)
	}

	// Agent-specific filters.
	if params.runID != "" {
		conditions = append(conditions, "run_id = ?")
		args = append(args, params.runID)
	}

	// Cursor-based pagination (18-REQ-6.3).
	if params.cursor != "" {
		cursorTS, cursorID, _ := decodeCursor(params.cursor)
		conditions = append(conditions,
			"(CAST(timestamp AS VARCHAR) < ? OR (CAST(timestamp AS VARCHAR) = ? AND id < ?))")
		args = append(args, cursorTS, cursorTS, cursorID)
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	q := `SELECT id, event_type, 'agent' AS source, CAST(timestamp AS VARCHAR) AS timestamp,
		severity, workspace,
		NULL AS actor_id, NULL AS actor_type, NULL AS resource_type, NULL AS resource_id, NULL AS action,
		run_id, node_id, session_id, archetype
		FROM agent_audit_events` + where

	return q, args
}

// nullableString converts a sql.NullString to *string for JSON serialization.
// Valid strings become non-nil pointers; NULL becomes nil (JSON null).
func nullableString(ns sql.NullString) *string {
	if ns.Valid {
		s := ns.String
		return &s
	}
	return nil
}

// ---------------------------------------------------------------------------
// Transcript handler stub — GET /api/v1/workspaces/:slug/runs/:run_id/transcript
// Requirements: 18-REQ-7 (full implementation in task group 7)
// ---------------------------------------------------------------------------

// handleTranscript implements GET /api/v1/workspaces/:slug/runs/:run_id/transcript.
// Returns a conversation transcript reconstructed from agent trace data.
func handleTranscript(store Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditRead(c)
		if auth == nil {
			return nil
		}

		slug := c.Param("slug")
		runID := c.Param("run_id")
		nodeID := c.QueryParam("node_id")

		// 18-REQ-7.E1: node_id is required.
		if nodeID == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "node_id is required")
		}

		// Query agent_traces for the given run and node.
		ds, ok := store.(*duckDBStore)
		if !ok {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		messages, err := queryTranscriptMessages(ds.db, slug, runID, nodeID)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		return c.JSON(http.StatusOK, map[string]any{
			"run_id":   runID,
			"node_id":  nodeID,
			"messages": messages,
		})
	}
}

// transcriptMsg represents a single message in a transcript response.
type transcriptMsg struct {
	Role      string  `json:"role"`
	Content   string  `json:"content"`
	ToolName  *string `json:"tool_name"`
	Timestamp string  `json:"timestamp"`
}

// traceEventTypeToRole maps agent trace event types to transcript roles.
var traceEventTypeToRole = map[string]string{
	"session.init":      "system",
	"assistant.message": "assistant",
	"tool.use":          "tool_use",
	"tool.error":        "tool_error",
}

// queryTranscriptMessages queries agent_traces and maps them to transcript
// messages. Filters by workspace, run_id, and node_id. Unrecognized event
// types are skipped (18-REQ-7.E5).
func queryTranscriptMessages(db *sql.DB, workspace, runID, nodeID string) ([]transcriptMsg, error) {
	var conditions []string
	var args []any

	// 18-REQ-7.1: Filter by workspace to ensure cross-workspace isolation.
	conditions = append(conditions, "workspace = ?")
	args = append(args, workspace)

	conditions = append(conditions, "run_id = ?")
	args = append(args, runID)

	conditions = append(conditions, "node_id = ?")
	args = append(args, nodeID)

	where := "WHERE " + strings.Join(conditions, " AND ")
	query := fmt.Sprintf(
		`SELECT event_type, data, CAST(timestamp AS VARCHAR) AS timestamp
		FROM agent_traces %s ORDER BY timestamp ASC, sequence ASC`, where)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("transcript query: %w", err)
	}
	defer rows.Close()

	var messages []transcriptMsg
	for rows.Next() {
		var eventType, dataJSON, ts string
		if err := rows.Scan(&eventType, &dataJSON, &ts); err != nil {
			return nil, fmt.Errorf("transcript scan: %w", err)
		}

		// 18-REQ-7.E5: Skip unrecognized event types.
		role, ok := traceEventTypeToRole[eventType]
		if !ok {
			continue
		}

		msg := transcriptMsg{
			Role:      role,
			Timestamp: ts,
		}

		// Extract content and tool_name from data JSON.
		var data map[string]any
		if json.Unmarshal([]byte(dataJSON), &data) == nil {
			if content, ok := data["content"].(string); ok {
				msg.Content = content
			}
			if toolName, ok := data["tool_name"].(string); ok {
				msg.ToolName = &toolName
			}
		}

		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("transcript iterate: %w", err)
	}

	if messages == nil {
		messages = []transcriptMsg{}
	}

	return messages, nil
}

// ---------------------------------------------------------------------------
// SSE handler — GET /api/v1/events
// Requirements: 18-REQ-8 (full implementation in task group 7)
// ---------------------------------------------------------------------------

// handleSSEStream implements GET /api/v1/events — the SSE streaming endpoint.
func handleSSEStream(store Store, mgr *SSEManager) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditRead(c)
		if auth == nil {
			return nil
		}

		// Check connection limit (18-REQ-8.E1).
		if mgr != nil {
			filters := sseFilters{
				workspace: c.QueryParam("workspace"),
				runID:     c.QueryParam("run_id"),
				category:  c.QueryParam("category"),
			}

			conn, err := mgr.Register(filters)
			if err != nil {
				return c.JSON(http.StatusServiceUnavailable,
					map[string]string{"error": "too many SSE connections"})
			}

			// Set SSE headers.
			c.Response().Header().Set("Content-Type", "text/event-stream")
			c.Response().Header().Set("Cache-Control", "no-cache")
			c.Response().Header().Set("Connection", "keep-alive")
			c.Response().WriteHeader(http.StatusOK)

			// Send initial heartbeat event to confirm the connection is live
			// (18-REQ-8.8).
			hb := heartbeatEvent()
			hbData, _ := json.Marshal(map[string]string{"timestamp": hb.Timestamp})
			if _, writeErr := fmt.Fprintf(c.Response(), "event: heartbeat\ndata: %s\n\n", hbData); writeErr != nil {
				slog.Debug("sse: initial write failed", "conn_id", conn.id, "error", writeErr)
				mgr.Unregister(conn.id)
				return nil
			}
			c.Response().Flush()

			// Stream events from the per-client channel.
			ctx := c.Request().Context()
			for {
				select {
				case <-ctx.Done():
					mgr.Unregister(conn.id)
					return nil
				case event, ok := <-conn.ch:
					if !ok {
						return nil
					}

					mgr.TouchLastRead(conn.id)

					var writeErr error
					if event.EventType == "heartbeat" {
						hbJSON, _ := json.Marshal(map[string]string{"timestamp": event.Timestamp})
						_, writeErr = fmt.Fprintf(c.Response(), "event: heartbeat\ndata: %s\n\n", hbJSON)
					} else {
						eventData, _ := json.Marshal(event)
						_, writeErr = fmt.Fprintf(c.Response(), "event: audit_event\ndata: %s\n\n", eventData)
					}

					// 18-REQ-8.E7: Treat write errors as client disconnect.
					if writeErr != nil {
						slog.Debug("sse: write failed, treating as disconnect",
							"conn_id", conn.id, "error", writeErr)
						mgr.Unregister(conn.id)
						return nil
					}
					c.Response().Flush()
				}
			}
		}

		// Fallback: no SSEManager wired. Write SSE headers and initial
		// frames, then return. This path is used by route registration
		// tests where a mock broadcaster is provided.
		c.Response().Header().Set("Content-Type", "text/event-stream")
		c.Response().Header().Set("Cache-Control", "no-cache")
		c.Response().Header().Set("Connection", "keep-alive")
		c.Response().WriteHeader(http.StatusOK)

		// Send initial keep-alive and heartbeat.
		_, _ = fmt.Fprintf(c.Response(), ": keep-alive\n\n")
		hb := heartbeatEvent()
		hbData, _ := json.Marshal(map[string]string{"timestamp": hb.Timestamp})
		_, _ = fmt.Fprintf(c.Response(), "event: heartbeat\ndata: %s\n\n", hbData)
		c.Response().Flush()
		return nil
	}
}
