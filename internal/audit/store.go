package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/txsvc/apikit"
)

// Store defines the audit storage interface for inserting and querying
// audit records, session lifecycle, and token usage operations. The concrete
// implementation is backed by DuckDB.
type Store interface {
	// InsertHubEvent inserts a prepared hub audit event row.
	InsertHubEvent(ctx context.Context, row HubEventRow) error

	// CreateSession inserts a new agent_sessions row. If a session with the
	// same ID already exists, it returns the existing session and false.
	// Returns the session, true if created, and any error.
	CreateSession(ctx context.Context, s *Session) (*Session, bool, error)

	// GetSession retrieves a session by ID. Returns ErrSessionNotFound if
	// no session with the given ID exists.
	GetSession(ctx context.Context, id string) (*Session, error)

	// CompleteSession atomically transitions an active session to a terminal
	// state. Returns ErrSessionNotActive if the session is not active, and
	// ErrSessionNotFound if the session does not exist.
	CompleteSession(ctx context.Context, id string, req *CompleteSessionRequest) (*Session, error)

	// InsertTokenUsage creates a new token_usage record linked to a session.
	// If a record with the same ID already exists, returns the existing record.
	InsertTokenUsage(ctx context.Context, u *TokenUsage) (*TokenUsage, error)

	// ListSessions returns a paginated list of sessions matching the given
	// parameters, with token_summary aggregated from token_usage rows.
	ListSessions(ctx context.Context, params SessionListParams, accessibleWorkspaces []string) (*SessionListResponse, error)

	// GetSessionWithSummary retrieves a session by ID with its aggregated
	// token_summary. Returns ErrSessionNotFound if no session exists.
	GetSessionWithSummary(ctx context.Context, id string) (*Session, error)

	// ListTokenUsage returns a paginated list of token_usage records for
	// a session, plus unbounded totals across all records.
	ListTokenUsage(ctx context.Context, sessionID string, params UsageListParams) (*UsageListResponse, error)

	// GetWorkspaceCost aggregates token_usage records within a time window,
	// grouped by the specified dimension.
	GetWorkspaceCost(ctx context.Context, params CostParams) (*CostResponse, error)

	// ForceCloseSessions sets all active sessions for a workspace to
	// terminated status. Returns the list of session IDs that were closed.
	ForceCloseSessions(ctx context.Context, workspaceSlug string, reason string, timestamp string) ([]ForceCloseResult, error)
}

// NewStore creates a Store backed by the given DuckDB connection.
func NewStore(db *sql.DB) Store {
	return &duckDBStore{db: db}
}

type duckDBStore struct {
	db *sql.DB
}

// ---------------------------------------------------------------------------
// InsertHubEvent
// ---------------------------------------------------------------------------

func (s *duckDBStore) InsertHubEvent(_ context.Context, row HubEventRow) error {
	_, err := s.db.Exec(
		`INSERT INTO hub_audit_events (id, event_type, actor_id, actor_type, resource_type,
			resource_id, action, workspace, metadata, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.EventType, row.ActorID, row.ActorType, row.ResourceType,
		row.ResourceID, row.Action, row.Workspace, row.Metadata, row.IngestedAt,
	)
	return err
}

// ---------------------------------------------------------------------------
// Session CRUD
// ---------------------------------------------------------------------------

func (s *duckDBStore) CreateSession(_ context.Context, sess *Session) (*Session, bool, error) {
	now := apikit.NowUTC()

	// Try to insert. Use INSERT OR IGNORE for idempotency.
	result, err := s.db.Exec(
		`INSERT OR IGNORE INTO agent_sessions (id, run_id, workspace_slug, node_id, archetype,
			status, started_at, model, credential_id, credential_type, error_message,
			ended_at, duration_ms, cache_creation_input_tokens, metadata, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, 0, ?, ?)`,
		sess.ID, sess.RunID, sess.WorkspaceSlug, sess.NodeID, sess.Archetype,
		sess.Status, sess.StartedAt, sess.Model, sess.CredentialID, sess.CredentialType,
		metadataToJSON(sess.Metadata), now,
	)
	if err != nil {
		return nil, false, fmt.Errorf("create session: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		// Row already exists — return existing.
		existing, err := s.GetSession(context.Background(), sess.ID)
		if err != nil {
			return nil, false, fmt.Errorf("create session (fetch existing): %w", err)
		}
		return existing, false, nil
	}

	// Return the newly created session.
	created, err := s.GetSession(context.Background(), sess.ID)
	if err != nil {
		return nil, false, fmt.Errorf("create session (fetch created): %w", err)
	}
	return created, true, nil
}

func (s *duckDBStore) GetSession(_ context.Context, id string) (*Session, error) {
	return s.getSessionByID(id)
}

func (s *duckDBStore) getSessionByID(id string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT id, run_id, workspace_slug, node_id, archetype, status, started_at,
			model, credential_id, credential_type, error_message, ended_at,
			duration_ms, cache_creation_input_tokens, metadata
		FROM agent_sessions WHERE id = ?`, id,
	)

	var sess Session
	var errorMsg sql.NullString
	var endedAt sql.NullString
	var durationMs sql.NullInt64
	var metadataJSON sql.NullString

	err := row.Scan(
		&sess.ID, &sess.RunID, &sess.WorkspaceSlug, &sess.NodeID, &sess.Archetype,
		&sess.Status, &sess.StartedAt, &sess.Model, &sess.CredentialID,
		&sess.CredentialType, &errorMsg, &endedAt, &durationMs,
		&sess.CacheCreationInputTokens, &metadataJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}

	if errorMsg.Valid {
		sess.ErrorMessage = errorMsg.String
	}
	if endedAt.Valid {
		s := endedAt.String
		sess.EndedAt = &s
	}
	if durationMs.Valid {
		d := durationMs.Int64
		sess.DurationMs = &d
	}
	if metadataJSON.Valid && metadataJSON.String != "" && metadataJSON.String != "null" {
		var meta any
		if err := json.Unmarshal([]byte(metadataJSON.String), &meta); err == nil {
			sess.Metadata = meta
		}
	}

	return &sess, nil
}

func (s *duckDBStore) CompleteSession(_ context.Context, id string, req *CompleteSessionRequest) (*Session, error) {
	now := apikit.NowUTC()

	// Conditional update: only transition active sessions.
	result, err := s.db.Exec(
		`UPDATE agent_sessions SET status = ?, ended_at = ?, error_message = ?,
			duration_ms = ?, cache_creation_input_tokens = ?
		WHERE id = ? AND status = 'active'`,
		req.Status, now, nilStr(req.ErrorMessage), req.DurationMs,
		req.CacheCreationInputTokens, id,
	)
	if err != nil {
		return nil, fmt.Errorf("complete session: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		// Either session doesn't exist or is already terminal.
		sess, err := s.getSessionByID(id)
		if err != nil {
			return nil, err // ErrSessionNotFound or DB error
		}
		// Session exists but is not active — check if it's terminal.
		if isTerminalStatus(sess.Status) {
			return sess, nil // Idempotent — return existing terminal session.
		}
		return nil, ErrSessionNotActive
	}

	// Return updated session.
	return s.getSessionByID(id)
}

// ---------------------------------------------------------------------------
// Token Usage
// ---------------------------------------------------------------------------

func (s *duckDBStore) InsertTokenUsage(_ context.Context, u *TokenUsage) (*TokenUsage, error) {
	now := apikit.NowUTC()

	result, err := s.db.Exec(
		`INSERT OR IGNORE INTO token_usage (id, session_id, workspace_slug, model,
			input_tokens, output_tokens, cache_read_tokens, reported_at, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.SessionID, u.WorkspaceSlug, u.Model,
		u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.ReportedAt, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert token usage: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		// Duplicate — fetch and return existing.
		return s.getTokenUsageByID(u.ID)
	}

	return u, nil
}

func (s *duckDBStore) getTokenUsageByID(id string) (*TokenUsage, error) {
	row := s.db.QueryRow(
		`SELECT id, session_id, workspace_slug, model, input_tokens, output_tokens,
			cache_read_tokens, reported_at
		FROM token_usage WHERE id = ?`, id,
	)

	var u TokenUsage
	err := row.Scan(&u.ID, &u.SessionID, &u.WorkspaceSlug, &u.Model,
		&u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.ReportedAt)
	if err != nil {
		return nil, fmt.Errorf("get token usage: %w", err)
	}
	return &u, nil
}

// ---------------------------------------------------------------------------
// List Sessions
// ---------------------------------------------------------------------------

func (s *duckDBStore) ListSessions(_ context.Context, params SessionListParams, accessibleWorkspaces []string) (*SessionListResponse, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	order := strings.ToLower(params.Order)
	if order != "asc" {
		order = "desc"
	}

	// Build WHERE clauses.
	var conditions []string
	var args []any

	if params.WorkspaceSlug != "" {
		conditions = append(conditions, "workspace_slug = ?")
		args = append(args, params.WorkspaceSlug)
	} else if accessibleWorkspaces != nil {
		// For non-admin callers, restrict to accessible workspaces.
		if len(accessibleWorkspaces) == 0 {
			// No accessible workspaces — return empty.
			return &SessionListResponse{Sessions: []Session{}}, nil
		}
		placeholders := make([]string, len(accessibleWorkspaces))
		for i, ws := range accessibleWorkspaces {
			placeholders[i] = "?"
			args = append(args, ws)
		}
		conditions = append(conditions, "workspace_slug IN ("+strings.Join(placeholders, ",")+")")
	}

	if params.RunID != "" {
		conditions = append(conditions, "run_id = ?")
		args = append(args, params.RunID)
	}
	if params.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, params.Status)
	}
	if params.CredentialType != "" {
		conditions = append(conditions, "credential_type = ?")
		args = append(args, params.CredentialType)
	}
	if params.Since != "" {
		conditions = append(conditions, "started_at >= CAST(? AS TIMESTAMPTZ)")
		args = append(args, params.Since)
	}

	// Cursor-based pagination.
	if params.Cursor != "" {
		cursorTS, cursorID, err := decodeCursor(params.Cursor)
		if err != nil {
			return nil, fmt.Errorf("invalid cursor: %w", err)
		}
		if order == "desc" {
			conditions = append(conditions, "(started_at < CAST(? AS TIMESTAMPTZ) OR (started_at = CAST(? AS TIMESTAMPTZ) AND id < ?))")
			args = append(args, cursorTS, cursorTS, cursorID)
		} else {
			conditions = append(conditions, "(started_at > CAST(? AS TIMESTAMPTZ) OR (started_at = CAST(? AS TIMESTAMPTZ) AND id > ?))")
			args = append(args, cursorTS, cursorTS, cursorID)
		}
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Fetch limit+1 to detect has_more.
	query := fmt.Sprintf(
		`SELECT id, run_id, workspace_slug, node_id, archetype, status, started_at,
			model, credential_id, credential_type, error_message, ended_at,
			duration_ms, cache_creation_input_tokens, metadata
		FROM agent_sessions %s
		ORDER BY started_at %s, id %s
		LIMIT ?`, where, order, order,
	)
	args = append(args, limit+1)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var sess Session
		var errorMsg sql.NullString
		var endedAt sql.NullString
		var durationMs sql.NullInt64
		var metadataJSON sql.NullString

		if err := rows.Scan(
			&sess.ID, &sess.RunID, &sess.WorkspaceSlug, &sess.NodeID, &sess.Archetype,
			&sess.Status, &sess.StartedAt, &sess.Model, &sess.CredentialID,
			&sess.CredentialType, &errorMsg, &endedAt, &durationMs,
			&sess.CacheCreationInputTokens, &metadataJSON,
		); err != nil {
			return nil, fmt.Errorf("list sessions scan: %w", err)
		}

		if errorMsg.Valid {
			sess.ErrorMessage = errorMsg.String
		}
		if endedAt.Valid {
			e := endedAt.String
			sess.EndedAt = &e
		}
		if durationMs.Valid {
			d := durationMs.Int64
			sess.DurationMs = &d
		}
		if metadataJSON.Valid && metadataJSON.String != "" && metadataJSON.String != "null" {
			var meta any
			if json.Unmarshal([]byte(metadataJSON.String), &meta) == nil {
				sess.Metadata = meta
			}
		}

		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions iterate: %w", err)
	}

	hasMore := len(sessions) > limit
	if hasMore {
		sessions = sessions[:limit]
	}

	// Fetch token summaries for the sessions.
	if len(sessions) > 0 {
		sessionIDs := make([]string, len(sessions))
		for i, sess := range sessions {
			sessionIDs[i] = sess.ID
		}
		summaries, err := s.getTokenSummariesBatch(sessionIDs)
		if err != nil {
			return nil, fmt.Errorf("list sessions token summaries: %w", err)
		}
		for i := range sessions {
			if summary, ok := summaries[sessions[i].ID]; ok {
				sessions[i].TokenSummary = summary
			} else {
				sessions[i].TokenSummary = &TokenSummary{ModelsUsed: []string{}}
			}
		}
	}

	resp := &SessionListResponse{
		Sessions: sessions,
		HasMore:  hasMore,
	}

	if hasMore && len(sessions) > 0 {
		last := sessions[len(sessions)-1]
		cursor := encodeCursor(last.StartedAt, last.ID)
		resp.NextCursor = &cursor
	}

	if resp.Sessions == nil {
		resp.Sessions = []Session{}
	}

	return resp, nil
}

// ---------------------------------------------------------------------------
// GetSessionWithSummary
// ---------------------------------------------------------------------------

func (s *duckDBStore) GetSessionWithSummary(_ context.Context, id string) (*Session, error) {
	sess, err := s.getSessionByID(id)
	if err != nil {
		return nil, err
	}

	summary, err := s.getTokenSummary(id)
	if err != nil {
		return nil, fmt.Errorf("get session summary: %w", err)
	}
	sess.TokenSummary = summary
	return sess, nil
}

// ---------------------------------------------------------------------------
// ListTokenUsage
// ---------------------------------------------------------------------------

func (s *duckDBStore) ListTokenUsage(_ context.Context, sessionID string, params UsageListParams) (*UsageListResponse, error) {
	// First verify session exists.
	_, err := s.getSessionByID(sessionID)
	if err != nil {
		return nil, err
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	order := strings.ToLower(params.Order)
	if order != "asc" {
		order = "desc"
	}

	var conditions []string
	var args []any

	conditions = append(conditions, "session_id = ?")
	args = append(args, sessionID)

	// Cursor-based pagination.
	if params.Cursor != "" {
		cursorTS, cursorID, err := decodeCursor(params.Cursor)
		if err != nil {
			return nil, fmt.Errorf("invalid cursor: %w", err)
		}
		if order == "desc" {
			conditions = append(conditions, "(reported_at < CAST(? AS TIMESTAMPTZ) OR (reported_at = CAST(? AS TIMESTAMPTZ) AND id < ?))")
			args = append(args, cursorTS, cursorTS, cursorID)
		} else {
			conditions = append(conditions, "(reported_at > CAST(? AS TIMESTAMPTZ) OR (reported_at = CAST(? AS TIMESTAMPTZ) AND id > ?))")
			args = append(args, cursorTS, cursorTS, cursorID)
		}
	}

	where := "WHERE " + strings.Join(conditions, " AND ")

	query := fmt.Sprintf(
		`SELECT id, session_id, workspace_slug, model, input_tokens, output_tokens,
			cache_read_tokens, reported_at
		FROM token_usage %s
		ORDER BY reported_at %s, id %s
		LIMIT ?`, where, order, order,
	)
	args = append(args, limit+1)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list token usage: %w", err)
	}
	defer rows.Close()

	var records []TokenUsage
	for rows.Next() {
		var u TokenUsage
		if err := rows.Scan(&u.ID, &u.SessionID, &u.WorkspaceSlug, &u.Model,
			&u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.ReportedAt); err != nil {
			return nil, fmt.Errorf("list token usage scan: %w", err)
		}
		records = append(records, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list token usage iterate: %w", err)
	}

	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}

	// Unbounded totals across ALL records for the session.
	totals, err := s.getTokenSummary(sessionID)
	if err != nil {
		return nil, fmt.Errorf("list token usage totals: %w", err)
	}

	resp := &UsageListResponse{
		SessionID: sessionID,
		Records:   records,
		Totals:    *totals,
		HasMore:   hasMore,
	}

	if hasMore && len(records) > 0 {
		last := records[len(records)-1]
		cursor := encodeCursor(last.ReportedAt, last.ID)
		resp.NextCursor = &cursor
	}

	if resp.Records == nil {
		resp.Records = []TokenUsage{}
	}

	return resp, nil
}

// ---------------------------------------------------------------------------
// GetWorkspaceCost
// ---------------------------------------------------------------------------

func (s *duckDBStore) GetWorkspaceCost(_ context.Context, params CostParams) (*CostResponse, error) {
	// Determine grouping column and discriminator.
	var groupExpr, discriminatorCol string
	switch params.GroupBy {
	case "day":
		groupExpr = "CAST(CAST(reported_at AS TIMESTAMP) AS DATE)"
		discriminatorCol = "date"
	case "session":
		groupExpr = "session_id"
		discriminatorCol = "session_id"
	case "model":
		groupExpr = "model"
		discriminatorCol = "model"
	default:
		groupExpr = "CAST(reported_at AS DATE)"
		discriminatorCol = "date"
	}

	// Totals query.
	var totals CostTotals
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0), COUNT(DISTINCT session_id)
		FROM token_usage
		WHERE workspace_slug = ? AND reported_at >= CAST(? AS TIMESTAMPTZ) AND reported_at < CAST(? AS TIMESTAMPTZ)`,
		params.WorkspaceSlug, params.Since, params.Until,
	).Scan(&totals.InputTokens, &totals.OutputTokens, &totals.CacheReadTokens, &totals.Sessions)
	if err != nil {
		return nil, fmt.Errorf("workspace cost totals: %w", err)
	}

	// Breakdown query.
	query := fmt.Sprintf(
		`SELECT %s AS discriminator,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COUNT(DISTINCT session_id)
		FROM token_usage
		WHERE workspace_slug = ? AND reported_at >= CAST(? AS TIMESTAMPTZ) AND reported_at < CAST(? AS TIMESTAMPTZ)
		GROUP BY %s
		ORDER BY %s`, groupExpr, groupExpr, groupExpr,
	)

	rows, err := s.db.Query(query, params.WorkspaceSlug, params.Since, params.Until)
	if err != nil {
		return nil, fmt.Errorf("workspace cost breakdown: %w", err)
	}
	defer rows.Close()

	var breakdown []CostBreakdownEntry
	for rows.Next() {
		var entry CostBreakdownEntry
		var discriminator string
		if err := rows.Scan(&discriminator, &entry.InputTokens, &entry.OutputTokens,
			&entry.CacheReadTokens, &entry.Sessions); err != nil {
			return nil, fmt.Errorf("workspace cost breakdown scan: %w", err)
		}
		switch discriminatorCol {
		case "date":
			// Parse the date from DuckDB format and normalize to YYYY-MM-DD.
			// DuckDB may return DATE as "2026-09-01" or as "2026-09-01T00:00:00Z".
			if t, err := time.Parse("2006-01-02", discriminator); err == nil {
				entry.Date = t.Format("2006-01-02")
			} else if t, err := time.Parse(time.RFC3339, discriminator); err == nil {
				entry.Date = t.Format("2006-01-02")
			} else if t, err := time.Parse(time.RFC3339Nano, discriminator); err == nil {
				entry.Date = t.Format("2006-01-02")
			} else {
				entry.Date = discriminator
			}
		case "session_id":
			entry.SessionID = discriminator
		case "model":
			entry.Model = discriminator
		}
		breakdown = append(breakdown, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workspace cost breakdown iterate: %w", err)
	}

	if breakdown == nil {
		breakdown = []CostBreakdownEntry{}
	}

	return &CostResponse{
		Workspace: params.WorkspaceSlug,
		Period: CostPeriod{
			Since: params.Since,
			Until: params.Until,
		},
		Totals:    totals,
		Breakdown: breakdown,
	}, nil
}

// ---------------------------------------------------------------------------
// ForceCloseSessions
// ---------------------------------------------------------------------------

func (s *duckDBStore) ForceCloseSessions(_ context.Context, workspaceSlug string, reason string, timestamp string) ([]ForceCloseResult, error) {
	// First find all active sessions for this workspace.
	rows, err := s.db.Query(
		`SELECT id FROM agent_sessions WHERE workspace_slug = ? AND status = 'active'`,
		workspaceSlug,
	)
	if err != nil {
		return nil, fmt.Errorf("force close query: %w", err)
	}
	defer rows.Close()

	var results []ForceCloseResult
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("force close scan: %w", err)
		}
		results = append(results, ForceCloseResult{SessionID: id})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("force close iterate: %w", err)
	}

	if len(results) == 0 {
		return results, nil
	}

	// Update all active sessions to terminated.
	_, err = s.db.Exec(
		`UPDATE agent_sessions SET status = 'terminated', ended_at = ?, error_message = ?
		WHERE workspace_slug = ? AND status = 'active'`,
		timestamp, reason, workspaceSlug,
	)
	if err != nil {
		return nil, fmt.Errorf("force close update: %w", err)
	}

	return results, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// getTokenSummary aggregates token usage for a single session.
func (s *duckDBStore) getTokenSummary(sessionID string) (*TokenSummary, error) {
	summary := &TokenSummary{ModelsUsed: []string{}}

	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0)
		FROM token_usage WHERE session_id = ?`, sessionID,
	).Scan(&summary.TotalInputTokens, &summary.TotalOutputTokens, &summary.TotalCacheReadTokens)
	if err != nil {
		return nil, err
	}

	// Distinct models.
	rows, err := s.db.Query(
		`SELECT DISTINCT model FROM token_usage WHERE session_id = ? ORDER BY model`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		if model != "" {
			summary.ModelsUsed = append(summary.ModelsUsed, model)
		}
	}
	return summary, rows.Err()
}

// getTokenSummariesBatch aggregates token usage for multiple sessions in batch.
func (s *duckDBStore) getTokenSummariesBatch(sessionIDs []string) (map[string]*TokenSummary, error) {
	result := make(map[string]*TokenSummary, len(sessionIDs))
	if len(sessionIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(sessionIDs))
	args := make([]any, len(sessionIDs))
	for i, id := range sessionIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// Aggregate totals.
	query := fmt.Sprintf(
		`SELECT session_id, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0)
		FROM token_usage WHERE session_id IN (%s)
		GROUP BY session_id`,
		strings.Join(placeholders, ","),
	)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var sessionID string
		summary := &TokenSummary{ModelsUsed: []string{}}
		if err := rows.Scan(&sessionID, &summary.TotalInputTokens, &summary.TotalOutputTokens,
			&summary.TotalCacheReadTokens); err != nil {
			return nil, err
		}
		result[sessionID] = summary
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fetch distinct models per session.
	modelQuery := fmt.Sprintf(
		`SELECT session_id, model FROM token_usage
		WHERE session_id IN (%s)
		GROUP BY session_id, model
		ORDER BY session_id, model`,
		strings.Join(placeholders, ","),
	)

	modelRows, err := s.db.Query(modelQuery, args...)
	if err != nil {
		return nil, err
	}
	defer modelRows.Close()

	for modelRows.Next() {
		var sessionID, model string
		if err := modelRows.Scan(&sessionID, &model); err != nil {
			return nil, err
		}
		if summary, ok := result[sessionID]; ok && model != "" {
			summary.ModelsUsed = append(summary.ModelsUsed, model)
		}
	}
	if err := modelRows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// isTerminalStatus reports whether the given status is a terminal session state.
func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "timeout", "terminated":
		return true
	}
	return false
}

// nilStr returns nil if s is empty, otherwise returns s. Used for nullable
// text columns.
func nilStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// metadataToJSON converts a metadata value to JSON string for DuckDB storage.
func metadataToJSON(metadata any) any {
	if metadata == nil {
		return nil
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return nil
	}
	return string(data)
}

// Ensure uuid is used (needed for session/usage ID generation in handlers).
var _ = uuid.New
