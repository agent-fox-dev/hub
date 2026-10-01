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
//
// Besides the mutex, the package keeps a per-workspace rebuild-active flag
// (BeginRebuild / RebuildActive). A rebuild applies patches in a worktree
// under <root>/<slug>/rebuild without holding the mutex, so operations that
// delete the workspace directory (archive, reclone) consult the flag as well.
package wslock

import "sync"

var (
	mu    sync.Mutex
	locks = map[string]*sync.Mutex{}

	// rebuildGuards holds the rebuild-active guard per slug. Each guard is a
	// distinct token so an end function can tell whether the guard it set is
	// still the current one. Protected by mu; independent of the per-slug
	// mutex.
	rebuildGuards = map[string]*struct{ _ byte }{}
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

// BeginRebuild sets the rebuild-active guard for slug and returns the
// function that clears it. The guard is independent of the workspace mutex: it
// stays set while the mutex is free, so that handlers which delete the
// workspace directory (archive, reclone) can refuse to run for the whole
// rebuild while the others proceed. The rebuild calls it while holding the
// workspace lock, which leaves no gap against archive and reclone.
//
// If a guard is already set for slug, BeginRebuild returns a no-op end
// function and ok=false and leaves the existing guard untouched. end is
// idempotent: it clears only the guard this call set, so calling it again
// never clears a guard set later by another run. The guard lives in memory
// only and is therefore cleared by a process exit.
func BeginRebuild(slug string) (end func(), ok bool) {
	mu.Lock()
	defer mu.Unlock()
	if _, set := rebuildGuards[slug]; set {
		return func() {}, false
	}
	token := &struct{ _ byte }{}
	rebuildGuards[slug] = token
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if rebuildGuards[slug] == token {
			delete(rebuildGuards, slug)
		}
	}, true
}

// RebuildActive reports whether the rebuild-active guard is set for slug.
// Archive and reclone call it after a successful TryLock.
func RebuildActive(slug string) bool {
	mu.Lock()
	defer mu.Unlock()
	_, set := rebuildGuards[slug]
	return set
}
