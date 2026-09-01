package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/txsvc/apikit"
)

// writeTimeout returns the configured write timeout duration.
func writeTimeout() time.Duration {
	if v := os.Getenv("AF_AUDIT_WRITE_TIMEOUT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 2000 * time.Millisecond
}

// wrapTimeout checks if err is a context deadline exceeded and wraps it.
func wrapTimeout(err error) error {
	if err == nil {
		return nil
	}
	if err == context.DeadlineExceeded || strings.Contains(err.Error(), "context deadline exceeded") {
		return &WriteTimeoutError{Err: err}
	}
	return err
}

// execWithTimeout executes an INSERT with a write timeout context.
func (s *duckDBStore) execWithTimeout(query string, args ...any) (sql.Result, error) {
	timeout := writeTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		elapsed := time.Since(start)
		if ctx.Err() == context.DeadlineExceeded {
			slog.Warn("audit: DuckDB write timeout",
				"elapsed_ms", elapsed.Milliseconds(),
				"timeout_ms", timeout.Milliseconds())
			return nil, &WriteTimeoutError{Err: err}
		}
		return nil, err
	}
	return result, nil
}

func (s *duckDBStore) InsertAuditEvent(ctx context.Context, req PostEventRequest, runID, workspace string) (PostEventResponse, bool, error) {
	now := apikit.NowUTC()
	id := req.ID
	if id == "" {
		id = uuid.New().String()
	}
	timestamp := req.Timestamp
	if timestamp == "" {
		timestamp = now
	}
	severity := req.Severity
	if severity == "" {
		severity = DefaultSeverityFor(req.EventType)
	}
	payloadStr := "{}"
	if req.Payload != nil {
		data, err := json.Marshal(req.Payload)
		if err != nil {
			return PostEventResponse{}, false, fmt.Errorf("marshal payload: %w", err)
		}
		payloadStr = string(data)
	}
	result, err := s.execWithTimeout(
		`INSERT OR IGNORE INTO agent_audit_events
		(id, run_id, workspace, event_type, severity, node_id, session_id, timestamp, payload, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, runID, workspace, req.EventType, severity,
		req.NodeID, req.SessionID, timestamp, payloadStr, now,
	)
	if err != nil {
		return PostEventResponse{}, false, wrapTimeout(err)
	}
	affected, _ := result.RowsAffected()
	resp := PostEventResponse{
		ID: id, RunID: runID, EventType: req.EventType,
		Severity: severity, CreatedAt: timestamp,
	}
	return resp, affected > 0, nil
}

func (s *duckDBStore) InsertSessionOutcome(ctx context.Context, req PostSessionOutcomeRequest, runID, workspace string) (PostSessionOutcomeResponse, bool, error) {
	now := apikit.NowUTC()
	id := req.ID
	if id == "" {
		id = uuid.New().String()
	}
	timestamp := req.Timestamp
	if timestamp == "" {
		timestamp = now
	}
	tokenUsageStr := "{}"
	if req.TokenUsage != nil {
		data, _ := json.Marshal(req.TokenUsage)
		tokenUsageStr = string(data)
	}
	result, err := s.execWithTimeout(
		`INSERT OR IGNORE INTO session_outcomes
		(id, run_id, workspace, session_id, node_id, status, timestamp, duration_ms, token_usage, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, runID, workspace, req.SessionID, req.NodeID,
		req.Status, timestamp, req.DurationMs, tokenUsageStr, now,
	)
	if err != nil {
		return PostSessionOutcomeResponse{}, false, wrapTimeout(err)
	}
	affected, _ := result.RowsAffected()
	resp := PostSessionOutcomeResponse{
		ID: id, RunID: runID, NodeID: req.NodeID,
		Status: req.Status, CreatedAt: timestamp,
	}
	return resp, affected > 0, nil
}

func (s *duckDBStore) InsertToolCall(ctx context.Context, req PostToolCallRequest, runID, workspace string) (PostToolCallResponse, bool, error) {
	now := apikit.NowUTC()
	id := req.ID
	if id == "" {
		id = uuid.New().String()
	}
	timestamp := req.Timestamp
	if timestamp == "" {
		timestamp = now
	}
	inputStr := "{}"
	if req.Input != nil {
		data, _ := json.Marshal(req.Input)
		inputStr = string(data)
	}
	outputStr := "{}"
	if req.Output != nil {
		data, _ := json.Marshal(req.Output)
		outputStr = string(data)
	}
	result, err := s.execWithTimeout(
		`INSERT OR IGNORE INTO tool_calls
		(id, run_id, workspace, tool_name, node_id, session_id, timestamp, duration_ms, input, output, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, runID, workspace, req.ToolName, req.NodeID,
		req.SessionID, timestamp, req.DurationMs, inputStr, outputStr, now,
	)
	if err != nil {
		return PostToolCallResponse{}, false, wrapTimeout(err)
	}
	affected, _ := result.RowsAffected()
	resp := PostToolCallResponse{
		ID: id, RunID: runID, ToolName: req.ToolName, CalledAt: timestamp,
	}
	return resp, affected > 0, nil
}

func (s *duckDBStore) InsertToolError(ctx context.Context, req PostToolErrorRequest, runID, workspace string) (PostToolErrorResponse, bool, error) {
	now := apikit.NowUTC()
	id := req.ID
	if id == "" {
		id = uuid.New().String()
	}
	timestamp := req.Timestamp
	if timestamp == "" {
		timestamp = now
	}
	result, err := s.execWithTimeout(
		`INSERT OR IGNORE INTO tool_errors
		(id, run_id, workspace, tool_name, node_id, session_id, error_code, error_msg, timestamp, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, runID, workspace, req.ToolName, req.NodeID,
		req.SessionID, req.ErrorCode, req.ErrorMsg, timestamp, now,
	)
	if err != nil {
		return PostToolErrorResponse{}, false, wrapTimeout(err)
	}
	affected, _ := result.RowsAffected()
	resp := PostToolErrorResponse{
		ID: id, RunID: runID, ToolName: req.ToolName, FailedAt: timestamp,
	}
	return resp, affected > 0, nil
}

func (s *duckDBStore) InsertAgentTrace(ctx context.Context, req PostTraceRequest, runID, workspace string) (PostTraceResponse, bool, error) {
	now := apikit.NowUTC()
	id := req.ID
	if id == "" {
		id = uuid.New().String()
	}
	timestamp := req.Timestamp
	if timestamp == "" {
		timestamp = now
	}
	dataStr := "{}"
	if req.Data != nil {
		data, _ := json.Marshal(req.Data)
		dataStr = string(data)
	}
	result, err := s.execWithTimeout(
		`INSERT OR IGNORE INTO agent_traces
		(id, run_id, workspace, event_type, node_id, session_id, sequence, timestamp, data, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, runID, workspace, req.EventType, req.NodeID,
		req.SessionID, req.Sequence, timestamp, dataStr, now,
	)
	if err != nil {
		return PostTraceResponse{}, false, wrapTimeout(err)
	}
	affected, _ := result.RowsAffected()
	resp := PostTraceResponse{
		ID: id, RunID: runID, EventType: req.EventType, Timestamp: timestamp,
	}
	return resp, affected > 0, nil
}

func (s *duckDBStore) InsertPostmortem(ctx context.Context, req PostPostmortemRequest, runID, workspace string) (PostPostmortemResponse, bool, error) {
	now := apikit.NowUTC()
	taskSummaryStr := "{}"
	if req.TaskSummary != nil {
		data, _ := json.Marshal(req.TaskSummary)
		taskSummaryStr = string(data)
	}
	costSummaryStr := "{}"
	if req.CostSummary != nil {
		data, _ := json.Marshal(req.CostSummary)
		costSummaryStr = string(data)
	}
	blockedTasksStr := "[]"
	if req.BlockedTasks != nil {
		data, _ := json.Marshal(req.BlockedTasks)
		blockedTasksStr = string(data)
	}
	sessionHistoryStr := "[]"
	if req.SessionHistory != nil {
		data, _ := json.Marshal(req.SessionHistory)
		sessionHistoryStr = string(data)
	}
	schemaVersion := 1
	if req.SchemaVersion != nil {
		schemaVersion = *req.SchemaVersion
	}
	result, err := s.execWithTimeout(
		`INSERT OR IGNORE INTO postmortems
		(run_id, workspace, schema_version, run_status, started_at, completed_at,
		 task_summary, cost_summary, blocked_tasks, session_history, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, workspace, schemaVersion, req.RunStatus,
		req.StartedAt, req.CompletedAt,
		taskSummaryStr, costSummaryStr, blockedTasksStr, sessionHistoryStr, now,
	)
	if err != nil {
		return PostPostmortemResponse{}, false, wrapTimeout(err)
	}
	affected, _ := result.RowsAffected()
	resp := PostPostmortemResponse{
		RunID: runID, RunStatus: req.RunStatus, CreatedAt: now,
	}
	return resp, affected > 0, nil
}

// Batch inserts

func (s *duckDBStore) InsertAuditEventBatch(ctx context.Context, events []PostEventRequest, runID, workspace string) (accepted, duplicates int, err error) {
	now := apikit.NowUTC()
	timeout := writeTimeout()
	txCtx, cancel := context.WithTimeout(ctx, timeout*time.Duration(len(events)+1))
	defer cancel()
	tx, err := s.db.BeginTx(txCtx, nil)
	if err != nil {
		return 0, 0, wrapTimeout(err)
	}
	defer tx.Rollback() //nolint:errcheck
	for _, req := range events {
		id := req.ID
		if id == "" {
			id = uuid.New().String()
		}
		timestamp := req.Timestamp
		if timestamp == "" {
			timestamp = now
		}
		severity := req.Severity
		if severity == "" {
			severity = DefaultSeverityFor(req.EventType)
		}
		payloadStr := "{}"
		if req.Payload != nil {
			data, _ := json.Marshal(req.Payload)
			payloadStr = string(data)
		}
		result, err := tx.ExecContext(txCtx,
			`INSERT OR IGNORE INTO agent_audit_events
			(id, run_id, workspace, event_type, severity, node_id, session_id, timestamp, payload, ingested_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, runID, workspace, req.EventType, severity,
			req.NodeID, req.SessionID, timestamp, payloadStr, now,
		)
		if err != nil {
			return 0, 0, wrapTimeout(err)
		}
		affected, _ := result.RowsAffected()
		if affected > 0 {
			accepted++
		} else {
			duplicates++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, wrapTimeout(err)
	}
	return accepted, duplicates, nil
}

func (s *duckDBStore) InsertAgentTraceBatch(ctx context.Context, traces []PostTraceRequest, runID, workspace string) (accepted, duplicates int, err error) {
	now := apikit.NowUTC()
	timeout := writeTimeout()
	txCtx, cancel := context.WithTimeout(ctx, timeout*time.Duration(len(traces)+1))
	defer cancel()
	tx, err := s.db.BeginTx(txCtx, nil)
	if err != nil {
		return 0, 0, wrapTimeout(err)
	}
	defer tx.Rollback() //nolint:errcheck
	for _, req := range traces {
		id := req.ID
		if id == "" {
			id = uuid.New().String()
		}
		timestamp := req.Timestamp
		if timestamp == "" {
			timestamp = now
		}
		dataStr := "{}"
		if req.Data != nil {
			data, _ := json.Marshal(req.Data)
			dataStr = string(data)
		}
		result, err := tx.ExecContext(txCtx,
			`INSERT OR IGNORE INTO agent_traces
			(id, run_id, workspace, event_type, node_id, session_id, sequence, timestamp, data, ingested_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, runID, workspace, req.EventType, req.NodeID,
			req.SessionID, req.Sequence, timestamp, dataStr, now,
		)
		if err != nil {
			return 0, 0, wrapTimeout(err)
		}
		affected, _ := result.RowsAffected()
		if affected > 0 {
			accepted++
		} else {
			duplicates++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, wrapTimeout(err)
	}
	return accepted, duplicates, nil
}

// Query methods

func parseAuditQueryNorms(params QueryParams) (string, int) {
	order := strings.ToLower(params.Order)
	if order != "desc" {
		order = "asc"
	}
	limit := params.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	return order, limit
}

func (s *duckDBStore) QueryAuditEvents(ctx context.Context, runID, workspace string, params QueryParams) ([]map[string]any, *string, bool, error) {
	order, limit := parseAuditQueryNorms(params)
	var conditions []string
	var args []any
	conditions = append(conditions, "run_id = ?")
	args = append(args, runID)
	conditions = append(conditions, "workspace = ?")
	args = append(args, workspace)
	if params.EventType != "" {
		conditions = append(conditions, "event_type = ?")
		args = append(args, params.EventType)
	}
	if params.Severity != "" {
		conditions = append(conditions, "severity = ?")
		args = append(args, params.Severity)
	}
	if params.NodeID != "" {
		conditions = append(conditions, "node_id = ?")
		args = append(args, params.NodeID)
	}
	if params.Since != "" {
		conditions = append(conditions, "timestamp >= CAST(? AS TIMESTAMPTZ)")
		args = append(args, params.Since)
	}
	if params.Until != "" {
		conditions = append(conditions, "timestamp <= CAST(? AS TIMESTAMPTZ)")
		args = append(args, params.Until)
	}
	if params.Cursor != "" {
		cursorTS, cursorID, err := decodeCursor(params.Cursor)
		if err != nil {
			return nil, nil, false, fmt.Errorf("invalid cursor: %w", err)
		}
		if order == "desc" {
			conditions = append(conditions, "(timestamp < CAST(? AS TIMESTAMPTZ) OR (timestamp = CAST(? AS TIMESTAMPTZ) AND id < ?))")
		} else {
			conditions = append(conditions, "(timestamp > CAST(? AS TIMESTAMPTZ) OR (timestamp = CAST(? AS TIMESTAMPTZ) AND id > ?))")
		}
		args = append(args, cursorTS, cursorTS, cursorID)
	}
	where := "WHERE " + strings.Join(conditions, " AND ")
	query := fmt.Sprintf(
		`SELECT id, run_id, workspace, event_type, severity, node_id, session_id,
		        timestamp, payload, ingested_at
		 FROM agent_audit_events %s
		 ORDER BY timestamp %s, id %s
		 LIMIT ?`, where, order, order,
	)
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, false, fmt.Errorf("query audit events: %w", err)
	}
	defer rows.Close()
	var results []map[string]any
	for rows.Next() {
		var (
			id, rID, ws, eventType, severity, nodeID, sessionID string
			ts                                                  time.Time
			payload, ingestedAt                                 string
		)
		if err := rows.Scan(&id, &rID, &ws, &eventType, &severity, &nodeID, &sessionID,
			&ts, &payload, &ingestedAt); err != nil {
			return nil, nil, false, fmt.Errorf("scan audit event: %w", err)
		}
		event := map[string]any{
			"id": id, "run_id": rID, "workspace": ws,
			"event_type": eventType, "severity": severity,
			"node_id": nodeID, "session_id": sessionID,
			"timestamp": ts.Format(time.RFC3339Nano),
			"payload": jsonStringToAny(payload),
			"ingested_at": ingestedAt,
		}
		results = append(results, event)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("iterate audit events: %w", err)
	}
	hasMore := len(results) > limit
	if hasMore {
		results = results[:limit]
	}
	var nextCursor *string
	if hasMore && len(results) > 0 {
		last := results[len(results)-1]
		c := encodeCursor(last["timestamp"].(string), last["id"].(string))
		nextCursor = &c
	}
	if results == nil {
		results = []map[string]any{}
	}
	return results, nextCursor, hasMore, nil
}

func (s *duckDBStore) QuerySessionOutcomes(ctx context.Context, runID, workspace string, params QueryParams) ([]map[string]any, *string, bool, error) {
	order, limit := parseAuditQueryNorms(params)
	var conditions []string
	var args []any
	conditions = append(conditions, "run_id = ?")
	args = append(args, runID)
	conditions = append(conditions, "workspace = ?")
	args = append(args, workspace)
	if params.NodeID != "" {
		conditions = append(conditions, "node_id = ?")
		args = append(args, params.NodeID)
	}
	if params.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, params.Status)
	}
	if params.Since != "" {
		conditions = append(conditions, "timestamp >= CAST(? AS TIMESTAMPTZ)")
		args = append(args, params.Since)
	}
	if params.Until != "" {
		conditions = append(conditions, "timestamp <= CAST(? AS TIMESTAMPTZ)")
		args = append(args, params.Until)
	}
	if params.Cursor != "" {
		cursorTS, cursorID, err := decodeCursor(params.Cursor)
		if err != nil {
			return nil, nil, false, fmt.Errorf("invalid cursor: %w", err)
		}
		if order == "desc" {
			conditions = append(conditions, "(timestamp < CAST(? AS TIMESTAMPTZ) OR (timestamp = CAST(? AS TIMESTAMPTZ) AND id < ?))")
		} else {
			conditions = append(conditions, "(timestamp > CAST(? AS TIMESTAMPTZ) OR (timestamp = CAST(? AS TIMESTAMPTZ) AND id > ?))")
		}
		args = append(args, cursorTS, cursorTS, cursorID)
	}
	where := "WHERE " + strings.Join(conditions, " AND ")
	query := fmt.Sprintf(
		`SELECT id, run_id, workspace, session_id, node_id, status,
		        timestamp, duration_ms, token_usage, ingested_at
		 FROM session_outcomes %s
		 ORDER BY timestamp %s, id %s
		 LIMIT ?`, where, order, order,
	)
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, false, fmt.Errorf("query session outcomes: %w", err)
	}
	defer rows.Close()
	var results []map[string]any
	for rows.Next() {
		var (
			id, rID, ws, sessionID, nodeID, status string
			ts                                     time.Time
			durationMs                             int
			tokenUsage, ingestedAt                 string
		)
		if err := rows.Scan(&id, &rID, &ws, &sessionID, &nodeID, &status,
			&ts, &durationMs, &tokenUsage, &ingestedAt); err != nil {
			return nil, nil, false, fmt.Errorf("scan session outcome: %w", err)
		}
		outcome := map[string]any{
			"id": id, "run_id": rID, "workspace": ws,
			"session_id": sessionID, "node_id": nodeID, "status": status,
			"timestamp": ts.Format(time.RFC3339Nano),
			"duration_ms": durationMs, "token_usage": jsonStringToAny(tokenUsage),
			"ingested_at": ingestedAt,
		}
		results = append(results, outcome)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("iterate session outcomes: %w", err)
	}
	hasMore := len(results) > limit
	if hasMore {
		results = results[:limit]
	}
	var nextCursor *string
	if hasMore && len(results) > 0 {
		last := results[len(results)-1]
		c := encodeCursor(last["timestamp"].(string), last["id"].(string))
		nextCursor = &c
	}
	if results == nil {
		results = []map[string]any{}
	}
	return results, nextCursor, hasMore, nil
}

func (s *duckDBStore) QueryToolCalls(ctx context.Context, runID, workspace string, params QueryParams) ([]map[string]any, *string, bool, error) {
	order, limit := parseAuditQueryNorms(params)
	var conditions []string
	var args []any
	conditions = append(conditions, "run_id = ?")
	args = append(args, runID)
	conditions = append(conditions, "workspace = ?")
	args = append(args, workspace)
	if params.NodeID != "" {
		conditions = append(conditions, "node_id = ?")
		args = append(args, params.NodeID)
	}
	if params.SessionID != "" {
		conditions = append(conditions, "session_id = ?")
		args = append(args, params.SessionID)
	}
	if params.ToolName != "" {
		conditions = append(conditions, "tool_name = ?")
		args = append(args, params.ToolName)
	}
	if params.Since != "" {
		conditions = append(conditions, "timestamp >= CAST(? AS TIMESTAMPTZ)")
		args = append(args, params.Since)
	}
	if params.Until != "" {
		conditions = append(conditions, "timestamp <= CAST(? AS TIMESTAMPTZ)")
		args = append(args, params.Until)
	}
	if params.Cursor != "" {
		cursorTS, cursorID, err := decodeCursor(params.Cursor)
		if err != nil {
			return nil, nil, false, fmt.Errorf("invalid cursor: %w", err)
		}
		if order == "desc" {
			conditions = append(conditions, "(timestamp < CAST(? AS TIMESTAMPTZ) OR (timestamp = CAST(? AS TIMESTAMPTZ) AND id < ?))")
		} else {
			conditions = append(conditions, "(timestamp > CAST(? AS TIMESTAMPTZ) OR (timestamp = CAST(? AS TIMESTAMPTZ) AND id > ?))")
		}
		args = append(args, cursorTS, cursorTS, cursorID)
	}
	where := "WHERE " + strings.Join(conditions, " AND ")
	query := fmt.Sprintf(
		`SELECT id, run_id, workspace, tool_name, node_id, session_id,
		        timestamp, duration_ms, input, output, ingested_at
		 FROM tool_calls %s
		 ORDER BY timestamp %s, id %s
		 LIMIT ?`, where, order, order,
	)
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, false, fmt.Errorf("query tool calls: %w", err)
	}
	defer rows.Close()
	var results []map[string]any
	for rows.Next() {
		var (
			id, rID, ws, toolName, nodeID, sessionID string
			ts                                       time.Time
			durationMs                               int
			input, output, ingestedAt                string
		)
		if err := rows.Scan(&id, &rID, &ws, &toolName, &nodeID, &sessionID,
			&ts, &durationMs, &input, &output, &ingestedAt); err != nil {
			return nil, nil, false, fmt.Errorf("scan tool call: %w", err)
		}
		call := map[string]any{
			"id": id, "run_id": rID, "workspace": ws,
			"tool_name": toolName, "node_id": nodeID, "session_id": sessionID,
			"timestamp": ts.Format(time.RFC3339Nano),
			"duration_ms": durationMs,
			"input": jsonStringToAny(input), "output": jsonStringToAny(output),
			"ingested_at": ingestedAt,
		}
		results = append(results, call)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("iterate tool calls: %w", err)
	}
	hasMore := len(results) > limit
	if hasMore {
		results = results[:limit]
	}
	var nextCursor *string
	if hasMore && len(results) > 0 {
		last := results[len(results)-1]
		c := encodeCursor(last["timestamp"].(string), last["id"].(string))
		nextCursor = &c
	}
	if results == nil {
		results = []map[string]any{}
	}
	return results, nextCursor, hasMore, nil
}

func (s *duckDBStore) QueryToolErrors(ctx context.Context, runID, workspace string, params QueryParams) ([]map[string]any, *string, bool, error) {
	order, limit := parseAuditQueryNorms(params)
	var conditions []string
	var args []any
	conditions = append(conditions, "run_id = ?")
	args = append(args, runID)
	conditions = append(conditions, "workspace = ?")
	args = append(args, workspace)
	if params.NodeID != "" {
		conditions = append(conditions, "node_id = ?")
		args = append(args, params.NodeID)
	}
	if params.SessionID != "" {
		conditions = append(conditions, "session_id = ?")
		args = append(args, params.SessionID)
	}
	if params.ToolName != "" {
		conditions = append(conditions, "tool_name = ?")
		args = append(args, params.ToolName)
	}
	if params.Since != "" {
		conditions = append(conditions, "timestamp >= CAST(? AS TIMESTAMPTZ)")
		args = append(args, params.Since)
	}
	if params.Until != "" {
		conditions = append(conditions, "timestamp <= CAST(? AS TIMESTAMPTZ)")
		args = append(args, params.Until)
	}
	if params.Cursor != "" {
		cursorTS, cursorID, err := decodeCursor(params.Cursor)
		if err != nil {
			return nil, nil, false, fmt.Errorf("invalid cursor: %w", err)
		}
		if order == "desc" {
			conditions = append(conditions, "(timestamp < CAST(? AS TIMESTAMPTZ) OR (timestamp = CAST(? AS TIMESTAMPTZ) AND id < ?))")
		} else {
			conditions = append(conditions, "(timestamp > CAST(? AS TIMESTAMPTZ) OR (timestamp = CAST(? AS TIMESTAMPTZ) AND id > ?))")
		}
		args = append(args, cursorTS, cursorTS, cursorID)
	}
	where := "WHERE " + strings.Join(conditions, " AND ")
	query := fmt.Sprintf(
		`SELECT id, run_id, workspace, tool_name, node_id, session_id,
		        error_code, error_msg, timestamp, ingested_at
		 FROM tool_errors %s
		 ORDER BY timestamp %s, id %s
		 LIMIT ?`, where, order, order,
	)
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, false, fmt.Errorf("query tool errors: %w", err)
	}
	defer rows.Close()
	var results []map[string]any
	for rows.Next() {
		var (
			id, rID, ws, toolName, nodeID, sessionID string
			errorCode, errorMsg                      string
			ts                                       time.Time
			ingestedAt                               string
		)
		if err := rows.Scan(&id, &rID, &ws, &toolName, &nodeID, &sessionID,
			&errorCode, &errorMsg, &ts, &ingestedAt); err != nil {
			return nil, nil, false, fmt.Errorf("scan tool error: %w", err)
		}
		toolErr := map[string]any{
			"id": id, "run_id": rID, "workspace": ws,
			"tool_name": toolName, "node_id": nodeID, "session_id": sessionID,
			"error_code": errorCode, "error_msg": errorMsg,
			"timestamp": ts.Format(time.RFC3339Nano),
			"ingested_at": ingestedAt,
		}
		results = append(results, toolErr)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("iterate tool errors: %w", err)
	}
	hasMore := len(results) > limit
	if hasMore {
		results = results[:limit]
	}
	var nextCursor *string
	if hasMore && len(results) > 0 {
		last := results[len(results)-1]
		c := encodeCursor(last["timestamp"].(string), last["id"].(string))
		nextCursor = &c
	}
	if results == nil {
		results = []map[string]any{}
	}
	return results, nextCursor, hasMore, nil
}

func (s *duckDBStore) QueryAgentTraces(ctx context.Context, runID, workspace string, params QueryParams) ([]map[string]any, *string, bool, error) {
	order, limit := parseAuditQueryNorms(params)
	var conditions []string
	var args []any
	conditions = append(conditions, "run_id = ?")
	args = append(args, runID)
	conditions = append(conditions, "workspace = ?")
	args = append(args, workspace)
	if params.EventType != "" {
		conditions = append(conditions, "event_type = ?")
		args = append(args, params.EventType)
	}
	if params.NodeID != "" {
		conditions = append(conditions, "node_id = ?")
		args = append(args, params.NodeID)
	}
	if params.Since != "" {
		conditions = append(conditions, "timestamp >= CAST(? AS TIMESTAMPTZ)")
		args = append(args, params.Since)
	}
	if params.Until != "" {
		conditions = append(conditions, "timestamp <= CAST(? AS TIMESTAMPTZ)")
		args = append(args, params.Until)
	}
	if params.Cursor != "" {
		cursorTS, cursorID, err := decodeCursor(params.Cursor)
		if err != nil {
			return nil, nil, false, fmt.Errorf("invalid cursor: %w", err)
		}
		if order == "desc" {
			conditions = append(conditions, "(timestamp < CAST(? AS TIMESTAMPTZ) OR (timestamp = CAST(? AS TIMESTAMPTZ) AND id < ?))")
		} else {
			conditions = append(conditions, "(timestamp > CAST(? AS TIMESTAMPTZ) OR (timestamp = CAST(? AS TIMESTAMPTZ) AND id > ?))")
		}
		args = append(args, cursorTS, cursorTS, cursorID)
	}
	where := "WHERE " + strings.Join(conditions, " AND ")
	query := fmt.Sprintf(
		`SELECT id, run_id, workspace, event_type, node_id, session_id,
		        sequence, timestamp, data, ingested_at
		 FROM agent_traces %s
		 ORDER BY timestamp %s, id %s
		 LIMIT ?`, where, order, order,
	)
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, false, fmt.Errorf("query agent traces: %w", err)
	}
	defer rows.Close()
	var results []map[string]any
	for rows.Next() {
		var (
			id, rID, ws, eventType, nodeID, sessionID string
			seq                                       int
			ts                                        time.Time
			data, ingestedAt                          string
		)
		if err := rows.Scan(&id, &rID, &ws, &eventType, &nodeID, &sessionID,
			&seq, &ts, &data, &ingestedAt); err != nil {
			return nil, nil, false, fmt.Errorf("scan agent trace: %w", err)
		}
		trace := map[string]any{
			"id": id, "run_id": rID, "workspace": ws,
			"event_type": eventType, "node_id": nodeID, "session_id": sessionID,
			"sequence": seq, "timestamp": ts.Format(time.RFC3339Nano),
			"data": jsonStringToAny(data), "ingested_at": ingestedAt,
		}
		results = append(results, trace)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("iterate agent traces: %w", err)
	}
	hasMore := len(results) > limit
	if hasMore {
		results = results[:limit]
	}
	var nextCursor *string
	if hasMore && len(results) > 0 {
		last := results[len(results)-1]
		c := encodeCursor(last["timestamp"].(string), last["id"].(string))
		nextCursor = &c
	}
	if results == nil {
		results = []map[string]any{}
	}
	return results, nextCursor, hasMore, nil
}

func (s *duckDBStore) GetPostmortem(ctx context.Context, runID string) (map[string]any, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT run_id, workspace, schema_version, run_status,
		        started_at, completed_at,
		        task_summary, cost_summary, blocked_tasks, session_history,
		        ingested_at
		 FROM postmortems WHERE run_id = ?`, runID,
	)
	var (
		rID, ws, runStatus                                     string
		schemaVersion                                          int
		startedAt, completedAt                                 time.Time
		taskSummary, costSummary, blockedTasks, sessionHistory string
		ingestedAt                                             string
	)
	err := row.Scan(&rID, &ws, &schemaVersion, &runStatus,
		&startedAt, &completedAt,
		&taskSummary, &costSummary, &blockedTasks, &sessionHistory,
		&ingestedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get postmortem: %w", err)
	}
	result := map[string]any{
		"run_id":          rID,
		"workspace":       ws,
		"schema_version":  schemaVersion,
		"run_status":      runStatus,
		"started_at":      startedAt.Format(time.RFC3339Nano),
		"completed_at":    completedAt.Format(time.RFC3339Nano),
		"task_summary":    jsonStringToAny(taskSummary),
		"cost_summary":    jsonStringToAny(costSummary),
		"blocked_tasks":   jsonStringToAny(blockedTasks),
		"session_history": jsonStringToAny(sessionHistory),
		"ingested_at":     ingestedAt,
	}
	return result, nil
}

// Helpers

func jsonStringToAny(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}
