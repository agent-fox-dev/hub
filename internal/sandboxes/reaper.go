package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// leaseEndReasonRestart is the end_reason of an active lease an earlier process
// left, ended by the next one at start.
const leaseEndReasonRestart = "restart"

// tickerFactory makes the ticks of the reaper: a channel that carries one value
// for each tick, and a function that releases the ticker. Tests supply one they
// fire by hand.
type tickerFactory func(d time.Duration) (ticks <-chan time.Time, stop func())

// timeTicker is the tickerFactory of production: a time.Ticker.
func timeTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// Reaper is the loop that sweeps every reap_interval.
type Reaper struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Stop cancels the loop and waits until it has ended, which includes a sweep in
// progress. Stopping again does nothing.
func (r *Reaper) Stop() {
	r.cancel()
	<-r.done
}

// RecoverActive ends the leases an earlier process left active. Their boxes
// belong to that process's gateway, which this one cannot relay to, so each is
// destroyed and its lease ended as destroyed with the reason restart, with one
// hub.sandbox.destroy event by the system. A box that cannot be destroyed is
// logged and does not keep the lease: a later sweep tries it again. The gauge of
// active leases is recalibrated afterwards. A lease that cannot be ended is
// logged and reported, and the others are still ended.
func (s *Sweeper) RecoverActive(ctx context.Context) error {
	leases, err := s.store.selectLeases(`state = 'active'`)
	if err != nil {
		slog.Error("sandbox expiry: reading the leases an earlier process left active failed", "error", err)
		return fmt.Errorf("recovery: %w", err)
	}
	var errs []error
	for _, lease := range leases {
		if _, err := s.ender.endDestroyed(ctx, lease, leaseEndReasonRestart); err != nil {
			slog.Error("sandbox expiry: ending a lease an earlier process left active failed", "lease_id", lease.ID, "error", err)
			errs = append(errs, fmt.Errorf("recovery of lease %s: %w", lease.ID, err))
		}
	}
	if len(leases) > 0 {
		slog.Info("sandbox expiry: ended the leases an earlier process left active", "leases", len(leases))
	}
	if err := s.metrics.recalibrate(ctx, s.db); err != nil {
		slog.Error("sandbox expiry: recalibrating the gauge after recovery failed", "error", err)
		errs = append(errs, fmt.Errorf("recovery: %w", err))
	}
	return errors.Join(errs...)
}

// StartReaper runs one sweep and then starts the loop that sweeps once for every
// tick of the reap interval, until ctx is cancelled or the Reaper is stopped. The
// first sweep is done when StartReaper returns, so that the caller can rely on it;
// its error is returned together with the running Reaper, which is never nil. No
// sweep starts after the context ends.
func (s *Sweeper) StartReaper(ctx context.Context) (*Reaper, error) {
	ctx, cancel := context.WithCancel(ctx)
	ticks, stop := s.tickers(s.interval)
	reaper := &Reaper{cancel: cancel, done: make(chan struct{})}
	s.setReaper(reaper)

	err := s.Sweep(ctx)
	go func() {
		defer close(reaper.done)
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
				if ctx.Err() != nil {
					return
				}
				// Sweep logs every step that fails.
				_ = s.Sweep(ctx)
			}
		}
	}()
	return reaper, err
}

// RunStartup does the start-up work of the feature: it ends the leases an earlier
// process left active, then starts the reaper, whose first sweep is done before it
// returns. Whatever fails is logged and returned together, and the reaper runs all
// the same: a failing provider must not stop hub from starting. The reaper stops
// with ctx or with StopReaper.
func (s *Sweeper) RunStartup(ctx context.Context) error {
	recoveryErr := s.RecoverActive(ctx)
	_, sweepErr := s.StartReaper(ctx)
	return errors.Join(recoveryErr, sweepErr)
}

// setReaper remembers the running reaper, so that it can be stopped.
func (s *Sweeper) setReaper(r *Reaper) {
	s.reaperMu.Lock()
	defer s.reaperMu.Unlock()
	s.reaper = r
}

// StopReaper stops the reaper if one is running, and waits for a sweep in
// progress. It does nothing when none was started.
func (s *Sweeper) StopReaper() {
	s.reaperMu.Lock()
	r := s.reaper
	s.reaperMu.Unlock()
	if r != nil {
		r.Stop()
	}
}
