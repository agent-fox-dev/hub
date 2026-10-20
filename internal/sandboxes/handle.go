package sandboxes

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/hub/internal/audit"
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
			redacted[key] = redactedMarker
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
// has. The error of a provider that cannot is scrubbed.
func (b *Backend) closeProviderBox(ctx context.Context, h *handle) error {
	if err := h.Sandbox.Close(ctx); err != nil {
		return errors.New(scrub(err.Error(), h.values))
	}
	b.mu.Lock()
	if b.boxes[h.lease.ID] == h {
		delete(b.boxes, h.lease.ID)
	}
	b.mu.Unlock()
	return nil
}

// isEnding reports whether a DELETE is ending the lease.
func (b *Backend) isEnding(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ending := b.ending[id]
	return ending
}

// endBox destroys the box of a lease for a DELETE and records nothing about the
// lease: the DELETE does that, with its own actor and its own reason.
//
// The box is ended through the gateway, the embedder's way, which also frees its
// place in the gateway. A box the gateway does not hold is either gone or one
// hub still holds the handle of: the gateway forgets a box whose destruction
// failed, and it does not hold one whose Create has not returned yet. Hub closes
// the handle it holds, and when it holds none the box is gone and there is
// nothing to destroy.
func (b *Backend) endBox(ctx context.Context, server *gatewayd.Server, lease *Lease) error {
	b.mu.Lock()
	b.ending[lease.ID] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.ending, lease.ID)
		b.mu.Unlock()
	}()

	err := server.Destroy(ctx, *lease.SandboxID)
	if !errors.Is(err, gatewayd.ErrNotHeld) {
		return err
	}
	b.mu.Lock()
	h := b.boxes[lease.ID]
	b.mu.Unlock()
	if h == nil {
		return nil
	}
	return b.closeProviderBox(ctx, h)
}

// expiryMargin is how close to a lease's expires_at a Close is taken to be the end
// of the box's own time and not the client's doing. The box's TTL is the time the
// lease had left cut down to whole seconds, so gatewayd, which closes a box when
// its TTL ends, closes it up to a second before expires_at.
const expiryMargin = 2 * time.Second

// endByClient moves the lease from active to destroyed with the reason client
// and emits one destroy event, the first time it is called for the lease. A lease
// that has ended already is not touched.
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
	changed, err := b.store.DestroyLease(h.lease.ID, leaseActive, leaseEndReasonClient)
	switch {
	case err != nil:
		slog.Error("sandbox gateway: ending a lease failed", "lease_id", h.lease.ID, "error", err)
	case changed:
		b.emit(ctx, h.lease, audit.EventSandboxDestroy, "destroy", map[string]any{
			"lease_id":   h.lease.ID,
			"sandbox_id": h.ID(),
			"reason":     leaseEndReasonClient,
		})
	}
}
