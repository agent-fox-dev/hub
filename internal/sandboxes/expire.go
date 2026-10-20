package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// The end reasons of the endings time, shutdown and restart cause.
const (
	leaseEndReasonExpired  = "expired"
	leaseEndReasonShutdown = "shutdown"
)

// Box is a box as a listing shows it.
type Box struct {
	ID     string
	Labels map[string]string
}

// Boxes is what the endings and the sweep need of the boxes that exist.
//
// AgentKit's providers have no listing and no destroy by id, so this is hub's
// own view of the boxes it made: the gateway implements it (see Gateway.List and
// Gateway.Destroy). Destroy treats a box that is gone as destroyed.
type Boxes interface {
	List(ctx context.Context) ([]Box, error)
	Destroy(ctx context.Context, id string) error
}

// ender ends leases for the reasons that are no caller's doing: time, shutdown
// and restart. Each ending destroys the box, if there is one, ends the lease in
// the store, whose conditional update decides whether this ending is the one, and
// only then emits its event. An ending that lost the update, because another
// path got there first, emits nothing and the store records no metric for it.
type ender struct {
	store       leaseEnder
	audit       audit.Emitter
	boxes       Boxes
	callTimeout time.Duration
	// extra are values besides a lease's environment that stay out of the log:
	// those of Service.scrubValues.
	extra []string
}

// leaseEnder is what the endings need of the lease store.
type leaseEnder interface {
	ExpireLease(id, reason string) (bool, error)
	DestroyLease(id, from, reason string) (bool, error)
}

func newEnder(store leaseEnder, emitter audit.Emitter, boxes Boxes, callTimeout time.Duration, extra ...string) *ender {
	return &ender{store: store, audit: emitter, boxes: boxes, callTimeout: callTimeout, extra: extra}
}

// endExpired is the expiry path. A leased lease, which has no box, becomes
// expired; an active lease has its box destroyed and becomes destroyed. Either
// way the end reason is expired, the token is revoked and one hub.sandbox.expire
// event is emitted, but no hub.sandbox.destroy.
//
// A box that cannot be destroyed does not keep the lease: revoking access must not
// depend on a provider call, and the failure is logged. A context that has ended
// does: hub is stopping, the box may be there still, and the lease stays active for
// the next start to recover. lease is the state the caller read, and the ending is
// conditioned on it, so a lease that has moved on is left alone and the result is
// (false, nil).
func (e *ender) endExpired(ctx context.Context, lease *Lease) (bool, error) {
	switch lease.State {
	case leaseLeased:
		changed, err := e.store.ExpireLease(lease.ID, leaseEndReasonExpired)
		if err != nil || !changed {
			return false, err
		}
		e.emitExpired(ctx, lease, leaseExpired)
	case leaseActive:
		e.destroyBox(ctx, lease)
		if err := ctx.Err(); err != nil {
			return false, err
		}
		changed, err := e.store.DestroyLease(lease.ID, leaseActive, leaseEndReasonExpired)
		if err != nil || !changed {
			return false, err
		}
		e.emitExpired(ctx, lease, leaseDestroyed)
	default:
		return false, fmt.Errorf("sandbox lease %s is %s: only a live lease ends by time", lease.ID, lease.State)
	}
	return true, nil
}

// endDestroyed ends an active lease as destroyed with the given end reason,
// shutdown or restart, after destroying its box, and emits one
// hub.sandbox.destroy event by the system. As with endExpired, a box that cannot
// be destroyed does not keep the lease, an ended context does, and a lease that has
// moved on is left alone.
func (e *ender) endDestroyed(ctx context.Context, lease *Lease, reason string) (bool, error) {
	e.destroyBox(ctx, lease)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	changed, err := e.store.DestroyLease(lease.ID, leaseActive, reason)
	if err != nil || !changed {
		return false, err
	}
	emitEvent(ctx, e.audit, audit.HubEvent{
		EventType:    audit.EventSandboxDestroy,
		ResourceType: "sandbox",
		ResourceID:   lease.ID,
		Action:       "destroy",
		Workspace:    lease.Workspace,
		ActorType:    "system",
		Metadata: map[string]any{
			"lease_id":   lease.ID,
			"sandbox_id": sandboxIDOf(lease),
			"reason":     reason,
		},
	})
	return true, nil
}

// destroyBox destroys the box of an active lease within the call limit. The
// failure of the provider is logged with the lease, the box and the error text
// with the secrets of the lease's environment and URL userinfo removed, and is
// not returned.
func (e *ender) destroyBox(ctx context.Context, lease *Lease) {
	if lease.SandboxID == nil || e.boxes == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, e.callTimeout)
	defer cancel()
	err := e.boxes.Destroy(ctx, *lease.SandboxID)
	if err == nil {
		return
	}
	values := slices.Clone(e.extra)
	for _, v := range lease.Env {
		values = append(values, v)
	}
	slog.Error("sandbox expiry: destroying a box failed; the lease is ended all the same",
		"lease_id", lease.ID, "sandbox_id", *lease.SandboxID, "error", callFailure("destroy", e.callTimeout, err, values))
}

// callFailure is the text of a failed call to the provider, safe to log: a call
// that ran into its limit says so, and any other error has the given values and
// the user information of URLs removed.
func callFailure(call string, limit time.Duration, err error, values []string) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("%s timed out after %s", call, limit)
	}
	return scrub(err.Error(), values)
}

// emitExpired records that time ended the lease, into state. The box is named
// only when the lease had one, which only a lease that ends destroyed has.
func (e *ender) emitExpired(ctx context.Context, lease *Lease, state string) {
	metadata := map[string]any{"lease_id": lease.ID, "state": state}
	if lease.SandboxID != nil {
		metadata["sandbox_id"] = *lease.SandboxID
	}
	emitEvent(ctx, e.audit, audit.HubEvent{
		EventType:    audit.EventSandboxExpire,
		ResourceType: "sandbox",
		ResourceID:   lease.ID,
		Action:       "expire",
		Workspace:    lease.Workspace,
		ActorType:    "system",
		Metadata:     metadata,
	})
}

// sandboxIDOf is the id of the lease's box, or nil for a lease without one.
func sandboxIDOf(lease *Lease) any {
	if lease.SandboxID == nil {
		return nil
	}
	return *lease.SandboxID
}
