package boundtoken

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// fakeClock is an injectable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }

func (f *fakeClock) set(t time.Time) { f.mu.Lock(); f.t = t; f.mu.Unlock() }

var t0 = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func refusal(token string) wsaccess.BoundRefusal {
	return wsaccess.BoundRefusal{TokenID: token, UserID: "u", BoundSlug: "S", RequestedSlug: "X", Method: "GET",
		Route: "/api/v1/workspaces/:slug/patches", Status: 404, Surface: wsaccess.SurfaceAPI}
}

func refusedEvents(em *recordingEmitter, token string) []audit.HubEvent {
	var out []audit.HubEvent
	for _, e := range em.events() {
		if e.EventType == audit.EventTokenBoundRefused && (token == "" || e.ResourceID == token) {
			out = append(out, e)
		}
	}
	return out
}

// TS-34-59: The first report for a token emits one fully populated
// hub.token.bound_refused event. Requirement: 34-REQ-10.4
func TestReporter_FirstReportEmits_TS_34_59(t *testing.T) {
	em := &recordingEmitter{}
	clock := &fakeClock{t: t0}
	r := NewReporter(em, clock.now)
	r.Report(refusal("T"))
	r.Report(wsaccess.BoundRefusal{TokenID: "U", BoundSlug: "S", Method: "POST", Route: "/api/v1/user/tokens", Status: 403, Surface: wsaccess.SurfaceAPI})

	evs := refusedEvents(em, "")
	if len(evs) != 2 {
		t.Fatalf("events = %d; want 2", len(evs))
	}
	e := evs[0]
	if e.ResourceType != "token" || e.ResourceID != "T" || e.Action != "refuse" || e.Workspace != "S" || e.ActorType != "pat" || e.ActorID != "u" {
		t.Errorf("event %+v", e)
	}
	want := map[string]any{"token_id": "T", "bound_workspace": "S", "requested_workspace": "X", "method": "GET",
		"route": "/api/v1/workspaces/:slug/patches", "status": 404, "surface": "api", "suppressed": 0}
	if fmt.Sprint(e.Metadata) != fmt.Sprint(want) {
		t.Errorf("metadata %v; want %v", e.Metadata, want)
	}
	if evs[1].ActorID != "" {
		t.Errorf("second actor id %q; want empty", evs[1].ActorID)
	}
	if _, ok := evs[1].Metadata["requested_workspace"]; ok {
		t.Errorf("second metadata %v has requested_workspace; want it absent", evs[1].Metadata)
	}
}

// TS-34-60: Reports inside the minute are held back and counted into the next
// event's suppressed. Requirement: 34-REQ-10.5
func TestReporter_HoldBack_TS_34_60(t *testing.T) {
	em := &recordingEmitter{}
	clock := &fakeClock{t: t0}
	r := NewReporter(em, clock.now)
	r.Report(refusal("T"))
	clock.set(t0.Add(5 * time.Second))
	r.Report(refusal("V"))
	for _, s := range []int{10, 20, 30} {
		clock.set(t0.Add(time.Duration(s) * time.Second))
		r.Report(refusal("T"))
	}
	if n := len(refusedEvents(em, "T")); n != 1 {
		t.Fatalf("events for T inside the minute = %d; want 1", n)
	}
	clock.set(t0.Add(61 * time.Second))
	r.Report(refusal("T"))
	clock.set(t0.Add(123 * time.Second))
	r.Report(refusal("T"))
	evs := refusedEvents(em, "T")
	if len(evs) != 3 || evs[1].Metadata["suppressed"] != 3 || evs[2].Metadata["suppressed"] != 0 {
		t.Errorf("T events %d, suppressed %v, %v; want 3 events with 3 then 0", len(evs), evs[1].Metadata["suppressed"], evs[len(evs)-1].Metadata["suppressed"])
	}
	if v := refusedEvents(em, "V"); len(v) != 1 || v[0].Metadata["suppressed"] != 0 {
		t.Errorf("V events %v; want one with suppressed 0", v)
	}
}

// TS-34-61 (property): For any sequence of timestamped reports, events per
// token are at least a minute apart and each suppressed equals the number of
// that token's reports since its previous event.
// Requirement: 34-REQ-10.6
func TestReporter_PropertyWindow_TS_34_61(t *testing.T) {
	rng := rand.New(rand.NewSource(34))
	for run := 0; run < 200; run++ {
		em := &recordingEmitter{}
		clock := &fakeClock{t: t0}
		r := NewReporter(em, clock.now)
		type sent struct {
			at    time.Time
			token string
		}
		var reports []sent
		at := t0
		for i := 0; i < 60; i++ {
			at = at.Add(time.Duration(rng.Intn(40)) * time.Second)
			tok := fmt.Sprintf("T%d", rng.Intn(3))
			clock.set(at)
			r.Report(refusal(tok))
			reports = append(reports, sent{at, tok})
		}
		// Replay: the events and their times, per token.
		emittedAt := map[string][]time.Time{}
		ei := map[string]int{}
		evsOf := map[string][]audit.HubEvent{}
		for _, tok := range []string{"T0", "T1", "T2"} {
			evsOf[tok] = refusedEvents(em, tok)
		}
		since := map[string]int{}
		for _, s := range reports {
			last := emittedAt[s.token]
			if len(last) == 0 || s.at.Sub(last[len(last)-1]) >= time.Minute {
				e := evsOf[s.token][ei[s.token]]
				ei[s.token]++
				if got := e.Metadata["suppressed"]; got != since[s.token] {
					t.Fatalf("run %d: %s event at %v suppressed %v; want %d", run, s.token, s.at, got, since[s.token])
				}
				emittedAt[s.token] = append(last, s.at)
				since[s.token] = 0
			} else {
				since[s.token]++
			}
		}
		for tok, evs := range evsOf {
			if len(evs) != ei[tok] {
				t.Fatalf("run %d: %s has %d events; want %d", run, tok, len(evs), ei[tok])
			}
		}
	}
}

// TS-34-62: Limiter entries older than the window are dropped, and the
// injected clock drives the decisions. Requirement: 34-REQ-10.7
func TestReporter_BoundedMemory_TS_34_62(t *testing.T) {
	em := &recordingEmitter{}
	clock := &fakeClock{t: t0}
	r := NewReporter(em, clock.now)
	for i := 0; i < 1000; i++ {
		r.Report(refusal(fmt.Sprintf("tok%d", i)))
	}
	if n := r.entryCount(); n != 1000 {
		t.Fatalf("entries = %d; want 1000", n)
	}
	clock.set(t0.Add(61 * time.Second))
	r.Report(refusal("tok0"))
	if n := r.entryCount(); n != 1 {
		t.Errorf("entries after the window = %d; want 1", n)
	}
	if n := len(refusedEvents(em, "tok0")); n != 2 {
		t.Errorf("tok0 events = %d; want 2, the injected clock moved past the window", n)
	}
}

// TS-34-63: Neither the token nor the Authorization header appears in an
// event or a log line, also when emission fails. Requirement: 34-REQ-10.8
func TestReporter_NoSecrets_TS_34_63(t *testing.T) {
	const secret = "ak_pat_T_DISTINCTIVE0SECRET"
	logs := captureLogs(t)
	em := &recordingEmitter{err: errors.New("emit failed")}
	clock := &fakeClock{t: t0}
	r := NewReporter(em, clock.now)
	r.Report(refusal("T"))
	clock.set(t0.Add(10 * time.Second))
	r.Report(refusal("T"))
	raw, _ := json.Marshal(em.events())
	if len(em.events()) != 1 || !strings.Contains(logs.String(), "emit failed") {
		t.Fatalf("events %d, logs %q; want one event and the emit error logged", len(em.events()), logs)
	}
	for _, s := range []string{secret, "DISTINCTIVE0SECRET", "Bearer"} {
		if strings.Contains(string(raw), s) || strings.Contains(logs.String(), s) {
			t.Errorf("%q appears in an event or the log", s)
		}
	}
}
