package sandboxes

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// callLog records the calls of the doubles in the order they were made. A nil
// log records nothing.
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

// fakeBoxes is the Boxes of the expiry tests: a set of boxes that exist, and a
// record of what was destroyed.
type fakeBoxes struct {
	mu    sync.Mutex
	boxes []Box
	// listErr and destroyErr are what List and Destroy return; a Destroy that
	// fails leaves the box where it is.
	listErr    error
	destroyErr error
	// failNext, when set, is what the next Destroy returns, once; the box stays.
	failNext error
	// destroyed lists the ids passed to Destroy, in call order, failed calls
	// included.
	destroyed []string
	// listHang makes List, and destroyHang the Destroy of those box ids, wait for
	// their context to end.
	listHang    bool
	destroyHang map[string]bool
	// calls, when set, records every List and Destroy call; before is called
	// first by each of them.
	calls  *callLog
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

// clearLabels makes the listing show no labels.
func (f *fakeBoxes) clearLabels() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.boxes {
		f.boxes[i].Labels = nil
	}
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

var _ Boxes = (*fakeBoxes)(nil)

// metricSample returns the value of the series of a gauge or a counter that has
// exactly these labels, and whether the series exists.
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

// activeGauge is afhub_sandboxes_active for a workspace; 0 when it has no series.
func activeGauge(t *testing.T, m *audit.Metrics, workspace string) float64 {
	t.Helper()
	v, _ := metricSample(t, m, "afhub_sandboxes_active", map[string]string{"workspace": workspace})
	return v
}

// createsCounter is afhub_sandbox_creates_total for a result.
func createsCounter(t *testing.T, m *audit.Metrics, result string) float64 {
	t.Helper()
	v, _ := metricSample(t, m, "afhub_sandbox_creates_total", map[string]string{"result": result})
	return v
}

// leaseSeconds returns the sample count and the sum of afhub_sandbox_lease_seconds.
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

// eventsOf returns the recorded events of one type.
func eventsOf(r *recordingEmitter, eventType string) []audit.HubEvent {
	var out []audit.HubEvent
	for _, e := range r.recorded() {
		if e.EventType == eventType {
			out = append(out, e)
		}
	}
	return out
}

var errTestDestroy = errors.New("the engine could not destroy the box")
