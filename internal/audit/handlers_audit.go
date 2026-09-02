package audit

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// handlePostEventImpl handles POST /workspaces/:slug/runs/:run_id/events.
func handlePostEventImpl(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditWrite(c)
		if auth == nil {
			return nil
		}

		slug := c.Param("slug")
		if err := checkWorkspaceAccess(c, auth, slug, sqliteDB); err != nil {
			return nil
		}

		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}

		var req PostEventRequest
		if err := c.Bind(&req); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}

		if req.EventType == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "missing required field: event_type")
		}

		if req.RunID != "" && req.RunID != runID {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "run_id mismatch between URL path and request body")
		}

		if req.ID != "" {
			if err := ValidateUUID(req.ID); err != nil {
				return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
			}
		}

		if req.Severity != "" {
			if err := ValidateSeverity(req.Severity); err != nil {
				return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
			}
		}

		// Validate payload is a JSON object (map) if present.
		if req.Payload != nil {
			if _, ok := req.Payload.(map[string]any); !ok {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid payload: must be a JSON object")
			}
		}

		if req.EventType != "" && !isKnownAuditEventType(req.EventType) {
			slog.Warn("audit: unknown event_type", "event_type", req.EventType)
		}

		ds := store.(*duckDBStore)
		resp, isNew, err := ds.InsertAuditEvent(c.Request().Context(), req, runID, slug)
		if err != nil {
			return writeStoreError(c, err)
		}

		if isNew {
			return c.JSON(http.StatusCreated, resp)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handlePostSessionOutcomeImpl handles POST /workspaces/:slug/runs/:run_id/sessions/outcomes.
func handlePostSessionOutcomeImpl(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditWrite(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		if err := checkWorkspaceAccess(c, auth, slug, sqliteDB); err != nil {
			return nil
		}
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		var req PostSessionOutcomeRequest
		if err := c.Bind(&req); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}
		if req.SessionID == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "missing required field: session_id")
		}
		if req.Status == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "missing required field: status")
		}
		if req.ID != "" {
			if err := ValidateUUID(req.ID); err != nil {
				return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
			}
		}
		ds := store.(*duckDBStore)
		resp, isNew, err := ds.InsertSessionOutcome(c.Request().Context(), req, runID, slug)
		if err != nil {
			return writeStoreError(c, err)
		}
		if isNew {
			return c.JSON(http.StatusCreated, resp)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handlePostToolCallImpl handles POST /workspaces/:slug/runs/:run_id/tools/calls.
func handlePostToolCallImpl(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditWrite(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		if err := checkWorkspaceAccess(c, auth, slug, sqliteDB); err != nil {
			return nil
		}
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		var req PostToolCallRequest
		if err := c.Bind(&req); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}
		if req.ToolName == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "missing required field: tool_name")
		}
		if req.ID != "" {
			if err := ValidateUUID(req.ID); err != nil {
				return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
			}
		}
		ds := store.(*duckDBStore)
		resp, isNew, err := ds.InsertToolCall(c.Request().Context(), req, runID, slug)
		if err != nil {
			return writeStoreError(c, err)
		}
		if isNew {
			return c.JSON(http.StatusCreated, resp)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handlePostToolErrorImpl handles POST /workspaces/:slug/runs/:run_id/tools/errors.
func handlePostToolErrorImpl(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditWrite(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		if err := checkWorkspaceAccess(c, auth, slug, sqliteDB); err != nil {
			return nil
		}
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		var req PostToolErrorRequest
		if err := c.Bind(&req); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}
		if req.ToolName == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "missing required field: tool_name")
		}
		if req.ErrorMsg == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "missing required field: error_msg")
		}
		if req.ID != "" {
			if err := ValidateUUID(req.ID); err != nil {
				return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
			}
		}
		ds := store.(*duckDBStore)
		resp, isNew, err := ds.InsertToolError(c.Request().Context(), req, runID, slug)
		if err != nil {
			return writeStoreError(c, err)
		}
		if isNew {
			return c.JSON(http.StatusCreated, resp)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handlePostTraceImpl handles POST /workspaces/:slug/runs/:run_id/traces.
func handlePostTraceImpl(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditWrite(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		if err := checkWorkspaceAccess(c, auth, slug, sqliteDB); err != nil {
			return nil
		}
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		var req PostTraceRequest
		if err := c.Bind(&req); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}
		if req.EventType == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "missing required field: event_type")
		}
		if err := ValidateTraceEventType(req.EventType); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		if req.ID != "" {
			if err := ValidateUUID(req.ID); err != nil {
				return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
			}
		}
		ds := store.(*duckDBStore)
		resp, isNew, err := ds.InsertAgentTrace(c.Request().Context(), req, runID, slug)
		if err != nil {
			return writeStoreError(c, err)
		}
		if isNew {
			return c.JSON(http.StatusCreated, resp)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handlePostPostmortemImpl handles POST /workspaces/:slug/runs/:run_id/postmortem.
func handlePostPostmortemImpl(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditWrite(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		if err := checkWorkspaceAccess(c, auth, slug, sqliteDB); err != nil {
			return nil
		}
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		var req PostPostmortemRequest
		if err := c.Bind(&req); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}
		if req.SchemaVersion != nil && *req.SchemaVersion != 1 {
			return apikit.WriteAPIErrorWithType(c, http.StatusUnprocessableEntity,
				"unsupported schema_version", "unknown_schema_version")
		}
		if req.RunStatus == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "missing required field: run_status")
		}
		if !validRunStatuses[req.RunStatus] {
			return apikit.WriteAPIError(c, http.StatusBadRequest,
				"invalid run_status: must be one of stalled, block_limit, cost_limit, session_limit")
		}
		if req.TaskSummary == nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "missing required field: task_summary")
		}
		if req.CostSummary == nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "missing required field: cost_summary")
		}
		if req.StartedAt == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "missing required field: started_at")
		}
		if req.CompletedAt == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "missing required field: completed_at")
		}
		// Validate task_summary has required fields.
		if tsMap, ok := req.TaskSummary.(map[string]any); ok {
			if _, exists := tsMap["total"]; !exists {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "task_summary missing required field: total")
			}
		}
		ds := store.(*duckDBStore)
		resp, isNew, err := ds.InsertPostmortem(c.Request().Context(), req, runID, slug)
		if err != nil {
			return writeStoreError(c, err)
		}
		if isNew {
			return c.JSON(http.StatusCreated, resp)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handleGetPostmortemImpl handles GET /workspaces/:slug/runs/:run_id/postmortem.
func handleGetPostmortemImpl(store Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditRead(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		ds := store.(*duckDBStore)
		result, err := ds.GetPostmortem(c.Request().Context(), runID, slug)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}
		if result == nil {
			return apikit.WriteAPIErrorWithType(c, http.StatusNotFound,
				"no postmortem found for the given run_id", "postmortem_not_found")
		}
		return c.JSON(http.StatusOK, result)
	}
}

// handlePostEventsBatchImpl handles POST /workspaces/:slug/runs/:run_id/events/batch.
func handlePostEventsBatchImpl(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditWrite(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		if err := checkWorkspaceAccess(c, auth, slug, sqliteDB); err != nil {
			return nil
		}
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		var batch []PostEventRequest
		if err := c.Bind(&batch); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}
		if len(batch) == 0 {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "batch array must not be empty")
		}
		if len(batch) > 1000 {
			return apikit.WriteAPIError(c, http.StatusRequestEntityTooLarge, "batch exceeds maximum of 1000 items")
		}
		var validItems []PostEventRequest
		var batchErrors []BatchItemError
		for i, req := range batch {
			if req.EventType == "" {
				batchErrors = append(batchErrors, BatchItemError{Index: i, ID: req.ID, Message: "missing required field: event_type"})
				continue
			}
			if req.ID != "" {
				if err := ValidateUUID(req.ID); err != nil {
					batchErrors = append(batchErrors, BatchItemError{Index: i, ID: req.ID, Message: err.Error()})
					continue
				}
			}
			if req.Severity != "" {
				if err := ValidateSeverity(req.Severity); err != nil {
					batchErrors = append(batchErrors, BatchItemError{Index: i, ID: req.ID, Message: err.Error()})
					continue
				}
			}
			validItems = append(validItems, req)
		}
		resp := BatchIngestResponse{Errors: batchErrors}
		if resp.Errors == nil {
			resp.Errors = []BatchItemError{}
		}
		if len(validItems) == 0 {
			return c.JSON(http.StatusOK, resp)
		}
		ds := store.(*duckDBStore)
		accepted, duplicates, err := ds.InsertAuditEventBatch(c.Request().Context(), validItems, runID, slug)
		if err != nil {
			return writeStoreError(c, err)
		}
		resp.Accepted = accepted
		resp.Duplicates = duplicates
		return c.JSON(http.StatusOK, resp)
	}
}

// handlePostTracesBatchImpl handles POST /workspaces/:slug/runs/:run_id/traces/batch.
func handlePostTracesBatchImpl(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditWrite(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		if err := checkWorkspaceAccess(c, auth, slug, sqliteDB); err != nil {
			return nil
		}
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		var batch []PostTraceRequest
		if err := c.Bind(&batch); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}
		if len(batch) == 0 {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "batch array must not be empty")
		}
		if len(batch) > 1000 {
			return apikit.WriteAPIError(c, http.StatusRequestEntityTooLarge, "batch exceeds maximum of 1000 items")
		}
		var validItems []PostTraceRequest
		var batchErrors []BatchItemError
		for i, req := range batch {
			if req.EventType == "" {
				batchErrors = append(batchErrors, BatchItemError{Index: i, ID: req.ID, Message: "missing required field: event_type"})
				continue
			}
			if err := ValidateTraceEventType(req.EventType); err != nil {
				batchErrors = append(batchErrors, BatchItemError{Index: i, ID: req.ID, Message: err.Error()})
				continue
			}
			if req.ID != "" {
				if err := ValidateUUID(req.ID); err != nil {
					batchErrors = append(batchErrors, BatchItemError{Index: i, ID: req.ID, Message: err.Error()})
					continue
				}
			}
			validItems = append(validItems, req)
		}
		resp := BatchIngestResponse{Errors: batchErrors}
		if resp.Errors == nil {
			resp.Errors = []BatchItemError{}
		}
		if len(validItems) == 0 {
			return c.JSON(http.StatusOK, resp)
		}
		ds := store.(*duckDBStore)
		accepted, duplicates, err := ds.InsertAgentTraceBatch(c.Request().Context(), validItems, runID, slug)
		if err != nil {
			return writeStoreError(c, err)
		}
		resp.Accepted = accepted
		resp.Duplicates = duplicates
		return c.JSON(http.StatusOK, resp)
	}
}

// ---------------------------------------------------------------------------
// Audit query handlers
// ---------------------------------------------------------------------------

// handleGetEventsImpl handles GET /workspaces/:slug/runs/:run_id/events.
func handleGetEventsImpl(store Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditRead(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		params, err := parseAuditQueryParamsFromRequest(c)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		ds := store.(*duckDBStore)
		events, nextCursor, hasMore, qerr := ds.QueryAuditEvents(c.Request().Context(), runID, slug, params)
		if qerr != nil {
			if strings.Contains(qerr.Error(), "invalid cursor") {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid cursor")
			}
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}
		return c.JSON(http.StatusOK, map[string]any{
			"events":      events,
			"next_cursor": nextCursor,
			"has_more":    hasMore,
		})
	}
}

// handleGetSessionOutcomesImpl handles GET /workspaces/:slug/runs/:run_id/sessions/outcomes.
func handleGetSessionOutcomesImpl(store Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditRead(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		params, err := parseAuditQueryParamsFromRequest(c)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		ds := store.(*duckDBStore)
		outcomes, nextCursor, hasMore, qerr := ds.QuerySessionOutcomes(c.Request().Context(), runID, slug, params)
		if qerr != nil {
			if strings.Contains(qerr.Error(), "invalid cursor") {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid cursor")
			}
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}
		return c.JSON(http.StatusOK, map[string]any{
			"outcomes":    outcomes,
			"next_cursor": nextCursor,
			"has_more":    hasMore,
		})
	}
}

// handleGetToolCallsImpl handles GET /workspaces/:slug/runs/:run_id/tools/calls.
func handleGetToolCallsImpl(store Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditRead(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		params, err := parseAuditQueryParamsFromRequest(c)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		ds := store.(*duckDBStore)
		calls, nextCursor, hasMore, qerr := ds.QueryToolCalls(c.Request().Context(), runID, slug, params)
		if qerr != nil {
			if strings.Contains(qerr.Error(), "invalid cursor") {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid cursor")
			}
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}
		return c.JSON(http.StatusOK, map[string]any{
			"calls":       calls,
			"next_cursor": nextCursor,
			"has_more":    hasMore,
		})
	}
}

// handleGetToolErrorsImpl handles GET /workspaces/:slug/runs/:run_id/tools/errors.
func handleGetToolErrorsImpl(store Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditRead(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		params, err := parseAuditQueryParamsFromRequest(c)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		ds := store.(*duckDBStore)
		toolErrors, nextCursor, hasMore, qerr := ds.QueryToolErrors(c.Request().Context(), runID, slug, params)
		if qerr != nil {
			if strings.Contains(qerr.Error(), "invalid cursor") {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid cursor")
			}
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}
		return c.JSON(http.StatusOK, map[string]any{
			"errors":      toolErrors,
			"next_cursor": nextCursor,
			"has_more":    hasMore,
		})
	}
}

// handleGetTracesImpl handles GET /workspaces/:slug/runs/:run_id/traces.
func handleGetTracesImpl(store Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requireAuditRead(c)
		if auth == nil {
			return nil
		}
		slug := c.Param("slug")
		runID := c.Param("run_id")
		if err := ValidateRunIDErr(runID); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		params, err := parseAuditQueryParamsFromRequest(c)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, err.Error())
		}
		ds := store.(*duckDBStore)
		traces, nextCursor, hasMore, qerr := ds.QueryAgentTraces(c.Request().Context(), runID, slug, params)
		if qerr != nil {
			if strings.Contains(qerr.Error(), "invalid cursor") {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid cursor")
			}
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}
		return c.JSON(http.StatusOK, map[string]any{
			"traces":      traces,
			"next_cursor": nextCursor,
			"has_more":    hasMore,
		})
	}
}

// ---------------------------------------------------------------------------
// Shared handler helpers
// ---------------------------------------------------------------------------

// parseAuditQueryParamsFromRequest extracts QueryParams from the echo.Context.
func parseAuditQueryParamsFromRequest(c echo.Context) (QueryParams, error) {
	params := QueryParams{
		EventType: c.QueryParam("event_type"),
		Severity:  c.QueryParam("severity"),
		NodeID:    c.QueryParam("node_id"),
		SessionID: c.QueryParam("session_id"),
		ToolName:  c.QueryParam("tool_name"),
		Status:    c.QueryParam("status"),
		Since:     c.QueryParam("since"),
		Until:     c.QueryParam("until"),
		Order:     c.QueryParam("order"),
		Cursor:    c.QueryParam("cursor"),
	}
	if limitStr := c.QueryParam("limit"); limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err == nil {
			params.Limit = l
		}
	}
	// Validate cursor if present.
	if params.Cursor != "" {
		if _, _, err := decodeCursor(params.Cursor); err != nil {
			return QueryParams{}, fmt.Errorf("invalid cursor")
		}
	}
	// Validate since/until if present.
	if params.Since != "" {
		if _, err := time.Parse(time.RFC3339, params.Since); err != nil {
			if _, err2 := time.Parse(time.RFC3339Nano, params.Since); err2 != nil {
				return QueryParams{}, fmt.Errorf("invalid since timestamp")
			}
		}
	}
	if params.Until != "" {
		if _, err := time.Parse(time.RFC3339, params.Until); err != nil {
			if _, err2 := time.Parse(time.RFC3339Nano, params.Until); err2 != nil {
				return QueryParams{}, fmt.Errorf("invalid until timestamp")
			}
		}
	}
	return params, nil
}

// writeStoreError converts store errors into HTTP responses.
func writeStoreError(c echo.Context, err error) error {
	var wte *WriteTimeoutError
	if errors.As(err, &wte) {
		c.Response().Header().Set("Retry-After", "5")
		return apikit.WriteAPIError(c, http.StatusServiceUnavailable, "write timeout: retry after 5 seconds")
	}
	return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
}

// isKnownAuditEventType checks if an audit event type is known. Unknown
// types are accepted but logged as warnings.
func isKnownAuditEventType(et string) bool {
	switch et {
	case "session.start", "session.end", "session.fail",
		"run.limit_reached", "git.conflict", "harvest.empty",
		"review.parse_failure":
		return true
	}
	return false
}
