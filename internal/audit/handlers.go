package audit

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// handleCreateSession handles POST /api/v1/sessions.
// Creates a new agent session or returns existing if id is duplicate.
func handleCreateSession(store Store, metrics *Metrics) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if !hasSessionsWrite(auth) {
			return apikit.WriteAPIError(c, http.StatusForbidden, "sessions:write scope required")
		}

		var req CreateSessionRequest
		if err := c.Bind(&req); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}

		if strings.TrimSpace(req.WorkspaceSlug) == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "workspace_slug is required")
		}

		if req.ID == "" {
			req.ID = uuid.New().String()
		}

		now := apikit.NowUTC()

		sess := &Session{
			ID:             req.ID,
			WorkspaceSlug:  req.WorkspaceSlug,
			RunID:          req.RunID,
			NodeID:         req.NodeID,
			Archetype:      req.Archetype,
			Model:          req.Model,
			Status:         "active",
			CredentialID:   auth.UserID,
			CredentialType: auth.CredentialType,
			StartedAt:      now,
			Metadata:       req.Metadata,
		}

		created, isNew, err := store.CreateSession(c.Request().Context(), sess)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		if isNew {
			// Increment active sessions gauge only for genuinely new sessions.
			if metrics != nil {
				metrics.AgentSessionsActive.WithLabelValues(req.WorkspaceSlug).Inc()
			}
			return c.JSON(http.StatusCreated, created)
		}
		return c.JSON(http.StatusOK, created)
	}
}

// handleCompleteSession handles POST /api/v1/sessions/:id/complete.
// Transitions an active session to a terminal state.
func handleCompleteSession(store Store, metrics *Metrics) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if !hasSessionsWrite(auth) {
			return apikit.WriteAPIError(c, http.StatusForbidden, "sessions:write scope required")
		}

		sessionID := c.Param("id")

		var req CompleteSessionRequest
		if err := c.Bind(&req); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}

		// Validate status — 'terminated' is not client-settable.
		switch req.Status {
		case "completed", "failed", "timeout":
			// valid
		case "terminated":
			return apikit.WriteAPIError(c, http.StatusBadRequest, "terminated is not a valid client-settable status")
		default:
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid status")
		}

		// Fetch session to check ownership.
		sess, err := store.GetSession(c.Request().Context(), sessionID)
		if errors.Is(err, ErrSessionNotFound) {
			return apikit.WriteAPIError(c, http.StatusNotFound, "session not found")
		}
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		// Ownership check: only session owner or admin can complete.
		if !isAdmin(auth) && auth.UserID != sess.CredentialID {
			return apikit.WriteAPIError(c, http.StatusForbidden, "not session owner")
		}

		// If session is already terminal, return idempotently (no gauge decrement).
		if isTerminalStatus(sess.Status) {
			return c.JSON(http.StatusOK, sess)
		}

		updated, err := store.CompleteSession(c.Request().Context(), sessionID, &req)
		if errors.Is(err, ErrSessionNotActive) {
			// Re-fetch to return current state.
			current, getErr := store.GetSession(c.Request().Context(), sessionID)
			if getErr != nil {
				return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
			}
			if isTerminalStatus(current.Status) {
				return c.JSON(http.StatusOK, current)
			}
			return apikit.WriteAPIError(c, http.StatusConflict, "session is not active")
		}
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		// Decrement active sessions gauge only on successful transition from active.
		if metrics != nil {
			metrics.AgentSessionsActive.WithLabelValues(sess.WorkspaceSlug).Dec()
		}

		return c.JSON(http.StatusOK, updated)
	}
}

// handleReportUsage handles POST /api/v1/sessions/:id/usage.
// Records incremental token usage for an active session.
func handleReportUsage(store Store, metrics *Metrics) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if !hasSessionsWrite(auth) {
			return apikit.WriteAPIError(c, http.StatusForbidden, "sessions:write scope required")
		}

		sessionID := c.Param("id")

		var req ReportUsageRequest
		if err := c.Bind(&req); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}

		// Validate model field.
		if strings.TrimSpace(req.Model) == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "model is required")
		}

		// Validate non-negative token counts.
		if req.InputTokens < 0 || req.OutputTokens < 0 || req.CacheReadTokens < 0 {
			return apikit.WriteAPIError(c, http.StatusUnprocessableEntity, "token counts must be non-negative")
		}

		// Fetch session.
		sess, err := store.GetSession(c.Request().Context(), sessionID)
		if errors.Is(err, ErrSessionNotFound) {
			return apikit.WriteAPIError(c, http.StatusNotFound, "session not found")
		}
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		// Ownership check.
		if !isAdmin(auth) && auth.UserID != sess.CredentialID {
			return apikit.WriteAPIError(c, http.StatusForbidden, "not session owner")
		}

		// Session must be active.
		if sess.Status != "active" {
			return apikit.WriteAPIError(c, http.StatusConflict, "session is not active")
		}

		if req.ID == "" {
			req.ID = uuid.New().String()
		}

		now := time.Now().UTC().Format(time.RFC3339Nano)

		usage := &TokenUsage{
			ID:              req.ID,
			SessionID:       sessionID,
			WorkspaceSlug:   sess.WorkspaceSlug,
			Model:           req.Model,
			InputTokens:     req.InputTokens,
			OutputTokens:    req.OutputTokens,
			CacheReadTokens: req.CacheReadTokens,
			ReportedAt:      now,
		}

		created, err := store.InsertTokenUsage(c.Request().Context(), usage)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		// Increment token usage counters by direction.
		if metrics != nil {
			ws := sess.WorkspaceSlug
			model := req.Model
			if req.InputTokens > 0 {
				metrics.AgentTokensTotal.WithLabelValues(ws, model, "input").Add(float64(req.InputTokens))
			}
			if req.OutputTokens > 0 {
				metrics.AgentTokensTotal.WithLabelValues(ws, model, "output").Add(float64(req.OutputTokens))
			}
			if req.CacheReadTokens > 0 {
				metrics.AgentTokensTotal.WithLabelValues(ws, model, "cache_read").Add(float64(req.CacheReadTokens))
			}
		}

		return c.JSON(http.StatusCreated, created)
	}
}

// handleListSessions handles GET /api/v1/sessions.
// Returns a paginated list of sessions with token summaries.
func handleListSessions(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if !hasSessionsRead(auth) {
			return apikit.WriteAPIError(c, http.StatusForbidden, "sessions:read scope required")
		}

		params := SessionListParams{
			WorkspaceSlug:  c.QueryParam("workspace_slug"),
			RunID:          c.QueryParam("run_id"),
			Status:         c.QueryParam("status"),
			CredentialType: c.QueryParam("credential_type"),
			Since:          c.QueryParam("since"),
			Order:          c.QueryParam("order"),
			Cursor:         c.QueryParam("cursor"),
		}

		if limitStr := c.QueryParam("limit"); limitStr != "" {
			l, err := strconv.Atoi(limitStr)
			if err == nil {
				params.Limit = l
			}
		}

		// Validate cursor before passing to store.
		if params.Cursor != "" {
			if _, _, err := decodeCursor(params.Cursor); err != nil {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid cursor")
			}
		}

		// Workspace access control.
		var accessibleWorkspaces []string
		if !isAdmin(auth) {
			if params.WorkspaceSlug != "" {
				// Check explicit workspace access.
				if sqliteDB != nil {
					if !hasWorkspaceAccess(sqliteDB, auth, params.WorkspaceSlug) {
						return apikit.WriteAPIError(c, http.StatusForbidden, "workspace access denied")
					}
				}
			} else if sqliteDB != nil {
				// Get list of accessible workspaces.
				ws, err := getAccessibleWorkspaces(sqliteDB, auth)
				if err != nil {
					return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
				}
				accessibleWorkspaces = ws
			}
		}

		resp, err := store.ListSessions(c.Request().Context(), params, accessibleWorkspaces)
		if err != nil {
			if strings.Contains(err.Error(), "invalid cursor") {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid cursor")
			}
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		return c.JSON(http.StatusOK, resp)
	}
}

// handleGetSession handles GET /api/v1/sessions/:id.
// Returns a single session with its token summary.
func handleGetSession(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if !hasSessionsRead(auth) {
			return apikit.WriteAPIError(c, http.StatusForbidden, "sessions:read scope required")
		}

		sessionID := c.Param("id")

		sess, err := store.GetSessionWithSummary(c.Request().Context(), sessionID)
		if errors.Is(err, ErrSessionNotFound) {
			return apikit.WriteAPIError(c, http.StatusNotFound, "session not found")
		}
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		// Workspace access check for non-admin.
		if !isAdmin(auth) && sqliteDB != nil {
			if !hasWorkspaceAccess(sqliteDB, auth, sess.WorkspaceSlug) {
				return apikit.WriteAPIError(c, http.StatusForbidden, "workspace access denied")
			}
		}

		return c.JSON(http.StatusOK, sess)
	}
}

// handleListSessionUsage handles GET /api/v1/sessions/:id/usage.
// Returns paginated token usage records and unbounded totals.
func handleListSessionUsage(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if !hasSessionsRead(auth) {
			return apikit.WriteAPIError(c, http.StatusForbidden, "sessions:read scope required")
		}

		sessionID := c.Param("id")

		// Verify session exists and check access.
		sess, err := store.GetSession(c.Request().Context(), sessionID)
		if errors.Is(err, ErrSessionNotFound) {
			return apikit.WriteAPIError(c, http.StatusNotFound, "session not found")
		}
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		// Workspace access check for non-admin.
		if !isAdmin(auth) && sqliteDB != nil {
			if !hasWorkspaceAccess(sqliteDB, auth, sess.WorkspaceSlug) {
				return apikit.WriteAPIError(c, http.StatusForbidden, "workspace access denied")
			}
		}

		params := UsageListParams{
			Order:  c.QueryParam("order"),
			Cursor: c.QueryParam("cursor"),
		}

		if limitStr := c.QueryParam("limit"); limitStr != "" {
			l, err := strconv.Atoi(limitStr)
			if err == nil {
				params.Limit = l
			}
		}

		// Validate cursor before passing to store.
		if params.Cursor != "" {
			if _, _, err := decodeCursor(params.Cursor); err != nil {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid cursor")
			}
		}

		resp, err := store.ListTokenUsage(c.Request().Context(), sessionID, params)
		if errors.Is(err, ErrSessionNotFound) {
			return apikit.WriteAPIError(c, http.StatusNotFound, "session not found")
		}
		if err != nil {
			if strings.Contains(err.Error(), "invalid cursor") {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid cursor")
			}
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		return c.JSON(http.StatusOK, resp)
	}
}

// handleWorkspaceCost handles GET /api/v1/workspaces/:slug/cost.
// Returns aggregated token usage grouped by day, session, or model.
func handleWorkspaceCost(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if !hasSessionsRead(auth) {
			return apikit.WriteAPIError(c, http.StatusForbidden, "sessions:read scope required")
		}

		slug := c.Param("slug")

		// Workspace access check for non-admin.
		if !isAdmin(auth) {
			if sqliteDB != nil {
				if !hasWorkspaceAccess(sqliteDB, auth, slug) {
					return apikit.WriteAPIError(c, http.StatusForbidden, "workspace access denied")
				}
			} else {
				// Without SQLite, non-admin users have no way to verify workspace access.
				return apikit.WriteAPIError(c, http.StatusForbidden, "workspace access denied")
			}
		}

		// Parse and validate group_by.
		groupBy := c.QueryParam("group_by")
		if groupBy == "" {
			groupBy = "day"
		}
		switch groupBy {
		case "day", "session", "model":
			// valid
		default:
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid group_by value")
		}

		// Parse time range.
		now := time.Now().UTC()
		sinceStr := c.QueryParam("since")
		untilStr := c.QueryParam("until")

		if sinceStr == "" {
			sinceStr = now.Add(-24 * time.Hour).Format(time.RFC3339)
		}
		if untilStr == "" {
			untilStr = now.Format(time.RFC3339)
		}

		sinceTime, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid since timestamp")
		}
		untilTime, err := time.Parse(time.RFC3339, untilStr)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid until timestamp")
		}

		if !sinceTime.Before(untilTime) {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "since must be before until")
		}

		params := CostParams{
			WorkspaceSlug: slug,
			GroupBy:       groupBy,
			Since:         sinceStr,
			Until:         untilStr,
		}

		resp, err := store.GetWorkspaceCost(c.Request().Context(), params)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		return c.JSON(http.StatusOK, resp)
	}
}

// ---------------------------------------------------------------------------
// Workspace access helpers
// ---------------------------------------------------------------------------

// hasWorkspaceAccess checks if a non-admin caller can access a workspace.
// Returns true if the workspace exists in SQLite and the caller is the owner.
func hasWorkspaceAccess(sqliteDB *sql.DB, auth *apikit.AuthInfo, slug string) bool {
	var ownerID string
	err := sqliteDB.QueryRow(
		"SELECT owner_id FROM workspaces WHERE slug = ? AND status = 'active'",
		slug,
	).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		return false
	}
	return ownerID == auth.UserID
}

// getAccessibleWorkspaces returns workspace slugs the caller owns.
func getAccessibleWorkspaces(sqliteDB *sql.DB, auth *apikit.AuthInfo) ([]string, error) {
	rows, err := sqliteDB.Query(
		"SELECT slug FROM workspaces WHERE owner_id = ? AND status = 'active'",
		auth.UserID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var slugs []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		slugs = append(slugs, slug)
	}
	return slugs, rows.Err()
}

// ---------------------------------------------------------------------------
// Audit ingestion handlers (stubs — implemented in later task groups)
// ---------------------------------------------------------------------------

// handlePostEvent handles POST /workspaces/:slug/runs/:run_id/events.
// Ingests a single audit event into agent_audit_events.
func handlePostEvent(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handlePostSessionOutcome handles POST /workspaces/:slug/runs/:run_id/sessions/outcomes.
// Ingests a session outcome into session_outcomes.
func handlePostSessionOutcome(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handlePostToolCall handles POST /workspaces/:slug/runs/:run_id/tools/calls.
// Ingests a tool call record into tool_calls.
func handlePostToolCall(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handlePostToolError handles POST /workspaces/:slug/runs/:run_id/tools/errors.
// Ingests a tool error record into tool_errors.
func handlePostToolError(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handlePostTrace handles POST /workspaces/:slug/runs/:run_id/traces.
// Ingests a single trace event into agent_traces.
func handlePostTrace(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handlePostPostmortem handles POST /workspaces/:slug/runs/:run_id/postmortem.
// Ingests a postmortem report into postmortems.
func handlePostPostmortem(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handleGetPostmortem handles GET /workspaces/:slug/runs/:run_id/postmortem.
// Retrieves a postmortem report by run_id.
func handleGetPostmortem(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handlePostEventsBatch handles POST /workspaces/:slug/runs/:run_id/events/batch.
// Ingests a batch of audit events into agent_audit_events.
func handlePostEventsBatch(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handleGetEvents handles GET /workspaces/:slug/runs/:run_id/events.
// Queries audit events with filters and cursor-based pagination.
func handleGetEvents(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handleGetSessionOutcomes handles GET /workspaces/:slug/runs/:run_id/sessions/outcomes.
// Queries session outcomes with filters and cursor-based pagination.
func handleGetSessionOutcomes(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handleGetToolCalls handles GET /workspaces/:slug/runs/:run_id/tools/calls.
// Queries tool calls with filters and cursor-based pagination.
func handleGetToolCalls(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handleGetToolErrors handles GET /workspaces/:slug/runs/:run_id/tools/errors.
// Queries tool errors with filters and cursor-based pagination.
func handleGetToolErrors(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handlePostTracesBatch handles POST /workspaces/:slug/runs/:run_id/traces/batch.
// Ingests a batch of trace events into agent_traces.
func handlePostTracesBatch(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handleGetTraces handles GET /workspaces/:slug/runs/:run_id/traces.
// Queries trace events with filters and cursor-based pagination.
func handleGetTraces(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}
