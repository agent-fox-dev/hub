package sandboxes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
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
	// Interval is how often the reaper sweeps (reap_interval); ten minutes when
	// zero.
	Interval time.Duration
	// Tickers makes the ticks of the reaper; a time.Ticker when nil.
	Tickers tickerFactory
	// Service is the sandbox service, whose shutdown mode the sweeper reads and
	// sets; nil means the sweeper is never in shutdown mode.
	Service *Service
}

// Sweeper enforces the time limits of leases. One sweep reaps the provider,
// lapses the leases nobody used, destroys the boxes past their lease, destroys
// the boxes that belong to leases that have ended, prunes the leases that ended
// long ago and recalibrates the gauge of active leases, in that order. Every
// decision reads the sweeper's clock. Sweeps never overlap, and no call to the
// provider takes longer than the call timeout.
type Sweeper struct {
	db          *sql.DB
	store       *LeaseStore
	provider    sandbox.Provider
	boxes       Boxes
	ender       *ender
	metrics     metricsRecorder
	now         func() time.Time
	callTimeout time.Duration
	// running holds one value while a sweep runs.
	running chan struct{}
	// interval and tickers drive the reaper; reaper is the one running.
	interval time.Duration
	tickers  tickerFactory
	reaperMu sync.Mutex
	reaper   *reaperLoop
	// service holds the shutdown mode; shuttingDown is set by the first Shutdown.
	service      *Service
	shuttingDown atomic.Bool
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
	interval := cfg.Interval
	if interval == 0 {
		interval = defaultReapInterval
	}
	tickers := cfg.Tickers
	if tickers == nil {
		tickers = timeTicker
	}
	store := NewLeaseStore(cfg.DB, now).WithMetrics(cfg.Metrics)
	return &Sweeper{
		db:          cfg.DB,
		store:       store,
		provider:    cfg.Provider,
		boxes:       cfg.Boxes,
		ender:       newEnder(store, cfg.Audit, cfg.Boxes, timeout, cfg.Service.scrubValues()...),
		metrics:     metricsRecorder{m: cfg.Metrics},
		now:         now,
		callTimeout: timeout,
		running:     make(chan struct{}, 1),
		interval:    interval,
		tickers:     tickers,
		service:     cfg.Service,
	}
}

// sweepStep is one step of a sweep.
type sweepStep struct {
	name string
	run  func(context.Context) error
}

// Sweep runs the steps in order, once any sweep in progress has finished: sweeps
// are serialised, and a caller that waits is let go when its context ends. A step
// that fails is logged and its error is kept, and the steps after it still run;
// the result joins the errors of all of them. It returns nil when every step
// worked.
func (s *Sweeper) Sweep(ctx context.Context) error {
	select {
	case s.running <- struct{}{}:
		defer func() { <-s.running }()
	case <-ctx.Done():
		return ctx.Err()
	}
	// A hub that is shutting down ends its leases itself, and starts no sweep.
	if s.service.InShutdown() {
		return nil
	}

	var errs []error
	for _, step := range []sweepStep{
		{"reap", s.reap},
		{"lapse", s.lapse},
		{"overdue", s.destroyOverdue},
		{"orphans", s.removeOrphans},
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
	ctx, cancel := context.WithTimeout(ctx, s.callTimeout)
	defer cancel()
	ids, err := s.provider.Reap(ctx)
	if len(ids) > 0 {
		slog.Info("sandbox expiry: the provider removed boxes past their time", "sandbox_ids", ids)
	}
	if err != nil {
		return errors.New(callFailure("reap", s.callTimeout, err, s.service.scrubValues()))
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

// removeOrphans destroys the boxes that belong to leases that have ended: the
// boxes an ending could not destroy, and the ones a lease that ended while its box
// was being made left behind. A listed box belongs to such a lease when its id is
// the sandbox_id of a terminal lease in this hub's table, or, when the listing
// shows labels, when its hub.lease label names one. Any other box is not hub's to
// destroy, and is left alone: the box of a leased or active lease, one without the
// label, one whose lease this hub does not know. A box that cannot be destroyed
// is logged and left for the next sweep.
func (s *Sweeper) removeOrphans(ctx context.Context) error {
	listCtx, cancel := context.WithTimeout(ctx, s.callTimeout)
	listed, err := s.boxes.List(listCtx)
	cancel()
	if err != nil {
		return errors.New(callFailure("list", s.callTimeout, err, s.service.scrubValues()))
	}
	if len(listed) == 0 {
		return nil
	}
	byBox, terminal, err := s.store.terminalLeases()
	if err != nil {
		return err
	}
	for _, box := range listed {
		leaseID, ok := orphanOf(box, byBox, terminal)
		if !ok {
			continue
		}
		slog.Info("sandbox expiry: destroying a box that belongs to an ended lease", "lease_id", leaseID, "sandbox_id", box.ID)
		s.destroyOrphan(ctx, leaseID, box.ID)
	}
	return nil
}

// orphanOf returns the terminal lease a listed box belongs to. byBox maps the
// sandbox_id of every terminal lease that had a box to the lease, and terminal is
// the set of terminal lease ids.
func orphanOf(box Box, byBox map[string]string, terminal map[string]struct{}) (string, bool) {
	if leaseID, ok := byBox[box.ID]; ok {
		return leaseID, true
	}
	if leaseID, ok := box.Labels[hubLabelPrefix+"lease"]; ok {
		if _, ended := terminal[leaseID]; ended {
			return leaseID, true
		}
	}
	return "", false
}

// destroyOrphan destroys one orphaned box within the call limit and logs a
// failure.
func (s *Sweeper) destroyOrphan(ctx context.Context, leaseID, boxID string) {
	ctx, cancel := context.WithTimeout(ctx, s.callTimeout)
	defer cancel()
	if err := s.boxes.Destroy(ctx, boxID); err != nil {
		slog.Error("sandbox expiry: destroying a box that belongs to an ended lease failed",
			"lease_id", leaseID, "sandbox_id", boxID, "error", callFailure("destroy", s.callTimeout, err, s.service.scrubValues()))
	}
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

// terminalLeases reads the terminal leases of the table: byBox maps the sandbox_id
// of each one that had a box to its id, and terminal is the set of all their ids.
func (s *LeaseStore) terminalLeases() (byBox map[string]string, terminal map[string]struct{}, err error) {
	rows, err := s.db.Query(`SELECT id, sandbox_id FROM sandbox_leases WHERE state IN ('destroyed', 'expired', 'failed')`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byBox, terminal = map[string]string{}, map[string]struct{}{}
	for rows.Next() {
		var id string
		var boxID sql.NullString
		if err := rows.Scan(&id, &boxID); err != nil {
			return nil, nil, err
		}
		terminal[id] = struct{}{}
		if boxID.Valid {
			byBox[boxID.String] = id
		}
	}
	return byBox, terminal, rows.Err()
}
