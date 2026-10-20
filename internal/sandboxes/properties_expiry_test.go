package sandboxes

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// propWorkspaces are the workspaces the property tests issue leases in.
var propWorkspaces = []string{"widgets", "gadgets"}

// propEnv is the whole expiry machinery over one database and one fake clock: the
// lease API, the gateway with its backend and sweeper, the metrics and the audit
// stream. The property tests apply random operations to it and check, after every
// one, what must hold whatever the order.
type propEnv struct {
	t *testing.T
	*expiryBackendEnv
	gw      *Gateway
	tickers *fakeTickers
	rnd     *rand.Rand
	logs    *logBuffer

	// sensitive are the secret values, environment values and tokens that must
	// appear in no event, no log line and no field the expiry writes.
	sensitive []string
	// leaseExpiry is lease_expires_at as POST returned it, and tokens the token of
	// each lease. clients are the boxes the lease holders hold.
	leaseExpiry map[string]string
	tokens      map[string]string
	order       []string
	clients     map[string]sandbox.Sandbox
	advanced    time.Duration
}

func newPropEnv(t *testing.T, seed uint64) *propEnv {
	t.Helper()
	rnd := rand.New(rand.NewPCG(28, seed))
	secretValue, envValue := fmt.Sprintf("pat-%016x", rnd.Uint64()), fmt.Sprintf("env-%016x", rnd.Uint64())

	cfg := bigLeaseConfig()
	cfg.Profiles[0].RequireLandlock = false
	cfg.Profiles[0].Env = map[string]string{"SECRET_ENV": envValue}
	m := audit.NewMetrics()
	env := newBackendEnv(t, leaseEnvOptions{config: cfg, metrics: m})
	boxes := &fakeBoxes{}
	e := &propEnv{
		t:   t,
		rnd: rnd,
		expiryBackendEnv: &expiryBackendEnv{backendEnv: env, metrics: m, sweeper: NewSweeper(SweeperConfig{
			DB: env.db, Provider: env.prov, Boxes: boxes, Audit: env.sink, Metrics: m, Now: env.clock.Now, Service: env.profile,
		})},
		tickers:     &fakeTickers{},
		leaseExpiry: map[string]string{},
		tokens:      map[string]string{},
		clients:     map[string]sandbox.Sandbox{},
		sensitive:   []string{secretValue, envValue},
	}
	env.addWorkspace("gadgets", leaseOwner, "", "active")
	for _, ws := range propWorkspaces {
		if _, err := env.secrets.UpdateSecret("workspace", ws, "GIT_PAT", secretValue); err != nil {
			env.plantSecret("workspace", ws, "GIT_PAT", secretValue)
		}
	}
	e.logs = captureDefaultLog(t)

	base := env.prov.reapCount()
	e.gw = e.runGateway(e.tickers)
	// Run's first sweep and the one gatewayd asks for at its start.
	waitFor(t, "the first sweeps", func() bool { return env.prov.reapCount()-base >= 2 })
	return e
}

// issue posts a lease in a random workspace.
func (e *propEnv) issue() {
	ws := propWorkspaces[e.rnd.IntN(len(propWorkspaces))]
	body := `{"profile":"tools"}`
	if e.rnd.IntN(3) == 0 {
		body = fmt.Sprintf(`{"profile":"tools","ttl":"%dm"}`, 2+e.rnd.IntN(50))
	}
	rec := e.post(ws, body, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
	}
	obj := jsonObject(e.t, rec)
	id, _ := obj["id"].(string)
	e.leaseExpiry[id], _ = obj["lease_expires_at"].(string)
	e.tokens[id], _ = obj["token"].(string)
	e.order = append(e.order, id)
	e.sensitive = append(e.sensitive, e.tokens[id])
}

// pick returns a random issued lease, or "" when there is none.
func (e *propEnv) pick() string {
	if len(e.order) == 0 {
		return ""
	}
	return e.order[e.rnd.IntN(len(e.order))]
}

// create has the lease holder make the box of a lease through the real gateway
// client; the lease may refuse. The client stays connected until the test ends,
// since closing it closes the boxes it made.
func (e *propEnv) create(id string) {
	p, err := gateway.Dial(context.Background(), e.gw.URL(), gateway.Options{Token: e.tokens[id]})
	if err != nil {
		return // the token no longer works
	}
	e.t.Cleanup(func() { _ = p.Close() })
	if sb, err := p.Create(context.Background(), sandbox.Spec{Image: "ignored"}); err == nil {
		e.clients[id] = sb
	}
}

// step applies one random operation.
func (e *propEnv) step() string {
	id := e.pick()
	switch n := e.rnd.IntN(100); {
	case n < 24 || id == "":
		e.issue()
		return "issue"
	case n < 50:
		e.create(id)
		return "create " + id
	case n < 60:
		e.do(http.MethodDelete, "/api/v1/workspaces/"+e.workspaceOf(id)+"/sandboxes/"+id, "", apiKeyAuth())
		return "delete " + id
	case n < 68:
		if sb := e.clients[id]; sb != nil {
			_ = sb.Close(context.Background())
		}
		return "close " + id
	case n < 82:
		if e.advanced < 12*time.Hour {
			d := []time.Duration{10 * time.Second, 2 * time.Minute, 6 * time.Minute, 40 * time.Minute, 70 * time.Minute, 3 * time.Hour}[e.rnd.IntN(6)]
			e.clock.Advance(d)
			e.advanced += d
		}
		return "advance"
	case n < 94:
		_ = e.gw.sweeper.Sweep(context.Background())
		return "sweep"
	case n < 98:
		_ = e.gw.sweeper.RecoverActive(context.Background())
		return "recover"
	default:
		_ = e.gw.Shutdown(context.Background())
		return "shutdown"
	}
}

// workspaceOf reads the workspace of a lease.
func (e *propEnv) workspaceOf(id string) string {
	var ws string
	if err := e.db.QueryRow(`SELECT workspace FROM sandbox_leases WHERE id = ?`, id).Scan(&ws); err != nil {
		e.t.Fatalf("reading the workspace of %s: %v", id, err)
	}
	return ws
}

// propLeaseRow is what the invariants read of a lease.
type propLeaseRow struct {
	id, workspace, state, createdAt string
	endedAt, endReason, sandboxID   *string
	leaseExpires                    *string
}

func (e *propEnv) rows() []propLeaseRow {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT id, workspace, state, created_at, ended_at, end_reason, sandbox_id, lease_expires_at FROM sandbox_leases ORDER BY created_at, id`)
	if err != nil {
		e.t.Fatalf("reading the leases: %v", err)
	}
	defer rows.Close()
	var out []propLeaseRow
	for rows.Next() {
		var r propLeaseRow
		if err := rows.Scan(&r.id, &r.workspace, &r.state, &r.createdAt, &r.endedAt, &r.endReason, &r.sandboxID, &r.leaseExpires); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// check applies every invariant that holds after each operation, whatever it was.
func (e *propEnv) check(history []string) {
	e.t.Helper()
	fail := func(format string, args ...any) {
		e.t.Helper()
		e.t.Fatalf(format+"\nafter: %s", append(args, strings.Join(history, ", "))...)
	}
	rows := e.rows()

	// 28-REQ-2.4: the deadline for a Create is the one POST returned.
	active := map[string]int{}
	destroyed, wantSum := 0, 0.0
	for _, r := range rows {
		if r.leaseExpires == nil || *r.leaseExpires != e.leaseExpiry[r.id] {
			fail("lease %s has lease_expires_at %v; want %q, the value POST returned", r.id, r.leaseExpires, e.leaseExpiry[r.id])
		}
		if r.state == leaseActive {
			active[r.workspace]++
		}
		if r.state == leaseDestroyed {
			destroyed++
			created, _ := apikit.ParseUTC(r.createdAt)
			ended, _ := apikit.ParseUTC(*r.endedAt)
			wantSum += ended.Sub(created).Seconds()
		}
	}

	// 28-REQ-9.8: the gauge is the number of active leases of each workspace.
	for _, ws := range propWorkspaces {
		if got := activeGauge(e.t, e.metrics, ws); got != float64(active[ws]) {
			fail("afhub_sandboxes_active{%s} = %v; want %d active leases", ws, got, active[ws])
		}
	}

	// 28-REQ-9.6: a lease that became destroyed was observed once, with its duration.
	if count, sum := leaseSeconds(e.t, e.metrics); count != uint64(destroyed) || sum != wantSum {
		fail("afhub_sandbox_lease_seconds = (%d, %v); want (%d, %v) over the destroyed leases", count, sum, destroyed, wantSum)
	}

	// 28-REQ-8.3: each ending left exactly one event of the kind its cause calls for.
	byLease := map[string][]audit.HubEvent{}
	for _, ev := range e.emitter.recorded() {
		byLease[ev.ResourceID] = append(byLease[ev.ResourceID], ev)
	}
	for _, r := range rows {
		var endings, failures []audit.HubEvent
		for _, ev := range byLease[r.id] {
			switch ev.EventType {
			case audit.EventSandboxExpire, audit.EventSandboxDestroy:
				endings = append(endings, ev)
			case audit.EventSandboxFailed:
				failures = append(failures, ev)
			}
		}
		switch r.state {
		case leaseLeased, leaseActive:
			if len(endings) != 0 || len(failures) != 0 {
				fail("live lease %s has ending events %v %v", r.id, endings, failures)
			}
		case leaseFailed:
			if len(endings) != 0 || len(failures) != 1 {
				fail("failed lease %s has %d ending events and %d failed events; want 0 and 1", r.id, len(endings), len(failures))
			}
		default:
			if len(endings) != 1 || len(failures) != 0 {
				fail("%s lease %s has %d ending events (%v); want exactly one", r.state, r.id, len(endings), endings)
			}
			ev, reason := endings[0], *r.endReason
			wantType := audit.EventSandboxDestroy
			if reason == leaseEndReasonExpired {
				wantType = audit.EventSandboxExpire
			}
			if ev.EventType != wantType {
				fail("lease %s ended with %s and emitted %s; want %s", r.id, reason, ev.EventType, wantType)
			}
			if wantType == audit.EventSandboxDestroy && ev.Metadata["reason"] != reason {
				fail("lease %s ended with %s but its destroy event says %v", r.id, reason, ev.Metadata["reason"])
			}
			if wantType == audit.EventSandboxExpire && (ev.ActorType != "system" || ev.ActorID != "") {
				fail("the expire event of %s has actor %q/%q", r.id, ev.ActorType, ev.ActorID)
			}
		}
	}
}

// userinfo matches the user information of a URL.
var userinfo = regexp.MustCompile(`://[^/@\s"]+@`)

// checkNoLeaks looks for the sensitive values in every event, every log line and
// every field of a lease row except two that are not the expiry's: the token hash,
// which a live lease keeps by design and a terminal one has lost, and the fixed
// environment, which the lease keeps from the day it was issued for the gateway.
func (e *propEnv) checkNoLeaks() {
	e.t.Helper()
	var blob strings.Builder
	for _, ev := range e.emitter.recorded() {
		fmt.Fprintf(&blob, "%+v\n", ev)
	}
	blob.WriteString(e.logs.String())
	rows, err := e.db.Query(`SELECT ` + strings.Replace(leaseColumns, " env,", "", 1) + ` FROM sandbox_leases`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			e.t.Fatal(err)
		}
		fmt.Fprintln(&blob, values...)
	}
	text := blob.String()
	needles := slices.Clone(e.sensitive)
	for _, token := range e.tokens {
		needles = append(needles, HashToken(token))
	}
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			e.t.Errorf("%q appears in an event, a log line or a lease field", needle)
		}
	}
	if m := userinfo.FindString(e.logs.String()); m != "" {
		e.t.Errorf("a log line holds URL user information (%q)", m)
	}
	for _, r := range e.rows() {
		var hash *string
		if err := e.db.QueryRow(`SELECT token_hash FROM sandbox_leases WHERE id = ?`, r.id).Scan(&hash); err != nil {
			e.t.Fatal(err)
		}
		if r.state != leaseLeased && r.state != leaseActive && hash != nil {
			e.t.Errorf("terminal lease %s still has a token hash", r.id)
		}
	}
}

// TS-28-9, TS-28-41, TS-28-50, TS-28-52 (properties): for any sequence of
// operations, run one at a time, lease_expires_at never changes, each ending
// leaves one event of the kind its cause calls for, every lease that became
// destroyed was observed once, and the gauge is the number of active leases of
// each workspace.
// Requirements: 28-REQ-2.4, 28-REQ-8.3, 28-REQ-9.6, 28-REQ-9.8
func TestExpiryProperties_TS_28_9_41_50_52(t *testing.T) {
	for seed := range uint64(20) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			e := newPropEnv(t, seed)
			var history []string
			for range 45 {
				history = append(history, e.step())
				e.check(history)
			}
		})
	}
}

// TS-28-44 (property): no event, log line or lease field the expiry flows write
// holds a secret value, an environment value, a token or a token hash, and no log
// line holds the user information of a URL, even when the provider's errors do.
// Requirement: 28-REQ-8.6
func TestExpiryFlowsDoNotLeak_TS_28_44(t *testing.T) {
	for seed := range uint64(12) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			e := newPropEnv(t, 1000+seed)
			// The provider's errors name a URL with user information, a secret value and
			// an environment value, as an engine's might.
			leak := fmt.Sprintf("podman rm failed at https://user:%s@registry.example/x with GIT_PAT=%s SECRET_ENV=%s", "pw-"+e.sensitive[1], e.sensitive[0], e.sensitive[1])
			e.prov.setCloseErr(fmt.Errorf("%s", leak))
			e.sensitive = append(e.sensitive, "pw-"+e.sensitive[1])
			for range 45 {
				e.step()
			}
			// Every flow once more: a late Create, a sweep, a restart, the shutdown.
			id := e.issueNow()
			e.clock.Advance(6 * time.Minute)
			e.create(id)
			_ = e.gw.sweeper.Sweep(context.Background())
			_ = e.gw.sweeper.RecoverActive(context.Background())
			_ = e.gw.Shutdown(context.Background())

			e.checkNoLeaks()
		})
	}
}

// issueNow issues a lease and returns its id.
func (e *propEnv) issueNow() string {
	e.issue()
	return e.order[len(e.order)-1]
}

// TS-28-41: Each ending emits one event according to its cause: a late Create and a
// sweep emit hub.sandbox.expire, shutdown and restart emit hub.sandbox.destroy by
// the system with their reason.
// Requirement: 28-REQ-8.3
func TestEachEndingEmitsOneEvent_TS_28_41(t *testing.T) {
	e := newPropEnv(t, 41)
	tokensFor := func(id string) *gateway.Provider {
		p, err := gateway.Dial(context.Background(), e.gw.URL(), gateway.Options{Token: e.tokens[id]})
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		t.Cleanup(func() { _ = p.Close() })
		return p
	}

	late, lapsed, overdue, forRestart := e.issueNow(), e.issueNow(), e.issueNow(), e.issueNow()
	makeBox := func(id string) {
		t.Helper()
		sb, err := tokensFor(id).Create(context.Background(), sandbox.Spec{Image: "ignored"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		e.clients[id] = sb
	}
	makeBox(overdue)
	makeBox(forRestart)

	// A late Create, and a sweep: the lease that never got a Create, and the box that
	// outlived its lease.
	e.clock.Advance(6 * time.Minute)
	_, _ = tokensFor(late).Create(context.Background(), sandbox.Spec{Image: "ignored"})
	e.do(http.MethodGet, "/api/v1/workspaces/widgets/sandboxes", "", apiKeyAuth())
	// The lease holders' boxes live for the hour; move past it for the overdue one only.
	if _, err := e.db.Exec(`UPDATE sandbox_leases SET expires_at = ? WHERE id = ?`, apikit.FormatUTC(e.clock.Now().Add(-time.Minute)), overdue); err != nil {
		t.Fatal(err)
	}
	if err := e.gw.sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	// Restart recovery ends the active lease, and then the shutdown ends the one that
	// became active after it.
	if _, err := e.db.Exec(`UPDATE sandbox_leases SET expires_at = '2099-01-01T00:00:00Z' WHERE id = ?`, forRestart); err != nil {
		t.Fatal(err)
	}
	if err := e.gw.sweeper.RecoverActive(context.Background()); err != nil {
		t.Fatalf("RecoverActive: %v", err)
	}
	forShutdown := e.issueNow()
	makeBox(forShutdown)
	if err := e.gw.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	want := map[string]struct{ eventType, reason string }{
		late:        {audit.EventSandboxExpire, "expired"},
		lapsed:      {audit.EventSandboxExpire, "expired"},
		overdue:     {audit.EventSandboxExpire, "expired"},
		forRestart:  {audit.EventSandboxDestroy, "restart"},
		forShutdown: {audit.EventSandboxDestroy, "shutdown"},
	}
	for id, w := range want {
		var got []audit.HubEvent
		for _, ev := range e.emitter.recorded() {
			if ev.ResourceID == id && (ev.EventType == audit.EventSandboxExpire || ev.EventType == audit.EventSandboxDestroy) {
				got = append(got, ev)
			}
		}
		if len(got) != 1 || got[0].EventType != w.eventType {
			t.Errorf("lease %s: events %v; want exactly one %s", id, got, w.eventType)
			continue
		}
		if s := e.readState(id); s.endReason != w.reason {
			t.Errorf("lease %s ended with %s; want %s", id, s.endReason, w.reason)
		}
		if w.eventType == audit.EventSandboxDestroy && (got[0].ActorType != "system" || got[0].ActorID != "" || got[0].Metadata["reason"] != w.reason) {
			t.Errorf("lease %s: event %+v; want reason %s by the system", id, got[0], w.reason)
		}
	}
}

// TS-28-50: A lease that becomes destroyed, by DELETE, a sweep, shutdown or
// restart, is observed once with ended_at minus created_at; one that lapses is not.
// Requirement: 28-REQ-9.6
func TestLeaseSeconds_OncePerDestroyedLease_TS_28_50(t *testing.T) {
	e := newPropEnv(t, 50)
	observed := func() (uint64, float64) { return leaseSeconds(t, e.metrics) }

	mkActive := func() string {
		id := e.issueNow()
		p, err := gateway.Dial(context.Background(), e.gw.URL(), gateway.Options{Token: e.tokens[id]})
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		t.Cleanup(func() { _ = p.Close() })
		sb, err := p.Create(context.Background(), sandbox.Spec{Image: "ignored"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		e.clients[id] = sb
		return id
	}
	step := func(name string, seconds float64, end func(id string)) {
		t.Helper()
		id := mkActive()
		count0, sum0 := observed()
		e.clock.Advance(time.Duration(seconds) * time.Second)
		end(id)
		count, sum := observed()
		if count != count0+1 || sum != sum0+seconds {
			t.Errorf("%s: lease_seconds went from (%d, %v) to (%d, %v); want one more observation of %v seconds", name, count0, sum0, count, sum, seconds)
		}
	}

	step("DELETE", 90, func(id string) {
		e.do(http.MethodDelete, "/api/v1/workspaces/"+e.workspaceOf(id)+"/sandboxes/"+id, "", apiKeyAuth())
	})
	step("sweep", 120, func(id string) {
		if _, err := e.db.Exec(`UPDATE sandbox_leases SET expires_at = ? WHERE id = ?`, apikit.FormatUTC(e.clock.Now().Add(-time.Second)), id); err != nil {
			t.Fatal(err)
		}
		_ = e.gw.sweeper.Sweep(context.Background())
	})
	step("restart", 30, func(string) { _ = e.gw.sweeper.RecoverActive(context.Background()) })

	// A lease that lapses is not observed.
	e.issueNow()
	count0, _ := observed()
	e.clock.Advance(6 * time.Minute)
	_ = e.gw.sweeper.Sweep(context.Background())
	if count, _ := observed(); count != count0 {
		t.Errorf("a lapsed lease was observed: the count went from %d to %d", count0, count)
	}

	step("shutdown", 45, func(string) { _ = e.gw.Shutdown(context.Background()) })
}
