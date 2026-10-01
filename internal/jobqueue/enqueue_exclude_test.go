package jobqueue

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-01-51: ExcludeJobID makes both active (type, key) dedup queries ignore
// the named job and leaves dedup unchanged when it is empty.
// Requirement: 01-REQ-7.6
// ---------------------------------------------------------------------------

func TestEnqueue_TS_01_51_ExcludeJobID(t *testing.T) {
	t.Run("empty ExcludeJobID dedups against the running job", func(t *testing.T) {
		queue, db := newTestQueue(t)
		registerTestHandler(t, queue, "rebuild")
		seedJob(t, db, "J", "rebuild", "k", "n-orig", "running")

		id, dup, err := queue.Enqueue(EnqueueParams{Type: "rebuild", Key: "k", Nonce: "n-1", Payload: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if !dup || id != "J" {
			t.Errorf("got (%q, dup=%v), want (J, true)", id, dup)
		}
	})

	t.Run("ExcludeJobID inserts a new queued job next to the running one", func(t *testing.T) {
		queue, db := newTestQueue(t)
		registerTestHandler(t, queue, "rebuild")
		seedJob(t, db, "J", "rebuild", "k", "n-orig", "running")

		id, dup, err := queue.Enqueue(EnqueueParams{
			Type: "rebuild", Key: "k", Nonce: "n-2", Payload: json.RawMessage(`{}`),
			Group: "g1", ExcludeJobID: "J",
		})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if dup || id == "" || id == "J" {
			t.Fatalf("got (%q, dup=%v), want a new non-duplicate job", id, dup)
		}
		var status, group string
		if err := db.QueryRow("SELECT status, group_key FROM jobs WHERE id = ?", id).Scan(&status, &group); err != nil {
			t.Fatalf("query: %v", err)
		}
		if status != "queued" || group != "g1" {
			t.Errorf("status=%q group_key=%q, want queued/g1", status, group)
		}

		// The new queued job is now an active duplicate for a later enqueue.
		id2, dup2, err := queue.Enqueue(EnqueueParams{Type: "rebuild", Key: "k", Nonce: "n-3", Payload: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if !dup2 {
			t.Errorf("later enqueue got (%q, dup=false), want a duplicate", id2)
		}
	})

	t.Run("a different active job is still deduplicated", func(t *testing.T) {
		queue, db := newTestQueue(t)
		registerTestHandler(t, queue, "rebuild")
		seedJob(t, db, "J", "rebuild", "k", "n-orig", "running")
		seedJob(t, db, "K", "rebuild", "k", "n-other", "queued")

		id, dup, err := queue.Enqueue(EnqueueParams{
			Type: "rebuild", Key: "k", Nonce: "n-4", Payload: json.RawMessage(`{}`), ExcludeJobID: "J",
		})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if !dup || id != "K" {
			t.Errorf("got (%q, dup=%v), want (K, true)", id, dup)
		}
	})

	t.Run("race recovery also ignores the excluded job", func(t *testing.T) {
		queue, db := newTestQueue(t)
		registerTestHandler(t, queue, "rebuild")
		seedJob(t, db, "J", "rebuild", "k", "n-orig", "running")
		// Make the INSERT fail after the pre-insert check passed, which
		// sends Enqueue into the race-recovery path.
		if _, err := db.Exec(`CREATE TRIGGER fail_insert BEFORE INSERT ON jobs
			WHEN NEW.nonce = 'n-race' BEGIN SELECT RAISE(ABORT, 'forced race'); END`); err != nil {
			t.Fatalf("create trigger: %v", err)
		}

		// Without the exclusion the recovery query finds J.
		id, dup, err := queue.Enqueue(EnqueueParams{Type: "rebuild", Key: "k", Nonce: "n-race", Payload: json.RawMessage(`{}`)})
		if err != nil || !dup || id != "J" {
			t.Fatalf("without exclusion: got (%q, dup=%v, err=%v), want (J, true, nil)", id, dup, err)
		}

		// With the exclusion the pre-insert check passes, the insert fails
		// and the recovery query must not report J: the error surfaces.
		id, dup, err = queue.Enqueue(EnqueueParams{
			Type: "rebuild", Key: "k", Nonce: "n-race", Payload: json.RawMessage(`{}`), ExcludeJobID: "J",
		})
		if err == nil {
			t.Fatalf("with exclusion: got (%q, dup=%v, nil), want the insert error", id, dup)
		}
		if dup || id == "J" {
			t.Errorf("with exclusion the recovery query returned the excluded job: (%q, dup=%v)", id, dup)
		}
	})
}
