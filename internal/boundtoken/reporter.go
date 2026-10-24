package boundtoken

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// refusalWindow is the rate limit of hub.token.bound_refused: at most one
// event per token per window.
const refusalWindow = time.Minute

// Reporter turns binding refusals into hub.token.bound_refused events,
// rate-limited per token. Register it with Install.
type Reporter struct {
	emitter audit.Emitter
	now     func() time.Time

	mu      sync.Mutex
	entries map[string]*limiterEntry
}

// limiterEntry is the limiter's state of one token.
type limiterEntry struct {
	lastEmitted time.Time // when the token's last event was emitted
	heldBack    int       // refusals held back since then
}

// NewReporter returns a reporter that emits through emitter and reads the
// time from clock (time.Now when nil).
func NewReporter(emitter audit.Emitter, clock func() time.Time) *Reporter {
	if clock == nil {
		clock = time.Now
	}
	return &Reporter{emitter: emitter, now: clock, entries: make(map[string]*limiterEntry)}
}

// Install registers a reporter over emitter as wsaccess's process-wide
// refusal observer and returns it.
func Install(emitter audit.Emitter) *Reporter {
	r := NewReporter(emitter, nil)
	wsaccess.SetRefusalObserver(r.Report)
	return r
}

// Report records one refusal. The first refusal of a token, and the first
// one a window after the token's last event, emits an event whose suppressed
// is the number of refusals held back since that last event; the others are
// only counted. Entries whose window has passed and that hold nothing back
// are dropped; an entry with a held-back count stays until its token's next
// refusal records the count (erratum 34, entry 4).
func (r *Reporter) Report(b wsaccess.BoundRefusal) {
	now := r.now()
	r.mu.Lock()
	for token, e := range r.entries {
		if e.heldBack == 0 && now.Sub(e.lastEmitted) >= refusalWindow {
			delete(r.entries, token)
		}
	}
	e := r.entries[b.TokenID]
	if e != nil && now.Sub(e.lastEmitted) < refusalWindow {
		e.heldBack++
		r.mu.Unlock()
		return
	}
	suppressed := 0
	if e != nil {
		suppressed = e.heldBack
	}
	r.entries[b.TokenID] = &limiterEntry{lastEmitted: now}
	r.mu.Unlock()

	if r.emitter == nil {
		return
	}
	metadata := map[string]any{
		"token_id":        b.TokenID,
		"bound_workspace": b.BoundSlug,
		"method":          b.Method,
		"route":           b.Route,
		"status":          b.Status,
		"surface":         b.Surface,
		"suppressed":      suppressed,
	}
	if b.RequestedSlug != "" {
		metadata["requested_workspace"] = b.RequestedSlug
	}
	event := audit.HubEvent{
		EventType:    audit.EventTokenBoundRefused,
		ActorID:      b.UserID,
		ActorType:    "pat",
		ResourceType: "token",
		ResourceID:   b.TokenID,
		Action:       "refuse",
		Workspace:    b.BoundSlug,
		Metadata:     metadata,
	}
	if err := r.emitter.Emit(context.Background(), event); err != nil {
		slog.Error("boundtoken: emitting hub.token.bound_refused failed", "token_id", b.TokenID, "error", err)
	}
}

// entryCount is the number of tokens the limiter holds state for.
func (r *Reporter) entryCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}
