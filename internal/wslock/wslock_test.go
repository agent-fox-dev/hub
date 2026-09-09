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
