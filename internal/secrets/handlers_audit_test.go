package secrets

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// ===========================================================================
// Mock Audit Emitter for secrets tests
// ===========================================================================

type secretsAuditEmitter struct {
	mu     sync.Mutex
	events []audit.HubEvent
}

func newSecretsAuditEmitter() *secretsAuditEmitter {
	return &secretsAuditEmitter{}
}

func (m *secretsAuditEmitter) Emit(_ context.Context, event audit.HubEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *secretsAuditEmitter) Events() []audit.HubEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]audit.HubEvent, len(m.events))
	copy(result, m.events)
	return result
}

func (m *secretsAuditEmitter) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = nil
}

// failingSecretsAuditEmitter always returns an error from Emit.
type failingSecretsAuditEmitter struct{}

func (f *failingSecretsAuditEmitter) Emit(_ context.Context, _ audit.HubEvent) error {
	return context.DeadlineExceeded
}

// ===========================================================================
// Audit-aware test environment for secrets
// ===========================================================================

// secretsAuditTestEnv wraps handlerTestEnv with a capturing audit emitter.
type secretsAuditTestEnv struct {
	*handlerTestEnv
	emitter *secretsAuditEmitter
}

// newSecretsAuditTestEnv creates a secrets test environment with a capturing
// audit emitter wired into the route registration.
func newSecretsAuditTestEnv(t *testing.T) *secretsAuditTestEnv {
	t.Helper()

	mock := newSecretsAuditEmitter()
	db := openTestDB(t)

	// Create supporting tables for org/workspace scope tests.
	schemaSQL := []string{
		`CREATE TABLE IF NOT EXISTS orgs (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			url TEXT,
			owner_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS org_members (
			org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (org_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			slug TEXT NOT NULL PRIMARY KEY,
			git_url TEXT NOT NULL,
			branch TEXT,
			owner_id TEXT NOT NULL,
			org_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			display_name TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			clone_status TEXT NOT NULL DEFAULT 'pending',
			head_sha TEXT,
			clone_error TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range schemaSQL {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create supporting schema: %v", err)
		}
	}

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())

	cfg := SecretsRouteConfig{
		DB:    db,
		Audit: mock,
	}
	if err := RegisterRoutesWithAudit(api, cfg); err != nil {
		t.Fatalf("RegisterRoutesWithAudit() returned error: %v", err)
	}

	return &secretsAuditTestEnv{
		handlerTestEnv: &handlerTestEnv{echo: e, db: db},
		emitter:        mock,
	}
}

// doAuditRequest performs an HTTP request against the given echo instance.
func doSecretsAuditRequest(t *testing.T, e *echo.Echo, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	if auth != nil {
		authJSON, err := json.Marshal(auth)
		if err != nil {
			t.Fatalf("failed to marshal auth info: %v", err)
		}
		req.Header.Set("X-Test-Auth", string(authJSON))
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// ===========================================================================
// TS-18-18: Secret create emits hub.secret.create with metadata {scope, key}
// REQ: 18-REQ-4.1
// ===========================================================================

func TestSecretCreateAuditEmission(t *testing.T) {
	env := newSecretsAuditTestEnv(t)
	auth := userAuth("user-1")

	body := `{"entries":[{"key":"MY_TOKEN","value":"secret-value"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	events := env.emitter.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one audit event to be emitted, got none")
	}

	event := events[0]
	if event.EventType != "hub.secret.create" {
		t.Errorf("event_type: want %q, got %q", "hub.secret.create", event.EventType)
	}
	if event.Metadata == nil {
		t.Fatal("metadata should not be nil")
	}
	if v, ok := event.Metadata["scope"]; !ok || v != "user" {
		t.Errorf("metadata[scope]: want %q, got %v", "user", v)
	}
	if v, ok := event.Metadata["key"]; !ok || v != "MY_TOKEN" {
		t.Errorf("metadata[key]: want %q, got %v", "MY_TOKEN", v)
	}
	if event.ActorID != "user-1" {
		t.Errorf("actor_id: want %q, got %q", "user-1", event.ActorID)
	}
}

// ===========================================================================
// TS-18-19: Secret update emits hub.secret.update with metadata {scope, key}
// REQ: 18-REQ-4.2
// ===========================================================================

func TestSecretUpdateAuditEmission(t *testing.T) {
	env := newSecretsAuditTestEnv(t)
	auth := userAuth("user-1")

	// Create a secret first.
	seedSecret(t, env.db, "user", "user-1", "MY_TOKEN", "old-value")

	// Clear any creation events.
	env.emitter.Reset()

	body := `{"value":"new-secret-value"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/MY_TOKEN", body, auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	events := env.emitter.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one audit event for secret update, got none")
	}

	event := events[0]
	if event.EventType != "hub.secret.update" {
		t.Errorf("event_type: want %q, got %q", "hub.secret.update", event.EventType)
	}
	if event.Metadata == nil {
		t.Fatal("metadata should not be nil")
	}
	if v, ok := event.Metadata["scope"]; !ok || v != "user" {
		t.Errorf("metadata[scope]: want %q, got %v", "user", v)
	}
	if v, ok := event.Metadata["key"]; !ok || v != "MY_TOKEN" {
		t.Errorf("metadata[key]: want %q, got %v", "MY_TOKEN", v)
	}
	if event.ActorID != "user-1" {
		t.Errorf("actor_id: want %q, got %q", "user-1", event.ActorID)
	}
}

// ===========================================================================
// TS-18-20: Secret delete emits hub.secret.delete with metadata {scope, key}
// REQ: 18-REQ-4.3
// ===========================================================================

func TestSecretDeleteAuditEmission(t *testing.T) {
	env := newSecretsAuditTestEnv(t)
	auth := userAuth("user-1")

	// Create a secret first.
	seedSecret(t, env.db, "user", "user-1", "MY_TOKEN", "to-delete")

	// Clear any creation events.
	env.emitter.Reset()

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/user/secrets/MY_TOKEN", "", auth)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", rec.Code, rec.Body.String())
	}

	events := env.emitter.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one audit event for secret delete, got none")
	}

	event := events[0]
	if event.EventType != "hub.secret.delete" {
		t.Errorf("event_type: want %q, got %q", "hub.secret.delete", event.EventType)
	}
	if event.Metadata == nil {
		t.Fatal("metadata should not be nil")
	}
	if v, ok := event.Metadata["scope"]; !ok || v != "user" {
		t.Errorf("metadata[scope]: want %q, got %v", "user", v)
	}
	if v, ok := event.Metadata["key"]; !ok || v != "MY_TOKEN" {
		t.Errorf("metadata[key]: want %q, got %v", "MY_TOKEN", v)
	}
	if event.ActorID != "user-1" {
		t.Errorf("actor_id: want %q, got %q", "user-1", event.ActorID)
	}
}

// ===========================================================================
// TS-18-21: Variable create emits hub.variable.create with metadata {scope, key}
// REQ: 18-REQ-4.4
// ===========================================================================

func TestVariableCreateAuditEmission(t *testing.T) {
	env := newSecretsAuditTestEnv(t)
	auth := userAuth("user-1")

	body := `{"entries":[{"key":"MY_VAR","value":"var-value"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/vars", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	events := env.emitter.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one audit event to be emitted, got none")
	}

	event := events[0]
	if event.EventType != "hub.variable.create" {
		t.Errorf("event_type: want %q, got %q", "hub.variable.create", event.EventType)
	}
	if event.Metadata == nil {
		t.Fatal("metadata should not be nil")
	}
	if v, ok := event.Metadata["scope"]; !ok || v != "user" {
		t.Errorf("metadata[scope]: want %q, got %v", "user", v)
	}
	if v, ok := event.Metadata["key"]; !ok || v != "MY_VAR" {
		t.Errorf("metadata[key]: want %q, got %v", "MY_VAR", v)
	}
	if event.ActorID != "user-1" {
		t.Errorf("actor_id: want %q, got %q", "user-1", event.ActorID)
	}
	if event.ActorType == "" {
		t.Error("actor_type should be populated from apikit.GetAuthInfo")
	}
}

// ===========================================================================
// TS-18-22: Variable update emits hub.variable.update with metadata {scope, key}
// REQ: 18-REQ-4.5
// ===========================================================================

func TestVariableUpdateAuditEmission(t *testing.T) {
	env := newSecretsAuditTestEnv(t)
	auth := userAuth("user-1")

	// Create a variable first.
	seedVariable(t, env.db, "user", "user-1", "MY_VAR", "old-value")

	// Clear any creation events.
	env.emitter.Reset()

	body := `{"value":"new-var-value"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/vars/MY_VAR", body, auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	events := env.emitter.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one audit event for variable update, got none")
	}

	event := events[0]
	if event.EventType != "hub.variable.update" {
		t.Errorf("event_type: want %q, got %q", "hub.variable.update", event.EventType)
	}
	if event.Metadata == nil {
		t.Fatal("metadata should not be nil")
	}
	if v, ok := event.Metadata["scope"]; !ok || v != "user" {
		t.Errorf("metadata[scope]: want %q, got %v", "user", v)
	}
	if v, ok := event.Metadata["key"]; !ok || v != "MY_VAR" {
		t.Errorf("metadata[key]: want %q, got %v", "MY_VAR", v)
	}
	if event.ActorID != "user-1" {
		t.Errorf("actor_id: want %q, got %q", "user-1", event.ActorID)
	}
	if event.ActorType == "" {
		t.Error("actor_type should be populated from apikit.GetAuthInfo")
	}
}

// ===========================================================================
// TS-18-23: Variable delete emits hub.variable.delete with metadata {scope, key}
// REQ: 18-REQ-4.6
// ===========================================================================

func TestVariableDeleteAuditEmission(t *testing.T) {
	env := newSecretsAuditTestEnv(t)
	auth := userAuth("user-1")

	// Create a variable first.
	seedVariable(t, env.db, "user", "user-1", "MY_VAR", "to-delete")

	// Clear any creation events.
	env.emitter.Reset()

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/user/vars/MY_VAR", "", auth)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", rec.Code, rec.Body.String())
	}

	events := env.emitter.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one audit event for variable delete, got none")
	}

	event := events[0]
	if event.EventType != "hub.variable.delete" {
		t.Errorf("event_type: want %q, got %q", "hub.variable.delete", event.EventType)
	}
	if event.Metadata == nil {
		t.Fatal("metadata should not be nil")
	}
	if v, ok := event.Metadata["scope"]; !ok || v != "user" {
		t.Errorf("metadata[scope]: want %q, got %v", "user", v)
	}
	if v, ok := event.Metadata["key"]; !ok || v != "MY_VAR" {
		t.Errorf("metadata[key]: want %q, got %v", "MY_VAR", v)
	}
	if event.ActorID != "user-1" {
		t.Errorf("actor_id: want %q, got %q", "user-1", event.ActorID)
	}
	if event.ActorType == "" {
		t.Error("actor_type should be populated from apikit.GetAuthInfo")
	}
}

// ===========================================================================
// TS-18-24: Secrets route registration accepts an audit.Emitter parameter
//           and passes it to all secret and variable CRUD handlers
// REQ: 18-REQ-4.7
// ===========================================================================

func TestSecretsRouteRegistrationAcceptsEmitter(t *testing.T) {
	mock := newSecretsAuditEmitter()

	cfg := SecretsRouteConfig{Audit: mock}

	// Verify the Audit field is set and implements the audit.Emitter interface.
	if cfg.Audit == nil {
		t.Fatal("SecretsRouteConfig.Audit should not be nil when set")
	}

	// Verify it's the correct emitter by emitting and checking.
	_ = cfg.Audit.Emit(context.Background(), audit.HubEvent{EventType: "test"})
	events := mock.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventType != "test" {
		t.Errorf("event_type: want %q, got %q", "test", events[0].EventType)
	}
}

// TestSecretsAllCRUDHandlersReceiveEmitter verifies that all six CRUD
// operations (create/update/delete for both secrets and variables) emit
// audit events when the emitter is configured.
func TestSecretsAllCRUDHandlersReceiveEmitter(t *testing.T) {
	env := newSecretsAuditTestEnv(t)
	auth := userAuth("user-1")

	// --- Secret operations ---

	// 1. Create secret
	createSecretBody := `{"entries":[{"key":"S_KEY","value":"s-value"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", createSecretBody, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create secret: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Update secret
	updateSecretBody := `{"value":"new-s-value"}`
	rec = env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/S_KEY", updateSecretBody, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("update secret: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3. Delete secret
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/user/secrets/S_KEY", "", auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete secret: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// --- Variable operations ---

	// 4. Create variable
	createVarBody := `{"entries":[{"key":"V_KEY","value":"v-value"}]}`
	rec = env.doRequest(t, http.MethodPost, "/api/v1/user/vars", createVarBody, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create variable: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// 5. Update variable
	updateVarBody := `{"value":"new-v-value"}`
	rec = env.doRequest(t, http.MethodPatch, "/api/v1/user/vars/V_KEY", updateVarBody, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("update variable: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 6. Delete variable
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/user/vars/V_KEY", "", auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete variable: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// All six CRUD operations should have emitted exactly one event each.
	events := env.emitter.Events()
	if len(events) != 6 {
		t.Fatalf("expected 6 audit events (one per CRUD op), got %d", len(events))
	}

	expectedTypes := []string{
		"hub.secret.create",
		"hub.secret.update",
		"hub.secret.delete",
		"hub.variable.create",
		"hub.variable.update",
		"hub.variable.delete",
	}
	for i, want := range expectedTypes {
		if events[i].EventType != want {
			t.Errorf("event[%d].event_type: want %q, got %q", i, want, events[i].EventType)
		}
	}
}

// ===========================================================================
// Edge case: Nil Emitter does not panic during secrets route registration
// REQ: 18-REQ-4.E2
// ===========================================================================

func TestSecretsNilEmitterDoesNotPanic(t *testing.T) {
	db := openTestDB(t)

	// Create supporting tables.
	schemaSQL := []string{
		`CREATE TABLE IF NOT EXISTS orgs (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			url TEXT,
			owner_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS org_members (
			org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (org_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			slug TEXT NOT NULL PRIMARY KEY,
			git_url TEXT NOT NULL,
			branch TEXT,
			owner_id TEXT NOT NULL,
			org_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			display_name TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			clone_status TEXT NOT NULL DEFAULT 'pending',
			head_sha TEXT,
			clone_error TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range schemaSQL {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create supporting schema: %v", err)
		}
	}

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())

	// Register with nil Audit — should not panic.
	cfg := SecretsRouteConfig{
		DB:    db,
		Audit: nil,
	}
	if err := RegisterRoutesWithAudit(api, cfg); err != nil {
		t.Fatalf("RegisterRoutesWithAudit() returned error: %v", err)
	}

	auth := userAuth("user-1")
	env := &handlerTestEnv{echo: e, db: db}

	// Create a secret — should succeed without panicking.
	body := `{"entries":[{"key":"NIL_KEY","value":"nil-value"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with nil Audit: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Update — should succeed without panicking.
	updateBody := `{"value":"updated"}`
	rec = env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/NIL_KEY", updateBody, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("update with nil Audit: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Delete — should succeed without panicking.
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/user/secrets/NIL_KEY", "", auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete with nil Audit: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestVariablesNilEmitterDoesNotPanic(t *testing.T) {
	db := openTestDB(t)

	schemaSQL := []string{
		`CREATE TABLE IF NOT EXISTS orgs (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			url TEXT,
			owner_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS org_members (
			org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (org_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			slug TEXT NOT NULL PRIMARY KEY,
			git_url TEXT NOT NULL,
			branch TEXT,
			owner_id TEXT NOT NULL,
			org_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			display_name TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			clone_status TEXT NOT NULL DEFAULT 'pending',
			head_sha TEXT,
			clone_error TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range schemaSQL {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create supporting schema: %v", err)
		}
	}

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())

	cfg := SecretsRouteConfig{
		DB:    db,
		Audit: nil,
	}
	if err := RegisterRoutesWithAudit(api, cfg); err != nil {
		t.Fatalf("RegisterRoutesWithAudit() returned error: %v", err)
	}

	auth := userAuth("user-1")
	env := &handlerTestEnv{echo: e, db: db}

	// Create a variable — should succeed without panicking.
	body := `{"entries":[{"key":"NIL_VAR","value":"nil-value"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/vars", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create var with nil Audit: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Update — should succeed without panicking.
	updateBody := `{"value":"updated"}`
	rec = env.doRequest(t, http.MethodPatch, "/api/v1/user/vars/NIL_VAR", updateBody, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("update var with nil Audit: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Delete — should succeed without panicking.
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/user/vars/NIL_VAR", "", auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete var with nil Audit: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ===========================================================================
// Edge case: Emitter.Emit error does not affect CRUD operation result
// REQ: 18-REQ-4.E1
// ===========================================================================

func TestSecretsAuditEmitErrorDoesNotAffectResponse(t *testing.T) {
	failEmitter := &failingSecretsAuditEmitter{}
	db := openTestDB(t)

	schemaSQL := []string{
		`CREATE TABLE IF NOT EXISTS orgs (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			url TEXT,
			owner_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS org_members (
			org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (org_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			slug TEXT NOT NULL PRIMARY KEY,
			git_url TEXT NOT NULL,
			branch TEXT,
			owner_id TEXT NOT NULL,
			org_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			display_name TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			clone_status TEXT NOT NULL DEFAULT 'pending',
			head_sha TEXT,
			clone_error TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range schemaSQL {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create supporting schema: %v", err)
		}
	}

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())

	cfg := SecretsRouteConfig{
		DB:    db,
		Audit: failEmitter,
	}
	if err := RegisterRoutesWithAudit(api, cfg); err != nil {
		t.Fatalf("RegisterRoutesWithAudit() returned error: %v", err)
	}

	auth := userAuth("user-1")
	env := &handlerTestEnv{echo: e, db: db}

	// Create should succeed despite Emit errors.
	body := `{"entries":[{"key":"FAIL_KEY","value":"fail-value"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with failing Audit: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Update should succeed despite Emit errors.
	updateBody := `{"value":"updated"}`
	rec = env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/FAIL_KEY", updateBody, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("update with failing Audit: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Delete should succeed despite Emit errors.
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/user/secrets/FAIL_KEY", "", auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete with failing Audit: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}
