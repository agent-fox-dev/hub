package sandboxes

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/txsvc/apikit"
)

// capacityConfig is the default section with the given limit on live leases.
func capacityConfig(limit float64) *Config {
	cfg := defaultLeaseConfig()
	cfg.MaxSandboxes = &limit
	return cfg
}

// TS-26-31: Capacity counts only leased or active leases that have not passed
// expires_at, across workspaces.
// Requirement: 26-REQ-5.3
func TestCountLive_TS_26_31(t *testing.T) {
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	store, db := newLeaseStore(t, now)
	future, at, past := "2030-01-02T03:04:06Z", "2030-01-02T03:04:05Z", "2030-01-02T03:04:04Z"
	seed := func(n int, workspace, state, expires string) {
		t.Helper()
		insertRawLease(t, db, leaseID(n), workspace, state, HashToken(testToken(n)))
		if _, err := db.Exec(`UPDATE sandbox_leases SET expires_at = ? WHERE id = ?`, expires, leaseID(n)); err != nil {
			t.Fatal(err)
		}
	}
	seed(1, "widgets", "leased", future)
	seed(2, "gadgets", "active", future)
	seed(3, "widgets", "destroyed", future)
	seed(4, "gadgets", "leased", at)
	seed(5, "widgets", "leased", past)
	seed(6, "widgets", "expired", future)
	seed(7, "widgets", "failed", future)

	got, err := store.countLive(db, now)
	if err != nil || got != 2 {
		t.Errorf("countLive = (%d, %v); want 2: the leased and the active lease that have not expired", got, err)
	}
	if got, _ := store.countLive(db, now.Add(-time.Second)); got != 3 {
		t.Errorf("countLive one second earlier = %d; want 3 (the lease expiring at now counts again, the one that expired a second before does not)", got)
	}
	if got, _ := store.countLive(db, now.Add(-2*time.Second)); got != 4 {
		t.Errorf("countLive two seconds earlier = %d; want 4", got)
	}
}

// TS-26-32: POST at capacity answers 409 sandbox_capacity naming the limit, and
// a place frees up when a lease ends or expires.
// Requirement: 26-REQ-5.4
func TestIssue_AtCapacity_TS_26_32(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: capacityConfig(2)})
	env.addWorkspace("gadgets", leaseOwner, "", "ready")
	env.plantSecret("workspace", "gadgets", "GIT_PAT", "x")

	first := env.mustIssue()
	if rec := env.post("gadgets", `{"profile":"tools"}`, apiKeyAuth()); rec.Code != http.StatusCreated {
		t.Fatalf("second lease: status %d: %s", rec.Code, rec.Body)
	}

	rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
	if rec.Code != http.StatusConflict || errorType(t, rec) != "sandbox_capacity" {
		t.Fatalf("third lease: status %d, error_type %q; want 409 sandbox_capacity: %s", rec.Code, errorType(t, rec), rec.Body)
	}
	if !strings.Contains(message(t, rec), "2") {
		t.Errorf("message %q does not name the limit 2", message(t, rec))
	}
	if n := env.countLeases(); n != 2 {
		t.Errorf("%d leases; want the two that fit", n)
	}

	if changed, err := env.store.EndLease(&Lease{ID: first["id"].(string)}, "destroyed", "api"); err != nil || !changed {
		t.Fatalf("EndLease = (%v, %v)", changed, err)
	}
	if rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth()); rec.Code != http.StatusCreated {
		t.Errorf("after one lease ended: status %d; want 201: %s", rec.Code, rec.Body)
	}

	// Leases past their expires_at stop counting although nothing ended them.
	if rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth()); rec.Code != http.StatusConflict {
		t.Fatalf("at capacity again: status %d; want 409", rec.Code)
	}
	env.clock.Advance(2 * time.Hour)
	if rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth()); rec.Code != http.StatusCreated {
		t.Errorf("after the live leases passed expires_at: status %d; want 201: %s", rec.Code, rec.Body)
	}
}

// newFileLeaseDB opens a file-backed database the way hub does, with several
// connections and IMMEDIATE transactions, so that concurrent requests really
// run side by side.
func newFileLeaseDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := apikit.OpenDatabase(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("apikit.OpenDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db.SqlDB
}

// newDeferredLeaseDB opens a file-backed database whose transactions are
// deferred, the default of SQLite: a transaction that counts and then inserts
// takes the write lock only at the insert, so two of them can both pass the
// count. Only the lock in the process keeps the limit with such a handle.
func newDeferredLeaseDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "deferred.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("opening a database: %v", err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { db.Close() })
	return db
}

// TS-26-33: Concurrent POSTs never exceed max_sandboxes, on hub's database
// handle and on one with deferred transactions.
// Requirement: 26-REQ-5.5
func TestIssue_ConcurrentPOSTsRespectTheLimit_Property_TS_26_33(t *testing.T) {
	for name, open := range map[string]func(*testing.T) *sql.DB{
		"hub's database handle": newFileLeaseDB,
		"deferred transactions": newDeferredLeaseDB,
	} {
		t.Run(name, func(t *testing.T) {
			r := rand.New(rand.NewPCG(26, 33))
			for round := range 12 {
				limit := 1 + r.IntN(8)
				existing := r.IntN(limit + 1)
				posts := 1 + r.IntN(32)

				env := newLeaseEnv(t, leaseEnvOptions{config: capacityConfig(float64(limit)), db: open(t)})
				for i := range existing {
					insertRawLease(t, env.db, leaseID(1000+i), "widgets", "leased", HashToken(testToken(1000+i)))
				}

				var wg sync.WaitGroup
				codes := make([]int, posts)
				types := make([]string, posts)
				for i := range posts {
					wg.Add(1)
					go func() {
						defer wg.Done()
						rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
						codes[i] = rec.Code
						if rec.Code == http.StatusConflict {
							// Decoded here, not with errorType: that helper fails the
							// test, which a goroutine must not do.
							var body struct {
								Error struct {
									ErrorType string `json:"error_type"`
								} `json:"error"`
							}
							_ = json.Unmarshal(rec.Body.Bytes(), &body)
							types[i] = body.Error.ErrorType
						}
					}()
				}
				wg.Wait()

				created := 0
				for i, code := range codes {
					switch code {
					case http.StatusCreated:
						created++
					case http.StatusConflict:
						if types[i] != "sandbox_capacity" {
							t.Errorf("round %d: a 409 had error_type %q", round, types[i])
						}
					default:
						t.Errorf("round %d: POST %d answered %d; want 201 or 409", round, i, code)
					}
				}
				want := min(posts, limit-existing)
				if created != want {
					t.Errorf("round %d (limit %d, existing %d, posts %d): %d created; want %d", round, limit, existing, posts, created, want)
				}
				if live, err := env.store.countLive(env.db, env.clock.Now()); err != nil || live > limit {
					t.Errorf("round %d: %d live leases (%v); want at most the limit %d", round, live, err, limit)
				}
			}
		})
	}
}

// TS-26-34: A database error while counting or inserting answers 500, leaves no
// partial row, and does not block the next POST.
// Requirement: 26-REQ-5.6
func TestIssue_DatabaseErrors_TS_26_34(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: capacityConfig(5)})

	// The insert fails once.
	if _, err := env.db.Exec(`CREATE TRIGGER fail_insert BEFORE INSERT ON sandbox_leases BEGIN SELECT RAISE(ABORT, 'injected fault'); END`); err != nil {
		t.Fatal(err)
	}
	rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("failing insert: status %d; want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "injected fault") {
		t.Errorf("the 500 body leaks the cause: %s", rec.Body)
	}
	if env.countLeases() != 0 {
		t.Errorf("%d rows after a failed insert", env.countLeases())
	}
	if _, err := env.db.Exec(`DROP TRIGGER fail_insert`); err != nil {
		t.Fatal(err)
	}
	requireIssuedSoon(t, env, "after the insert fault was removed")

	// The count fails once.
	if _, err := env.db.Exec(`ALTER TABLE sandbox_leases RENAME TO sandbox_leases_away`); err != nil {
		t.Fatal(err)
	}
	if rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth()); rec.Code != http.StatusInternalServerError {
		t.Errorf("failing count: status %d; want 500", rec.Code)
	}
	if _, err := env.db.Exec(`ALTER TABLE sandbox_leases_away RENAME TO sandbox_leases`); err != nil {
		t.Fatal(err)
	}
	if n := env.countLeases(); n != 1 {
		t.Errorf("%d rows; want only the one issued after the first fault", n)
	}
	requireIssuedSoon(t, env, "after the count fault was removed")
}

// requireIssuedSoon posts a lease and fails unless it is created within two
// seconds, which a mutex left locked by an earlier failure would prevent.
func requireIssuedSoon(t *testing.T, env *leaseEnv, when string) {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- env.post("widgets", `{"profile":"tools"}`, apiKeyAuth()) }()
	select {
	case rec := <-done:
		if rec.Code != http.StatusCreated {
			t.Errorf("POST %s: status %d; want 201: %s", when, rec.Code, rec.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("POST %s did not finish within 2s: the lock was not released", when)
	}
}

// failingReader is a random source that always fails.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("random source down") }

// shortReader returns a few bytes and then ends, as a broken source might.
type shortReader struct{}

func (shortReader) Read(p []byte) (int, error) {
	n := copy(p, []byte{1, 2, 3})
	return n, io.EOF
}

// TS-26-37: A random-source failure answers 500 and stores nothing.
// Requirement: 26-REQ-6.3
func TestIssue_RandomSourceFailure_TS_26_37(t *testing.T) {
	for name, source := range map[string]io.Reader{"failing": failingReader{}, "short": shortReader{}} {
		env := newLeaseEnv(t, leaseEnvOptions{rand: source})
		rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s source: status %d; want 500: %s", name, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "random source down") {
			t.Errorf("%s source: the 500 body leaks the cause: %s", name, rec.Body)
		}
		if n := env.countLeases(); n != 0 {
			t.Errorf("%s source: %d leases stored", name, n)
		}
	}
}
