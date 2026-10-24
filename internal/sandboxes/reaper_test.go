package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// withTickers makes the sweeper reap on tickers the test fires.
func (e *sweepEnv) withTickers(tickers *fakeTickers) {
	cfg := e.sweeperConfig()
	cfg.Interval, cfg.Tickers = time.Minute, tickers.make
	e.sweeper = NewSweeper(cfg)
}

// Recovery ends the leases left active as destroyed/restart with one event by
// the system, destroys their boxes (a failure is left to the sweep), leaves
// leased leases alone, recalibrates the gauge and reports a lease it cannot end.
func TestRecoverActive(t *testing.T) {
	env := newSweepEnv(t)
	now := env.clock.Now()
	a := env.seed(1, "active", seedFields{boxID: "box-1", expires: now.Add(time.Hour)})
	b := env.seed(2, "active", seedFields{workspace: "gadgets", boxID: "box-2", expires: now.Add(time.Hour)})
	stuck := env.seed(3, "active", seedFields{boxID: "box-3", expires: now.Add(time.Hour)})
	leased := env.seed(4, "leased", seedFields{leaseExpires: timePtr(now.Add(time.Minute))})
	leasedBefore := rowText(t, env.db, leased)
	env.boxes.failNext = errors.New("podman rm failed at https://user:pw@registry.example/x")
	testutil.Exec(t, env.db, fmt.Sprintf(`CREATE TRIGGER refuse BEFORE UPDATE ON sandbox_leases WHEN OLD.id = '%s' BEGIN SELECT RAISE(ABORT, 'database is locked'); END`, stuck))
	logs := captureDefaultLog(t)
	reaps := env.prov.reapCount()

	err := env.sweeper.RecoverActive(context.Background())
	if err == nil || !strings.Contains(err.Error(), stuck) || !strings.Contains(err.Error(), "database is locked") {
		t.Errorf("RecoverActive = %v; want the failure of the stuck lease", err)
	}
	for _, id := range []string{a, b} {
		if s := env.readState(id); s.state != "destroyed" || s.endReason != "restart" || s.hash.Valid {
			t.Errorf("lease %s reads %+v", id, s)
		}
	}
	if s := env.readState(stuck); s.state != "active" || rowText(t, env.db, leased) != leasedBefore || env.prov.reapCount() != reaps {
		t.Errorf("stuck %s, leased changed %v, reaps %d; want active, unchanged, none", s.state, rowText(t, env.db, leased) != leasedBefore, env.prov.reapCount()-reaps)
	}
	events := env.eventsOf(audit.EventSandboxDestroy)
	if len(events) != 2 || events[0].Metadata["reason"] != "restart" || events[0].ActorType != "system" || len(env.eventsOf(audit.EventSandboxExpire)) != 0 {
		t.Errorf("events = %+v; want two destroys by the system with the reason restart", events)
	}
	if out := logs.String(); !strings.Contains(out, "podman rm failed") || strings.Contains(out, "user:pw") || !strings.Contains(out, stuck) {
		t.Errorf("the log:\n%s", out)
	}
	// The box is destroyed before the lease is ended, so the stuck lease's box is
	// gone too; the failed destroy is left for the sweep's orphan step.
	if left := env.boxes.ids(); !slices.Equal(left, []string{"box-1"}) || activeGauge(t, env.metrics, "widgets") != 1 || activeGauge(t, env.metrics, "gadgets") != 0 {
		t.Errorf("boxes left %v, gauge widgets %v gadgets %v; want box-1, 1 and 0", left, activeGauge(t, env.metrics, "widgets"), activeGauge(t, env.metrics, "gadgets"))
	}
	testutil.Exec(t, env.db, `DROP TRIGGER refuse`)
	env.sweep()
	if left := env.boxes.ids(); len(left) != 0 {
		t.Errorf("after a sweep the boxes left are %v; want the orphan box-1 destroyed", left)
	}
}

// StartReaper sweeps once before it returns and then once per tick until its
// context ends, releasing the ticker; RunStartup returns and logs the failures
// of the recovery and the first sweep, and the reaper runs all the same.
func TestStartReaperAndRunStartup(t *testing.T) {
	env := newSweepEnv(t)
	tickers := &fakeTickers{}
	env.withTickers(tickers)
	env.prov.setReap(nil, errors.New("reap: engine down"))
	env.boxes.listErr = errors.New("list: engine down")
	env.boxes.setDestroyErr(errors.New("destroy: engine down"))
	id := env.seed(1, "active", seedFields{boxID: "box-1", expires: env.clock.Now().Add(time.Hour)})
	base := env.prov.reapCount()
	logs := captureDefaultLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := env.sweeper.RunStartup(ctx)
	if err == nil || !strings.Contains(err.Error(), "reap") || !strings.Contains(err.Error(), "list") || env.prov.reapCount()-base != 1 {
		t.Errorf("RunStartup = %v after %d sweeps; want the failures of the first sweep", err, env.prov.reapCount()-base)
	}
	for _, want := range []string{"reap: engine down", "list: engine down", "destroy: engine down"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %q", want)
		}
	}
	if s := env.readState(id); s.state != "destroyed" || s.endReason != "restart" {
		t.Errorf("the lease reads %+v; want destroyed/restart although every call failed", s)
	}
	for range 3 {
		tickers.fire()
	}
	waitFor(t, "three sweeps on three ticks", func() bool { return env.prov.reapCount()-base == 4 })
	cancel()
	env.sweeper.reaper.stop(context.Background())
	tickers.fire()
	time.Sleep(30 * time.Millisecond)
	if env.prov.reapCount()-base != 4 || tickers.stoppedCount() != 1 {
		t.Errorf("after the cancel: %d sweeps, ticker stopped %d times; want 4 and 1", env.prov.reapCount()-base, tickers.stoppedCount())
	}
}

// Shutdown enters shutdown mode, waits for a sweep in progress, ends every
// active lease as destroyed/shutdown with one event, leaves leased leases alone
// and does nothing twice; at its limit the leases not yet ended stay active.
func TestSweeperShutdown(t *testing.T) {
	env := newSweepEnv(t)
	now := env.clock.Now()
	a := env.seed(1, "active", seedFields{boxID: "box-1", expires: now.Add(time.Hour)})
	b := env.seed(2, "active", seedFields{workspace: "gadgets", boxID: "box-2", expires: now.Add(time.Hour)})
	leased := env.seed(3, "leased", seedFields{leaseExpires: timePtr(now.Add(time.Hour))})
	leasedBefore := rowText(t, env.db, leased)
	env.prov.set(func(p *recordingProvider) { p.reapDelay = 100 * time.Millisecond })
	swept := make(chan struct{})
	go func() { defer close(swept); _ = env.sweeper.Sweep(context.Background()) }()
	waitFor(t, "the sweep to reach the provider", func() bool { env.prov.mu.Lock(); defer env.prov.mu.Unlock(); return env.prov.inflight > 0 })

	if err := env.sweeper.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-swept:
	default:
		t.Error("Shutdown returned while a sweep was running")
	}
	for _, id := range []string{a, b} {
		if s := env.readState(id); s.state != "destroyed" || s.endReason != "shutdown" || s.hash.Valid {
			t.Errorf("lease %s reads %+v", id, s)
		}
	}
	events := env.eventsOf(audit.EventSandboxDestroy)
	if len(events) != 2 || events[0].ActorType != "system" || events[0].Metadata["reason"] != "shutdown" || len(env.boxes.ids()) != 0 || rowText(t, env.db, leased) != leasedBefore || !env.profile.InShutdown() {
		t.Errorf("events %+v, boxes %v, leased changed %v, shutdown mode %v", events, env.boxes.ids(), rowText(t, env.db, leased) != leasedBefore, env.profile.InShutdown())
	}
	again := env.seed(4, "active", seedFields{boxID: "box-4", expires: now.Add(time.Hour)})
	if err := env.sweeper.Shutdown(context.Background()); err != nil || env.readState(again).state != "active" {
		t.Errorf("the second Shutdown: %v, lease %s; want nothing done", err, env.readState(again).state)
	}

	for name, prepare := range map[string]func(env *sweepEnv, hung string){
		"a Destroy that hangs": func(env *sweepEnv, hung string) { env.boxes.destroyHang = map[string]bool{hung: true} },
		"a sweep that does not end": func(env *sweepEnv, _ string) {
			env.prov.set(func(p *recordingProvider) { p.reapDelay = time.Second })
			go func() { _ = env.sweeper.Sweep(context.Background()) }()
			waitFor(t, "the sweep to reach the provider", func() bool { env.prov.mu.Lock(); defer env.prov.mu.Unlock(); return env.prov.inflight > 0 })
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := newSweepEnv(t)
			hung := env.seed(1, "active", seedFields{boxID: "box-hung", expires: env.clock.Now().Add(time.Hour)})
			ok := env.seed(2, "active", seedFields{boxID: "box-ok", expires: env.clock.Now().Add(time.Hour)})
			prepare(env, "box-hung")
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := env.sweeper.Shutdown(ctx)
			if time.Since(start) > time.Second || err == nil {
				t.Errorf("Shutdown took %v and returned %v; want an error at the limit", time.Since(start), err)
			}
			if s := env.readState(hung); s.state != "active" || !s.hash.Valid {
				t.Errorf("the hung lease reads %+v; want it left active", s)
			}
			if s := env.readState(ok); name == "a Destroy that hangs" && (s.state != "destroyed" || s.endReason != "shutdown") {
				t.Errorf("the other lease reads %+v; want destroyed/shutdown", s)
			}
		})
	}
}

// The gateway's Shutdown stops the reaper, ends the active leases, stops
// gatewayd and does nothing twice or on a nil gateway; the counters keep the
// creates before it; a hung provider does not hold it past its limit.
func TestGatewayShutdown(t *testing.T) {
	m := audit.NewMetrics()
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig(), metrics: m})
	tickers := &fakeTickers{}
	g, err := newGateway(env.profile, GatewayDeps{DB: env.db, Secrets: env.secrets, Audit: env.sink, Now: env.clock.Now, Metrics: m}, gatewaySeams{})
	if err != nil {
		t.Fatal(err)
	}
	g.sweeper.tickers = tickers.make
	gw := env.runGateway(g, true)
	var ids []string
	for range 2 {
		id, token := env.issueToken("")
		createVia(t, gw.dial(t, token))
		ids = append(ids, id)
	}
	leased := env.issue("")
	swept := env.prov.reapCount()

	if err := gw.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for _, id := range ids {
		if s := env.readState(id); s.state != "destroyed" || s.endReason != "shutdown" {
			t.Errorf("lease %s reads %+v", id, s)
		}
	}
	events := env.eventsOf(audit.EventSandboxDestroy)
	if s := env.readState(leased); s.state != "leased" || len(env.prov.live()) != 0 || len(events) != 2 || events[0].ActorType != "system" || events[0].Metadata["reason"] != "shutdown" {
		t.Errorf("leased %s, live %v, events %+v", s.state, env.prov.live(), events)
	}
	select {
	case <-g.sweeper.reaper.done:
	default:
		t.Error("the reaper loop is still running")
	}
	tickers.fire()
	time.Sleep(30 * time.Millisecond)
	if tickers.stoppedCount() != 1 || env.prov.reapCount() != swept || createsCounter(t, m, "created") != 2 {
		t.Errorf("ticker stopped %d, reaps %d, created %v", tickers.stoppedCount(), env.prov.reapCount()-swept, createsCounter(t, m, "created"))
	}
	if _, err := gateway.Dial(context.Background(), gw.URL(), gateway.Options{Token: "sbl_" + strings.Repeat("a", 44)}); err == nil {
		t.Error("the gateway still answers")
	}
	row, n := rowText(t, env.db, leased), len(env.emitter.recorded())
	if err := gw.Shutdown(context.Background()); err != nil || rowText(t, env.db, leased) != row || len(env.emitter.recorded()) != n {
		t.Errorf("the second Shutdown: %v, changed %v", err, rowText(t, env.db, leased) != row)
	}
	if err := (*Gateway)(nil).Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown of a nil gateway: %v", err)
	}

	env = newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	gw = env.startGateway()
	id, token := env.issueToken("")
	createVia(t, gw.dial(t, token))
	gw.shutdownLimit = 150 * time.Millisecond
	block := make(chan struct{})
	env.prov.setCloseBlock(block)
	defer close(block)
	start := time.Now()
	if err := gw.Shutdown(context.Background()); err == nil || time.Since(start) > 3*time.Second || env.readState(id).state != "active" {
		t.Errorf("Shutdown with a hung provider = %v after %v, lease %s; want an error at the limit and the lease left active", err, time.Since(start), env.readState(id).state)
	}
}

// Hub's shutdown destroys every box through the remote client and only then
// closes it, once; the error of Close is part of the result and a Close that
// never returns does not hold the shutdown past its limit.
func TestGatewayShutdown_ClosesTheRemoteClientLast(t *testing.T) {
	log := &callLog{}
	env := newScriptedEnv(t, func(p *recordingProvider) RemoteClient {
		return orderedRemote{scriptedRemote{recordingProvider: p, closeLog: log}, log}
	})
	idA, _, _ := env.activeLease()
	idB, _, _ := env.activeLease()
	if err := env.gw.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	got := log.all()
	if len(got) != 3 || got[2] != "close" || !strings.HasPrefix(got[0], "destroy ") || !strings.HasPrefix(got[1], "destroy ") || got[0] == got[1] {
		t.Errorf("calls = %v; want the destroy of each box, then close", got)
	}
	for _, id := range []string{idA, idB} {
		if l := env.lease(id); l.State != "destroyed" || *l.EndReason != "shutdown" {
			t.Errorf("lease %s = %s/%v", id, l.State, l.EndReason)
		}
	}
	env.gw.Shutdown(context.Background())
	if env.profile.Close() != nil || len(log.all()) != 3 || len(env.prov.live()) != 0 {
		t.Errorf("after a second shutdown: calls %v, live %v", log.all(), env.prov.live())
	}

	boom := errors.New("closing the client failed")
	env = newScriptedEnv(t, func(p *recordingProvider) RemoteClient {
		return scriptedRemote{recordingProvider: p, closeLog: &callLog{}, closeErr: boom}
	})
	if err := env.gw.Shutdown(context.Background()); err == nil || !strings.Contains(err.Error(), boom.Error()) {
		t.Errorf("Shutdown = %v; want the error of Close", err)
	}
	hang := make(chan struct{})
	defer close(hang)
	env = newScriptedEnv(t, func(p *recordingProvider) RemoteClient {
		return hangingRemote{scriptedRemote{recordingProvider: p}, hang}
	})
	env.gw.shutdownLimit = 300 * time.Millisecond
	began := time.Now()
	if err := env.gw.Shutdown(context.Background()); err == nil || !strings.Contains(err.Error(), "remote gateway") || time.Since(began) > 5*time.Second {
		t.Errorf("Shutdown with a client that never closes = %v after %v", err, time.Since(began))
	}
}

// orderedRemote logs the destroy of every box before the Close of the client.
type orderedRemote struct {
	scriptedRemote
	log *callLog
}

func (o orderedRemote) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	box, err := o.scriptedRemote.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	return orderedBox{box, o.log}, nil
}

type orderedBox struct {
	sandbox.Sandbox
	log *callLog
}

func (b orderedBox) Unwrap() sandbox.Sandbox { return b.Sandbox }
func (b orderedBox) Close(ctx context.Context) error {
	b.log.add("destroy " + b.ID())
	return b.Sandbox.Close(ctx)
}

type hangingRemote struct {
	scriptedRemote
	hang <-chan struct{}
}

func (h hangingRemote) Close() error { <-h.hang; return nil }
