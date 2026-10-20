package sandboxes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
)

// createWorkspaceTables creates the workspaces and org_members tables with the
// columns the lease code reads, as the other packages' tests do.
func createWorkspaceTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS workspaces (slug TEXT PRIMARY KEY, owner_id TEXT NOT NULL, org_id TEXT, status TEXT NOT NULL DEFAULT 'active', clone_status TEXT NOT NULL DEFAULT 'ready')`,
		`CREATE TABLE IF NOT EXISTS org_members (org_id TEXT NOT NULL, user_id TEXT NOT NULL, PRIMARY KEY (org_id, user_id))`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("creating a workspace table: %v", err)
		}
	}
}

// leaseClock is a clock a test moves by hand.
type leaseClock struct {
	mu  sync.Mutex
	now time.Time
}

func newLeaseClock(now time.Time) *leaseClock { return &leaseClock{now: now} }

func (c *leaseClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *leaseClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// recordingEmitter is an audit.Emitter that keeps what it is given.
type recordingEmitter struct {
	mu     sync.Mutex
	events []audit.HubEvent
	err    error
}

func (r *recordingEmitter) Emit(_ context.Context, event audit.HubEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return r.err
}

func (r *recordingEmitter) recorded() []audit.HubEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.HubEvent(nil), r.events...)
}

// leaseOwner is the user who owns the test workspaces; apiKeyAuth() is theirs.
const leaseOwner = "user-1"

// leaseEnvOptions changes how newLeaseEnv builds its environment.
type leaseEnvOptions struct {
	// config replaces the default [sandbox] section.
	config *Config
	// lister replaces the real secrets store in the service.
	lister SecretLister
	// rand replaces crypto/rand in the service.
	rand io.Reader
	// emitter is the audit emitter; nil means none.
	emitter audit.Emitter
	// db replaces the in-memory database.
	db *sql.DB
	// start is the clock's first reading.
	start time.Time
	// provider replaces the provider the profile service is checked against.
	provider sandbox.Provider
	// noEmitter builds a backend environment with no audit emitter at all. An
	// emitter given in emitter is used in place of the recording one.
	noEmitter bool
	// metrics receives the sandbox metrics; nil records none.
	metrics *audit.Metrics
}

// defaultLeaseConfig is a [sandbox] section with a default profile "tools",
// which requires GIT_PAT, and a second profile "data".
func defaultLeaseConfig() *Config {
	return &Config{
		Provider: "podman",
		// A free port: any test that starts the gateway must not take 8443.
		Listen: "127.0.0.1:0",
		Profiles: []ProfileConfig{
			{
				Name: "tools", Default: true, Image: "registry.example/tools:1", TTL: "1h",
				CPUs: 1, MemoryMB: 512, PIDs: 100, RequireLandlock: true,
				Secrets: []string{"GIT_PAT"}, OptionalSecrets: []string{"EXTRA_TOKEN"},
				Env: map[string]string{"SECRET_ENV": "envval"},
			},
			{Name: "data", Image: "registry.example/data:1", TTL: "30m"},
		},
	}
}

// gatewayLeaseConfig is defaultLeaseConfig without require_landlock. The real
// client checks the Landlock report of the agent against the spec it gets back,
// and the in-process agent of AgentKit's local provider confines nothing.
func gatewayLeaseConfig() *Config {
	cfg := defaultLeaseConfig()
	cfg.Profiles[0].RequireLandlock = false
	return cfg
}

// leaseEnv is a lease service over a real database with the routes mounted
// behind the test auth middleware.
type leaseEnv struct {
	t       *testing.T
	db      *sql.DB
	store   *LeaseStore
	svc     *LeaseService
	profile *Service
	secrets *secrets.Store
	clock   *leaseClock
	echo    *echo.Echo
}

// newLeaseEnv builds the environment and the workspace "widgets", owned by
// leaseOwner, with the secret GIT_PAT planted in its workspace scope.
func newLeaseEnv(t *testing.T, opts leaseEnvOptions) *leaseEnv {
	t.Helper()
	db := opts.db
	if db == nil {
		db = openLeaseDB(t)
	}
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := secrets.InitSchema(db); err != nil {
		t.Fatalf("secrets.InitSchema: %v", err)
	}
	createWorkspaceTables(t, db)

	cfg := opts.config
	if cfg == nil {
		cfg = defaultLeaseConfig()
	}
	var provider sandbox.Provider = newFakeProvider()
	if opts.provider != nil {
		provider = opts.provider
	}
	profileSvc, err := New(requireValid(t, cfg), provider)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := opts.start
	if start.IsZero() {
		start = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	}
	clock := newLeaseClock(start)
	store := secrets.NewStore(db)
	var lister SecretLister = store
	if opts.lister != nil {
		lister = opts.lister
	}
	svc := NewLeaseService(LeaseConfig{
		Service: profileSvc, DB: db, Secrets: lister, Audit: opts.emitter, Now: clock.Now, Rand: opts.rand,
		Metrics: opts.metrics,
	})

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	RegisterLeaseRoutes(api, svc)

	env := &leaseEnv{t: t, db: db, store: NewLeaseStore(db, clock.Now).WithMetrics(opts.metrics), svc: svc, profile: profileSvc, secrets: store, clock: clock, echo: e}
	env.addWorkspace("widgets", leaseOwner, "", "active")
	env.plantSecret("workspace", "widgets", "GIT_PAT", "PLANTED-VALUE-123")
	return env
}

// addWorkspace inserts a workspace; org may be "" for none.
func (e *leaseEnv) addWorkspace(slug, owner, org, status string) {
	e.t.Helper()
	insertWorkspace(e.t, e.db, slug, owner, org)
	if status != "active" {
		if _, err := e.db.Exec(`UPDATE workspaces SET status = ? WHERE slug = ?`, status, slug); err != nil {
			e.t.Fatalf("setting the status of %s: %v", slug, err)
		}
	}
}

// plantSecret stores a secret with a value in the real secrets store.
func (e *leaseEnv) plantSecret(ownerType, ownerID, key, value string) {
	e.t.Helper()
	if _, err := e.secrets.CreateSecrets(ownerType, ownerID, []secrets.EntryInput{{Key: key, Value: value}}); err != nil {
		e.t.Fatalf("planting secret %s: %v", key, err)
	}
}

// do sends a request to the mounted routes. auth nil means unauthenticated.
func (e *leaseEnv) do(method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.serve(method, path, body, auth)
}

// serve sends a request to the environment's router.
func (e *leaseEnv) serve(method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	if auth != nil {
		header, err := json.Marshal(auth)
		if err != nil {
			panic(fmt.Sprintf("marshalling auth info: %v", err))
		}
		req.Header.Set("X-Test-Auth", string(header))
	}
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	return rec
}

// post issues a lease in workspace slug.
func (e *leaseEnv) post(slug, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/v1/workspaces/"+slug+"/sandboxes", body, auth)
}

// mustIssue posts {"profile":"tools"} as the owner and returns the decoded
// 201 body.
func (e *leaseEnv) mustIssue() map[string]any {
	e.t.Helper()
	rec := e.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
	}
	return jsonObject(e.t, rec)
}

// jsonObject decodes a response body that is a JSON object.
func jsonObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &obj); err != nil {
		t.Fatalf("body is not a JSON object: %v\n%s", err, rec.Body)
	}
	return obj
}

// message returns the error message of an apikit error response.
func message(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	obj, ok := jsonObject(t, rec)["error"].(map[string]any)
	if !ok {
		t.Fatalf("not an apikit error envelope: %s", rec.Body)
	}
	msg, _ := obj["message"].(string)
	return msg
}

// errorType returns the error_type of an apikit error response.
func errorType(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	obj, ok := jsonObject(t, rec)["error"].(map[string]any)
	if !ok {
		t.Fatalf("not an apikit error envelope: %s", rec.Body)
	}
	kind, _ := obj["error_type"].(string)
	return kind
}

// countLeases returns the number of rows in sandbox_leases.
func (e *leaseEnv) countLeases() int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM sandbox_leases`).Scan(&n); err != nil {
		e.t.Fatalf("counting leases: %v", err)
	}
	return n
}

// rowText returns every column of a lease row as text, for scanning a row for
// a string that must not be in it.
func (e *leaseEnv) rowText(id string) []string {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT * FROM sandbox_leases WHERE id = ?`, id)
	if err != nil {
		e.t.Fatalf("reading lease %s: %v", id, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		e.t.Fatalf("lease %s not found", id)
	}
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		e.t.Fatalf("scanning lease %s: %v", id, err)
	}
	out := make([]string, len(cols))
	for i, v := range values {
		out[i] = fmt.Sprint(v)
	}
	return out
}

// recordingProvider is a sandbox.Provider for the gateway tests. It records the
// spec of every Create, then makes a real sandbox with AgentKit's local
// provider, which has an agent to relay to but honours no image, no resources
// and only an unrestricted network: the spec handed to the local provider is
// reduced to what it accepts, and the box reports the spec hub asked for, as a
// container provider reports its effective spec.
type recordingProvider struct {
	mu        sync.Mutex
	caps      sandbox.Capabilities
	local     sandbox.Provider
	specs     []sandbox.Spec
	boxes     []*recordedBox
	createErr error
	// closeErr, when set, is what every box's Close returns, without closing.
	closeErr error
	// closeBlock, when set, makes every box's Close wait until it is closed or
	// the context of the Close ends.
	closeBlock chan struct{}
	block      chan struct{}
	// createDelay, when set, is how long every Create takes before it makes the
	// box.
	createDelay time.Duration
	// reapIDs and reapErr are what Reap returns.
	reapIDs []string
	reapErr error
	reaps   int
	// entered receives one value for each Create that waits on block.
	entered chan struct{}
}

func newRecordingProvider() *recordingProvider {
	return &recordingProvider{
		local:   local.Unisolated(),
		entered: make(chan struct{}, 64),
		caps: sandbox.Capabilities{
			Provider:  "recording",
			Isolation: sandbox.IsolationContainer,
			Network:   []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkAllowlist, sandbox.NetworkUnrestricted},
			Resources: sandbox.ResourceCaps{CPUs: true, MemoryBytes: true, PIDs: true, DiskBytes: true},
			Image:     true,
			Workdir:   true,
			Landlock:  true,
		},
	}
}

func (p *recordingProvider) Capabilities() sandbox.Capabilities {
	p.mu.Lock()
	defer p.mu.Unlock()
	caps := p.caps
	caps.Network = slices.Clone(caps.Network)
	return caps
}

func (p *recordingProvider) Reap(context.Context) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reaps++
	return slices.Clone(p.reapIDs), p.reapErr
}

// setReap makes Reap return ids and err.
func (p *recordingProvider) setReap(ids []string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reapIDs, p.reapErr = ids, err
}

func (p *recordingProvider) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	p.mu.Lock()
	p.specs = append(p.specs, cloneSpec(spec))
	failure, block, delay := p.createErr, p.block, p.createDelay
	p.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if block != nil {
		p.entered <- struct{}{}
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if failure != nil {
		return nil, failure
	}
	inner, err := p.local.Create(ctx, sandbox.Spec{
		Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted},
		TTL:     spec.TTL,
		Env:     maps.Clone(spec.Env),
		Labels:  maps.Clone(spec.Labels),
	})
	if err != nil {
		return nil, err
	}
	box := &recordedBox{Sandbox: inner, p: p, spec: cloneSpec(spec)}
	p.mu.Lock()
	p.boxes = append(p.boxes, box)
	p.mu.Unlock()
	return box, nil
}

// creates is the number of Create calls.
func (p *recordingProvider) creates() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.specs)
}

// lastSpec is the spec of the latest Create call.
func (p *recordingProvider) lastSpec() sandbox.Spec {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.specs) == 0 {
		return sandbox.Spec{}
	}
	return cloneSpec(p.specs[len(p.specs)-1])
}

// live lists the ids of the boxes made and not closed.
func (p *recordingProvider) live() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ids []string
	for _, b := range p.boxes {
		if !b.closed {
			ids = append(ids, b.ID())
		}
	}
	return ids
}

// liveBoxes lists the boxes made and not closed.
func (p *recordingProvider) liveBoxes() []*recordedBox {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*recordedBox
	for _, b := range p.boxes {
		if !b.closed {
			out = append(out, b)
		}
	}
	return out
}

// recordedBox is a box of the recordingProvider: the local box underneath, the
// spec hub asked for, and a record of the calls that reach it.
type recordedBox struct {
	sandbox.Sandbox
	p      *recordingProvider
	spec   sandbox.Spec
	closed bool
	// closes counts the Close calls, and deadlines is the time each of them had
	// left on its context, or -1 for a context without a deadline.
	closes    int
	deadlines []time.Duration
	syncs     []sandbox.Source
	exports   []string
}

// Unwrap lets the gateway reach the agent of the local box.
func (b *recordedBox) Unwrap() sandbox.Sandbox { return b.Sandbox }

// Spec returns the box's spec as a provider's box does, with the maps it holds
// and not copies of them, so that a caller that changes them in place is seen.
func (b *recordedBox) Spec() sandbox.Spec { return b.spec }

func (b *recordedBox) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	b.p.mu.Lock()
	b.syncs = append(b.syncs, src)
	b.p.mu.Unlock()
	return b.Sandbox.Sync(ctx, src)
}

func (b *recordedBox) Export(ctx context.Context, path string, dst sandbox.Destination) error {
	b.p.mu.Lock()
	b.exports = append(b.exports, path)
	b.p.mu.Unlock()
	return b.Sandbox.Export(ctx, path, dst)
}

func (b *recordedBox) Close(ctx context.Context) error {
	left := time.Duration(-1)
	if deadline, ok := ctx.Deadline(); ok {
		left = time.Until(deadline)
	}
	b.p.mu.Lock()
	b.closes++
	b.deadlines = append(b.deadlines, left)
	failure, wait := b.p.closeErr, b.p.closeBlock
	b.p.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if failure != nil {
		return failure
	}
	err := b.Sandbox.Close(ctx)
	if err == nil {
		b.p.mu.Lock()
		b.closed = true
		b.p.mu.Unlock()
	}
	return err
}

// setCloseErr makes every box's Close fail with err, or work again for nil.
func (p *recordingProvider) setCloseErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeErr = err
}

// setCreateDelay makes every Create take d before it makes the box.
func (p *recordingProvider) setCreateDelay(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.createDelay = d
}

// setCloseBlock makes every box's Close wait for ch, or for its context to end;
// nil lets Close through again.
func (p *recordingProvider) setCloseBlock(ch chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeBlock = ch
}

// reapCount is the number of Reap calls.
func (p *recordingProvider) reapCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reaps
}

// cloneSpec copies the maps and slices of spec.
func cloneSpec(spec sandbox.Spec) sandbox.Spec {
	spec.Env = maps.Clone(spec.Env)
	spec.Labels = maps.Clone(spec.Labels)
	spec.Provider = maps.Clone(spec.Provider)
	spec.Network.Allow = slices.Clone(spec.Network.Allow)
	return spec
}

// testPrincipalKey carries the principal in the contexts of the backend tests,
// in place of the one gatewayd puts there.
type testPrincipalKey struct{}

func withPrincipal(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, testPrincipalKey{}, id)
}

func testPrincipal(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(testPrincipalKey{}).(string)
	return id, ok
}

// backendEnv is a lease environment with a wrapping backend over a recording
// provider.
type backendEnv struct {
	*leaseEnv
	prov    *recordingProvider
	emitter *recordingEmitter
	// sink is what the services emit to: the emitter, or nil for none.
	sink    audit.Emitter
	backend *Backend
}

// newBackendEnv builds the environment and a backend over it. The profile
// service, the lease service and the backend share the clock, the database and
// the emitter.
func newBackendEnv(t *testing.T, opts leaseEnvOptions) *backendEnv {
	t.Helper()
	env := &backendEnv{prov: newRecordingProvider(), emitter: &recordingEmitter{}}
	switch {
	case opts.emitter != nil:
		env.sink = opts.emitter
	case !opts.noEmitter:
		env.sink = env.emitter
	}
	opts.provider, opts.emitter = env.prov, env.sink
	env.leaseEnv = newLeaseEnv(t, opts)
	env.backend = newBackend(backendConfig{
		Service:   env.profile,
		Store:     env.store,
		Secrets:   env.secrets,
		DB:        env.db,
		Audit:     env.sink,
		Now:       env.clock.Now,
		Principal: testPrincipal,
	})
	t.Cleanup(func() {
		// A test may leave the provider failing or blocked; the boxes are closed
		// all the same.
		env.prov.setCloseErr(nil)
		env.prov.setCloseBlock(nil)
		env.prov.mu.Lock()
		boxes := slices.Clone(env.prov.boxes)
		env.prov.mu.Unlock()
		for _, box := range boxes {
			_ = box.Close(context.Background())
		}
	})
	return env
}

// issue posts a lease of the body's profile in the workspace "widgets" and
// returns its id.
func (e *backendEnv) issue(body string) string {
	e.t.Helper()
	rec := e.post("widgets", body, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
	}
	return jsonObject(e.t, rec)["id"].(string)
}

// create calls the backend as the lease with the given id.
func (e *backendEnv) create(id string, spec sandbox.Spec) (sandbox.Sandbox, error) {
	return e.backend.Create(withPrincipal(context.Background(), id), spec)
}

// lease reads a lease from the store.
func (e *backendEnv) lease(id string) *Lease {
	e.t.Helper()
	l, err := e.store.GetLease(id)
	if err != nil {
		e.t.Fatalf("reading lease %s: %v", id, err)
	}
	return l
}

// eventsOf returns the recorded events of one type.
func (e *backendEnv) eventsOf(eventType string) []audit.HubEvent {
	var out []audit.HubEvent
	for _, ev := range e.emitter.recorded() {
		if ev.EventType == eventType {
			out = append(out, ev)
		}
	}
	return out
}

// issueToken posts a lease of the body's profile in "widgets" as the owner and
// returns its id and its token.
func (e *backendEnv) issueToken(body string) (id, token string) {
	e.t.Helper()
	return e.issueTokenAs(body, apiKeyAuth())
}

// issueTokenAs is issueToken for any credential.
func (e *backendEnv) issueTokenAs(body string, auth *apikit.AuthInfo) (id, token string) {
	e.t.Helper()
	rec := e.post("widgets", body, auth)
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
	}
	obj := jsonObject(e.t, rec)
	return obj["id"].(string), obj["token"].(string)
}

// testGateway is the gateway hub starts, serving on a free loopback port.
type testGateway struct{ *Gateway }

// startGateway starts the gateway over the environment's database, secrets and
// emitter the way hub does, hands it to the lease service, which then shows its
// address and destroys boxes through it, and shuts it down when the test ends.
// Its backend reads the principal the way production does, from gatewayd's own
// context value.
func (e *backendEnv) startGateway() *testGateway {
	e.t.Helper()
	gw, err := StartGateway(e.profile, GatewayDeps{DB: e.db, Secrets: e.secrets, Audit: e.sink, Now: e.clock.Now})
	if err != nil {
		e.t.Fatalf("StartGateway: %v", err)
	}
	stopAfter(e.t, gw)
	e.svc.gatewayURL = gw.URL()
	e.svc.boxes = gw
	return &testGateway{gw}
}

// dial connects the real gateway client with a lease token.
func (g *testGateway) dial(t *testing.T, token string) *gateway.Provider {
	t.Helper()
	p, err := gateway.Dial(context.Background(), g.URL(), gateway.Options{Token: token})
	if err != nil {
		t.Fatalf("gateway.Dial: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// createVia creates the box of a lease through the real client. The image is the
// client's to give: the client fills in an empty one with the image published
// for its own version, which a development build has none of, and hub ignores
// whatever it is.
func createVia(t *testing.T, p *gateway.Provider) sandbox.Sandbox {
	t.Helper()
	sb, err := p.Create(context.Background(), sandbox.Spec{Image: "ignored"})
	if err != nil {
		t.Fatalf("Create through the gateway: %v", err)
	}
	return sb
}

// claimed reports whether a Create for the lease is in flight.
func (b *Backend) claimed(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, busy := b.claims[id]
	return busy
}
