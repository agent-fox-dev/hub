package boundtoken

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/workspace"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// The team fixture: the active workspace s of owner in the org acme, of
// which member is a member; stranger belongs to neither.
const (
	owner    = "owner"
	member   = "member"
	stranger = "stranger"
	orgID    = "org-acme"
	slug     = "s"
	mintPath = "/api/v1/workspaces/s/tokens"
	okAnswer = `{"token_id":"T1","name":"svc","token":"ak_pat_T1_supersecret","permissions":["workspaces:read"],"expires_at":"2027-01-19T10:00:00Z","created_at":"2026-10-21T10:00:00Z"}`
)

// stubAPIKit stands in for apikit's server.Echo(): it keeps the last request
// and answers with status and body; when tokenID is set it first writes that
// token's pats row, as apikit's mint does, and then runs before.
type stubAPIKit struct {
	mu      sync.Mutex
	calls   int
	req     *http.Request
	body    []byte
	status  int
	answer  string
	db      *sql.DB
	tokenID string
	before  func()
}

func (s *stubAPIKit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.calls++
	s.req, s.body = r, body
	s.mu.Unlock()
	if s.tokenID != "" {
		if _, err := s.db.Exec(`INSERT INTO pats (token_id, user_id, name, secret_hash, permissions, expires_days, created_at)
			VALUES (?, ?, 'svc', 'hash', '["workspaces:read"]', 90, ?)`, s.tokenID, owner, apikit.NowUTC()); err != nil {
			panic(err)
		}
	}
	if s.before != nil {
		s.before()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(s.status)
	_, _ = io.WriteString(w, s.answer)
}

// recordingEmitter keeps the events and contexts it is given and fails with
// err when set.
type recordingEmitter struct {
	mu   sync.Mutex
	evs  []audit.HubEvent
	ctxs []context.Context
	err  error
}

func (r *recordingEmitter) Emit(ctx context.Context, e audit.HubEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, e)
	r.ctxs = append(r.ctxs, ctx)
	return r.err
}

func (r *recordingEmitter) events() []audit.HubEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.HubEvent(nil), r.evs...)
}

// env is the bound-token routes behind the test auth middleware, over a
// stub apikit, on apikit's schema plus the workspace and binding tables.
type env struct {
	e     *echo.Echo
	db    *sql.DB
	stub  *stubAPIKit
	audit *recordingEmitter
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := testutil.OpenDB(t)
	if err := workspace.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := wsaccess.InitBindingSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{owner, member, stranger} {
		testutil.SeedUser(t, db, u)
	}
	testutil.SeedOrg(t, db, orgID, "acme", owner)
	testutil.SeedOrgMember(t, db, orgID, owner)
	testutil.SeedOrgMember(t, db, orgID, member)
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: slug, OwnerID: owner, OrgID: orgID, Status: "ready"})
	e, api := testutil.API()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	v := &env{e: e, db: db, stub: &stubAPIKit{status: http.StatusCreated, answer: okAnswer, db: db}, audit: &recordingEmitter{}}
	RegisterRoutes(api, Config{DB: db, APIKit: v.stub, Audit: v.audit})
	return v
}

// mint posts body (the default token request when "") to the mint route as
// auth, with the Bearer header.
func (v *env) mint(t *testing.T, auth *apikit.AuthInfo, body string) *httptest.ResponseRecorder {
	t.Helper()
	if body == "" {
		body = `{"name":"svc","permissions":["workspaces:read"]}`
	}
	req := httptest.NewRequest(http.MethodPost, mintPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer ak_key1_verbatim-SECRET")
	raw, _ := json.Marshal(auth)
	req.Header.Set(testutil.AuthHeader, string(raw))
	rec := httptest.NewRecorder()
	v.e.ServeHTTP(rec, req)
	return rec
}

// seedPAT inserts a pats row of owner; empty optional times are NULL.
func (v *env) seedPAT(t *testing.T, id, name, perms, expiresAt, revokedAt, createdAt string) {
	t.Helper()
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	testutil.Exec(t, v.db, `INSERT INTO pats (token_id, user_id, name, secret_hash, permissions, expires_days, expires_at, revoked_at, created_at)
		VALUES (?, ?, ?, ?, ?, 90, ?, ?, ?)`, id, owner, name, "hash-"+id, perms, null(expiresAt), null(revokedAt), createdAt)
}

func (v *env) revoked(t *testing.T, tokenID string) bool {
	t.Helper()
	var at sql.NullString
	if err := v.db.QueryRow(`SELECT revoked_at FROM pats WHERE token_id = ?`, tokenID).Scan(&at); err != nil {
		t.Fatalf("revoked_at of %s: %v", tokenID, err)
	}
	return at.Valid
}

func (v *env) bindingsOf(t *testing.T, tokenID string) int {
	t.Helper()
	var n int
	if err := v.db.QueryRow(`SELECT COUNT(*) FROM token_workspace_bindings WHERE token_id = ?`, tokenID).Scan(&n); err != nil {
		return 0 // the table may be gone
	}
	return n
}

// wantRefusal fails the test unless rec is the apikit error envelope with the
// status, message and error_type ("" for none).
func wantRefusal(t *testing.T, rec *httptest.ResponseRecorder, status int, message, errorType string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	if e := testutil.Error(t, rec); e.Message != message || e.ErrorType != errorType {
		t.Fatalf("error %+v, want %q error_type %q", e, message, errorType)
	}
}

// captureRefusals registers a refusal observer for the test and returns the
// refusals it receives.
func captureRefusals(t *testing.T) *[]wsaccess.BoundRefusal {
	t.Helper()
	var got []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { got = append(got, r) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })
	return &got
}

// captureLogs sends slog's default logger to a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}
