package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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

// reaperLoop is the loop that sweeps every reap_interval.
type reaperLoop struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// stop cancels the loop and waits until it has ended, which includes a sweep in
// progress, or until ctx has. Stopping again does nothing.
func (r *reaperLoop) stop(ctx context.Context) {
	r.cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
	}
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
		// This process holds no handle of those boxes and the provider cannot list or
		// destroy them: if they are still running they live until their TTL, which is
		// never later than the lease's expires_at, or until the provider's Reap.
		boxIDs := make([]string, 0, len(leases))
		for _, lease := range leases {
			if lease.SandboxID != nil {
				boxIDs = append(boxIDs, *lease.SandboxID)
			}
		}
		slog.Warn("sandbox expiry: ended the leases an earlier process left active; their boxes, if any are still running, are out of this process's reach and live until their TTL",
			"leases", len(leases), "sandbox_ids", boxIDs)
	}
	if err := s.metrics.recalibrate(ctx, s.db); err != nil {
		slog.Error("sandbox expiry: recalibrating the gauge after recovery failed", "error", err)
		errs = append(errs, fmt.Errorf("recovery: %w", err))
	}
	return errors.Join(errs...)
}

// StartReaper runs one sweep and then starts the loop that sweeps once for every
// tick of the reap interval, until ctx is cancelled or Shutdown stops it. The first
// sweep is done when StartReaper returns, so that the caller can rely on it; its
// error is returned, and the loop runs all the same. No sweep starts after the
// context ends.
func (s *Sweeper) StartReaper(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	ticks, stop := s.tickers(s.interval)
	reaper := &reaperLoop{cancel: cancel, done: make(chan struct{})}
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
	return err
}

// RunStartup does the start-up work of the feature: it ends the leases an earlier
// process left active, then starts the reaper, whose first sweep is done before it
// returns. Whatever fails is logged and returned together, and the reaper runs all
// the same: a failing provider must not stop hub from starting. The reaper stops
// with ctx or with StopReaper.
func (s *Sweeper) RunStartup(ctx context.Context) error {
	recoveryErr := s.RecoverActive(ctx)
	return errors.Join(recoveryErr, s.StartReaper(ctx))
}

// setReaper remembers the running reaper, so that Shutdown can stop it.
func (s *Sweeper) setReaper(r *reaperLoop) {
	s.reaperMu.Lock()
	defer s.reaperMu.Unlock()
	s.reaper = r
}

// Shutdown is the sandbox part of hub's shutdown. It puts hub in shutdown mode,
// stops the reaper and waits for a sweep in progress, then destroys the box of
// every active lease, all at once, and ends each lease as destroyed with the
// reason shutdown and one hub.sandbox.destroy event by the system. A lease in
// state leased is not touched. ctx carries the time limit: when it runs out
// Shutdown returns, and a lease whose ending has not completed stays active, for
// the recovery at the next start. A second call, and a call on a hub that has
// shut down already, do nothing.
func (s *Sweeper) Shutdown(ctx context.Context) error {
	if !s.shuttingDown.CompareAndSwap(false, true) {
		return nil
	}
	s.service.EnterShutdown()

	s.reaperMu.Lock()
	loop := s.reaper
	s.reaperMu.Unlock()
	if loop != nil {
		loop.stop(ctx)
	}
	// A sweep that gatewayd started is not the reaper's. Sweeps never overlap, so
	// holding the lock for a moment waits for it, and none starts after.
	select {
	case s.running <- struct{}{}:
		<-s.running
	case <-ctx.Done():
		return fmt.Errorf("shutdown: waiting for a sweep: %w", ctx.Err())
	}

	leases, err := s.store.selectLeases(`state = 'active'`)
	if err != nil {
		slog.Error("sandbox expiry: reading the active leases for shutdown failed", "error", err)
		return fmt.Errorf("shutdown: %w", err)
	}
	var ending sync.WaitGroup
	for _, lease := range leases {
		ending.Go(func() {
			if _, err := s.ender.endDestroyed(ctx, lease, leaseEndReasonShutdown); err != nil && ctx.Err() == nil {
				slog.Error("sandbox expiry: ending a lease for shutdown failed", "lease_id", lease.ID, "error", err)
			}
		})
	}
	done := make(chan struct{})
	go func() {
		ending.Wait()
		close(done)
	}()
	select {
	case <-done:
		if len(leases) > 0 {
			slog.Info("sandbox expiry: ended the active leases for shutdown", "leases", len(leases))
		}
		return nil
	case <-ctx.Done():
		slog.Warn("sandbox expiry: the shutdown limit ran out; the leases still active are ended at the next start")
		return fmt.Errorf("shutdown: %w", ctx.Err())
	}
}
