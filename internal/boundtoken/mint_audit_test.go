package boundtoken

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

func bindEvents(em *recordingEmitter) []audit.HubEvent {
	var out []audit.HubEvent
	for _, e := range em.events() {
		if e.EventType == audit.EventTokenBind {
			out = append(out, e)
		}
	}
	return out
}

// TS-34-52: A successful mint emits exactly one fully populated
// hub.token.bind event. Requirement: 34-REQ-9.2
func TestMint_BindEvent_TS_34_52(t *testing.T) {
	for _, c := range []struct {
		name, expires string
		want          any
	}{
		{"no expiry", `null`, nil},
		{"expiry", `"2027-01-19T10:00:00Z"`, "2027-01-19T10:00:00Z"},
	} {
		db := openTestDB(t)
		seedWorkspace(t, db, "s", "u1", "ready")
		em := &recordingEmitter{}
		body := `{"token_id":"T1","name":"svc","token":"ak_pat_T1_x","permissions":["a","b"],"expires_at":` + c.expires + `,"created_at":"2026-10-21T10:00:00Z"}`
		env := newTestEnv(t, Config{DB: db, APIKit: mintingStub(db, "T1", "", nil, body), Audit: em})
		if rec := env.do(http.MethodPost, mintPath, apiKey("u1"), `{"name":"svc"}`, nil); rec.Code != http.StatusCreated {
			t.Fatalf("%s: (%d, %s)", c.name, rec.Code, rec.Body)
		}
		evs := bindEvents(em)
		if len(evs) != 1 {
			t.Fatalf("%s: bind events = %d; want 1", c.name, len(evs))
		}
		e := evs[0]
		if e.ResourceType != "token" || e.ResourceID != "T1" || e.Action != "bind" || e.Workspace != "s" || e.ActorID != "u1" || e.ActorType != "api_key" {
			t.Errorf("%s: event %+v", c.name, e)
		}
		perms, _ := e.Metadata["permissions"].([]string)
		if e.Metadata["token_id"] != "T1" || e.Metadata["workspace"] != "s" || !slices.Equal(perms, []string{"a", "b"}) || e.Metadata["expires_at"] != c.want {
			t.Errorf("%s: metadata %#v", c.name, e.Metadata)
		}
		if _, ok := e.Metadata["expires_at"]; !ok {
			t.Errorf("%s: metadata lacks expires_at", c.name)
		}
	}
}

// TS-34-53: hub.token.bind is emitted on a context the caller's cancellation
// does not cancel. Requirement: 34-REQ-9.3
func TestMint_BindEventContext_TS_34_53(t *testing.T) {
	db := openTestDB(t)
	seedWorkspace(t, db, "s", "u1", "ready")
	em := &recordingEmitter{}
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware(), func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx, cancel := context.WithCancel(c.Request().Context())
			cancel() // the caller has gone before the handler emits
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	RegisterRoutes(api, Config{DB: db, APIKit: mintingStub(db, "T1", "ak_pat_T1_x", nil, ""), Audit: em})
	(&testEnv{e: e, db: db}).do(http.MethodPost, mintPath, apiKey("u1"), `{"name":"svc"}`, nil)
	if len(bindEvents(em)) != 1 {
		t.Fatalf("bind events = %d; want 1", len(bindEvents(em)))
	}
	if err := em.ctxs[0].Err(); err != nil {
		t.Errorf("the emission context is done (%v); want it detached from the caller's", err)
	}
}

// TS-34-54: Refused, passed-through and rolled-back mints emit no
// hub.token.bind event. Requirement: 34-REQ-9.4
func TestMint_NoBindEventOnFailure_TS_34_54(t *testing.T) {
	type scenario struct {
		name   string
		auth   *apikit.AuthInfo
		status string
		stub   func(db *sql.DB) http.Handler
		setup  func(db *sql.DB)
	}
	ok := func(db *sql.DB) http.Handler { return mintingStub(db, "T1", "ak_pat_T1_x", nil, "") }
	for _, s := range []scenario{
		{"401", nil, "ready", ok, nil},
		{"bound pat", pat("u1", "TB", "tokens:manage"), "ready", ok, func(db *sql.DB) { bind(t, db, "TB", "s") }},
		{"admin", admin(), "ready", ok, nil},
		{"404", apiKey("u2"), "ready", ok, nil},
		{"400 archived", apiKey("u1"), "archived", ok, nil},
		{"apikit 400", apiKey("u1"), "ready", func(*sql.DB) http.Handler { return newStub(400, `{"error":{"code":400,"message":"x"}}`) }, nil},
		{"undecodable", apiKey("u1"), "ready", func(db *sql.DB) http.Handler { return mintingStub(db, "T1", "", nil, `{nope`) }, nil},
		{"vanished", apiKey("u1"), "ready", func(db *sql.DB) http.Handler {
			return mintingStub(db, "T1", "ak_pat_T1_x", func() { _, _ = db.Exec(`DELETE FROM workspaces`) }, "")
		}, nil},
		{"insert error", apiKey("u1"), "ready", func(db *sql.DB) http.Handler {
			return mintingStub(db, "T1", "ak_pat_T1_x", func() { _, _ = db.Exec(`DROP TABLE token_workspace_bindings`) }, "")
		}, nil},
	} {
		db := openTestDB(t)
		seedWorkspace(t, db, "s", "u1", s.status)
		if s.setup != nil {
			s.setup(db)
		}
		em := &recordingEmitter{}
		env := newTestEnv(t, Config{DB: db, APIKit: s.stub(db), Audit: em})
		rec := env.do(http.MethodPost, mintPath, s.auth, `{"name":"svc"}`, nil)
		if rec.Code == http.StatusCreated {
			t.Fatalf("%s: answered 201; the scenario must fail", s.name)
		}
		if n := len(bindEvents(em)); n != 0 {
			t.Errorf("%s: bind events = %d; want 0", s.name, n)
		}
	}
}

// TS-34-55: A nil emitter or an emit error leaves the 201 response unchanged,
// and the error is logged. Requirement: 34-REQ-9.5
func TestMint_EmitterNilOrFailing_TS_34_55(t *testing.T) {
	for _, c := range []struct {
		name string
		em   audit.Emitter
	}{
		{"nil", nil}, {"failing", &recordingEmitter{err: errors.New("duckdb down")}},
	} {
		logs := captureLogs(t)
		db := openTestDB(t)
		seedWorkspace(t, db, "s", "u1", "ready")
		env := newTestEnv(t, Config{DB: db, APIKit: mintingStub(db, "T1", "ak_pat_T1_x", nil, ""), Audit: c.em})
		rec := env.do(http.MethodPost, mintPath, apiKey("u1"), `{"name":"svc"}`, nil)
		if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"workspace":"s"`) {
			t.Errorf("%s: (%d, %s); want the 201 success body", c.name, rec.Code, rec.Body)
		}
		if c.name == "failing" {
			if !strings.Contains(logs.String(), "emit") || !strings.Contains(logs.String(), "duckdb down") || strings.Contains(logs.String(), "ak_pat_T1_x") {
				t.Errorf("failing: logs %q; want the emit error, without the token", logs)
			}
			if len(bindEvents(c.em.(*recordingEmitter))) != 1 {
				t.Error("failing: Emit was not called")
			}
		}
	}
}
