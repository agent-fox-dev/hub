package sandboxes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

// sweeping runs a sweep.
type sweeping interface {
	Sweep(ctx context.Context) error
}

// leaseRepo is what the backend needs of the lease store.
type leaseRepo interface {
	GetLease(id string) (*Lease, error)
	MarkActive(id, sandboxID string) (bool, error)
	FailLease(id, message string) (bool, error)
	DestroyLease(id, from, reason string) (bool, error)
	ExpireLease(id, reason string) (bool, error)
}

// backendConfig is what the wrapping backend is built from.
type backendConfig struct {
	// Service holds the provider and the validated profiles.
	Service *Service
	Store   leaseRepo
	// Secrets reads secret values, and DB holds the workspace tables the scope
	// chain is read from.
	Secrets SecretReader
	DB      *sql.DB
	// Audit receives the gateway's events; nil means none.
	Audit audit.Emitter
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Principal reads the principal of a call from its context;
	// gatewayd.PrincipalFromContext when nil. Tests supply their own.
	Principal func(context.Context) (string, bool)
	// Metrics receives the sandbox metrics; nil records none.
	Metrics *audit.Metrics
	// Sweeper runs the sweep when gatewayd reaps.
	Sweeper sweeping
}

// Backend is the sandbox.Provider hub gives gatewayd. It makes a lease usable:
// it creates exactly one box for the lease the call is made under, from what the
// lease records and never from what the caller asked for, and it keeps the
// lease's state and the audit stream in step with the box.
type Backend struct {
	provider  sandbox.Provider
	service   *Service
	store     leaseRepo
	secrets   SecretReader
	db        *sql.DB
	audit     audit.Emitter
	now       func() time.Time
	principal func(context.Context) (string, bool)
	// ender ends a lease whose deadline for a Create has passed. It destroys no
	// box: a leased lease has none.
	ender   *ender
	metrics metricsRecorder
	// sweeper runs the sweep when gatewayd reaps.
	sweeper sweeping

	mu sync.Mutex
	// claims are the leases with a Create in flight.
	claims map[string]struct{}
	// boxes are the boxes made and not yet closed, by lease id. gatewayd holds
	// them too, but forgets a box whose Close failed; hub keeps the handle, so
	// that a DELETE can try again.
	boxes map[string]*handle
	// ending are the leases a DELETE is ending. A Close of their boxes only
	// closes: the DELETE records the end.
	ending map[string]struct{}
}

func newBackend(cfg backendConfig) *Backend {
	b := &Backend{
		provider:  cfg.Service.Provider(),
		service:   cfg.Service,
		store:     cfg.Store,
		secrets:   cfg.Secrets,
		db:        cfg.DB,
		audit:     cfg.Audit,
		now:       cfg.Now,
		principal: cfg.Principal,
		ender:     newEnder(cfg.Store, cfg.Audit, nil, providerCallTimeout),
		metrics:   metricsRecorder{m: cfg.Metrics},
		sweeper:   cfg.Sweeper,
		claims:    map[string]struct{}{},
		boxes:     map[string]*handle{},
		ending:    map[string]struct{}{},
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

// discardTimeout bounds the destruction of a box that was created for nothing.
const discardTimeout = 30 * time.Second

// Create makes the box of the lease the call is made under. The lease is the
// principal gatewayd authenticated; the caller's spec contributes only its
// working directory and its labels.
//
// A lease that cannot be created from (not leased) is refused with
// FailedPrecondition and left as it is. A leased lease whose deadline for a Create
// has passed is refused the same way and ended as expired in the same call, with
// no provider call. A lease whose profile is gone, whose spec cannot be built or
// whose required secrets exist nowhere fails. A store error leaves the lease
// leased, so the caller can try again. If the provider fails, the lease fails with
// the provider's message, scrubbed.
//
// Every Create that gets as far as making a box is counted once, as created when
// the lease becomes active and as failed when it does not. A refusal before that
// is not counted.
func (b *Backend) Create(ctx context.Context, caller sandbox.Spec) (sandbox.Sandbox, error) {
	id, ok := b.principal(ctx)
	if !ok {
		return nil, &statusError{code: codes.Unauthenticated, msg: "the call carries no principal"}
	}
	if !b.claim(id) {
		return nil, failedPrecondition("a Create for lease %s is already in progress", id)
	}
	defer b.release(id)

	lease, remaining, err := b.creatable(ctx, id)
	if err != nil {
		return nil, err
	}
	box, err := b.makeBox(ctx, lease, caller, remaining)
	if err != nil {
		b.metrics.createResult(createFailed)
		return nil, err
	}
	b.metrics.createResult(createCreated)
	return box, nil
}

// makeBox makes the box of a lease that can be created from.
func (b *Backend) makeBox(ctx context.Context, lease *Lease, caller sandbox.Spec, remaining time.Duration) (sandbox.Sandbox, error) {
	if !b.hasProfile(lease.Profile) {
		msg := fmt.Sprintf("profile %q is no longer configured", lease.Profile)
		return nil, b.failLease(ctx, lease, msg, failedPrecondition("%s", msg))
	}
	spec, err := buildSpec(lease, caller, b.provider.Capabilities(), remaining)
	if err != nil {
		return nil, b.failLease(ctx, lease, err.Error(), failedPrecondition("%s", err.Error()))
	}
	values, err := b.addSecrets(ctx, lease, &spec)
	if err != nil {
		return nil, err
	}
	return b.createBox(ctx, lease, spec, values)
}

// creatable loads the lease and returns it with the time it has left, in whole
// seconds, when a box can be made for it: it is leased and its time is not up.
// The time left is the TTL of the box. Less than a second is no TTL at all, since
// a zero TTL is an hour to a provider, so it counts as expired. A lease past its
// deadline for a Create (lease_expires_at, or expires_at for a lease from before
// that column) is ended as expired before the refusal is returned.
func (b *Backend) creatable(ctx context.Context, id string) (*Lease, time.Duration, error) {
	lease, err := b.store.GetLease(id)
	switch {
	case errors.Is(err, ErrLeaseNotFound):
		return nil, 0, &statusError{code: codes.NotFound, msg: fmt.Sprintf("lease %s not found", id)}
	case err != nil:
		return nil, 0, internalError("reading a lease failed", err)
	case lease.State != leaseLeased:
		return nil, 0, failedPrecondition("lease %s is %s: only a leased lease can create its box", id, lease.State)
	}
	expires, err := apikit.ParseUTC(lease.ExpiresAt)
	if err != nil {
		return nil, 0, internalError("a lease has an expires_at that cannot be read", err)
	}
	deadline := expires
	if lease.LeaseExpiresAt != nil {
		if deadline, err = apikit.ParseUTC(*lease.LeaseExpiresAt); err != nil {
			return nil, 0, internalError("a lease has a lease_expires_at that cannot be read", err)
		}
	}
	now := b.now()
	remaining := expires.Sub(now).Truncate(time.Second)
	if !deadline.After(now) || remaining <= 0 {
		return nil, 0, b.expireLate(ctx, lease)
	}
	return lease, remaining, nil
}

// expireLate ends a leased lease whose time is up, through the expiry path, and
// returns the refusal of the Create. If another path has ended the lease first,
// nothing is changed and the refusal names the state it is in.
func (b *Backend) expireLate(ctx context.Context, lease *Lease) error {
	changed, err := b.ender.endExpired(ctx, lease)
	switch {
	case err != nil:
		return internalError("expiring a lease failed", err)
	case !changed:
		return b.staleLease(lease.ID)
	}
	return failedPrecondition("lease %s has expired", lease.ID)
}

// addSecrets reads the secrets of the lease and puts them in the environment of
// spec, next to the fixed environment. This is when the values are read, not
// when the lease was issued, so a value rotated in between is the one injected.
// The values live in memory for the length of the call and nowhere else.
//
// It returns every value that must stay out of any text derived from the
// provider's errors: those of the secrets and those of the fixed environment.
func (b *Backend) addSecrets(ctx context.Context, lease *Lease, spec *sandbox.Spec) ([]string, error) {
	resolved, err := resolveSecrets(b.secrets, b.db, lease.Workspace, lease.Secrets, lease.OptionalSecrets)
	var missing *secretMissingError
	switch {
	case errors.As(err, &missing):
		return nil, b.failLease(ctx, lease, missing.Error(), failedPrecondition("%s", missing.Error()))
	case err != nil:
		return nil, internalError("resolving the secrets of a lease failed", err)
	}
	values := make([]string, 0, len(resolved)+len(lease.Env))
	for name, value := range resolved {
		spec.Env[name] = value
		values = append(values, value)
	}
	for _, value := range lease.Env {
		values = append(values, value)
	}
	return values, nil
}

// createBox asks the provider for the box and records the outcome on the lease.
//
// If the provider fails, the lease fails with the scrubbed message, one failed
// event is emitted and the caller gets the scrubbed error. If it succeeds, the
// lease goes from leased to active with the id of the box and one create event
// is emitted. If the lease is not leased by then, a DELETE ended it while the
// provider worked: the new box is destroyed at once, no create event is emitted
// and the call fails.
func (b *Backend) createBox(ctx context.Context, lease *Lease, spec sandbox.Spec, values []string) (sandbox.Sandbox, error) {
	box, err := b.provider.Create(ctx, spec)
	if err != nil {
		msg := scrub(err.Error(), values)
		return nil, b.failLease(ctx, lease, msg, errors.New(msg))
	}
	// The handle is held before the lease reads active: from then on a DELETE finds
	// the lease active, asks the gateway for a box it has not recorded yet, and
	// must be able to close the box through the handle instead.
	h := newHandle(b, lease, box, values)
	b.remember(h)
	changed, err := b.store.MarkActive(lease.ID, box.ID())
	switch {
	case err != nil:
		b.forget(h)
		b.discard(lease, box, "the box of a lease could not be recorded; destroyed it", values)
		return nil, internalError("recording the box of a lease failed", err)
	case !changed:
		b.forget(h)
		b.discard(lease, box, "a box was created for a lease that had ended; destroyed it", values)
		return nil, b.staleLease(lease.ID)
	}
	b.emit(ctx, lease, audit.EventSandboxCreate, "create", map[string]any{
		"lease_id":   lease.ID,
		"sandbox_id": box.ID(),
		"image":      lease.Image,
		"network":    lease.Network,
	})
	return h, nil
}

// remember holds the handle of a box until it is closed.
func (b *Backend) remember(h *handle) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.boxes[h.lease.ID] = h
}

// forget lets go of a handle whose box nobody will use.
func (b *Backend) forget(h *handle) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.boxes[h.lease.ID] == h {
		delete(b.boxes, h.lease.ID)
	}
}

// discard destroys a box nobody will use, whatever has become of the call that
// made it, and logs a warning with the lease id and the box id.
func (b *Backend) discard(lease *Lease, box sandbox.Sandbox, why string, values []string) {
	ctx, cancel := context.WithTimeout(context.Background(), discardTimeout)
	defer cancel()
	slog.Warn("sandbox gateway: "+why, "lease_id", lease.ID, "sandbox_id", box.ID())
	if err := box.Close(ctx); err != nil {
		slog.Error("sandbox gateway: destroying a box failed", "lease_id", lease.ID, "sandbox_id", box.ID(), "error", scrub(err.Error(), values))
	}
}

// failLease ends a lease that cannot create its box: leased to failed with the
// message as its error, and one failed event. It returns result, the error the
// caller gets. If the lease is not leased any more, a DELETE got there first,
// nothing changes and the caller gets the state the lease is in.
func (b *Backend) failLease(ctx context.Context, lease *Lease, message string, result error) error {
	changed, err := b.store.FailLease(lease.ID, message)
	switch {
	case err != nil:
		return internalError("failing a lease failed", err)
	case !changed:
		return b.staleLease(lease.ID)
	}
	b.emit(ctx, lease, audit.EventSandboxFailed, "fail", map[string]any{"lease_id": lease.ID, "error": message})
	return result
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

// Capabilities is the provider's, with the network modes cut down to those
// network_modes allows, in the provider's order. The provider's own capabilities
// are not changed.
func (b *Backend) Capabilities() sandbox.Capabilities {
	caps := b.provider.Capabilities()
	allowed := b.service.NetworkModes()
	caps.Network = slices.DeleteFunc(slices.Clone(caps.Network), func(mode sandbox.NetworkMode) bool {
		return !slices.Contains(allowed, mode.String())
	})
	return caps
}

// Reap runs the sweep. gatewayd calls it when it starts serving and at an interval,
// with no principal, and the sweep begins with the provider's own Reap, so the
// provider's work is done and the leases' time limits are enforced by the same
// call. The sweep logs its failures, scrubbed, and Reap returns nothing.
//
// gatewayd also answers a Reap call that a client makes with its token, with the
// lease as the principal. That is not a lease holder's to ask: it would let it
// start sweeps across the whole hub, and hand it the ids of other leases' boxes
// and the provider's error text. A call with a principal does nothing.
func (b *Backend) Reap(ctx context.Context) ([]string, error) {
	if _, ok := b.principal(ctx); ok {
		return nil, nil
	}
	// The sweep logs what fails in it; the caller gets nothing to log twice.
	_ = b.sweeper.Sweep(ctx)
	return nil, nil
}

var _ sandbox.Provider = (*Backend)(nil)
