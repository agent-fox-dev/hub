package sandboxes

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/workspace"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// ---- configurations ----

const (
	// owner owns the test workspaces; ownerKey() is their API key.
	owner = "user-1"
	// plantedSecret is the value of GIT_PAT in the workspace "widgets".
	plantedSecret = "PLANTED-VALUE-123"
	// testGatewayToken passes validation: 32 printable characters.
	testGatewayToken = "gw-token-0123456789abcdef0123456"
	// remoteToken is hub's token for the in-process remote gateway.
	remoteToken = "remote-hub-token-0123456789abcdef"
)

func strPtr(s string) *string { return &s }

func validProfile(name string) ProfileConfig {
	return ProfileConfig{Name: name, Image: "registry.example/" + name + ":1", TTL: "1h"}
}

func profileWith(name string, mutate func(*ProfileConfig)) ProfileConfig {
	p := validProfile(name)
	mutate(&p)
	return p
}

// validConfig is the podman provider with one complete profile.
func validConfig() *Config {
	return &Config{Provider: "podman", Profiles: []ProfileConfig{validProfile("dev")}}
}

func configWith(profiles ...ProfileConfig) *Config {
	cfg := validConfig()
	cfg.Profiles = profiles
	return cfg
}

// remoteConfig is provider gateway at rawURL with the valid token and one profile.
func remoteConfig(rawURL string) *Config {
	return &Config{Provider: "gateway", GatewayURL: rawURL, GatewayToken: testGatewayToken, Profiles: []ProfileConfig{validProfile("dev")}}
}

// defaultLeaseConfig has a default profile "tools" requiring GIT_PAT and a
// second profile "data". The gateway listens on a free port.
func defaultLeaseConfig() *Config {
	return &Config{
		Provider: "podman",
		Listen:   "127.0.0.1:0",
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

// gatewayLeaseConfig is defaultLeaseConfig without require_landlock, which the
// in-process agent of AgentKit's local provider cannot honour.
func gatewayLeaseConfig() *Config {
	cfg := defaultLeaseConfig()
	cfg.Profiles[0].RequireLandlock = false
	return cfg
}

// bigLeaseConfig has room for many live leases.
func bigLeaseConfig() *Config {
	cfg := defaultLeaseConfig()
	limit := 1000.0
	cfg.MaxSandboxes = &limit
	return cfg
}

// remoteLeaseConfig is gatewayLeaseConfig for provider gateway at url.
func remoteLeaseConfig(url string) *Config {
	cfg := gatewayLeaseConfig()
	cfg.Provider, cfg.GatewayURL, cfg.GatewayToken = "gateway", url, remoteToken
	return cfg
}

func failures(err error) []string {
	if err == nil {
		return nil
	}
	return strings.Split(err.Error(), "\n")
}

// requireFailure fails unless one line of err holds every part.
func requireFailure(t *testing.T, err error, parts ...string) {
	t.Helper()
	for _, msg := range failures(err) {
		if containsAll(msg, parts) {
			return
		}
	}
	t.Errorf("no failure contains all of %q in:\n%v", parts, err)
}

func containsAll(s string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}

func requireValid(t *testing.T, cfg *Config) *Validated {
	t.Helper()
	v, err := Validate(cfg)
	if err != nil || v == nil {
		t.Fatalf("Validate = (%v, %v); want a definition", v, err)
	}
	return v
}

// ---- credentials ----

func ownerKey() *apikit.AuthInfo            { return testutil.User(owner) }
func adminAuth() *apikit.AuthInfo           { return testutil.Admin() }
func pat(scopes ...string) *apikit.AuthInfo { return testutil.PAT(owner, scopes...) }

// ---- doubles ----

// fakeProvider is a sandbox.Provider whose Reap, the probe, can fail or block.
type fakeProvider struct {
	mu            sync.Mutex
	caps          sandbox.Capabilities
	reapErr       error
	reapBlocks    bool
	reapCalls     int
	reapDeadlines []time.Duration
	createCalls   int
	capsCalls     int
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{caps: sandbox.Capabilities{
		Provider: "fake", Network: []sandbox.NetworkMode{sandbox.NetworkNone},
		Resources: sandbox.ResourceCaps{CPUs: true, MemoryBytes: true, PIDs: true, DiskBytes: true},
		Image:     true, Landlock: true,
	}}
}

func (f *fakeProvider) Capabilities() sandbox.Capabilities {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capsCalls++
	return f.caps
}

func (f *fakeProvider) Create(context.Context, sandbox.Spec) (sandbox.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	return nil, errors.New("fakeProvider: Create must not be called")
}

func (f *fakeProvider) Reap(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	f.reapCalls++
	left := time.Duration(-1)
	if deadline, ok := ctx.Deadline(); ok {
		left = time.Until(deadline)
	}
	f.reapDeadlines = append(f.reapDeadlines, left)
	blocks, err := f.reapBlocks, f.reapErr
	f.mu.Unlock()
	if blocks {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, err
}

func (f *fakeProvider) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reapCalls + f.createCalls + f.capsCalls
}

// recordingFactory is a ProviderFactory that counts its calls.
type recordingFactory struct {
	provider sandbox.Provider
	err      error
	calls    int
	options  podman.Options
}

func (r *recordingFactory) build(o podman.Options) (sandbox.Provider, error) {
	r.calls++
	r.options = o
	return r.provider, r.err
}

func withProbeTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := probeTimeout
	probeTimeout = d
	t.Cleanup(func() { probeTimeout = prev })
}

// recordingProvider records every Create's spec and makes real boxes with
// AgentKit's local provider; its failures are scripted.
type recordingProvider struct {
	mu          sync.Mutex
	caps        sandbox.Capabilities
	local       sandbox.Provider
	specs       []sandbox.Spec
	boxes       []*recordedBox
	createErr   error
	closeErr    error
	closeBlock  chan struct{}
	block       chan struct{}
	reapIDs     []string
	reapErr     error
	reaps       int
	reapDelay   time.Duration
	reapHang    bool
	inflight    int
	maxInflight int
	calls       *callLog
	// entered receives one value for each Create that waits on block.
	entered chan struct{}
}

func newRecordingProvider() *recordingProvider {
	return &recordingProvider{
		local: local.Unisolated(), entered: make(chan struct{}, 64),
		caps: sandbox.Capabilities{
			Provider: "recording", Isolation: sandbox.IsolationContainer,
			Network:   []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkAllowlist, sandbox.NetworkUnrestricted},
			Resources: sandbox.ResourceCaps{CPUs: true, MemoryBytes: true, PIDs: true, DiskBytes: true},
			Image:     true, Workdir: true, Landlock: true,
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

func (p *recordingProvider) Reap(ctx context.Context) ([]string, error) {
	p.mu.Lock()
	p.reaps++
	p.inflight++
	p.maxInflight = max(p.maxInflight, p.inflight)
	delay, hang, calls := p.reapDelay, p.reapHang, p.calls
	ids, err := slices.Clone(p.reapIDs), p.reapErr
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inflight--
		p.mu.Unlock()
	}()
	calls.add("Reap")
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return ids, err
}

func (p *recordingProvider) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	p.mu.Lock()
	p.specs = append(p.specs, cloneSpec(spec))
	failure, block := p.createErr, p.block
	p.mu.Unlock()
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
		Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}, TTL: spec.TTL,
		Env: maps.Clone(spec.Env), Labels: maps.Clone(spec.Labels),
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

func (p *recordingProvider) set(f func(*recordingProvider)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

func (p *recordingProvider) setCreateErr(err error) {
	p.set(func(p *recordingProvider) { p.createErr = err })
}
func (p *recordingProvider) setCloseErr(err error) {
	p.set(func(p *recordingProvider) { p.closeErr = err })
}
func (p *recordingProvider) setCloseBlock(ch chan struct{}) {
	p.set(func(p *recordingProvider) { p.closeBlock = ch })
}
func (p *recordingProvider) setBlock(ch chan struct{}) {
	p.set(func(p *recordingProvider) { p.block = ch })
}
func (p *recordingProvider) setReap(ids []string, err error) {
	p.set(func(p *recordingProvider) { p.reapIDs, p.reapErr = ids, err })
}

func (p *recordingProvider) creates() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.specs)
}

func (p *recordingProvider) reapCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reaps
}

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

func (p *recordingProvider) allBoxes() []*recordedBox {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.boxes)
}

// recordedBox is a box of the recordingProvider with the spec hub asked for.
type recordedBox struct {
	sandbox.Sandbox
	p         *recordingProvider
	spec      sandbox.Spec
	closed    bool
	closes    int
	deadlines []time.Duration
}

func (b *recordedBox) Unwrap() sandbox.Sandbox { return b.Sandbox }
func (b *recordedBox) Spec() sandbox.Spec      { return b.spec }

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

func cloneSpec(spec sandbox.Spec) sandbox.Spec {
	spec.Env = maps.Clone(spec.Env)
	spec.Labels = maps.Clone(spec.Labels)
	spec.Provider = maps.Clone(spec.Provider)
	spec.Network.Allow = slices.Clone(spec.Network.Allow)
	return spec
}

// leaseClock is a clock a test moves by hand.
type leaseClock struct {
	mu  sync.Mutex
	now time.Time
}

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

// recordingEmitter keeps the events it is given and answers err.
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
	return slices.Clone(r.events)
}

func eventsOf(r *recordingEmitter, eventType string) []audit.HubEvent {
	var out []audit.HubEvent
	for _, e := range r.recorded() {
		if e.EventType == eventType {
			out = append(out, e)
		}
	}
	return out
}

// callLog records the calls of the doubles in order; a nil log records nothing.
type callLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *callLog) add(entry string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *callLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries)
}

// fakeBoxes is the Boxes of the sweep tests: boxes that exist and a record of
// the destroys.
type fakeBoxes struct {
	mu          sync.Mutex
	boxes       []Box
	listErr     error
	destroyErr  error
	failNext    error
	destroyed   []string
	listHang    bool
	destroyHang map[string]bool
	calls       *callLog
	// before is called first by every List and Destroy.
	before func(call string)
}

func (f *fakeBoxes) add(id string, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boxes = append(f.boxes, Box{ID: id, Labels: labels})
}

func (f *fakeBoxes) List(ctx context.Context) ([]Box, error) {
	f.mu.Lock()
	hang, listErr, calls, before := f.listHang, f.listErr, f.calls, f.before
	f.mu.Unlock()
	calls.add("List")
	if before != nil {
		before("List")
	}
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if listErr != nil {
		return nil, listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.boxes), nil
}

func (f *fakeBoxes) Destroy(ctx context.Context, id string) error {
	f.mu.Lock()
	f.destroyed = append(f.destroyed, id)
	hang, calls, before := f.destroyHang[id], f.calls, f.before
	f.mu.Unlock()
	calls.add("Destroy " + id)
	if before != nil {
		before("Destroy " + id)
	}
	if hang {
		<-ctx.Done()
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	if f.destroyErr != nil {
		return f.destroyErr
	}
	f.boxes = slices.DeleteFunc(f.boxes, func(b Box) bool { return b.ID == id })
	return nil
}

func (f *fakeBoxes) destroyedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.destroyed)
}

func (f *fakeBoxes) setDestroyErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyErr = err
}

func (f *fakeBoxes) ids() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, b := range f.boxes {
		ids = append(ids, b.ID)
	}
	return ids
}

// fakeTickers makes tickers a test fires by hand.
type fakeTickers struct {
	mu      sync.Mutex
	made    []chan time.Time
	stopped int
}

func (f *fakeTickers) make(time.Duration) (<-chan time.Time, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 8)
	f.made = append(f.made, ch)
	return ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stopped++
	}
}

func (f *fakeTickers) fire() {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case f.made[len(f.made)-1] <- time.Now():
	default:
	}
}

func (f *fakeTickers) stoppedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

// waitFor polls until cond holds, for at most ten seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// ---- logs and metrics ----

// logBuffer is a goroutine-safe buffer of captured log output.
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

// captureDefaultLog routes slog's default logger to a buffer for the test.
func captureDefaultLog(t *testing.T) *logBuffer {
	t.Helper()
	buf := &logBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// newCapturedLogger returns a logger writing JSON records to the buffer.
func newCapturedLogger() (*slog.Logger, *logBuffer) {
	buf := &logBuffer{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// logRecords parses the JSON records of a buffer.
func logRecords(t *testing.T, buf *logBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v\n%s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// attributeKeys are the keys of a record other than slog's own, sorted.
func attributeKeys(rec map[string]any) []string {
	var keys []string
	for k := range rec {
		if k != "time" && k != "level" && k != "msg" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// metricSample is the value of the series with exactly these labels.
func metricSample(t *testing.T, m *audit.Metrics, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("gathering the metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
	series:
		for _, metric := range family.GetMetric() {
			got := map[string]string{}
			for _, pair := range metric.GetLabel() {
				got[pair.GetName()] = pair.GetValue()
			}
			if len(got) != len(labels) {
				continue
			}
			for k, v := range labels {
				if got[k] != v {
					continue series
				}
			}
			if metric.Gauge != nil {
				return metric.GetGauge().GetValue(), true
			}
			return metric.GetCounter().GetValue(), true
		}
	}
	return 0, false
}

func activeGauge(t *testing.T, m *audit.Metrics, workspace string) float64 {
	t.Helper()
	v, _ := metricSample(t, m, "afhub_sandboxes_active", map[string]string{"workspace": workspace})
	return v
}

func createsCounter(t *testing.T, m *audit.Metrics, result string) float64 {
	t.Helper()
	v, _ := metricSample(t, m, "afhub_sandbox_creates_total", map[string]string{"result": result})
	return v
}

// leaseSeconds is the count and sum of afhub_sandbox_lease_seconds.
func leaseSeconds(t *testing.T, m *audit.Metrics) (count uint64, sum float64) {
	t.Helper()
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("gathering the metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() == "afhub_sandbox_lease_seconds" && len(family.GetMetric()) == 1 {
			h := family.GetMetric()[0].GetHistogram()
			return h.GetSampleCount(), h.GetSampleSum()
		}
	}
	t.Fatal("afhub_sandbox_lease_seconds is not registered")
	return 0, 0
}

// ---- database and lease rows ----

// openDB opens apikit's database with the tables the package reads.
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.OpenDB(t)
	initTables(t, db)
	return db
}

func initTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for name, init := range map[string]func(*sql.DB) error{
		"sandboxes": InitSchema, "workspace": workspace.InitSchema, "secrets": secrets.InitSchema, "wsaccess": wsaccess.InitBindingSchema,
	} {
		if err := init(db); err != nil {
			t.Fatalf("%s.InitSchema: %v", name, err)
		}
	}
}

// newLeaseStore is a store over a fresh lease table with a fixed clock.
func newLeaseStore(t *testing.T, now time.Time) (*LeaseStore, *sql.DB) {
	t.Helper()
	db := testutil.OpenDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return NewLeaseStore(db, func() time.Time { return now }), db
}

func testToken(n int) string { return fmt.Sprintf("sbl_%044d", n) }
func leaseID(n int) string   { return fmt.Sprintf("sbx_%032x", n) }

// rawLeaseInsert names the columns without a default. Every lease expires at
// 2030-01-02T04:04:05Z.
const rawLeaseInsert = `INSERT INTO sandbox_leases
	(id, workspace, profile, state, token_hash, image, network, ttl_seconds, expires_at, created_by, created_at, updated_at)
	VALUES (?, ?, 'tools', ?, ?, 'img', 'none', 3600, '2030-01-02T04:04:05Z', 'u1', '2030-01-02T03:04:05Z', '2030-01-02T03:04:05Z')`

// insertRawLease inserts a row straight through SQL; hash may be nil.
func insertRawLease(t *testing.T, db *sql.DB, id, workspace, state string, hash any) {
	t.Helper()
	if _, err := db.Exec(rawLeaseInsert, id, workspace, state, hash); err != nil {
		t.Fatalf("inserting lease %s (%s): %v", id, state, err)
	}
}

// leaseState is the part of a row that ending a lease changes.
type leaseState struct {
	state, endedAt, updatedAt, endReason string
	hash                                 sql.NullString
}

func readState(t *testing.T, db *sql.DB, id string) leaseState {
	t.Helper()
	var s leaseState
	var ended, reason sql.NullString
	if err := db.QueryRow(`SELECT state, ended_at, updated_at, end_reason, token_hash FROM sandbox_leases WHERE id = ?`, id).
		Scan(&s.state, &ended, &s.updatedAt, &reason, &s.hash); err != nil {
		t.Fatalf("reading lease %s: %v", id, err)
	}
	s.endedAt, s.endReason = ended.String, reason.String
	return s
}

// rowText returns every column of one lease row as text.
func rowText(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM sandbox_leases WHERE id = ?`, id)
	if err != nil {
		t.Fatalf("reading lease %s: %v", id, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		t.Fatalf("lease %s not found", id)
	}
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		t.Fatalf("scanning lease %s: %v", id, err)
	}
	return fmt.Sprintln(values...)
}

// ---- the lease environment ----

// leaseEnvOptions changes how newLeaseEnv builds its environment.
type leaseEnvOptions struct {
	config    *Config
	lister    SecretLister
	rand      interface{ Read([]byte) (int, error) }
	emitter   audit.Emitter
	db        *sql.DB
	start     time.Time
	provider  sandbox.Provider
	service   *Service
	recording *recordingProvider
	noEmitter bool
	metrics   *audit.Metrics
}

// leaseEnv is a lease service over a real database with the routes mounted
// behind the test auth middleware, and the workspace "widgets" of owner with
// GIT_PAT planted in its scope.
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

func newLeaseEnv(t *testing.T, opts leaseEnvOptions) *leaseEnv {
	t.Helper()
	db := opts.db
	if db == nil {
		db = openDB(t)
	}
	profileSvc := opts.service
	if profileSvc == nil {
		cfg := opts.config
		if cfg == nil {
			cfg = defaultLeaseConfig()
		}
		var provider sandbox.Provider = newFakeProvider()
		if opts.provider != nil {
			provider = opts.provider
		}
		var err error
		if profileSvc, err = New(requireValid(t, cfg), provider); err != nil {
			t.Fatalf("New: %v", err)
		}
	}
	start := opts.start
	if start.IsZero() {
		start = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	}
	clock := &leaseClock{now: start}
	store := secrets.NewStore(db)
	var lister SecretLister = store
	if opts.lister != nil {
		lister = opts.lister
	}
	svc := NewLeaseService(LeaseConfig{Service: profileSvc, DB: db, Secrets: lister, Audit: opts.emitter, Now: clock.Now, Rand: opts.rand, Metrics: opts.metrics})
	e, api := testutil.API()
	RegisterLeaseRoutes(api, svc)
	env := &leaseEnv{t: t, db: db, store: NewLeaseStore(db, clock.Now).WithMetrics(opts.metrics), svc: svc, profile: profileSvc, secrets: store, clock: clock, echo: e}
	env.addWorkspace("widgets", "", "active")
	env.plantSecret("workspace", "widgets", "GIT_PAT", plantedSecret)
	return env
}

// addWorkspace inserts a workspace of owner; org may be "".
func (e *leaseEnv) addWorkspace(slug, org, status string) {
	e.t.Helper()
	testutil.SeedWorkspace(e.t, e.db, testutil.Workspace{Slug: slug, OwnerID: owner, OrgID: org, Status: status})
}

// addMember makes userID a member of org, which it creates with owner.
func (e *leaseEnv) addMember(org, userID string) {
	e.t.Helper()
	for _, u := range []string{owner, userID} {
		if testutil.Count(e.t, e.db, `SELECT COUNT(*) FROM users WHERE id = ?`, u) == 0 {
			testutil.SeedUser(e.t, e.db, u)
		}
	}
	if testutil.Count(e.t, e.db, `SELECT COUNT(*) FROM orgs WHERE id = ?`, org) == 0 {
		testutil.SeedOrg(e.t, e.db, org, org, owner)
	}
	testutil.SeedOrgMember(e.t, e.db, org, userID)
}

func (e *leaseEnv) plantSecret(ownerType, ownerID, key, value string) {
	e.t.Helper()
	if _, err := e.secrets.CreateSecrets(ownerType, ownerID, []secrets.EntryInput{{Key: key, Value: value}}); err != nil {
		e.t.Fatalf("planting secret %s: %v", key, err)
	}
}

func (e *leaseEnv) do(method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	e.t.Helper()
	return testutil.Request(e.t, e.echo, method, path, body, auth)
}

func (e *leaseEnv) post(slug, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/v1/workspaces/"+slug+"/sandboxes", body, auth)
}

func (e *leaseEnv) get(path string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodGet, path, "", auth)
}

// delete sends DELETE for a lease of widgets as the owner.
func (e *leaseEnv) delete(id string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", ownerKey())
}

// mustIssue posts body (profile tools when empty) in widgets as the owner and
// returns the 201 body.
func (e *leaseEnv) mustIssue(body string) map[string]any {
	e.t.Helper()
	return e.mustIssueAs("widgets", body, ownerKey())
}

func (e *leaseEnv) mustIssueAs(slug, body string, auth *apikit.AuthInfo) map[string]any {
	e.t.Helper()
	if body == "" {
		body = `{"profile":"tools"}`
	}
	rec := e.post(slug, body, auth)
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("POST %s answered %d: %s", body, rec.Code, rec.Body)
	}
	return testutil.Object(e.t, rec)
}

// issue is mustIssue returning the lease id.
func (e *leaseEnv) issue(body string) string {
	e.t.Helper()
	return e.mustIssue(body)["id"].(string)
}

// issueToken is mustIssue returning the id and the token.
func (e *leaseEnv) issueToken(body string) (id, token string) {
	e.t.Helper()
	obj := e.mustIssue(body)
	return obj["id"].(string), obj["token"].(string)
}

func (e *leaseEnv) countLeases() int {
	e.t.Helper()
	return testutil.Count(e.t, e.db, `SELECT COUNT(*) FROM sandbox_leases`)
}

func (e *leaseEnv) readState(id string) leaseState {
	e.t.Helper()
	return readState(e.t, e.db, id)
}

func (e *leaseEnv) lease(id string) *Lease {
	e.t.Helper()
	l, err := e.store.GetLease(id)
	if err != nil {
		e.t.Fatalf("reading lease %s: %v", id, err)
	}
	return l
}

// insertLeaseAt inserts lease n of workspace straight through SQL, created at
// the given time with an hour's TTL and the token of n.
func (e *leaseEnv) insertLeaseAt(n int, workspace, state string, created time.Time) string {
	e.t.Helper()
	id := leaseID(n)
	stamp := apikit.FormatUTC(created)
	testutil.Exec(e.t, e.db, `INSERT INTO sandbox_leases
		(id, workspace, profile, state, token_hash, image, network, ttl_seconds, expires_at, created_by, created_at, updated_at)
		VALUES (?, ?, 'tools', ?, ?, 'img', 'none', 3600, ?, ?, ?, ?)`,
		id, workspace, state, HashToken(testToken(n)), apikit.FormatUTC(created.Add(time.Hour)), owner, stamp, stamp)
	return id
}

// message and errorType read apikit's error envelope.
func message(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return testutil.Error(t, rec).Message
}

func errorType(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return testutil.Error(t, rec).ErrorType
}

// leaseList decodes a 200 response that is a bare array of leases.
func leaseList(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200: %s", rec.Code, rec.Body)
	}
	return testutil.Objects(t, rec)
}

func ids(list []map[string]any) []string {
	out := make([]string, len(list))
	for i, l := range list {
		out[i], _ = l["id"].(string)
	}
	return out
}

// ---- the backend environment ----

type testPrincipalKey struct{}

func withPrincipal(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, testPrincipalKey{}, id)
}

func testPrincipal(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(testPrincipalKey{}).(string)
	return id, ok
}

// backendEnv is a lease environment with a wrapping backend over a recording
// provider; the services share the clock, the database and the emitter.
type backendEnv struct {
	*leaseEnv
	prov    *recordingProvider
	emitter *recordingEmitter
	metrics *audit.Metrics
	sink    audit.Emitter
	backend *Backend
}

func newBackendEnv(t *testing.T, opts leaseEnvOptions) *backendEnv {
	t.Helper()
	env := &backendEnv{prov: opts.recording, emitter: &recordingEmitter{}, metrics: opts.metrics}
	if env.prov == nil {
		env.prov = newRecordingProvider()
	}
	switch {
	case opts.emitter != nil:
		env.sink = opts.emitter
	case !opts.noEmitter:
		env.sink = env.emitter
	}
	opts.provider, opts.emitter = env.prov, env.sink
	env.leaseEnv = newLeaseEnv(t, opts)
	env.backend = newBackend(backendConfig{
		Service: env.profile, Store: env.store, Secrets: env.secrets, DB: env.db, Audit: env.sink, Now: env.clock.Now,
		Principal: testPrincipal, Metrics: opts.metrics,
		Sweeper: NewSweeper(SweeperConfig{DB: env.db, Provider: env.prov, Boxes: &fakeBoxes{}, Audit: env.sink, Metrics: opts.metrics, Now: env.clock.Now}),
	})
	t.Cleanup(func() {
		env.prov.setCloseErr(nil)
		env.prov.setCloseBlock(nil)
		for _, box := range env.prov.allBoxes() {
			_ = box.Close(context.Background())
		}
	})
	return env
}

// create calls the backend as the lease with the given id.
func (e *backendEnv) create(id string, spec sandbox.Spec) (sandbox.Sandbox, error) {
	return e.backend.Create(withPrincipal(context.Background(), id), spec)
}

// mustCreate is create for a lease that must get its box.
func (e *backendEnv) mustCreate(id string) sandbox.Sandbox {
	e.t.Helper()
	sb, err := e.create(id, sandbox.Spec{})
	if err != nil {
		e.t.Fatalf("Create: %v", err)
	}
	return sb
}

func (e *backendEnv) eventsOf(eventType string) []audit.HubEvent {
	return eventsOf(e.emitter, eventType)
}

// blockCreates makes every Create wait on the returned channel; the test
// waits on entered for a Create to arrive.
func (e *backendEnv) blockCreates() (release chan struct{}, entered <-chan struct{}) {
	release = make(chan struct{})
	e.prov.setBlock(release)
	return release, e.prov.entered
}

// testGateway is the gateway hub starts, on a free loopback port.
type testGateway struct{ *Gateway }

// startGateway starts the gateway over the environment as hub does, hands it
// to the lease service and waits for the sweep gatewayd asks for at start.
func (e *backendEnv) startGateway() *testGateway {
	e.t.Helper()
	gw, err := newGateway(e.profile, GatewayDeps{DB: e.db, Secrets: e.secrets, Audit: e.sink, Now: e.clock.Now, Metrics: e.metrics}, gatewaySeams{})
	if err != nil {
		e.t.Fatalf("newGateway: %v", err)
	}
	return e.runGateway(gw, false)
}

// runGateway serves gw (through Run when run is true) and waits until every
// sweep of the start has ended.
func (e *backendEnv) runGateway(gw *Gateway, run bool) *testGateway {
	e.t.Helper()
	reaps, want := e.prov.reapCount(), 1
	stopAfter(e.t, gw)
	if run {
		want = 2
		if err := gw.Run(context.Background()); err != nil {
			e.t.Fatalf("Run: %v", err)
		}
	} else {
		gw.serve()
	}
	waitFor(e.t, "the sweeps of the start", func() bool { return e.prov.reapCount()-reaps >= want })
	if err := gw.sweeper.Sweep(context.Background()); err != nil { // returns after the one in progress
		e.t.Fatalf("Sweep: %v", err)
	}
	e.svc.gatewayURL = gw.URL()
	e.svc.boxes = gw
	return &testGateway{gw}
}

// stopAfter shuts a started gateway's server down when the test ends.
func stopAfter(t *testing.T, gw *Gateway) {
	t.Helper()
	if gw == nil {
		return
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = gw.server.Shutdown(ctx)
	})
}

// dial connects the real client to the gateway with a lease token.
func (g *testGateway) dial(t *testing.T, token string) *gateway.Provider {
	t.Helper()
	return dialGateway(t, g.URL(), token)
}

func dialGateway(t *testing.T, url, token string) *gateway.Provider {
	t.Helper()
	p, err := gateway.Dial(context.Background(), url, gateway.Options{Token: token})
	if err != nil {
		t.Fatalf("gateway.Dial: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// createVia creates a box through the real client; hub ignores the image.
func createVia(t *testing.T, p *gateway.Provider) sandbox.Sandbox {
	t.Helper()
	sb, err := p.Create(context.Background(), sandbox.Spec{Image: "ignored"})
	if err != nil {
		t.Fatalf("Create through the gateway: %v", err)
	}
	return sb
}

// The names of two RPCs of the gateway protocol; the authentication answers
// before a request is read, so an empty message shows the code of a refusal.
const (
	rpcGatewayCreate = "/agentkit.sandbox.v1.Gateway/Create"
	rpcAgentHello    = "/agentkit.sandbox.v1.Agent/Hello"
)

type bearerToken string

func (b bearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}
func (bearerToken) RequireTransportSecurity() bool { return false }

// rawCall makes one RPC with a token and an empty message, without the
// client's own checks.
func rawCall(t *testing.T, url, token, method string) error {
	t.Helper()
	cc, err := grpc.NewClient("passthrough:///"+strings.TrimPrefix(url, "http://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(bearerToken(token)))
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return cc.Invoke(ctx, method, &emptypb.Empty{}, &emptypb.Empty{})
}

// ---- the sweep environment ----

// sweepEnv is a backend environment with metrics and a sweeper over fake boxes.
type sweepEnv struct {
	*backendEnv
	boxes   *fakeBoxes
	sweeper *Sweeper
	ender   *ender
}

func newSweepEnv(t *testing.T) *sweepEnv {
	t.Helper()
	m := audit.NewMetrics()
	env := newBackendEnv(t, leaseEnvOptions{metrics: m, config: bigLeaseConfig()})
	e := &sweepEnv{backendEnv: env, boxes: &fakeBoxes{}}
	e.sweeper = NewSweeper(e.sweeperConfig())
	e.ender = newEnder(env.store, env.sink, e.boxes, time.Second)
	return e
}

func (e *sweepEnv) sweeperConfig() SweeperConfig {
	return SweeperConfig{DB: e.db, Provider: e.prov, Boxes: e.boxes, Audit: e.sink, Metrics: e.metrics, Now: e.clock.Now, CallTimeout: time.Second, Service: e.profile}
}

// seedFields are the times and the box of a seeded lease.
type seedFields struct {
	workspace    string
	created      time.Time
	expires      time.Time  // an hour after created when zero
	leaseExpires *time.Time // NULL when nil
	ended        *time.Time // NULL when nil
	updated      time.Time  // created when zero
	boxID        string     // none when empty; the box exists in the fake boxes
}

func timePtr(t time.Time) *time.Time { return &t }

// seed inserts a lease in state; a live one keeps its token hash.
func (e *sweepEnv) seed(n int, state string, f seedFields) string {
	e.t.Helper()
	if f.workspace == "" {
		f.workspace = "widgets"
	}
	if f.created.IsZero() {
		f.created = e.clock.Now()
	}
	if f.expires.IsZero() {
		f.expires = f.created.Add(time.Hour)
	}
	if f.updated.IsZero() {
		f.updated = f.created
	}
	id := e.insertLeaseAt(n, f.workspace, state, f.created)
	var leaseExpires, ended, boxID, reason any
	if f.leaseExpires != nil {
		leaseExpires = apikit.FormatUTC(*f.leaseExpires)
	}
	if f.ended != nil {
		ended, reason = apikit.FormatUTC(*f.ended), "api"
	}
	if f.boxID != "" {
		boxID = f.boxID
		e.boxes.add(f.boxID, map[string]string{"hub.lease": id})
	}
	var hash any = HashToken(testToken(n))
	if state != leaseLeased && state != leaseActive {
		hash = nil
	}
	testutil.Exec(e.t, e.db, `UPDATE sandbox_leases SET expires_at = ?, lease_expires_at = ?, ended_at = ?, end_reason = ?,
		updated_at = ?, sandbox_id = ?, token_hash = ? WHERE id = ?`,
		apikit.FormatUTC(f.expires), leaseExpires, ended, reason, apikit.FormatUTC(f.updated), boxID, hash, id)
	return id
}

func (e *sweepEnv) sweep() {
	e.t.Helper()
	if err := e.sweeper.Sweep(context.Background()); err != nil {
		e.t.Fatalf("Sweep: %v", err)
	}
}

// tokenWorks reports whether the authenticator accepts the token of seed n.
func (e *sweepEnv) tokenWorks(n int) bool {
	_, ok := NewLeaseAuthenticator(e.store, e.clock.Now)(context.Background(), testToken(n))
	return ok
}

func (e *sweepEnv) rowExists(id string) bool {
	e.t.Helper()
	return testutil.Count(e.t, e.db, `SELECT COUNT(*) FROM sandbox_leases WHERE id = ?`, id) == 1
}

// ---- the remote gateway ----

// remoteGateway is an in-process agentkit-gateway over a recording provider,
// which records every token it is shown and can be stopped and restarted.
type remoteGateway struct {
	t       *testing.T
	backend *recordingProvider
	addr    string
	mu      sync.Mutex
	seen    []string
	srv     *gatewayd.Server
}

func startRemoteGateway(t *testing.T, backend *recordingProvider) *remoteGateway {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the remote gateway: %v", err)
	}
	r := &remoteGateway{t: t, backend: backend, addr: l.Addr().String()}
	r.serve(l)
	t.Cleanup(r.stop)
	return r
}

func (r *remoteGateway) serve(l net.Listener) {
	r.t.Helper()
	srv, err := gatewayd.New(gatewayd.Config{
		Backend: r.backend, Authenticate: r.authenticate,
		NetworkModes: []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted},
	})
	if err != nil {
		r.t.Fatalf("gatewayd.New: %v", err)
	}
	reaps := r.backend.reapCount()
	go func() { _ = srv.Serve(l) }()
	waitFor(r.t, "the remote gateway's first reap", func() bool { return r.backend.reapCount() > reaps })
	r.mu.Lock()
	r.srv = srv
	r.mu.Unlock()
}

func (r *remoteGateway) authenticate(_ context.Context, token string) (string, bool) {
	r.mu.Lock()
	r.seen = append(r.seen, token)
	r.mu.Unlock()
	return "hub", subtle.ConstantTimeCompare([]byte(token), []byte(remoteToken)) == 1
}

func (r *remoteGateway) url() string  { return "http://" + r.addr }
func (r *remoteGateway) host() string { return r.addr }

func (r *remoteGateway) tokens() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.seen)
}

func (r *remoteGateway) rpcs() int { return len(r.tokens()) }

func (r *remoteGateway) stop() {
	r.mu.Lock()
	srv := r.srv
	r.srv = nil
	r.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func (r *remoteGateway) restart() {
	r.t.Helper()
	var l net.Listener
	waitFor(r.t, "the remote's address to be free", func() bool {
		var err error
		l, err = net.Listen("tcp", r.addr)
		return err == nil
	})
	r.serve(l)
}

// remoteFactory is a GatewayFactory that records its calls and builds the
// client with delegate, DialGateway when nil.
type remoteFactory struct {
	delegate GatewayFactory
	mu       sync.Mutex
	calls    int
	urls     []string
	tokens   []string
}

func (f *remoteFactory) dial(ctx context.Context, url string, opts gateway.Options) (RemoteClient, error) {
	f.mu.Lock()
	f.calls++
	f.urls = append(f.urls, url)
	f.tokens = append(f.tokens, opts.Token)
	delegate := f.delegate
	f.mu.Unlock()
	if delegate == nil {
		delegate = DialGateway
	}
	return delegate(ctx, url, opts)
}

func (f *remoteFactory) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// stubRemote is a RemoteClient that reaches nothing: a fakeProvider that lists.
type stubRemote struct {
	*fakeProvider
	lmu       sync.Mutex
	lists     int
	listErr   error
	listBlock bool
	closes    int
}

func newStubRemote() *stubRemote { return &stubRemote{fakeProvider: newFakeProvider()} }

func (s *stubRemote) List(ctx context.Context) ([]gateway.Info, error) {
	s.lmu.Lock()
	s.lists++
	block, err := s.listBlock, s.listErr
	s.lmu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, err
}

func (s *stubRemote) Destroy(context.Context, string) error { return nil }

func (s *stubRemote) Close() error {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	s.closes++
	return nil
}

func (s *stubRemote) listCount() int {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	return s.lists
}

// stubFactory returns client and err whatever it is called with.
func stubFactory(client RemoteClient, err error) *remoteFactory {
	return &remoteFactory{delegate: func(context.Context, string, gateway.Options) (RemoteClient, error) { return client, err }}
}

// noPodman is a Podman factory that fails the test when called.
func noPodman(t *testing.T) ProviderFactory {
	t.Helper()
	return func(podman.Options) (sandbox.Provider, error) {
		t.Error("the Podman factory was called")
		return nil, errors.New("the Podman factory must not be called")
	}
}

// scriptedRemote is a remote client over a recording provider, so that a test
// scripts the failures of its Create and the Close of its boxes. closeLog
// records the Close of the client when set.
type scriptedRemote struct {
	*recordingProvider
	closeLog *callLog
	closeErr error
}

func (scriptedRemote) List(context.Context) ([]gateway.Info, error) { return nil, nil }

// Destroy closes the live box with the id and answers OK for one it does not have.
func (r scriptedRemote) Destroy(ctx context.Context, id string) error {
	for _, box := range r.allBoxes() {
		if box.ID() == id && !box.closed {
			return box.Close(ctx)
		}
	}
	return nil
}

func (r scriptedRemote) Close() error {
	r.closeLog.add("close")
	return r.closeErr
}

// remoteEnv is hub over a remote client with hub's gateway serving: the real
// client against an in-process remote when remote is set, else a scripted one.
type remoteEnv struct {
	*backendEnv
	gw      *testGateway
	remote  *remoteGateway
	factory *remoteFactory
	logs    *logBuffer
}

// newRemoteEnv starts an in-process remote and hub over it through Start and
// the real gateway.Dial.
func newRemoteEnv(t *testing.T, opts leaseEnvOptions) *remoteEnv {
	t.Helper()
	logs := captureDefaultLog(t)
	prov := newRecordingProvider()
	remote := startRemoteGateway(t, prov)
	factory := &remoteFactory{}
	svc, err := Start(remoteLeaseConfig(remote.url()), noPodman(t), WithGatewayFactory(factory.dial))
	if err != nil {
		t.Fatalf("Start against the remote gateway: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	opts.service, opts.recording = svc, prov
	env := newBackendEnv(t, opts)
	return &remoteEnv{backendEnv: env, gw: env.startGateway(), remote: remote, factory: factory, logs: logs}
}

// newScriptedEnv is hub over a scriptedRemote built by mk (the plain one when nil).
func newScriptedEnv(t *testing.T, mk func(*recordingProvider) RemoteClient) *remoteEnv {
	t.Helper()
	logs := captureDefaultLog(t)
	prov := newRecordingProvider()
	if mk == nil {
		mk = func(p *recordingProvider) RemoteClient { return scriptedRemote{recordingProvider: p} }
	}
	svc, err := Start(remoteLeaseConfig("https://gw.example"), noPodman(t), WithGatewayFactory(stubFactory(mk(prov), nil).dial))
	if err != nil {
		t.Fatalf("Start over the scripted remote: %v", err)
	}
	env := newBackendEnv(t, leaseEnvOptions{service: svc, recording: prov})
	return &remoteEnv{backendEnv: env, gw: env.startGateway(), logs: logs}
}

// activeLease issues a lease and creates its box through hub's gateway.
func (e *remoteEnv) activeLease() (id, token string, box sandbox.Sandbox) {
	e.t.Helper()
	id, token = e.issueToken("")
	return id, token, createVia(e.t, e.gw.dial(e.t, token))
}

// remoteLive lists the boxes the remote holds for hub's token.
func (e *remoteEnv) remoteLive() []string {
	e.t.Helper()
	infos, err := e.profile.Provider().(RemoteClient).List(context.Background())
	if err != nil {
		e.t.Fatalf("List on the remote: %v", err)
	}
	var out []string
	for _, info := range infos {
		out = append(out, info.ID)
	}
	return out
}

// refusalRecords counts the error records naming sandbox.gateway_token.
func refusalRecords(t *testing.T, logs *logBuffer) int {
	t.Helper()
	n := 0
	for _, rec := range logRecords(t, logs) {
		if rec["level"] == "ERROR" && strings.Contains(fmt.Sprint(rec), "sandbox.gateway_token") {
			n++
		}
	}
	return n
}

// containsNone fails when text holds any of the secrets.
func containsNone(t *testing.T, what, text string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(text, s) {
			t.Errorf("%s holds %q:\n%s", what, s, text)
		}
	}
}
