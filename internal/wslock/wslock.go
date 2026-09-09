// Package wslock provides per-workspace mutual exclusion for operations that
// mutate a workspace's trunk working tree or its local refs.
//
// Every workspace has a single non-bare clone at <root>/<slug>/trunk that is
// shared by the git smart HTTP server (push resets the working tree), sync
// (fast-forward reset), reclone/archive (directory removal), merge and
// rebuild jobs (checkout, rebase, cherry-pick), rollback, and rerere forget.
// Running two of these concurrently corrupts the tree; the job queue only
// serializes jobs that share a group key. This package is the process-wide
// guard. The hub runs as a single replica, so an in-process lock suffices.
package wslock

import "sync"

var (
	mu    sync.Mutex
	locks = map[string]*sync.Mutex{}
)

func get(slug string) *sync.Mutex {
	mu.Lock()
	defer mu.Unlock()
	l, ok := locks[slug]
	if !ok {
		l = &sync.Mutex{}
		locks[slug] = l
	}
	return l
}

// Lock blocks until the workspace lock for slug is acquired and returns the
// function that releases it. Intended for background jobs, which may wait.
func Lock(slug string) (unlock func()) {
	l := get(slug)
	l.Lock()
	return l.Unlock
}

// TryLock acquires the workspace lock for slug without blocking. It returns
// the release function and true on success, or nil and false when another
// operation holds the lock. Intended for request handlers, which should
// answer 409 rather than wait for a long-running job.
func TryLock(slug string) (unlock func(), ok bool) {
	l := get(slug)
	if !l.TryLock() {
		return nil, false
	}
	return l.Unlock, true
}
