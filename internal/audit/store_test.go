package audit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

var auditTables = []string{"agent_audit_events", "hub_audit_events", "session_outcomes", "tool_calls",
	"tool_errors", "agent_traces", "postmortems", "agent_sessions", "token_usage"}

func TestOpenDB_CreatesParentDirectoriesAndPings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", auditDB)
	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("parent directory not created: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestOpenDB_RejectsEmptyPathAndUnwritableParent(t *testing.T) {
	if db, err := OpenDB(""); err == nil {
		db.Close()
		t.Error("OpenDB(\"\") succeeded; want an error")
	}
	if os.Getuid() == 0 {
		t.Skip("permission check needs a non-root user")
	}
	restricted := filepath.Join(t.TempDir(), "restricted")
	if err := os.MkdirAll(restricted, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(restricted, 0o755) })
	if db, err := OpenDB(filepath.Join(restricted, "sub", auditDB)); err == nil {
		db.Close()
		t.Error("OpenDB under an unwritable directory succeeded; want an error")
	}
}

// InitSchema creates the nine tables and the migration columns, and running
// it again changes nothing.
func TestInitSchema_CreatesAllTablesIdempotently(t *testing.T) {
	db := openAuditDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("second InitSchema: %v", err)
	}
	for _, table := range auditTables {
		if testutil.Count(t, db, "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?", table) != 1 {
			t.Errorf("table %s missing", table)
		}
	}
	for _, col := range []string{"severity", "timestamp"} {
		if testutil.Count(t, db, "SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'hub_audit_events' AND column_name = ?", col) != 1 {
			t.Errorf("hub_audit_events.%s missing", col)
		}
	}
	if testutil.Count(t, db, "SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'agent_audit_events' AND column_name = 'archetype'") != 1 {
		t.Error("agent_audit_events.archetype missing")
	}
}

// A failing statement rolls the whole schema back: a table whose name is
// taken by a view makes CREATE TABLE fail, and none of the other tables is
// created.
func TestInitSchema_RollsBackOnFailure(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), auditDB))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	testutil.Exec(t, db, "CREATE VIEW postmortems AS SELECT 1 AS x")
	if err := InitSchema(db); err == nil {
		t.Fatal("InitSchema succeeded over a conflicting view; want an error")
	}
	if n := testutil.Count(t, db, "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'agent_sessions'"); n != 0 {
		t.Errorf("agent_sessions created despite the failure")
	}
}

func TestCreateSession_ReturnsExistingOnDuplicateID(t *testing.T) {
	env := newBareEnv(t)
	ctx := context.Background()
	first, created, err := env.store.CreateSession(ctx, &Session{ID: "s1", WorkspaceSlug: "w", Status: "active", StartedAt: now(), Metadata: map[string]any{"k": "v"}})
	if err != nil || !created {
		t.Fatalf("first CreateSession: created=%v err=%v", created, err)
	}
	again, created, err := env.store.CreateSession(ctx, &Session{ID: "s1", WorkspaceSlug: "other", Status: "active", StartedAt: now()})
	if err != nil || created {
		t.Fatalf("second CreateSession: created=%v err=%v", created, err)
	}
	if again.WorkspaceSlug != first.WorkspaceSlug || again.Metadata.(map[string]any)["k"] != "v" {
		t.Errorf("replay returned %+v; want the first session", again)
	}
	if _, err := env.store.GetSession(ctx, "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("GetSession(missing) = %v; want ErrSessionNotFound", err)
	}
}

// CompleteSession moves an active session to the terminal state, returns a
// terminal session unchanged, refuses a session in an unknown state and
// reports a missing one.
func TestCompleteSession_Transitions(t *testing.T) {
	env := newBareEnv(t)
	ctx := context.Background()
	env.seedSession(t, Session{ID: "active", WorkspaceSlug: "w"})
	env.seedSession(t, Session{ID: "done", WorkspaceSlug: "w", Status: "completed"})
	env.seedSession(t, Session{ID: "odd", WorkspaceSlug: "w", Status: "paused"})
	d := int64(1500)
	got, err := env.store.CompleteSession(ctx, "active", &CompleteSessionRequest{Status: "failed", ErrorMessage: "boom", DurationMs: &d, CacheCreationInputTokens: 7})
	if err != nil || got.Status != "failed" || got.ErrorMessage != "boom" || got.EndedAt == nil || *got.DurationMs != 1500 || got.CacheCreationInputTokens != 7 {
		t.Errorf("completed active session: %+v, %v", got, err)
	}
	if got, err := env.store.CompleteSession(ctx, "done", &CompleteSessionRequest{Status: "failed"}); err != nil || got.Status != "completed" {
		t.Errorf("completing a terminal session: %+v, %v; want it unchanged", got, err)
	}
	if _, err := env.store.CompleteSession(ctx, "odd", &CompleteSessionRequest{Status: "completed"}); !errors.Is(err, ErrSessionNotActive) {
		t.Errorf("completing a paused session: %v; want ErrSessionNotActive", err)
	}
	if _, err := env.store.CompleteSession(ctx, "missing", &CompleteSessionRequest{Status: "completed"}); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("completing a missing session: %v; want ErrSessionNotFound", err)
	}
}

func TestInsertTokenUsage_ReturnsExistingOnDuplicateID(t *testing.T) {
	env := newBareEnv(t)
	ctx := context.Background()
	u := &TokenUsage{ID: "u1", SessionID: "s1", WorkspaceSlug: "w", Model: "m", InputTokens: 10, ReportedAt: now()}
	if _, err := env.store.InsertTokenUsage(ctx, u); err != nil {
		t.Fatal(err)
	}
	dup := *u
	dup.InputTokens = 99
	got, err := env.store.InsertTokenUsage(ctx, &dup)
	if err != nil || got.InputTokens != 10 {
		t.Errorf("duplicate insert returned %+v, %v; want the first record", got, err)
	}
	if env.count(t, "token_usage", "") != 1 {
		t.Error("duplicate insert added a row")
	}
}

// ListSessions filters by every parameter, clamps the limit, pages by a
// (started_at, id) cursor in both orders and attaches token summaries.
func TestStoreListSessions_FiltersAndPages(t *testing.T) {
	env := newBareEnv(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		s := Session{ID: uuidN(i), WorkspaceSlug: "w", RunID: runID, StartedAt: base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)}
		if i == 4 {
			s.WorkspaceSlug, s.Status, s.CredentialType = "w-x", "completed", "api_key"
		}
		env.seedSession(t, s)
	}
	env.seedUsage(t, TokenUsage{ID: "u-a", SessionID: uuidN(0), WorkspaceSlug: "w", Model: "b-model", InputTokens: 10, OutputTokens: 5})
	env.seedUsage(t, TokenUsage{ID: "u-b", SessionID: uuidN(0), WorkspaceSlug: "w", Model: "a-model", CacheReadTokens: 3})

	list := func(p SessionListParams, ws []string) *SessionListResponse {
		t.Helper()
		resp, err := env.store.ListSessions(ctx, p, ws)
		if err != nil {
			t.Fatalf("ListSessions(%+v): %v", p, err)
		}
		return resp
	}
	all := list(SessionListParams{}, nil)
	if len(all.Sessions) != 5 || all.HasMore || all.Sessions[0].ID != uuidN(4) {
		t.Errorf("default listing: %d sessions, has_more %v, first %s; want 5 newest first", len(all.Sessions), all.HasMore, all.Sessions[0].ID)
	}
	if ts := all.Sessions[4].TokenSummary; ts.TotalInputTokens != 10 || ts.TotalOutputTokens != 5 || ts.TotalCacheReadTokens != 3 || len(ts.ModelsUsed) != 2 || ts.ModelsUsed[0] != "a-model" {
		t.Errorf("token summary of the first session: %+v", ts)
	}
	if ts := all.Sessions[0].TokenSummary; ts == nil || len(ts.ModelsUsed) != 0 {
		t.Errorf("token summary without usage: %+v; want empty", ts)
	}
	for name, p := range map[string]SessionListParams{
		"workspace":  {WorkspaceSlug: "w-x"},
		"status":     {Status: "completed"},
		"credential": {CredentialType: "api_key"},
		"since":      {Since: base.Add(4 * time.Minute).Format(time.RFC3339)},
	} {
		if got := list(p, nil); len(got.Sessions) != 1 || got.Sessions[0].ID != uuidN(4) {
			t.Errorf("filter %s: %v; want only %s", name, got.Sessions, uuidN(4))
		}
	}
	if got := list(SessionListParams{RunID: "none"}, nil); len(got.Sessions) != 0 {
		t.Errorf("run filter: %d sessions; want none", len(got.Sessions))
	}
	if got := list(SessionListParams{}, []string{}); len(got.Sessions) != 0 {
		t.Errorf("no accessible workspace: %d sessions; want none", len(got.Sessions))
	}
	if got := list(SessionListParams{}, []string{"w-x"}); len(got.Sessions) != 1 {
		t.Errorf("accessible w-x: %d sessions; want 1", len(got.Sessions))
	}

	first := list(SessionListParams{Order: "asc", Limit: 2}, nil)
	if len(first.Sessions) != 2 || !first.HasMore || first.NextCursor == nil || first.Sessions[0].ID != uuidN(0) {
		t.Fatalf("first ascending page: %+v", first)
	}
	second := list(SessionListParams{Order: "asc", Limit: 2, Cursor: *first.NextCursor}, nil)
	if len(second.Sessions) != 2 || second.Sessions[0].ID != uuidN(2) || !second.HasMore {
		t.Errorf("second ascending page: %+v", second)
	}
	last := list(SessionListParams{Order: "asc", Limit: 2, Cursor: *second.NextCursor}, nil)
	if len(last.Sessions) != 1 || last.HasMore || last.NextCursor != nil {
		t.Errorf("last ascending page: %+v", last)
	}
	desc := list(SessionListParams{Limit: 1}, nil)
	next := list(SessionListParams{Limit: 1, Cursor: *desc.NextCursor}, nil)
	if next.Sessions[0].ID != uuidN(3) {
		t.Errorf("second descending page starts at %s; want %s", next.Sessions[0].ID, uuidN(3))
	}
	if _, err := env.store.ListSessions(ctx, SessionListParams{Cursor: "***"}, nil); err == nil {
		t.Error("malformed cursor accepted")
	}
}

// ListTokenUsage pages the records of a session while the totals span every
// record; a missing session is an error.
func TestListTokenUsage_PagesWithUnboundedTotals(t *testing.T) {
	env := newBareEnv(t)
	ctx := context.Background()
	env.seedSession(t, Session{ID: "s1", WorkspaceSlug: "w"})
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		env.seedUsage(t, TokenUsage{ID: uuidN(i), SessionID: "s1", WorkspaceSlug: "w", Model: "m", InputTokens: 10,
			OutputTokens: 1, ReportedAt: base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)})
	}
	first, err := env.store.ListTokenUsage(ctx, "s1", UsageListParams{Limit: 2, Order: "asc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Records) != 2 || !first.HasMore || first.Totals.TotalInputTokens != 30 || first.Records[0].ID != uuidN(0) {
		t.Errorf("first page: %+v", first)
	}
	second, err := env.store.ListTokenUsage(ctx, "s1", UsageListParams{Limit: 2, Order: "asc", Cursor: *first.NextCursor})
	if err != nil || len(second.Records) != 1 || second.HasMore || second.Records[0].ID != uuidN(2) || second.Totals.TotalOutputTokens != 3 {
		t.Errorf("second page: %+v, %v", second, err)
	}
	if desc, _ := env.store.ListTokenUsage(ctx, "s1", UsageListParams{}); desc.Records[0].ID != uuidN(2) {
		t.Errorf("default order lists %s first; want newest", desc.Records[0].ID)
	}
	if _, err := env.store.ListTokenUsage(ctx, "none", UsageListParams{}); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("unknown session: %v; want ErrSessionNotFound", err)
	}
	if _, err := env.store.ListTokenUsage(ctx, "s1", UsageListParams{Cursor: "***"}); err == nil {
		t.Error("malformed cursor accepted")
	}
}

// GetWorkspaceCost sums the usage of the window per day, session or model,
// counts the distinct sessions and keeps the window half-open.
func TestGetWorkspaceCost_SumsPerDimension(t *testing.T) {
	env := newBareEnv(t)
	ctx := context.Background()
	rows := []TokenUsage{
		{ID: "a", SessionID: "s1", Model: "m1", InputTokens: 10, OutputTokens: 1, ReportedAt: "2026-09-01T10:00:00Z"},
		{ID: "b", SessionID: "s1", Model: "m2", InputTokens: 20, CacheReadTokens: 2, ReportedAt: "2026-09-02T10:00:00Z"},
		{ID: "c", SessionID: "s2", Model: "m1", InputTokens: 30, ReportedAt: "2026-09-02T11:00:00Z"},
		{ID: "d", SessionID: "s3", Model: "m1", InputTokens: 99, ReportedAt: "2026-09-03T00:00:00Z"},
		{ID: "e", SessionID: "s3", Model: "m1", InputTokens: 99, ReportedAt: "2026-09-03T00:00:00Z", WorkspaceSlug: "other"},
	}
	for _, r := range rows {
		r.WorkspaceSlug = or(r.WorkspaceSlug, "w")
		env.seedUsage(t, r)
	}
	cost := func(groupBy string) *CostResponse {
		t.Helper()
		resp, err := env.store.GetWorkspaceCost(ctx, CostParams{WorkspaceSlug: "w", GroupBy: groupBy, Since: "2026-09-01T00:00:00Z", Until: "2026-09-03T00:00:00Z"})
		if err != nil {
			t.Fatalf("GetWorkspaceCost(%s): %v", groupBy, err)
		}
		return resp
	}
	day := cost("day")
	if day.Totals != (CostTotals{InputTokens: 60, OutputTokens: 1, CacheReadTokens: 2, Sessions: 2}) || day.Workspace != "w" || day.Period.Until != "2026-09-03T00:00:00Z" {
		t.Errorf("totals %+v, period %+v", day.Totals, day.Period)
	}
	if len(day.Breakdown) != 2 || day.Breakdown[0].Date != "2026-09-01" || day.Breakdown[1] != (CostBreakdownEntry{Date: "2026-09-02", InputTokens: 50, CacheReadTokens: 2, Sessions: 2}) {
		t.Errorf("by day: %+v", day.Breakdown)
	}
	if bySession := cost("session").Breakdown; len(bySession) != 2 || bySession[0].SessionID != "s1" || bySession[0].InputTokens != 30 {
		t.Errorf("by session: %+v", bySession)
	}
	if byModel := cost("model").Breakdown; len(byModel) != 2 || byModel[0].Model != "m1" || byModel[0].InputTokens != 40 || byModel[0].Sessions != 2 {
		t.Errorf("by model: %+v", byModel)
	}
	if none, _ := env.store.GetWorkspaceCost(ctx, CostParams{WorkspaceSlug: "w", Since: "2025-01-01T00:00:00Z", Until: "2025-01-02T00:00:00Z"}); none.Totals.Sessions != 0 || len(none.Breakdown) != 0 || none.Breakdown == nil {
		t.Errorf("empty window: %+v", none)
	}
}

// ForceCloseSessions terminates the active sessions of a workspace with the
// reason and time given, leaves terminal sessions and other workspaces alone
// and reports nothing for a workspace without active sessions.
func TestForceCloseSessions_TerminatesOnlyActiveSessions(t *testing.T) {
	env := newBareEnv(t)
	env.seedSession(t, Session{ID: "a", WorkspaceSlug: "w"})
	env.seedSession(t, Session{ID: "b", WorkspaceSlug: "w"})
	env.seedSession(t, Session{ID: "c", WorkspaceSlug: "w", Status: "failed"})
	env.seedSession(t, Session{ID: "d", WorkspaceSlug: "w-x"})
	at := "2026-09-01T12:00:00Z"
	results, err := env.store.ForceCloseSessions(context.Background(), "w", "workspace archived", at)
	if err != nil || len(results) != 2 {
		t.Fatalf("ForceCloseSessions: %v, %v", results, err)
	}
	for _, id := range []string{"a", "b"} {
		s, _ := env.store.GetSession(context.Background(), id)
		if s.Status != "terminated" || s.ErrorMessage != "workspace archived" || s.EndedAt == nil {
			t.Errorf("session %s: %+v; want terminated with the reason", id, s)
		}
	}
	if env.count(t, "agent_sessions", "status = 'failed'") != 1 || env.count(t, "agent_sessions", "id = 'd' AND status = 'active'") != 1 {
		t.Error("a terminal session or another workspace's session was changed")
	}
	if results, err := env.store.ForceCloseSessions(context.Background(), "empty", "r", at); err != nil || len(results) != 0 {
		t.Errorf("workspace without sessions: %v, %v", results, err)
	}
}

// Session metadata survives the round trip through DuckDB's JSON column in
// every shape the driver may return it, and the cursor is base64url JSON
// that rejects anything else.
func TestSessionMetadata_RoundTripAndDecoding(t *testing.T) {
	env := newBareEnv(t)
	env.seedSession(t, Session{ID: "s1", WorkspaceSlug: "w", Metadata: map[string]any{"depth": float64(2), "tags": []any{"a"}}})
	s, err := env.store.GetSession(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := s.Metadata.(map[string]any); !ok || m["depth"] != float64(2) {
		t.Errorf("metadata read back as %#v", s.Metadata)
	}
	for name, c := range map[string]struct{ in, want any }{
		"nil":          {nil, nil},
		"null text":    {"null", nil},
		"blank":        {"  ", nil},
		"json text":    {`{"a":1}`, map[string]any{"a": float64(1)}},
		"json bytes":   {[]byte(`[1]`), []any{float64(1)}},
		"plain text":   {"not json", "not json"},
		"decoded":      {map[string]any{"b": "c"}, map[string]any{"b": "c"}},
		"bad to json":  {metadataToJSON(make(chan int)), nil},
		"json of nil":  {metadataToJSON(nil), nil},
		"json of map":  {metadataToJSON(map[string]int{"n": 1}), map[string]any{"n": float64(1)}},
		"json literal": {`"x"`, "x"},
	} {
		got := decodeMetadata(c.in)
		if gotJSON, wantJSON := jsonOf(got), jsonOf(c.want); gotJSON != wantJSON {
			t.Errorf("%s: decodeMetadata(%#v) = %s; want %s", name, c.in, gotJSON, wantJSON)
		}
	}
}

func jsonOf(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func TestCursor_RoundTripsAndRejectsMalformedInput(t *testing.T) {
	c := encodeCursor("2026-09-01T10:00:00Z", "id-1")
	ts, id, err := decodeCursor(c)
	if err != nil || ts != "2026-09-01T10:00:00Z" || id != "id-1" {
		t.Errorf("decodeCursor(encodeCursor) = (%q, %q, %v)", ts, id, err)
	}
	for name, bad := range map[string]string{
		"not base64": "***",
		"not json":   "bm90IGpzb24",
		"no fields":  encodeCursor("", "x"),
	} {
		if _, _, err := decodeCursor(bad); err == nil {
			t.Errorf("%s: decodeCursor(%q) accepted", name, bad)
		}
	}
}

// The write timeout comes from AF_AUDIT_WRITE_TIMEOUT_MS (2 s by default), a
// deadline error is wrapped as WriteTimeoutError and that error answers 503
// with Retry-After; other store errors answer 500.
func TestWriteTimeout_ConfigurationAndAnswer(t *testing.T) {
	if d := writeTimeout(); d != 2*time.Second {
		t.Errorf("default write timeout %v; want 2s", d)
	}
	t.Setenv("AF_AUDIT_WRITE_TIMEOUT_MS", "250")
	if d := writeTimeout(); d != 250*time.Millisecond {
		t.Errorf("configured write timeout %v; want 250ms", d)
	}
	t.Setenv("AF_AUDIT_WRITE_TIMEOUT_MS", "nope")
	if d := writeTimeout(); d != 2*time.Second {
		t.Errorf("unparseable write timeout %v; want the default", d)
	}
	var wte *WriteTimeoutError
	if err := wrapTimeout(context.DeadlineExceeded); !errors.As(err, &wte) || !errors.Is(err, context.DeadlineExceeded) || wte.Error() == "" {
		t.Errorf("wrapTimeout(DeadlineExceeded) = %v; want a WriteTimeoutError wrapping it", err)
	}
	if err := wrapTimeout(errors.New("other")); errors.As(err, &wte) {
		t.Errorf("wrapTimeout(other) = %v; want it unchanged", err)
	}
	if wrapTimeout(nil) != nil {
		t.Error("wrapTimeout(nil) is not nil")
	}
	for _, tc := range []struct {
		err    error
		status int
		retry  string
	}{
		{&WriteTimeoutError{Err: context.DeadlineExceeded}, http.StatusServiceUnavailable, "5"},
		{errors.New("other"), http.StatusInternalServerError, ""},
	} {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec)
		_ = writeStoreError(c, tc.err)
		if rec.Code != tc.status || rec.Header().Get("Retry-After") != tc.retry {
			t.Errorf("writeStoreError(%v): (%d, Retry-After %q); want (%d, %q)", tc.err, rec.Code, rec.Header().Get("Retry-After"), tc.status, tc.retry)
		}
	}
}
