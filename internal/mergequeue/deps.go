package mergequeue

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// InMemoryBranchLocker provides per-target-branch mutual exclusion using
// an in-memory map of mutexes. Safe for concurrent use.
type InMemoryBranchLocker struct {
	mu      sync.Mutex
	mutexes map[string]*sync.Mutex
}

// NewInMemoryBranchLocker creates a new InMemoryBranchLocker.
func NewInMemoryBranchLocker() *InMemoryBranchLocker {
	return &InMemoryBranchLocker{
		mutexes: make(map[string]*sync.Mutex),
	}
}

// Lock acquires the lock for the given branch.
func (l *InMemoryBranchLocker) Lock(branch string) {
	l.mu.Lock()
	m, ok := l.mutexes[branch]
	if !ok {
		m = &sync.Mutex{}
		l.mutexes[branch] = m
	}
	l.mu.Unlock()
	m.Lock()
}

// Unlock releases the lock for the given branch.
func (l *InMemoryBranchLocker) Unlock(branch string) {
	l.mu.Lock()
	m := l.mutexes[branch]
	l.mu.Unlock()
	m.Unlock()
}

// DefaultCheckRunner executes a check command via sh -c with the given
// timeout and working directory. Returns combined stdout+stderr output.
func DefaultCheckRunner(ctx context.Context, dir, command string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}
