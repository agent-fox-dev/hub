package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/txsvc/apikit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// statusError is an error with a gRPC code and a plain message. status.Code
// finds the code through GRPCStatus, while Error is the message alone, so that
// what gatewayd makes of it, and what is stored on a lease, carries no
// "rpc error: code = ..." prefix.
type statusError struct {
	code codes.Code
	msg  string
}

func (e *statusError) Error() string              { return e.msg }
func (e *statusError) GRPCStatus() *status.Status { return status.New(e.code, e.msg) }

func failedPrecondition(format string, args ...any) error {
	return &statusError{code: codes.FailedPrecondition, msg: fmt.Sprintf(format, args...)}
}

// internalError logs cause, which is never shown to the caller, and returns the
// error the caller gets.
func internalError(what string, cause error) error {
	slog.Error("sandbox gateway: "+what, "error", cause)
	return &statusError{code: codes.Internal, msg: "internal error"}
}

// leaseRepo is what the backend needs of the lease store.
type leaseRepo interface {
	GetLease(id string) (*Lease, error)
	MarkActive(id, sandboxID string) (bool, error)
	FailLease(id, message string) (bool, error)
	DestroyLease(id, from, reason string) (bool, error)
}

// backendConfig is what the wrapping backend is built from.
type backendConfig struct {
	// Service holds the provider and the validated profiles.
	Service *Service
	Store   leaseRepo
	// Audit receives the gateway's events; nil means none.
	Audit audit.Emitter
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Principal reads the principal of a call from its context;
	// gatewayd.PrincipalFromContext when nil. Tests supply their own.
	Principal func(context.Context) (string, bool)
}

// Backend is the sandbox.Provider hub gives gatewayd. It makes a lease usable:
// it creates exactly one box for the lease the call is made under, from what the
// lease records and never from what the caller asked for, and it keeps the
// lease's state and the audit stream in step with the box.
type Backend struct {
	provider  sandbox.Provider
	service   *Service
	store     leaseRepo
	audit     audit.Emitter
	now       func() time.Time
	principal func(context.Context) (string, bool)

	mu sync.Mutex
	// claims are the leases with a Create in flight.
	claims map[string]struct{}
}

func newBackend(cfg backendConfig) *Backend {
	b := &Backend{
		provider:  cfg.Service.Provider(),
		service:   cfg.Service,
		store:     cfg.Store,
		audit:     cfg.Audit,
		now:       cfg.Now,
		principal: cfg.Principal,
		claims:    map[string]struct{}{},
	}
	if b.now == nil {
		b.now = time.Now
	}
	if b.principal == nil {
		b.principal = gatewayd.PrincipalFromContext
	}
	return b
}

// claim reserves the lease for one Create and reports whether it did: a lease
// with a Create in flight is not reserved a second time.
func (b *Backend) claim(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, busy := b.claims[id]; busy {
		return false
	}
	b.claims[id] = struct{}{}
	return true
}

func (b *Backend) release(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.claims, id)
}

// claimed reports whether a Create for the lease is in flight.
func (b *Backend) claimed(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, busy := b.claims[id]
	return busy
}

// hasProfile reports whether the profile is among the validated profiles of
// this run.
func (b *Backend) hasProfile(name string) bool {
	for _, p := range b.service.Profiles() {
		if p.Name == name {
			return true
		}
	}
	return false
}

// Create makes the box of the lease the call is made under. The lease is the
// principal gatewayd authenticated; the caller's spec contributes only its
// working directory and its labels.
//
// A lease that cannot be created from (not leased, expired) is refused with
// FailedPrecondition and left as it is. A lease whose profile is gone, or whose
// spec cannot be built, fails.
func (b *Backend) Create(ctx context.Context, caller sandbox.Spec) (sandbox.Sandbox, error) {
	id, ok := b.principal(ctx)
	if !ok {
		return nil, &statusError{code: codes.Unauthenticated, msg: "the call carries no principal"}
	}
	if !b.claim(id) {
		return nil, failedPrecondition("a Create for lease %s is already in progress", id)
	}
	defer b.release(id)

	lease, err := b.store.GetLease(id)
	switch {
	case errors.Is(err, ErrLeaseNotFound):
		return nil, &statusError{code: codes.NotFound, msg: fmt.Sprintf("lease %s not found", id)}
	case err != nil:
		return nil, internalError("reading a lease failed", err)
	case lease.State != leaseLeased:
		return nil, failedPrecondition("lease %s is %s: only a leased lease can create its box", id, lease.State)
	}

	// The lease's expiry is the box's TTL, in whole seconds. Less than a second
	// is no TTL at all: a zero TTL is an hour to a provider.
	expires, err := apikit.ParseUTC(lease.ExpiresAt)
	if err != nil {
		return nil, internalError("a lease has an expires_at that cannot be read", err)
	}
	remaining := expires.Sub(b.now()).Truncate(time.Second)
	if remaining <= 0 {
		return nil, failedPrecondition("lease %s has expired", id)
	}

	if !b.hasProfile(lease.Profile) {
		return nil, b.failLease(ctx, lease, fmt.Sprintf("profile %q is no longer configured", lease.Profile))
	}
	spec, err := buildSpec(lease, caller, b.provider.Capabilities(), remaining)
	if err != nil {
		return nil, b.failLease(ctx, lease, err.Error())
	}
	return b.provider.Create(ctx, spec)
}

// failLease ends a lease that cannot create its box: leased to failed with the
// message as its error, one failed event, and FailedPrecondition with the same
// message. If the lease is not leased any more, a DELETE got there first, and
// nothing changes.
func (b *Backend) failLease(ctx context.Context, lease *Lease, message string) error {
	changed, err := b.store.FailLease(lease.ID, message)
	switch {
	case err != nil:
		return internalError("failing a lease failed", err)
	case !changed:
		return b.staleLease(lease.ID)
	}
	b.emit(ctx, lease, audit.EventSandboxFailed, "fail", map[string]any{"lease_id": lease.ID, "error": message})
	return failedPrecondition("%s", message)
}

// staleLease is the error for a lease that is no longer in the state the call
// found it in: FailedPrecondition naming the state it is in now.
func (b *Backend) staleLease(id string) error {
	lease, err := b.store.GetLease(id)
	if err != nil {
		return internalError("reading a lease failed", err)
	}
	return failedPrecondition("lease %s is %s", id, lease.State)
}
