package sandboxes

import (
	"context"
	"log/slog"
	"maps"
	"sync/atomic"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/scrub"
)

// leaseEndReasonClient is the end_reason of a lease whose holder closed the box
// through the gateway.
const leaseEndReasonClient = "client"

// handle is the box of a lease as gatewayd holds it. Everything a box does is the
// provider's box's, which the handle embeds; the handle differs from it in two
// things only.
//
// Spec reports the environment with every value redacted: it travels back to the
// caller in the answer to Create, and no response may carry a secret. Close is
// the client destroy path: gatewayd calls it when the holder destroys the box,
// and it ends the lease.
//
// The handle keeps the values it must keep out of an error, the secrets and the
// fixed environment of its box, for as long as the box lives, to scrub the
// errors of Close. They are in the box's environment all that time; hub never
// writes them anywhere.
type handle struct {
	sandbox.Sandbox
	backend *Backend
	lease   *Lease
	values  []string
	// closeFailed is set when closing the provider's box has failed, and tells
	// the next try to destroy the box another way (see destroyProviderBox).
	closeFailed atomic.Bool
}

func newHandle(b *Backend, lease *Lease, box sandbox.Sandbox, values []string) *handle {
	return &handle{Sandbox: box, backend: b, lease: lease, values: values}
}

// Unwrap lets gatewayd reach the agent of the provider's box, to relay the
// calls of the lease holder to it. Without it a box cannot be relayed.
func (h *handle) Unwrap() sandbox.Sandbox { return h.Sandbox }

// Spec is the provider's effective spec with every environment value replaced by
// [redacted], the keys kept. The provider's own spec is not changed.
func (h *handle) Spec() sandbox.Spec {
	spec := h.Sandbox.Spec()
	if spec.Env != nil {
		redacted := make(map[string]string, len(spec.Env))
		for key := range spec.Env {
			redacted[key] = scrub.Redacted
		}
		spec.Env = redacted
	}
	return spec
}

// Close destroys the box and, if the lease holder gave it back in time, ends the
// lease.
func (h *handle) Close(ctx context.Context) error { return h.backend.closeBox(ctx, h) }

// closeBox destroys the provider's box. If the provider cannot, it returns the
// error with every secret and environment value removed, and the lease stays
// active. If it can, the lease is ended as the client's doing, unless a DELETE is
// ending it: that DELETE records the end itself.
func (b *Backend) closeBox(ctx context.Context, h *handle) error {
	if err := b.closeProviderBox(ctx, h); err != nil {
		return err
	}
	if !b.isEnding(h.lease.ID) {
		b.endByClient(ctx, h)
	}
	return nil
}

// closeProviderBox destroys the provider's box and forgets the handle once it
// has. A box the provider says is gone counts as destroyed. The error of a
// provider that cannot is scrubbed, and is Unavailable when the remote gateway
// refused hub's token (see Service.remoteFailure).
func (b *Backend) closeProviderBox(ctx context.Context, h *handle) error {
	if err := b.destroyProviderBox(ctx, h); err != nil && !isNotFound(err) {
		return b.service.remoteFailure("destroy", err, scrub.Text(err.Error(), h.values...))
	}
	b.mu.Lock()
	if b.boxes[h.lease.ID] == h {
		delete(b.boxes, h.lease.ID)
	}
	b.mu.Unlock()
	return nil
}

// destroyProviderBox destroys the provider's box. A sandbox AgentKit attached
// tears down once: its Close answers nil from then on, whatever became of the
// box, so a Close tried again after a failure would report a box destroyed that is
// still there. The first try is therefore the Close of the box, and after a failure
// the remote gateway's box is destroyed by id through the client, which can be
// repeated, and which the remote answers OK for a box it no longer has. A box of
// another provider is closed again, as it always was.
func (b *Backend) destroyProviderBox(ctx context.Context, h *handle) error {
	if h.closeFailed.Load() && b.service.isRemote() {
		return b.service.remote.Destroy(ctx, h.ID())
	}
	err := h.Sandbox.Close(ctx)
	if err != nil && !isNotFound(err) {
		h.closeFailed.Store(true)
	}
	return err
}

// isEnding reports whether a caller that records the end itself is destroying the
// box of the lease.
func (b *Backend) isEnding(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ending[id] > 0
}

// beginEnding marks the lease as being ended by a caller that records the end
// itself, and returns the function that takes the mark off. Marks are counted: two
// callers destroying the box of one lease at the same time (a DELETE and a sweep)
// leave it marked until both are done.
func (b *Backend) beginEnding(id string) (done func()) {
	b.mu.Lock()
	b.ending[id]++
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.ending[id]--; b.ending[id] <= 0 {
			delete(b.ending, id)
		}
	}
}

// endBox destroys the box of a lease for a DELETE and records nothing about the
// lease: the DELETE does that, with its own actor and its own reason.
func (b *Backend) endBox(ctx context.Context, server *gatewayd.Server, lease *Lease) error {
	return b.destroyBox(ctx, server, *lease.SandboxID, lease.ID)
}

// destroyBox destroys the box with this id, which belongs to the lease with this
// id, for a caller that records the end of the lease itself, and has the Close of
// the box record nothing.
//
// The box is ended through the gateway, the embedder's way, which also frees its
// place in the gateway. A box the gateway does not hold is either gone or one
// hub still holds the handle of: the gateway forgets a box whose destruction
// failed, and it does not hold one whose Create has not returned yet. Hub closes
// the handle it holds, and when it holds none the box is gone and there is
// nothing to destroy.
func (b *Backend) destroyBox(ctx context.Context, server *gatewayd.Server, boxID, leaseID string) error {
	if leaseID != "" {
		defer b.beginEnding(leaseID)()
	}

	err := server.Destroy(ctx, boxID)
	if !isNotFound(err) {
		return err
	}
	b.mu.Lock()
	h := b.boxes[leaseID]
	b.mu.Unlock()
	if h == nil {
		return nil
	}
	return b.closeProviderBox(ctx, h)
}

// heldBoxes lists the boxes hub holds a handle of: those made and not yet closed.
func (b *Backend) heldBoxes() []Box {
	b.mu.Lock()
	defer b.mu.Unlock()
	boxes := make([]Box, 0, len(b.boxes))
	for _, h := range b.boxes {
		boxes = append(boxes, Box{ID: h.ID(), Labels: maps.Clone(h.Spec().Labels)})
	}
	return boxes
}

// leaseOfBox returns the id of the lease whose box has this id, and "" when hub
// holds no such box.
func (b *Backend) leaseOfBox(boxID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for leaseID, h := range b.boxes {
		if h.ID() == boxID {
			return leaseID
		}
	}
	return ""
}

// expiryMargin is how close to a lease's expires_at a Close is taken to be the end
// of the box's own time and not the client's doing. The box's TTL is the time the
// lease had left cut down to whole seconds, so gatewayd, which closes a box when
// its TTL ends, closes it up to a second before expires_at.
const expiryMargin = 2 * time.Second

// endByClient moves the lease from active to destroyed with the reason client
// (shutdown, while hub is shutting down) and emits one destroy event, the first
// time it is called for the lease. A lease that has ended already is not touched.
//
// gatewayd also closes a box when its TTL passes, and a lease that has run out is
// not the client's doing: marking it is for the sweep that owns expiry, so a Close
// in the last moments of the lease's time, or after it, ends nothing.
func (b *Backend) endByClient(ctx context.Context, h *handle) {
	expires, err := apikit.ParseUTC(h.lease.ExpiresAt)
	if err != nil {
		slog.Error("sandbox gateway: a lease has an expires_at that cannot be read", "lease_id", h.lease.ID, "error", err)
		return
	}
	if !expires.After(b.now().Add(expiryMargin)) {
		return
	}
	reason := b.service.endReason(leaseEndReasonClient)
	changed, err := b.store.DestroyLease(h.lease.ID, leaseActive, reason)
	switch {
	case err != nil:
		slog.Error("sandbox gateway: ending a lease failed", "lease_id", h.lease.ID, "error", err)
	case changed:
		b.emit(ctx, h.lease, audit.EventSandboxDestroy, "destroy", map[string]any{
			"lease_id":   h.lease.ID,
			"sandbox_id": h.ID(),
			"reason":     reason,
		})
	}
}
