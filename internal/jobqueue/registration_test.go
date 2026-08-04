package jobqueue

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-4: Register with a unique type name, valid handler, and optional retry
// policy stores the handler and returns nil.
// Requirement: 10-REQ-2.1
// ---------------------------------------------------------------------------

func TestRegister_UniqueType(t *testing.T) {
	queue, _ := newTestQueue(t)

	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, false, nil
	}
	policy := &RetryPolicy{
		Base:       2 * time.Second,
		Multiplier: 2,
		Cap:        7200 * time.Second,
		MaxRetries: 20,
	}

	err := queue.Register("merge", handler, policy)
	if err != nil {
		t.Fatalf("Register('merge') returned error: %v", err)
	}

	// Verify that a subsequent Enqueue with this registered type succeeds.
	jobID, dup, enqErr := queue.Enqueue(EnqueueParams{
		Type:        "merge",
		Key:         "k",
		Nonce:       "n1",
		Payload:     json.RawMessage(`{}`),
		SubmittedBy: "sys",
	})
	if enqErr != nil {
		t.Fatalf("Enqueue with registered type returned error: %v", enqErr)
	}
	if dup {
		t.Error("expected duplicate=false for new job")
	}
	if jobID == "" {
		t.Error("expected non-empty job ID for registered type")
	}
}

// ---------------------------------------------------------------------------
// TS-10-5: Registering a type name that is already registered returns a
// non-nil error and does not overwrite the existing handler.
// Requirement: 10-REQ-2.2
// ---------------------------------------------------------------------------

func TestRegister_DuplicateType(t *testing.T) {
	queue, _ := newTestQueue(t)

	handler1 := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return "handler1", false, nil
	}
	handler2 := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return "handler2", false, nil
	}

	// First registration succeeds.
	err := queue.Register("merge", handler1, nil)
	if err != nil {
		t.Fatalf("first Register('merge') returned error: %v", err)
	}

	// Second registration with the same type name must fail.
	err = queue.Register("merge", handler2, nil)
	if err == nil {
		t.Fatal("expected error on duplicate Register('merge'), got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "already registered") && !strings.Contains(errMsg, "duplicate") {
		t.Errorf("error should mention 'already registered' or 'duplicate', got: %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// TS-10-6: Register accepts a handler with the exact signature
// func(ctx context.Context, payload json.RawMessage) (result any, retryable bool, err error).
// Requirement: 10-REQ-2.3
// ---------------------------------------------------------------------------

func TestRegister_HandlerSignature(t *testing.T) {
	queue, _ := newTestQueue(t)

	// The primary purpose of this test is compile-time verification that
	// the handler function signature is accepted by Register.
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return map[string]string{"status": "ok"}, false, nil
	}

	err := queue.Register("test-type", handler, nil)
	if err != nil {
		t.Fatalf("Register with valid handler signature returned error: %v", err)
	}
}
