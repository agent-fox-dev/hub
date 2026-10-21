package boundtoken

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// logBuffer is a log sink that background goroutines may write while the
// test reads it.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs sends slog's default logger to a buffer for the test.
func captureLogs(t *testing.T) *logBuffer {
	t.Helper()
	buf := &logBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// mintingStub answers like apikit's POST /user/tokens: it writes the pats row
// of tokenID and answers 201. before runs after the row is written and before
// the answer (a failure injector); body overrides the 201 body when set.
func mintingStub(db *sql.DB, tokenID, token string, before func(), body string) *stubAPIKit {
	s := &stubAPIKit{}
	s.reply = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		if tokenID != "" {
			if _, err := db.Exec(`INSERT INTO pats (token_id, user_id, name, secret_hash, permissions, expires_days, created_at)
				VALUES (?, 'u1', 'svc', 'hash', '["workspaces:read"]', 90, ?)`, tokenID, apikit.NowUTC()); err != nil {
				panic(err)
			}
		}
		if before != nil {
			before()
		}
		if body == "" {
			body = fmt.Sprintf(`{"token_id":%q,"name":"svc","token":%q,"permissions":["workspaces:read"],"expires_at":"2027-01-19T10:00:00Z","created_at":"2026-10-21T10:00:00Z"}`, tokenID, token)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, body)
	}
	return s
}

func isRevoked(t *testing.T, db *sql.DB, tokenID string) bool {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(`SELECT revoked_at FROM pats WHERE token_id = ?`, tokenID).Scan(&v); err != nil {
		t.Fatalf("revoked_at of %s: %v", tokenID, err)
	}
	return v.Valid
}

func bindingsOf(t *testing.T, db *sql.DB, tokenID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM token_workspace_bindings WHERE token_id = ?`, tokenID).Scan(&n); err != nil {
		return 0 // the table may have been dropped by the injector
	}
	return n
}

// TS-34-11: Any non-201 apikit answer reaches the caller byte-identical, with
// no binding and no event. Requirement: 34-REQ-2.3
func TestMint_PassThroughRefusals_TS_34_11(t *testing.T) {
	answers := []struct {
		status      int
		contentType string
		body        string
	}{
		{400, "application/json; charset=utf-8", `{"error":{"code":400,"message":"invalid request body"}}`},
		{400, "application/json", `{"error":{"code":400,"message":"unknown permission: x:y"}}`},
		{403, "application/json; charset=utf-8", `{"error":{"code":403,"message":"insufficient permissions"}}`},
		{403, "text/plain", `cannot grant permission: workspaces:write`},
		{400, "application/json", `{"error":{"code":400,"message":"expires must be one of 0, 30, 60, 90"}}` + "\n"},
	}
	for _, a := range answers {
		stub := &stubAPIKit{}
		stub.reply = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.Header().Set("Content-Type", a.contentType)
			w.WriteHeader(a.status)
			_, _ = io.WriteString(w, a.body)
		}
		em := &recordingEmitter{}
		env := newTestEnv(t, Config{APIKit: stub, Audit: em})
		seedWorkspace(t, env.db, "s", "u1", "active")
		rec := env.do(http.MethodPost, mintPath, apiKey("u1"), `{"name":"n"}`, nil)
		if rec.Code != a.status || rec.Header().Get("Content-Type") != a.contentType || rec.Body.String() != a.body {
			t.Errorf("(%d, %q, %q); want apikit's (%d, %q, %q)", rec.Code, rec.Header().Get("Content-Type"), rec.Body, a.status, a.contentType, a.body)
		}
		var n int
		_ = env.db.QueryRow(`SELECT COUNT(*) FROM token_workspace_bindings`).Scan(&n)
		if n != 0 || len(em.events()) != 0 {
			t.Errorf("bindings %d, events %d; want 0 and 0", n, len(em.events()))
		}
	}
}

// TS-34-12: A 201 with an undecodable body, or an empty token_id or token,
// answers 500 and revokes a known token; nothing is bound and the token never
// reaches the log. Requirement: 34-REQ-2.4
func TestMint_UndecodableCreated_TS_34_12(t *testing.T) {
	cases := []struct {
		name, tokenID, body string
		knownID             string
	}{
		{"invalid JSON", "", `{not json`, ""},
		{"empty token", "T1", `{"token_id":"T1","token":"","name":"svc"}`, "T1"},
		{"empty token_id", "", `{"token_id":"","token":"ak_pat_x_SECRETVALUE","name":"svc"}`, ""},
		{"no token_id", "", `{"token":"ak_pat_x_SECRETVALUE","name":"svc"}`, ""},
		{"cut short after token_id", "T1", `{"token_id":"T1","token":"ak_pat_T1_SECRETVALUE`, "T1"},
	}
	for _, c := range cases {
		logs := captureLogs(t)
		db := openTestDB(t)
		seedWorkspace(t, db, "s", "u1", "active")
		env := newTestEnv(t, Config{DB: db, APIKit: mintingStub(db, c.tokenID, "", nil, c.body)})
		rec := env.do(http.MethodPost, mintPath, apiKey("u1"), `{"name":"n"}`, nil)
		if rec.Code != http.StatusInternalServerError || parseError(t, rec).Error.Message != "failed to bind the token to the workspace" {
			t.Errorf("%s: (%d, %s); want 500 failed to bind the token to the workspace", c.name, rec.Code, rec.Body)
		}
		if c.knownID != "" && !isRevoked(t, db, c.knownID) {
			t.Errorf("%s: token %s not revoked", c.name, c.knownID)
		}
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM token_workspace_bindings`).Scan(&n)
		if n != 0 {
			t.Errorf("%s: bindings = %d; want 0", c.name, n)
		}
		if strings.Contains(logs.String(), "SECRETVALUE") || strings.Contains(rec.Body.String(), "SECRETVALUE") {
			t.Errorf("%s: the token leaked: logs %q, body %s", c.name, logs, rec.Body)
		}
	}
}

// TS-34-13: A decodable 201 writes exactly one binding row for the token
// naming the slug; the conditional insert writes nothing for a missing
// workspace. Requirement: 34-REQ-3.1
func TestMint_WritesBinding_TS_34_13(t *testing.T) {
	db := openTestDB(t)
	seedWorkspace(t, db, "s", "u1", "active")
	env := newTestEnv(t, Config{DB: db, APIKit: mintingStub(db, "T1", "ak_pat_T1_secret", nil, "")})
	if rec := env.do(http.MethodPost, mintPath, apiKey("u1"), `{"name":"svc"}`, nil); rec.Code != http.StatusCreated {
		t.Fatalf("mint: (%d, %s); want 201", rec.Code, rec.Body)
	}
	var slug, created string
	var n int
	if err := db.QueryRow(`SELECT COUNT(*), MAX(slug), MAX(created_at) FROM token_workspace_bindings WHERE token_id = 'T1'`).Scan(&n, &slug, &created); err != nil || n != 1 || slug != "s" {
		t.Fatalf("bindings of T1: %d naming %q (%v); want one naming s", n, slug, err)
	}
	if _, err := time.Parse(time.RFC3339, created); err != nil || !strings.HasSuffix(created, "Z") {
		t.Errorf("created_at %q is not a UTC timestamp", created)
	}
	written, err := wsaccess.InsertBinding(db, "U", "ghost", apikit.NowUTC())
	if written || err != nil {
		t.Errorf("InsertBinding for a missing workspace = (%v, %v); want (false, nil)", written, err)
	}
}

// TS-34-15: Success answers 201, Cache-Control: no-store and exactly the
// documented body. Requirement: 34-REQ-3.2
func TestMint_SuccessBody_TS_34_15(t *testing.T) {
	db := openTestDB(t)
	seedWorkspace(t, db, "s", "u1", "active")
	env := newTestEnv(t, Config{DB: db, APIKit: mintingStub(db, "T1", "ak_pat_T1_secret", nil, "")})
	rec := env.do(http.MethodPost, mintPath, apiKey("u1"), `{"name":"svc"}`, nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("(%d, Cache-Control %q); want 201 no-store", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"created_at", "expires_at", "name", "permissions", "token", "token_id", "workspace"}; !slices.Equal(keys, want) {
		t.Errorf("keys %v; want %v", keys, want)
	}
	if got["workspace"] != "s" || got["token"] != "ak_pat_T1_secret" || got["token_id"] != "T1" || got["name"] != "svc" ||
		got["expires_at"] != "2027-01-19T10:00:00Z" || got["created_at"] != "2026-10-21T10:00:00Z" {
		t.Errorf("body %v; want apikit's values and workspace s", got)
	}
	if p, ok := got["permissions"].([]any); !ok || len(p) != 1 || p[0] != "workspaces:read" {
		t.Errorf("permissions %v; want [workspaces:read]", got["permissions"])
	}
}

// TS-34-16: A workspace deleted between apikit's 201 and the insert yields
// 404 and a revoked token. Requirement: 34-REQ-3.3
func TestMint_WorkspaceVanished_TS_34_16(t *testing.T) {
	db := openTestDB(t)
	seedWorkspace(t, db, "s", "u1", "active")
	vanish := func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE slug = 's'`) }
	env := newTestEnv(t, Config{DB: db, APIKit: mintingStub(db, "T1", "ak_pat_T1_secret", vanish, "")})
	rec := env.do(http.MethodPost, mintPath, apiKey("u1"), `{"name":"svc"}`, nil)
	if rec.Code != http.StatusNotFound || parseError(t, rec).Error.Message != "workspace not found" {
		t.Errorf("(%d, %s); want 404 workspace not found", rec.Code, rec.Body)
	}
	if !isRevoked(t, db, "T1") || bindingsOf(t, db, "T1") != 0 {
		t.Error("want T1 revoked and unbound")
	}
}

// TS-34-17: A binding insert error or a primary-key conflict yields 500 and a
// revoked token. Requirement: 34-REQ-3.4
func TestMint_InsertFails_TS_34_17(t *testing.T) {
	for name, setup := range map[string]func(db *sql.DB) func(){
		"insert error": func(db *sql.DB) func() {
			return func() { _, _ = db.Exec(`DROP TABLE token_workspace_bindings`) }
		},
		"key conflict": func(db *sql.DB) func() {
			return func() {
				_, _ = db.Exec(`INSERT INTO token_workspace_bindings (token_id, slug, created_at) VALUES ('T1', 'other', 'x')`)
			}
		},
	} {
		db := openTestDB(t)
		seedWorkspace(t, db, "s", "u1", "active")
		env := newTestEnv(t, Config{DB: db, APIKit: mintingStub(db, "T1", "ak_pat_T1_secret", setup(db), "")})
		rec := env.do(http.MethodPost, mintPath, apiKey("u1"), `{"name":"svc"}`, nil)
		if rec.Code != http.StatusInternalServerError || parseError(t, rec).Error.Message != "failed to bind the token to the workspace" {
			t.Errorf("%s: (%d, %s); want 500 failed to bind the token to the workspace", name, rec.Code, rec.Body)
		}
		if !isRevoked(t, db, "T1") {
			t.Errorf("%s: T1 not revoked", name)
		}
		var slug string
		if err := db.QueryRow(`SELECT slug FROM token_workspace_bindings WHERE token_id = 'T1'`).Scan(&slug); err == nil && slug == "s" {
			t.Errorf("%s: a binding of T1 to s was written", name)
		}
	}
}

// TS-34-18: A failing revocation is logged at error level with the token id
// and both errors, and does not change the answer. Requirement: 34-REQ-3.5
func TestMint_RevocationFails_TS_34_18(t *testing.T) {
	type variant struct {
		name, body string
		status     int
		msg        string
		inject     func(db *sql.DB)
	}
	for _, v := range []variant{
		{"insert error", "", 500, "failed to bind the token to the workspace", func(db *sql.DB) {
			_, _ = db.Exec(`DROP TABLE token_workspace_bindings`)
			_, _ = db.Exec(`DROP TABLE pats`)
		}},
		{"vanished workspace", "", 404, "workspace not found", func(db *sql.DB) {
			_, _ = db.Exec(`DELETE FROM workspaces`)
			_, _ = db.Exec(`DROP TABLE pats`)
		}},
		{"undecodable body", `{"token_id":"T1","token":""}`, 500, "failed to bind the token to the workspace", func(db *sql.DB) {
			_, _ = db.Exec(`DROP TABLE pats`)
		}},
	} {
		logs := captureLogs(t)
		db := openTestDB(t)
		seedWorkspace(t, db, "s", "u1", "active")
		env := newTestEnv(t, Config{DB: db, APIKit: mintingStub(db, "T1", "ak_pat_T1_secret", func() { v.inject(db) }, v.body)})
		rec := env.do(http.MethodPost, mintPath, apiKey("u1"), `{"name":"svc"}`, nil)
		if rec.Code != v.status || parseError(t, rec).Error.Message != v.msg {
			t.Errorf("%s: (%d, %s); want %d %s", v.name, rec.Code, rec.Body, v.status, v.msg)
		}
		line := ""
		for _, l := range strings.Split(logs.String(), "\n") {
			if strings.Contains(l, "level=ERROR") && strings.Contains(l, "T1") {
				line = l
			}
		}
		if line == "" || !strings.Contains(line, "revoke_error=") || !strings.Contains(line, "error=") {
			t.Errorf("%s: logs %q; want one error-level line with the token id, the error and the revocation error", v.name, logs)
		}
	}
}

// TS-34-19: No log line on any path contains the plaintext token, apikit's 201
// body or the Authorization value, and the token is returned only on success.
// Requirement: 34-REQ-3.6
func TestMint_NothingSecretLogged_TS_34_19(t *testing.T) {
	const token = "ak_pat_T1_DISTINCTIVE0SECRET"
	const authz = "Bearer ak_key1_DISTINCTIVE0AUTHZ"
	paths := map[string]func(db *sql.DB) http.Handler{
		"success": func(db *sql.DB) http.Handler { return mintingStub(db, "T1", token, nil, "") },
		"passthrough": func(db *sql.DB) http.Handler {
			return newStub(http.StatusForbidden, `{"error":{"code":403,"message":"insufficient permissions"}}`)
		},
		"undecodable": func(db *sql.DB) http.Handler {
			return mintingStub(db, "T1", "", nil, `{"token_id":"T1","token":"`+token+`","name":`)
		},
		"vanished": func(db *sql.DB) http.Handler {
			return mintingStub(db, "T1", token, func() { _, _ = db.Exec(`DELETE FROM workspaces`) }, "")
		},
		"insert error": func(db *sql.DB) http.Handler {
			return mintingStub(db, "T1", token, func() { _, _ = db.Exec(`DROP TABLE token_workspace_bindings`) }, "")
		},
		"audit emit error": func(db *sql.DB) http.Handler { return mintingStub(db, "T1", token, nil, "") },
		"revocation failure": func(db *sql.DB) http.Handler {
			return mintingStub(db, "T1", token, func() {
				_, _ = db.Exec(`DROP TABLE token_workspace_bindings`)
				_, _ = db.Exec(`DROP TABLE pats`)
			}, "")
		},
	}
	for name, stub := range paths {
		logs := captureLogs(t)
		db := openTestDB(t)
		seedWorkspace(t, db, "s", "u1", "active")
		cfg := Config{DB: db, APIKit: stub(db)}
		if name == "audit emit error" {
			cfg.Audit = &recordingEmitter{err: errors.New("emit failed")}
		}
		env := newTestEnv(t, cfg)
		rec := env.do(http.MethodPost, mintPath, apiKey("u1"), `{"name":"svc"}`, http.Header{"Authorization": {authz}})
		if name == "audit emit error" && !strings.Contains(logs.String(), "emit failed") {
			t.Errorf("audit emit error: the emit error was not logged: %s", logs)
		}
		for _, secret := range []string{token, "DISTINCTIVE0SECRET", "DISTINCTIVE0AUTHZ", `"token_id":"T1"`, `"name":"svc","token"`} {
			if strings.Contains(logs.String(), secret) {
				t.Errorf("%s: the log contains %q: %s", name, secret, logs)
			}
		}
		success := name == "success" || name == "audit emit error"
		if !success && strings.Contains(rec.Body.String(), "DISTINCTIVE0SECRET") {
			t.Errorf("%s: the response contains the token: %s", name, rec.Body)
		}
		if success && !strings.Contains(rec.Body.String(), token) {
			t.Errorf("%s: the 201 lacks the token: %s", name, rec.Body)
		}
	}
}

// TS-34-20 (property): For any failure injected after apikit's 201 and any
// token name and permission set, the token is revoked and unbound when the
// response is written. Requirement: 34-REQ-3.7
func TestMint_PropertyRollback_TS_34_20(t *testing.T) {
	failures := map[string]struct {
		body   func(id string) string
		inject func(db *sql.DB, id string)
	}{
		"undecodable": {func(id string) string { return `{"token_id":"` + id + `","token":"",` }, nil},
		"vanished":    {nil, func(db *sql.DB, _ string) { _, _ = db.Exec(`DELETE FROM workspaces`) }},
		"insert error": {nil, func(db *sql.DB, _ string) {
			_, _ = db.Exec(`CREATE TRIGGER block BEFORE INSERT ON token_workspace_bindings BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		}},
		"key conflict": {nil, func(db *sql.DB, id string) {
			_, _ = db.Exec(`INSERT INTO token_workspace_bindings (token_id, slug, created_at) VALUES (?, 'zz', 'x')`, id)
		}},
	}
	names := []string{"svc", "a b", "ü"}
	perms := []string{`[]`, `["workspaces:read"]`, `["workspaces:read","patches:write"]`}
	for fname, f := range failures {
		for i, name := range names {
			id := fmt.Sprintf("T%d", i)
			db := openTestDB(t)
			seedWorkspace(t, db, "s", "u1", "active")
			var before func()
			if f.inject != nil {
				before = func() { f.inject(db, id) }
			}
			body := ""
			if f.body != nil {
				body = f.body(id)
			}
			env := newTestEnv(t, Config{DB: db, APIKit: mintingStub(db, id, "ak_pat_"+id+"_s", before, body)})
			req := fmt.Sprintf(`{"name":%q,"permissions":%s}`, name, perms[i])
			rec := env.do(http.MethodPost, mintPath, apiKey("u1"), req, nil)
			if rec.Code != http.StatusNotFound && rec.Code != http.StatusInternalServerError {
				t.Errorf("%s/%s: status %d; want 404 or 500", fname, name, rec.Code)
			}
			if !isRevoked(t, db, id) {
				t.Errorf("%s/%s: token not revoked", fname, name)
			}
			var bound string
			if err := db.QueryRow(`SELECT slug FROM token_workspace_bindings WHERE token_id = ?`, id).Scan(&bound); err == nil && bound == "s" {
				t.Errorf("%s/%s: token bound to s", fname, name)
			}
		}
	}
}
