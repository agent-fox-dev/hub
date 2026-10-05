package wsaccess_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// RequestScoped under a scope calls load once per key and returns the first
// result on every later call, even when load would now answer differently.
func TestRequestScoped_LoadsOncePerKeyUnderScope(t *testing.T) {
	ctx := wsaccess.WithRequestScope(context.Background())

	calls := 0
	value := "first"
	load := func() string {
		calls++
		return value
	}

	if got := wsaccess.RequestScoped(ctx, "k", load); got != "first" {
		t.Fatalf("first call = %q; want first", got)
	}
	value = "second"
	for i := 0; i < 3; i++ {
		if got := wsaccess.RequestScoped(ctx, "k", load); got != "first" {
			t.Errorf("call %d = %q; want memoised first", i, got)
		}
	}
	if calls != 1 {
		t.Errorf("load calls = %d; want 1", calls)
	}
}

// Distinct keys are memoised independently.
func TestRequestScoped_KeysAreIndependent(t *testing.T) {
	ctx := wsaccess.WithRequestScope(context.Background())

	a := wsaccess.RequestScoped(ctx, "a", func() string { return "va" })
	b := wsaccess.RequestScoped(ctx, "b", func() string { return "vb" })
	if a != "va" || b != "vb" {
		t.Errorf("got (%q, %q); want (va, vb)", a, b)
	}
	if got := wsaccess.RequestScoped(ctx, "a", func() string { return "other" }); got != "va" {
		t.Errorf("key a = %q; want va", got)
	}
}

// An empty string is a legitimate memoised value, not a cache miss.
func TestRequestScoped_MemoisesEmptyString(t *testing.T) {
	ctx := wsaccess.WithRequestScope(context.Background())

	calls := 0
	load := func() string {
		calls++
		return ""
	}
	wsaccess.RequestScoped(ctx, "k", load)
	wsaccess.RequestScoped(ctx, "k", load)
	if calls != 1 {
		t.Errorf("load calls = %d; want 1", calls)
	}
}

// Without a scope RequestScoped is a passthrough: load runs every time.
func TestRequestScoped_NoScopeLoadsEveryTime(t *testing.T) {
	ctx := context.Background()

	calls := 0
	load := func() string {
		calls++
		return "v"
	}
	for i := 0; i < 3; i++ {
		if got := wsaccess.RequestScoped(ctx, "k", load); got != "v" {
			t.Errorf("call %d = %q; want v", i, got)
		}
	}
	if calls != 3 {
		t.Errorf("load calls = %d; want 3", calls)
	}
}

// Separate scopes do not share memoised values.
func TestRequestScoped_ScopesAreIsolated(t *testing.T) {
	ctx1 := wsaccess.WithRequestScope(context.Background())
	ctx2 := wsaccess.WithRequestScope(context.Background())

	wsaccess.RequestScoped(ctx1, "k", func() string { return "one" })
	if got := wsaccess.RequestScoped(ctx2, "k", func() string { return "two" }); got != "two" {
		t.Errorf("second scope = %q; want two", got)
	}
}

// WithRequestScope on a context that already has a scope returns it unchanged
// and keeps the memoised values.
func TestWithRequestScope_Idempotent(t *testing.T) {
	ctx := wsaccess.WithRequestScope(context.Background())
	wsaccess.RequestScoped(ctx, "k", func() string { return "first" })

	again := wsaccess.WithRequestScope(ctx)
	if again != ctx {
		t.Error("WithRequestScope on a scoped context should return it unchanged")
	}
	if got := wsaccess.RequestScoped(again, "k", func() string { return "second" }); got != "first" {
		t.Errorf("value after re-scoping = %q; want first", got)
	}
}

// A scope derived from a parent context keeps working through child contexts.
func TestRequestScoped_SurvivesDerivedContext(t *testing.T) {
	ctx := wsaccess.WithRequestScope(context.Background())
	child, cancel := context.WithCancel(ctx)
	defer cancel()

	calls := 0
	load := func() string {
		calls++
		return "v"
	}
	wsaccess.RequestScoped(ctx, "k", load)
	wsaccess.RequestScoped(child, "k", load)
	if calls != 1 {
		t.Errorf("load calls = %d; want 1 across parent and child", calls)
	}
}

// Concurrent callers sharing one scope load exactly once (run with -race).
func TestRequestScoped_ConcurrentLoadsOnce(t *testing.T) {
	ctx := wsaccess.WithRequestScope(context.Background())

	var calls atomic.Int32
	load := func() string {
		calls.Add(1)
		return "v"
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := wsaccess.RequestScoped(ctx, "k", load); got != "v" {
				t.Errorf("got %q; want v", got)
			}
		}()
	}
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("load calls = %d; want 1", n)
	}
}
