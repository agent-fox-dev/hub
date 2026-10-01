package wslock

import "testing"

func TestTryLockAndLock(t *testing.T) {
	unlock, ok := TryLock("ws")
	if !ok {
		t.Fatal("first TryLock should succeed")
	}
	if _, ok := TryLock("ws"); ok {
		t.Fatal("second TryLock should fail while held")
	}
	if other, ok := TryLock("other"); !ok {
		t.Fatal("different slug should be independent")
	} else {
		other()
	}
	unlock()
	if u, ok := TryLock("ws"); !ok {
		t.Fatal("TryLock after unlock should succeed")
	} else {
		u()
	}
	done := make(chan struct{})
	release := Lock("ws")
	go func() {
		u := Lock("ws")
		u()
		close(done)
	}()
	release()
	<-done
}

// TS-01-32: BeginRebuild sets a flag independent of the mutex and its end
// function is idempotent.
// Requirements: 01-REQ-5.1, 01-REQ-5.3
func TestBeginRebuild_FlagIndependentOfMutexAndEndIdempotent_TS_01_32(t *testing.T) {
	const slug = "ts-01-32"
	end, ok := BeginRebuild(slug)
	if !ok || end == nil {
		t.Fatal("BeginRebuild on a fresh slug should succeed with a non-nil end")
	}
	if !RebuildActive(slug) {
		t.Fatal("RebuildActive should be true after BeginRebuild")
	}
	if RebuildActive("ts-01-32-other") {
		t.Fatal("guard must be per slug")
	}

	// The mutex is unaffected by the guard.
	unlock, locked := TryLock(slug)
	if !locked {
		t.Fatal("TryLock must succeed while only the guard is set")
	}
	unlock()

	// A second BeginRebuild is refused and leaves the existing guard set.
	end2, ok2 := BeginRebuild(slug)
	if ok2 {
		t.Fatal("second BeginRebuild for the same slug must return ok=false")
	}
	if end2 == nil {
		t.Fatal("end returned with ok=false must be non-nil")
	}
	end2() // a refused run's end must not clear the existing guard
	if !RebuildActive(slug) {
		t.Fatal("refused BeginRebuild's end must not clear the existing guard")
	}

	end()
	if RebuildActive(slug) {
		t.Fatal("RebuildActive should be false after end()")
	}

	// A guard set later by another run survives a repeated end().
	end3, ok3 := BeginRebuild(slug)
	if !ok3 {
		t.Fatal("BeginRebuild after end() should succeed")
	}
	defer end3()
	end()
	if !RebuildActive(slug) {
		t.Fatal("second end() must not clear a guard set by another run")
	}
}
