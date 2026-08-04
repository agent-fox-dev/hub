package jobqueue

import (
	"log/slog"
	"runtime"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-45: New constructs a queue with default configuration (4 workers,
// 5s poll interval, 30s grace period) without starting worker goroutines.
// Requirement: 10-REQ-15.1
// ---------------------------------------------------------------------------

func TestConfig_Defaults(t *testing.T) {
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))

	baseline := runtime.NumGoroutine()

	q, err := New(db, logger)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if q == nil {
		t.Fatal("New() returned nil queue")
	}

	// No goroutines should be started by New.
	runtime.Gosched()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > baseline+1 {
		t.Errorf("expected no new goroutines from New(), got delta=%d (before=%d, after=%d)",
			after-baseline, baseline, after)
	}

	// Verify default configuration.
	if q.workerCount != 4 {
		t.Errorf("expected default workerCount=4, got %d", q.workerCount)
	}
	if q.pollInterval != 5*time.Second {
		t.Errorf("expected default pollInterval=5s, got %v", q.pollInterval)
	}
	if q.gracePeriod != 30*time.Second {
		t.Errorf("expected default gracePeriod=30s, got %v", q.gracePeriod)
	}
}

// ---------------------------------------------------------------------------
// TS-10-47: WithWorkers(n) overrides the default worker count of 4 with the
// provided value n. After Start(), exactly n worker goroutines are running.
// Requirement: 10-REQ-15.3
// ---------------------------------------------------------------------------

func TestConfig_WithWorkers(t *testing.T) {
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))

	q, err := New(db, logger, WithWorkers(7))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	if q.workerCount != 7 {
		t.Errorf("expected workerCount=7, got %d", q.workerCount)
	}

	baseline := runtime.NumGoroutine()

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	// Allow goroutines to be scheduled.
	runtime.Gosched()
	time.Sleep(100 * time.Millisecond)

	after := runtime.NumGoroutine()
	delta := after - baseline

	// Expect at least 7 new goroutines (the workers).
	if delta < 7 {
		t.Errorf("expected at least 7 new goroutines for WithWorkers(7), "+
			"got delta=%d (before=%d, after=%d)", delta, baseline, after)
	}
}

// ---------------------------------------------------------------------------
// TS-10-E45: WithWorkers called with n <= 0 causes New to return an error.
// Requirement: 10-REQ-15.E1
// ---------------------------------------------------------------------------

func TestConfig_WithWorkersInvalid(t *testing.T) {
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))

	tests := []struct {
		name string
		n    int
	}{
		{"zero", 0},
		{"negative", -1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, err := New(db, logger, WithWorkers(tc.n))
			if err == nil {
				t.Errorf("New(WithWorkers(%d)) expected error, got nil", tc.n)
			}
			if q != nil {
				t.Errorf("New(WithWorkers(%d)) expected nil queue on error, got %+v", tc.n, q)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-10-E46: Start called on a queue that is already running returns a
// non-nil error without starting additional worker goroutines.
// Requirement: 10-REQ-15.E2
// ---------------------------------------------------------------------------

func TestConfig_StartAlreadyRunning(t *testing.T) {
	q, _ := newTestQueueWithOpts(t, WithWorkers(2))
	registerTestHandler(t, q, "test")

	if err := q.Start(); err != nil {
		t.Fatalf("first Start() failed: %v", err)
	}
	defer q.Stop()

	// Allow goroutines to be scheduled.
	runtime.Gosched()
	time.Sleep(50 * time.Millisecond)

	before := runtime.NumGoroutine()

	// Second Start() should return an error.
	err := q.Start()
	if err == nil {
		t.Error("expected error from second Start() call, got nil")
	}

	// Verify no additional goroutines were started.
	runtime.Gosched()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+1 {
		t.Errorf("expected no new goroutines from second Start(), "+
			"got delta=%d (before=%d, after=%d)", after-before, before, after)
	}
}

// ---------------------------------------------------------------------------
// TS-10-E47: New called with a nil *sql.DB returns (nil, non-nil error).
// Requirement: 10-REQ-15.E3
// ---------------------------------------------------------------------------

func TestConfig_NilDB(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))

	q, err := New(nil, logger)
	if err == nil {
		t.Error("New(nil db) expected error, got nil")
	}
	if q != nil {
		t.Errorf("New(nil db) expected nil queue, got %+v", q)
	}
}

// ---------------------------------------------------------------------------
// TS-10-47 (cont.): Verify the grace period defaults to 30s and can be
// overridden with WithGracePeriod.
// Requirement: 10-REQ-15.1
// ---------------------------------------------------------------------------

func TestConfig_GracePeriodDefault(t *testing.T) {
	q, _ := newTestQueue(t)

	if q.gracePeriod != 30*time.Second {
		t.Errorf("expected default gracePeriod=30s, got %v", q.gracePeriod)
	}
}

func TestConfig_WithGracePeriod(t *testing.T) {
	q, _ := newTestQueueWithOpts(t, WithGracePeriod(10*time.Second))

	if q.gracePeriod != 10*time.Second {
		t.Errorf("expected gracePeriod=10s, got %v", q.gracePeriod)
	}
}
