package sandboxes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// leaseRetention is how long a terminal lease stays readable after it ended.
const leaseRetention = 24 * time.Hour

// providerCallTimeout is how long one call to the provider may take in the
// endings and the sweep.
const providerCallTimeout = 30 * time.Second

// SweeperConfig is what the sweeper is built from.
type SweeperConfig struct {
	DB *sql.DB
	// Provider is the sandbox service's provider, whose Reap the sweep calls. It is
	// never the gateway's backend, which would run the sweep from the sweep.
	Provider sandbox.Provider
	// Boxes lists and destroys the boxes hub holds.
	Boxes Boxes
	// Audit receives the expiry events; nil means none.
	Audit audit.Emitter
	// Metrics receives the sandbox metrics; nil records none.
	Metrics *audit.Metrics
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// CallTimeout limits every call to the provider; 30 seconds when zero.
	CallTimeout time.Duration
}

// Sweeper enforces the time limits of leases. One sweep reaps the provider,
// lapses the leases nobody used, destroys the boxes past their lease, prunes the
// leases that ended long ago and recalibrates the gauge of active leases, in that
// order. Every decision reads the sweeper's clock.
type Sweeper struct {
	db       *sql.DB
	store    *LeaseStore
	provider sandbox.Provider
	ender    *ender
	metrics  metricsRecorder
	now      func() time.Time
}

// NewSweeper returns a sweeper built from cfg.
func NewSweeper(cfg SweeperConfig) *Sweeper {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	timeout := cfg.CallTimeout
	if timeout == 0 {
		timeout = providerCallTimeout
	}
	store := NewLeaseStore(cfg.DB, now).WithMetrics(cfg.Metrics)
	return &Sweeper{
		db:       cfg.DB,
		store:    store,
		provider: cfg.Provider,
		ender:    newEnder(store, cfg.Audit, cfg.Boxes, timeout),
		metrics:  metricsRecorder{m: cfg.Metrics},
		now:      now,
	}
}

// sweepStep is one step of a sweep.
type sweepStep struct {
	name string
	run  func(context.Context) error
}

// Sweep runs the steps in order. A step that fails is logged and its error is
// kept, and the steps after it still run; the result joins the errors of all of
// them. It returns nil when every step worked.
func (s *Sweeper) Sweep(ctx context.Context) error {
	var errs []error
	for _, step := range []sweepStep{
		{"reap", s.reap},
		{"lapse", s.lapse},
		{"overdue", s.destroyOverdue},
		{"prune", s.prune},
		{"recalibrate", s.recalibrate},
	} {
		if err := step.run(ctx); err != nil {
			slog.Error("sandbox expiry: a step of the sweep failed", "step", step.name, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", step.name, err))
		}
	}
	return errors.Join(errs...)
}

// reap has the provider remove the boxes left behind past their time. Its error
// text goes through the scrubber.
func (s *Sweeper) reap(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.ender.callTimeout)
	defer cancel()
	ids, err := s.provider.Reap(ctx)
	if len(ids) > 0 {
		slog.Info("sandbox expiry: the provider removed boxes past their time", "sandbox_ids", ids)
	}
	if err != nil {
		return errors.New(scrub(err.Error(), nil))
	}
	return nil
}

// lapse ends every leased lease whose deadline for a Create has passed. A lease
// from before the column has no deadline of its own and lapses at its expires_at.
func (s *Sweeper) lapse(ctx context.Context) error {
	leases, err := s.store.selectLeases(`state = 'leased' AND COALESCE(lease_expires_at, expires_at) <= ?`, apikit.FormatUTC(s.now()))
	if err != nil {
		return err
	}
	return s.endExpired(ctx, leases)
}

// destroyOverdue destroys the box of every active lease that has passed its
// expires_at and ends the lease, whether or not the box could be destroyed.
func (s *Sweeper) destroyOverdue(ctx context.Context) error {
	leases, err := s.store.selectLeases(`state = 'active' AND expires_at <= ?`, apikit.FormatUTC(s.now()))
	if err != nil {
		return err
	}
	return s.endExpired(ctx, leases)
}

// endExpired ends the leases through the expiry path and keeps going when one of
// them cannot be ended.
func (s *Sweeper) endExpired(ctx context.Context, leases []*Lease) error {
	var errs []error
	for _, lease := range leases {
		if _, err := s.ender.endExpired(ctx, lease); err != nil {
			errs = append(errs, fmt.Errorf("lease %s: %w", lease.ID, err))
		}
	}
	return errors.Join(errs...)
}

// prune deletes the terminal leases that ended more than a day ago. A lease that
// ended exactly a day ago stays. Ended means ended_at, or updated_at for a row
// without one. A leased or active lease is never deleted.
func (s *Sweeper) prune(context.Context) error {
	cutoff := apikit.FormatUTC(s.now().Add(-leaseRetention))
	res, err := s.db.Exec(`DELETE FROM sandbox_leases
		WHERE state IN ('destroyed', 'expired', 'failed') AND COALESCE(ended_at, updated_at) < ?`, cutoff)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		slog.Info("sandbox expiry: pruned leases that ended more than a day ago", "leases", n)
	}
	return nil
}

// recalibrate sets afhub_sandboxes_active from the table.
func (s *Sweeper) recalibrate(ctx context.Context) error {
	return s.metrics.recalibrate(ctx, s.db)
}

// selectLeases returns the leases the condition selects, oldest first. where is
// SQL written in this package and args are its parameters.
func (s *LeaseStore) selectLeases(where string, args ...any) ([]*Lease, error) {
	rows, err := s.db.Query(`SELECT `+leaseColumns+` FROM sandbox_leases WHERE `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var leases []*Lease
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		leases = append(leases, l)
	}
	return leases, rows.Err()
}
