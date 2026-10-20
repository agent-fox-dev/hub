package sandboxes

import (
	"context"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// providerError is the failure of the provider to destroy a box: the message is
// scrubbed, and the handler answers it as 502 sandbox_provider_error.
type providerError struct{ msg string }

func (e *providerError) Error() string { return e.msg }

// destroyLease ends a lease for DELETE, together with its box.
//
// A leased lease has no box: it is moved to destroyed, and a Create still in
// flight finds the lease ended when its box arrives and destroys it. An active
// lease has its box destroyed first, under a deadline, and only when that worked,
// or the box was gone already, is it moved to destroyed. A provider that fails
// changes nothing and returns a *providerError, so the DELETE can be repeated.
// A lease that has ended already is left alone.
//
// Every move is conditioned on the state the lease was read in. If it finds the
// lease in another state, which happens when a Create finished or the holder
// closed its box in between, the lease is read again once and the new state is
// acted on: a box that appeared is destroyed first, a lease that ended has
// nothing left to do. The DELETE that moves the lease into destroyed emits the
// one destroy event, with the actor of its HTTP credential.
func (s *LeaseService) destroyLease(ctx context.Context, auth *apikit.AuthInfo, lease *Lease) error {
	for pass := range 2 {
		if pass == 0 && s.afterRead != nil {
			s.afterRead()
		}
		switch lease.State {
		case leaseLeased:
		case leaseActive:
			if err := s.destroyBox(ctx, lease); err != nil {
				return err
			}
		default:
			return nil
		}
		changed, err := s.store.DestroyLease(lease.ID, lease.State, leaseEndReasonAPI)
		if err != nil {
			return err
		}
		if changed {
			s.emitDestroyed(ctx, auth, lease)
			return nil
		}
		if lease, err = s.store.GetLease(lease.ID); err != nil {
			return err
		}
	}
	return nil
}

// destroyBox destroys the box of an active lease within destroyTimeout. A lease
// without a gateway has no box to destroy.
func (s *LeaseService) destroyBox(ctx context.Context, lease *Lease) error {
	if s.boxes == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, destroyTimeout)
	defer cancel()
	if err := s.boxes.DestroyBox(ctx, lease); err != nil {
		values := make([]string, 0, len(lease.Env))
		for _, v := range lease.Env {
			values = append(values, v)
		}
		return &providerError{msg: scrub(err.Error(), values)}
	}
	return nil
}

// emitDestroyed records that DELETE ended the lease, with the box it destroyed or
// null when the lease had none.
func (s *LeaseService) emitDestroyed(ctx context.Context, auth *apikit.AuthInfo, lease *Lease) {
	var box any
	if lease.SandboxID != nil {
		box = *lease.SandboxID
	}
	emitEvent(ctx, s.audit, audit.HubEvent{
		EventType:    audit.EventSandboxDestroy,
		ResourceType: "sandbox",
		ResourceID:   lease.ID,
		Action:       "destroy",
		Workspace:    lease.Workspace,
		ActorID:      auth.UserID,
		ActorType:    auth.CredentialType,
		Metadata: map[string]any{
			"lease_id":   lease.ID,
			"sandbox_id": box,
			"reason":     leaseEndReasonAPI,
		},
	})
}
