package sandboxes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// logBuffer is a goroutine-safe buffer for captured log output.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureDefaultLog routes the default logger into a buffer for the length of
// the test.
func captureDefaultLog(t *testing.T) *logBuffer {
	t.Helper()
	buf := &logBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// Clock of the authenticator tests: rawLeaseInsert gives every lease the
// expires_at 2030-01-02T04:04:05Z.
var leaseExpiry = time.Date(2030, 1, 2, 4, 4, 5, 0, time.UTC)

// authAt returns an authenticator over a store holding the lease table, reading
// the clock through *now.
func authAt(t *testing.T, now *time.Time) (func(context.Context, string) (string, bool), *LeaseStore) {
	t.Helper()
	store, db := newLeaseStore(t, *now)
	insertRawLease(t, db, leaseID(1), "widgets", "leased", HashToken(testToken(1)))
	insertRawLease(t, db, leaseID(2), "widgets", "active", HashToken(testToken(2)))
	return NewLeaseAuthenticator(store, func() time.Time { return *now }), store
}

// TS-27-16: The authenticator accepts the token of a leased or an active lease
// that has not expired, and returns the lease id as the principal.
// Requirement: 27-REQ-3.1
func TestLeaseAuthenticator_AcceptsLiveLeases_TS_27_16(t *testing.T) {
	now := leaseExpiry.Add(-time.Hour)
	auth, _ := authAt(t, &now)
	for n, want := range map[int]string{1: leaseID(1), 2: leaseID(2)} {
		principal, ok := auth(context.Background(), testToken(n))
		if !ok || principal != want {
			t.Errorf("auth(token %d) = (%q, %v); want (%q, true)", n, principal, ok, want)
		}
	}

	now = leaseExpiry.Add(-time.Second)
	if principal, ok := auth(context.Background(), testToken(1)); !ok || principal != leaseID(1) {
		t.Errorf("auth one second before expires_at = (%q, %v); want the lease to be accepted", principal, ok)
	}
}

// TS-27-17: The authenticator refuses an empty, a malformed and an unknown
// token, the token of a lease that has ended, and the token of a lease whose
// expires_at is at or before now.
// Requirement: 27-REQ-3.2
func TestLeaseAuthenticator_RefusesTheRest_TS_27_17(t *testing.T) {
	now := leaseExpiry.Add(-time.Hour)
	auth, store := authAt(t, &now)
	insertRawLease(t, store.db, leaseID(3), "widgets", "destroyed", HashToken(testToken(3)))
	insertRawLease(t, store.db, leaseID(4), "widgets", "expired", HashToken(testToken(4)))
	insertRawLease(t, store.db, leaseID(5), "widgets", "failed", HashToken(testToken(5)))

	for name, token := range map[string]string{
		"empty":           "",
		"garbage":         "garbage",
		"unknown":         testToken(99),
		"a sandbox id":    leaseID(1),
		"the hash":        HashToken(testToken(1)),
		"a destroyed one": testToken(3),
		"an expired one":  testToken(4),
		"a failed one":    testToken(5),
		"padded":          testToken(1) + " ",
	} {
		if principal, ok := auth(context.Background(), token); ok || principal != "" {
			t.Errorf("auth(%s) = (%q, %v); want (\"\", false)", name, principal, ok)
		}
	}

	for _, at := range []time.Time{leaseExpiry, leaseExpiry.Add(time.Second), leaseExpiry.Add(24 * time.Hour)} {
		now = at
		for n := 1; n <= 2; n++ {
			if principal, ok := auth(context.Background(), testToken(n)); ok || principal != "" {
				t.Errorf("auth(token %d) at %s = (%q, %v); want a refusal: expires_at is not in the future", n, at.Format(time.RFC3339), principal, ok)
			}
		}
	}
}

// 27-REQ-3.3: The authenticator reads the lease store on every call, so a lease
// moved to destroyed or failed is refused on the next one.
func TestLeaseAuthenticator_ReadsTheStoreEveryCall_TS_27_16(t *testing.T) {
	now := leaseExpiry.Add(-time.Hour)
	auth, store := authAt(t, &now)
	for n := 1; n <= 2; n++ {
		if _, ok := auth(context.Background(), testToken(n)); !ok {
			t.Fatalf("auth(token %d) refused a live lease", n)
		}
	}

	if changed, err := store.DestroyLease(leaseID(2), "active", "api"); err != nil || !changed {
		t.Fatalf("DestroyLease = (%v, %v)", changed, err)
	}
	if changed, err := store.FailLease(leaseID(1), "boom"); err != nil || !changed {
		t.Fatalf("FailLease = (%v, %v)", changed, err)
	}
	for n := 1; n <= 2; n++ {
		if principal, ok := auth(context.Background(), testToken(n)); ok || principal != "" {
			t.Errorf("auth(token %d) after the lease ended = (%q, %v); want a refusal", n, principal, ok)
		}
	}
}

// failingLookup is a token lookup that answers from a map and fails for one
// token.
type failingLookup struct {
	leases map[string]*Lease
	failOn string
	err    error
}

func (f failingLookup) LookupByToken(token string) (*Lease, error) {
	if token == f.failOn {
		return nil, f.err
	}
	if l, ok := f.leases[token]; ok {
		return l, nil
	}
	return nil, ErrLeaseNotFound
}

// TS-27-19: A store error refuses the token and is logged, and no log line
// holds a presented token or its hash, whatever the outcome.
// Requirements: 27-REQ-3.4, 27-REQ-3.5
func TestLeaseAuthenticator_StoreErrorAndLogs_TS_27_19(t *testing.T) {
	logs := captureDefaultLog(t)
	now := leaseExpiry.Add(-time.Hour)
	expires := leaseExpiry.UTC().Format(time.RFC3339)

	good, bad, unknown := testToken(1), testToken(2), testToken(3)
	// A driver error that quotes the query arguments, which hold the hash.
	lookupErr := fmt.Errorf("query for token %s with hash %s failed: disk I/O error", bad, HashToken(bad))
	auth := newLeaseAuthenticator(failingLookup{
		leases: map[string]*Lease{good: {ID: leaseID(1), State: "active", ExpiresAt: expires}},
		failOn: bad,
		err:    lookupErr,
	}, func() time.Time { return now })

	if principal, ok := auth(context.Background(), good); !ok || principal != leaseID(1) {
		t.Errorf("auth(good) = (%q, %v); want the lease", principal, ok)
	}
	if principal, ok := auth(context.Background(), unknown); ok || principal != "" {
		t.Errorf("auth(unknown) = (%q, %v); want a refusal", principal, ok)
	}
	if principal, ok := auth(context.Background(), bad); ok || principal != "" {
		t.Errorf("auth(store error) = (%q, %v); want a refusal", principal, ok)
	}

	out := logs.String()
	if !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, "disk I/O error") {
		t.Errorf("the store error was not logged as an error:\n%s", out)
	}
	for _, token := range []string{good, bad, unknown} {
		for _, secret := range []string{token, HashToken(token)} {
			if strings.Contains(out, secret) {
				t.Errorf("a log line holds %q:\n%s", secret, out)
			}
		}
	}
}

// The authenticator checks the state itself as well: the store's lookup selects
// live leases, but a lease in any other state that reached the authenticator
// would not be accepted.
func TestLeaseAuthenticator_RefusesEndedStateFromTheStore(t *testing.T) {
	expires := leaseExpiry.UTC().Format(time.RFC3339)
	leases := map[string]*Lease{}
	for n, state := range []string{"destroyed", "expired", "failed", "bogus", ""} {
		leases[testToken(n)] = &Lease{ID: leaseID(n), State: state, ExpiresAt: expires}
	}
	auth := newLeaseAuthenticator(failingLookup{leases: leases}, func() time.Time { return leaseExpiry.Add(-time.Hour) })
	for token := range leases {
		if principal, ok := auth(context.Background(), token); ok || principal != "" {
			t.Errorf("auth(a %q lease) = (%q, %v); want a refusal", leases[token].State, principal, ok)
		}
	}
}

// A lease the store returns with an expires_at hub cannot read is refused, not
// accepted, and the lease id (not a secret) is logged.
func TestLeaseAuthenticator_UnreadableExpiry(t *testing.T) {
	logs := captureDefaultLog(t)
	auth := newLeaseAuthenticator(failingLookup{
		leases: map[string]*Lease{testToken(1): {ID: leaseID(1), State: "leased", ExpiresAt: "tomorrow"}},
		err:    errors.New("unused"),
	}, func() time.Time { return leaseExpiry })
	if principal, ok := auth(context.Background(), testToken(1)); ok || principal != "" {
		t.Errorf("auth = (%q, %v); want a refusal", principal, ok)
	}
	if out := logs.String(); !strings.Contains(out, leaseID(1)) {
		t.Errorf("the refusal was not logged with the lease id:\n%s", out)
	}
}
