package audit

// store_audit.go provides stub implementations of duckDBStore methods called
// by handlers_audit.go. These are placeholders until the corresponding store
// task group implements the real DuckDB queries.

import "context"

// ---------------------------------------------------------------------------
// Ingestion stubs
// ---------------------------------------------------------------------------

func (s *duckDBStore) InsertAuditEvent(_ context.Context, _ PostEventRequest, _ string, _ string) (*PostEventResponse, bool, error) {
	panic("not implemented: InsertAuditEvent")
}

func (s *duckDBStore) InsertSessionOutcome(_ context.Context, _ PostSessionOutcomeRequest, _ string, _ string) (*PostSessionOutcomeResponse, bool, error) {
	panic("not implemented: InsertSessionOutcome")
}

func (s *duckDBStore) InsertToolCall(_ context.Context, _ PostToolCallRequest, _ string, _ string) (*PostToolCallResponse, bool, error) {
	panic("not implemented: InsertToolCall")
}

func (s *duckDBStore) InsertToolError(_ context.Context, _ PostToolErrorRequest, _ string, _ string) (*PostToolErrorResponse, bool, error) {
	panic("not implemented: InsertToolError")
}

func (s *duckDBStore) InsertAgentTrace(_ context.Context, _ PostTraceRequest, _ string, _ string) (*PostTraceResponse, bool, error) {
	panic("not implemented: InsertAgentTrace")
}

func (s *duckDBStore) InsertPostmortem(_ context.Context, _ PostPostmortemRequest, _ string, _ string) (*PostPostmortemResponse, bool, error) {
	panic("not implemented: InsertPostmortem")
}

func (s *duckDBStore) GetPostmortem(_ context.Context, _ string) (any, error) {
	panic("not implemented: GetPostmortem")
}

// ---------------------------------------------------------------------------
// Batch ingestion stubs
// ---------------------------------------------------------------------------

func (s *duckDBStore) InsertAuditEventBatch(_ context.Context, _ []PostEventRequest, _ string, _ string) (int, int, error) {
	panic("not implemented: InsertAuditEventBatch")
}

func (s *duckDBStore) InsertAgentTraceBatch(_ context.Context, _ []PostTraceRequest, _ string, _ string) (int, int, error) {
	panic("not implemented: InsertAgentTraceBatch")
}

// ---------------------------------------------------------------------------
// Query stubs
// ---------------------------------------------------------------------------

func (s *duckDBStore) QueryAuditEvents(_ context.Context, _ string, _ string, _ QueryParams) ([]map[string]any, string, bool, error) {
	panic("not implemented: QueryAuditEvents")
}

func (s *duckDBStore) QuerySessionOutcomes(_ context.Context, _ string, _ string, _ QueryParams) ([]map[string]any, string, bool, error) {
	panic("not implemented: QuerySessionOutcomes")
}

func (s *duckDBStore) QueryToolCalls(_ context.Context, _ string, _ string, _ QueryParams) ([]map[string]any, string, bool, error) {
	panic("not implemented: QueryToolCalls")
}

func (s *duckDBStore) QueryToolErrors(_ context.Context, _ string, _ string, _ QueryParams) ([]map[string]any, string, bool, error) {
	panic("not implemented: QueryToolErrors")
}

func (s *duckDBStore) QueryAgentTraces(_ context.Context, _ string, _ string, _ QueryParams) ([]map[string]any, string, bool, error) {
	panic("not implemented: QueryAgentTraces")
}
