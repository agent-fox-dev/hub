package jobqueue

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-42: All queue log output uses log/slog; every log line for job events
// includes structured fields job_id, type, key, status, and retry_count where
// applicable.
// Requirement: 10-REQ-14.1
// ---------------------------------------------------------------------------

func TestLogging_StructuredFields(t *testing.T) {
	q, db, logBuf := newTestQueueWithLogCapture(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return map[string]string{"ok": "true"}, false, nil
	}
	if err := q.Register("merge", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a queued job.
	now := time.Now()
	seedJobFull(t, db, "j1", "merge", "main", "n1", "queued",
		0, now.Add(-1*time.Second), now.Add(-2*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Wait for the job to complete.
	waitForStatus(t, db, "j1", "completed", 5*time.Second)

	q.Shutdown()
	q.Wait()

	// Verify log output contains structured fields for job events.
	logOutput := logBuf.String()
	lines := strings.Split(logOutput, "\n")

	jobEventFound := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Look for lines related to job events (contain job_id).
		if strings.Contains(line, "job_id") {
			jobEventFound = true

			// Each job event log line should have these structured fields.
			requiredFields := []string{"job_id", "type", "key", "status"}
			for _, field := range requiredFields {
				if !strings.Contains(line, field) {
					t.Errorf("job event log line missing field %q: %q", field, line)
				}
			}
		}
	}

	if !jobEventFound {
		t.Errorf("no job event log lines found with 'job_id' field in output:\n%s", logOutput)
	}
}

// ---------------------------------------------------------------------------
// TS-10-43: Job claimed, completed, failed-with-retry, and promoted events
// are logged at DEBUG level; dead-letter, crash recovery, and grace period
// interruption events are logged at WARN level; queue started/stopped at
// INFO level.
// Requirement: 10-REQ-14.2
// ---------------------------------------------------------------------------

func TestLogging_CorrectLevels(t *testing.T) {
	q, db, logBuf := newTestQueueWithLogCapture(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return map[string]string{"done": "true"}, false, nil
	}
	if err := q.Register("merge", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a queued job.
	now := time.Now()
	seedJobFull(t, db, "j1", "merge", "main", "n1", "queued",
		0, now.Add(-1*time.Second), now.Add(-2*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	waitForStatus(t, db, "j1", "completed", 5*time.Second)

	q.Shutdown()
	q.Wait()

	logOutput := logBuf.String()

	// Verify INFO-level events for queue started and stopped.
	if !strings.Contains(logOutput, "INFO") {
		t.Errorf("expected INFO-level log lines (queue started/stopped), got:\n%s", logOutput)
	}

	// Verify DEBUG-level events for job claimed/completed.
	if !strings.Contains(logOutput, "DEBUG") {
		t.Errorf("expected DEBUG-level log lines (job claimed/completed), got:\n%s", logOutput)
	}

	// Check for specific event messages.
	hasStarted := false
	hasStopped := false
	lines := strings.Split(logOutput, "\n")
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(line, "INFO") && strings.Contains(lower, "start") {
			hasStarted = true
		}
		if strings.Contains(line, "INFO") && strings.Contains(lower, "stop") {
			hasStopped = true
		}
	}
	if !hasStarted {
		t.Errorf("expected INFO log for queue started, output:\n%s", logOutput)
	}
	if !hasStopped {
		t.Errorf("expected INFO log for queue stopped, output:\n%s", logOutput)
	}
}

// ---------------------------------------------------------------------------
// TS-10-43 (cont.): Dead-lettered job events are logged at WARN level.
// Requirement: 10-REQ-14.2
// ---------------------------------------------------------------------------

func TestLogging_DeadLetterWarnLevel(t *testing.T) {
	q, db, logBuf := newTestQueueWithLogCapture(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	policy := &RetryPolicy{MaxRetries: 0}
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, false, errors.New("permanent failure")
	}
	if err := q.Register("merge", handler, policy); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a queued job that will immediately dead-letter (maxRetries=0 +
	// permanent error).
	now := time.Now()
	seedJobFull(t, db, "j1", "merge", "main", "n1", "queued",
		0, now.Add(-1*time.Second), now.Add(-2*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	waitForStatus(t, db, "j1", "dead_letter", 5*time.Second)

	q.Shutdown()
	q.Wait()

	logOutput := logBuf.String()

	// Verify WARN-level log for dead_letter event.
	warnFound := false
	lines := strings.Split(logOutput, "\n")
	for _, line := range lines {
		if strings.Contains(line, "WARN") && strings.Contains(line, "dead") {
			warnFound = true
			break
		}
	}
	if !warnFound {
		t.Errorf("expected WARN log for dead_letter event, output:\n%s", logOutput)
	}
}

// ---------------------------------------------------------------------------
// TS-10-44: The queue never emits ERROR-level log lines; handler errors
// surface through the job's error field and state transition log entries.
// Requirement: 10-REQ-14.3
// ---------------------------------------------------------------------------

func TestLogging_NoErrorLevel(t *testing.T) {
	q, db, logBuf := newTestQueueWithLogCapture(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, false, errors.New("fatal error")
	}
	if err := q.Register("merge", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a job that will fail with a permanent error.
	now := time.Now()
	seedJobFull(t, db, "j1", "merge", "main", "n1", "queued",
		0, now.Add(-1*time.Second), now.Add(-2*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	waitForStatus(t, db, "j1", "dead_letter", 5*time.Second)

	q.Shutdown()
	q.Wait()

	// Verify NO error-level log lines were emitted.
	logOutput := logBuf.String()
	lines := strings.Split(logOutput, "\n")
	for _, line := range lines {
		if strings.Contains(line, "level=ERROR") || strings.Contains(line, "ERROR") {
			// Skip lines that contain "ERROR" as part of a field value, not as a level.
			// slog text format uses "level=ERROR".
			if strings.Contains(line, "level=ERROR") {
				t.Errorf("unexpected ERROR-level log line: %q", line)
			}
		}
	}

	// Also verify the error appears in the job record (not just logs).
	var jobErr string
	if err := db.QueryRow("SELECT error FROM jobs WHERE id=?", "j1").Scan(&jobErr); err != nil {
		t.Fatalf("query job error failed: %v", err)
	}
	if jobErr != "fatal error" {
		t.Errorf("expected job error='fatal error', got %q", jobErr)
	}
}
