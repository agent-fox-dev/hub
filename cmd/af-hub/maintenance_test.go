package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// runMaintenance runs every task at once, again on each tick, logs a task that
// fails without stopping the others, and returns when its context is cancelled.
func TestRunMaintenance(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	var first, second atomic.Int32
	tasks := []maintenanceTask{
		{name: "failing", run: func(context.Context) error { first.Add(1); return errors.New("boom") }},
		{name: "counting", run: func(context.Context) error { second.Add(1); return nil }},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runMaintenance(ctx, 10*time.Millisecond, tasks); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for second.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if second.Load() < 2 {
		t.Fatalf("the second task ran %d times; want at least the pass at start and one tick", second.Load())
	}
	if first.Load() < 2 {
		t.Errorf("the failing task ran %d times; a failure must not stop the loop", first.Load())
	}
	if !bytes.Contains(logs.Bytes(), []byte(`task=failing`)) {
		t.Errorf("the failure was not logged: %s", logs.String())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runMaintenance did not return after cancel")
	}
}

// A context that is cancelled before the loop starts ends it without a pass
// being logged as a failure.
func TestRunMaintenance_CancelledContext(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runMaintenance(ctx, time.Hour, []maintenanceTask{{name: "t", run: func(ctx context.Context) error { return ctx.Err() }}})
	if logs.Len() != 0 {
		t.Errorf("a cancelled context is no failure; logged %s", logs.String())
	}
}
