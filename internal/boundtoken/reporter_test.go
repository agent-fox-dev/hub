package boundtoken

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

var t0 = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func refusal(token string) wsaccess.BoundRefusal {
	return wsaccess.BoundRefusal{TokenID: token, UserID: "u", BoundSlug: "S", RequestedSlug: "X", Method: "GET",
		Route: "/api/v1/workspaces/:slug/patches", Status: 404, Surface: wsaccess.SurfaceAPI}
}

// The first refusal of a token emits one hub.token.bound_refused event with
// the refusal's details; the refusals within a minute of the last event are
// held back and counted in the next event's suppressed; tokens are tracked
// separately, and a token's state is dropped a minute after its last event
// once nothing is held back.
func TestReporter_OneEventPerTokenPerMinute(t *testing.T) {
	em := &recordingEmitter{}
	now := t0
	r := NewReporter(em, func() time.Time { return now })
	r.Report(refusal("T"))
	r.Report(wsaccess.BoundRefusal{TokenID: "U", BoundSlug: "S", Method: "POST", Route: "/api/v1/user/tokens", Status: 403, Surface: wsaccess.SurfaceAPI})
	evs := em.events()
	if len(evs) != 2 {
		t.Fatalf("%d events, want one per token", len(evs))
	}
	e := evs[0]
	if e.EventType != audit.EventTokenBoundRefused || e.ResourceType != "token" || e.ResourceID != "T" || e.Action != "refuse" || e.Workspace != "S" ||
		e.ActorType != "pat" || e.ActorID != "u" ||
		fmt.Sprint(e.Metadata) != "map[bound_workspace:S method:GET requested_workspace:X route:/api/v1/workspaces/:slug/patches status:404 suppressed:0 surface:api token_id:T]" {
		t.Errorf("event %+v", e)
	}
	if _, has := evs[1].Metadata["requested_workspace"]; has {
		t.Error("a refusal without a requested workspace names one")
	}
	for _, s := range []time.Duration{10 * time.Second, 59 * time.Second} {
		now = t0.Add(s)
		r.Report(refusal("T"))
	}
	if len(em.events()) != 2 {
		t.Fatalf("%d events after two refusals within the minute, want still 2", len(em.events()))
	}
	now = t0.Add(refusalWindow)
	r.Report(refusal("T"))
	evs = em.events()
	if len(evs) != 3 || evs[2].Metadata["suppressed"] != 2 {
		t.Errorf("after the window: %d events, suppressed %v; want 3 and 2", len(evs), evs[2].Metadata["suppressed"])
	}
	// U's entry has nothing held back and is older than a minute: dropped. T's
	// was just renewed.
	if r.entryCount() != 1 {
		t.Errorf("%d entries, want T only", r.entryCount())
	}
	now = t0.Add(refusalWindow + 30*time.Second)
	r.Report(refusal("T")) // held back
	now = t0.Add(3 * refusalWindow)
	r.Report(refusal("V")) // the sweep runs: T holds a count and stays
	if r.entryCount() != 2 {
		t.Errorf("%d entries, want T (with a held-back count) and V", r.entryCount())
	}
	r.Report(refusal("T"))
	if evs = em.events(); len(evs) != 5 || evs[4].Metadata["suppressed"] != 1 {
		t.Errorf("T's late event: %d events, suppressed %v; want 5 and 1", len(evs), evs[4].Metadata["suppressed"])
	}
}

// A reporter without an emitter only counts; an emitter's error is logged
// and swallowed; Install registers the reporter as the refusal observer.
func TestReporter_InstallAndEmitterFailures(t *testing.T) {
	NewReporter(nil, nil).Report(refusal("T"))
	em := &recordingEmitter{err: context.DeadlineExceeded}
	logs := captureLogs(t)
	r := Install(em)
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })
	wsaccess.ReportBoundRefusal(refusal("T"))
	if len(em.events()) != 1 || r.entryCount() != 1 || !strings.Contains(logs.String(), "bound_refused failed") {
		t.Errorf("events %d, entries %d, logs %q", len(em.events()), r.entryCount(), logs.String())
	}
}
