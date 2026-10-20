package sandboxes

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"
)

// leaseExpiryConfig is the default section with the given lease_timeout.
func leaseExpiryConfig(leaseTimeout string) *Config {
	cfg := defaultLeaseConfig()
	cfg.LeaseTimeout = leaseTimeout
	return cfg
}

// storedLeaseExpiry reads the lease_expires_at column of a lease; the second
// result is false for NULL.
func storedLeaseExpiry(t *testing.T, db *sql.DB, id string) (string, bool) {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(`SELECT lease_expires_at FROM sandbox_leases WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("reading lease_expires_at of %s: %v", id, err)
	}
	return v.String, v.Valid
}

// TS-28-6: POST stores lease_expires_at as the earlier of created_at +
// lease_timeout and expires_at, written like every other timestamp.
// Requirement: 28-REQ-2.1
func TestIssue_LeaseExpiresAt_TS_28_6(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: leaseExpiryConfig("5m"), start: time.Date(2030, 1, 2, 3, 4, 5, 123_000_000, time.UTC)})

	for _, tc := range []struct {
		name string
		body string
		// want is the offset of lease_expires_at from created_at.
		want time.Duration
	}{
		{"ttl above lease_timeout", `{"profile":"tools","ttl":"1h"}`, 5 * time.Minute},
		{"ttl below lease_timeout", `{"profile":"tools","ttl":"2m"}`, 2 * time.Minute},
		{"ttl equal to lease_timeout", `{"profile":"tools","ttl":"5m"}`, 5 * time.Minute},
		{"profile ttl", `{"profile":"tools"}`, 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.clock.Advance(time.Second)
			rec := env.post("widgets", tc.body, apiKeyAuth())
			if rec.Code != http.StatusCreated {
				t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
			}
			body := jsonObject(t, rec)
			created, err := apikit.ParseUTC(body["created_at"].(string))
			if err != nil {
				t.Fatalf("created_at: %v", err)
			}
			want := apikit.FormatUTC(created.Add(tc.want))
			if got := body["lease_expires_at"]; got != want {
				t.Errorf("lease_expires_at = %v; want %s", got, want)
			}
			if tc.want < 5*time.Minute && body["lease_expires_at"] != body["expires_at"] {
				t.Errorf("lease_expires_at = %v; want expires_at %v when the TTL is shorter", body["lease_expires_at"], body["expires_at"])
			}
			stored, ok := storedLeaseExpiry(t, env.db, body["id"].(string))
			if !ok || stored != want {
				t.Errorf("stored lease_expires_at = (%q, %v); want %s", stored, ok, want)
			}
		})
	}
}

// legacyLeaseSchema is the sandbox_leases table as it was before the column
// lease_expires_at: the current definition without that line.
func legacyLeaseSchema(t *testing.T) string {
	t.Helper()
	var kept []string
	removed := 0
	for line := range strings.SplitSeq(leaseSchemaSQL, "\n") {
		if strings.Contains(line, "lease_expires_at") {
			removed++
			continue
		}
		kept = append(kept, line)
	}
	if removed > 1 {
		t.Fatalf("the schema names lease_expires_at on %d lines", removed)
	}
	return strings.Join(kept, "\n")
}

// leaseColumnCount returns how often PRAGMA table_info lists the column.
func leaseColumnCount(t *testing.T, db *sql.DB, column string) int {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info('sandbox_leases')`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scanning table_info: %v", err)
		}
		if name == column {
			n++
		}
	}
	return n
}

// TS-28-7: The migration adds the column once, leaves old rows NULL and
// tolerates a nil database.
// Requirement: 28-REQ-2.2
func TestMigrateLeaseExpiry_TS_28_7(t *testing.T) {
	t.Run("a table from before the column", func(t *testing.T) {
		db := openLeaseDB(t)
		if _, err := db.Exec(legacyLeaseSchema(t)); err != nil {
			t.Fatalf("creating the legacy table: %v", err)
		}
		if n := leaseColumnCount(t, db, "lease_expires_at"); n != 0 {
			t.Fatalf("the legacy table has the column already (%d)", n)
		}
		insertRawLease(t, db, leaseID(1), "widgets", "leased", testToken(1))

		// InitSchema runs first at every start; on an existing table it changes nothing.
		if err := InitSchema(db); err != nil {
			t.Fatalf("InitSchema: %v", err)
		}
		for run := 1; run <= 2; run++ {
			if err := MigrateLeaseExpiry(db); err != nil {
				t.Fatalf("run %d of the migration returned error: %v", run, err)
			}
		}
		if n := leaseColumnCount(t, db, "lease_expires_at"); n != 1 {
			t.Errorf("PRAGMA table_info lists lease_expires_at %d times; want once", n)
		}
		if v, ok := storedLeaseExpiry(t, db, leaseID(1)); ok {
			t.Errorf("the pre-existing row reads lease_expires_at %q; want NULL", v)
		}
	})

	t.Run("a new table has the column and the migration changes nothing", func(t *testing.T) {
		db := openLeaseDB(t)
		if err := InitSchema(db); err != nil {
			t.Fatalf("InitSchema: %v", err)
		}
		insertRawLease(t, db, leaseID(1), "widgets", "leased", testToken(1))
		before := wholeRows(t, db)
		if err := MigrateLeaseExpiry(db); err != nil {
			t.Fatalf("the migration returned error: %v", err)
		}
		if n := leaseColumnCount(t, db, "lease_expires_at"); n != 1 {
			t.Errorf("PRAGMA table_info lists lease_expires_at %d times; want once", n)
		}
		if after := wholeRows(t, db); after != before {
			t.Errorf("the migration changed the rows:\n%s\nwant\n%s", after, before)
		}
	})

	t.Run("a nil database", func(t *testing.T) {
		if err := MigrateLeaseExpiry(nil); err != nil {
			t.Errorf("MigrateLeaseExpiry(nil) = %v; want nil", err)
		}
	})

	t.Run("a database without the table reports the failure", func(t *testing.T) {
		if err := MigrateLeaseExpiry(openLeaseDB(t)); err == nil {
			t.Error("MigrateLeaseExpiry on a database with no sandbox_leases table returned nil")
		}
	})
}

// findLease returns the lease with this id in a list.
func findLease(t *testing.T, list []map[string]any, id string) map[string]any {
	t.Helper()
	for _, l := range list {
		if l["id"] == id {
			return l
		}
	}
	t.Fatalf("lease %s is not in %v", id, ids(list))
	return nil
}

// TS-28-8: Every lease representation carries lease_expires_at, an RFC 3339
// string, or null for a row from before the column.
// Requirement: 28-REQ-2.3
func TestRender_LeaseExpiresAt_TS_28_8(t *testing.T) {
	cfg := leaseExpiryConfig("5m")
	cfg.Profiles[0].OptionalSecrets = nil
	env := newLeaseEnv(t, leaseEnvOptions{config: cfg})

	issued := env.mustIssue()
	id := issued["id"].(string)
	legacy := env.insertLeaseAt(2, "widgets", "leased", env.clock.Now())

	check := func(what string, lease map[string]any, wantNull bool) {
		t.Helper()
		v, present := lease["lease_expires_at"]
		if !present {
			t.Errorf("%s: no lease_expires_at key in %v", what, sortedKeys(lease))
			return
		}
		if wantNull {
			if v != nil {
				t.Errorf("%s: lease_expires_at = %v; want null", what, v)
			}
			return
		}
		s, _ := v.(string)
		if _, err := apikit.ParseUTC(s); err != nil || s == "" {
			t.Errorf("%s: lease_expires_at = %v; want an RFC 3339 string", what, v)
		}
	}

	check("POST 201", issued, false)
	check("GET one", jsonObject(t, env.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth())), false)
	check("GET one (legacy)", jsonObject(t, env.get("/api/v1/workspaces/widgets/sandboxes/"+legacy, apiKeyAuth())), true)
	for _, tc := range []struct {
		what, path string
		auth       *apikit.AuthInfo
	}{
		{"workspace list", "/api/v1/workspaces/widgets/sandboxes", apiKeyAuth()},
		{"workspace list all=true", "/api/v1/workspaces/widgets/sandboxes?all=true", apiKeyAuth()},
		{"admin list", "/api/v1/sandboxes", adminAuth()},
		{"admin list all=true", "/api/v1/sandboxes?all=true", adminAuth()},
	} {
		list := leaseList(t, env.get(tc.path, tc.auth))
		check(tc.what, findLease(t, list, id), false)
		check(tc.what+" (legacy)", findLease(t, list, legacy), true)
	}
}

// TS-28-10: The capacity count leaves out leased leases whose lease_expires_at
// is at or before now, the moment itself included.
// Requirement: 28-REQ-2.5
func TestCapacity_LeaseDeadline_TS_28_10(t *testing.T) {
	// seed adds a lease of the given state whose expires_at is an hour away and
	// whose lease_expires_at is the given offset from the clock; a nil offset
	// leaves the column NULL.
	seed := func(t *testing.T, env *leaseEnv, n int, state string, offset *time.Duration) {
		t.Helper()
		id := env.insertLeaseAt(n, "widgets", state, env.clock.Now())
		if offset == nil {
			return
		}
		if _, err := env.db.Exec(`UPDATE sandbox_leases SET lease_expires_at = ? WHERE id = ?`,
			apikit.FormatUTC(env.clock.Now().Add(*offset)), id); err != nil {
			t.Fatal(err)
		}
	}
	at := func(d time.Duration) *time.Duration { return &d }

	for _, tc := range []struct {
		name    string
		state   string
		offset  *time.Duration
		counted bool
	}{
		{"deadline before now", "leased", at(-time.Second), false},
		{"deadline exactly now", "leased", at(0), false},
		{"deadline one second ahead", "leased", at(time.Second), true},
		{"no deadline, expires_at ahead", "leased", nil, true},
		{"active past its deadline", "active", at(-time.Minute), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newLeaseEnv(t, leaseEnvOptions{config: capacityConfig(1)})
			seed(t, env, 1, tc.state, tc.offset)

			rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
			if tc.counted {
				if rec.Code != http.StatusConflict || errorType(t, rec) != "sandbox_capacity" {
					t.Errorf("POST answered %d %q; want 409 sandbox_capacity: %s", rec.Code, errorType(t, rec), rec.Body)
				}
				return
			}
			if rec.Code != http.StatusCreated {
				t.Errorf("POST answered %d; want 201 because the lease no longer counts: %s", rec.Code, rec.Body)
			}
		})
	}

	t.Run("countLive", func(t *testing.T) {
		now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
		store, db := newLeaseStore(t, now)
		for n, deadline := range []string{"2030-01-02T03:04:04Z", "2030-01-02T03:04:05Z", "2030-01-02T03:04:06Z"} {
			insertRawLease(t, db, leaseID(n+1), "widgets", "leased", HashToken(testToken(n+1)))
			if _, err := db.Exec(`UPDATE sandbox_leases SET lease_expires_at = ? WHERE id = ?`, deadline, leaseID(n+1)); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := store.countLive(db, now); err != nil || got != 1 {
			t.Errorf("countLive = (%d, %v); want 1: only the lease whose deadline is a second ahead", got, err)
		}
	})
}
