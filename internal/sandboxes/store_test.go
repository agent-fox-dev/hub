package sandboxes

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// A lease written through the store reads back whole, JSON columns included,
// and an unknown id is ErrLeaseNotFound.
func TestLeaseStore_InsertAndGetRoundTrip(t *testing.T) {
	store, _ := newLeaseStore(t, time.Now())
	want := &Lease{
		ID: leaseID(7), Workspace: "widgets", Profile: "tools", Purpose: "triage", State: "leased",
		Image: "registry.example/tools:1", Network: "none", Resources: Resources{CPUs: 1.5, MemoryMB: 512, PIDs: 100}, RequireLandlock: true,
		Secrets: []string{"GIT_PAT"}, OptionalSecrets: []string{"EXTRA"}, Env: map[string]string{"LANG": "C"}, Labels: map[string]string{"k": "v"},
		TTLSeconds: 3600, ExpiresAt: "2030-01-02T04:04:05Z", LeaseExpiresAt: strPtr("2030-01-02T03:09:05Z"), CreatedBy: "u1",
		CreatedAt: "2030-01-02T03:04:05Z", UpdatedAt: "2030-01-02T03:04:05Z",
	}
	if err := store.InsertLease(store.db, want, HashToken(testToken(7))); err != nil {
		t.Fatalf("InsertLease: %v", err)
	}
	got, err := store.GetLease(want.ID)
	if err != nil || fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
		t.Errorf("read back (%+v, %v)\nwant      %+v", got, err, want)
	}
	// Nil lists and maps are stored and read back as empty ones.
	bare := &Lease{ID: leaseID(8), Workspace: "w", Profile: "p", State: "leased", Image: "i", Network: "none", TTLSeconds: 60, ExpiresAt: "2030-01-02T04:04:05Z", CreatedBy: "u", CreatedAt: "x", UpdatedAt: "x"}
	if err := store.InsertLease(store.db, bare, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetLease(bare.ID); got.Secrets == nil || got.Env == nil || got.Labels == nil || got.LeaseExpiresAt != nil {
		t.Errorf("a bare lease reads %+v; want empty lists and maps", got)
	}
	if _, err := store.GetLease(leaseID(9)); !errors.Is(err, ErrLeaseNotFound) {
		t.Errorf("GetLease(unknown) = %v; want ErrLeaseNotFound", err)
	}
}

// Ids are sbx_ and 32 hex characters, tokens sbl_ and 44 RawURL characters of
// 33 random bytes, and a random source that fails or runs short is an error.
func TestTokens_ShapeAndRandomness(t *testing.T) {
	id, err := generateLeaseID(strings.NewReader(strings.Repeat("x", 64)))
	if err != nil || !regexp.MustCompile(`^sbx_[0-9a-f]{32}$`).MatchString(id) {
		t.Errorf("generateLeaseID = (%q, %v)", id, err)
	}
	token, err := generateToken(strings.NewReader(strings.Repeat("y", 64)))
	raw, decErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "sbl_"))
	if err != nil || len(token) != 48 || !tokenShape.MatchString(token) || decErr != nil || len(raw) != 33 {
		t.Errorf("generateToken = (%q, %v)", token, err)
	}
	for name, r := range map[string]io.Reader{"failing": failingReader{}, "short": strings.NewReader("abc")} {
		if _, err := generateToken(r); err == nil {
			t.Errorf("%s source: generateToken returned no error", name)
		}
	}
	sum := sha256.Sum256([]byte(token))
	if HashToken(token) != hex.EncodeToString(sum[:]) {
		t.Error("HashToken is not the lowercase hex SHA-256")
	}
}

// failingReader is a random source that always fails.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("random source down") }

// LookupByToken finds a live lease by its hash and refuses, with the one error,
// a malformed, unknown or padded token and the token of an ended lease.
func TestLeaseStore_LookupByToken(t *testing.T) {
	store, db := newLeaseStore(t, time.Now())
	insertRawLease(t, db, leaseID(1), "widgets", "leased", HashToken(testToken(1)))
	insertRawLease(t, db, leaseID(2), "gadgets", "active", HashToken(testToken(2)))
	for n := 1; n <= 2; n++ {
		if got, err := store.LookupByToken(testToken(n)); err != nil || got.ID != leaseID(n) {
			t.Errorf("LookupByToken(token %d) = (%v, %v)", n, got, err)
		}
	}
	// Terminal leases that kept their hash: the state filter alone refuses them.
	for n, state := range map[int]string{3: "destroyed", 4: "expired", 5: "failed"} {
		insertRawLease(t, db, leaseID(n), "widgets", state, HashToken(testToken(n)))
	}
	for name, token := range map[string]string{
		"empty": "", "too short": "abc", "no prefix": "xyz_" + strings.Repeat("a", 44), "47 characters": testToken(1)[:47], "49 characters": testToken(1) + "0",
		"illegal character": "sbl_" + strings.Repeat("a", 43) + "!", "unknown": testToken(99), "destroyed": testToken(3), "expired": testToken(4), "failed": testToken(5),
	} {
		if got, err := store.LookupByToken(token); got != nil || err != ErrLeaseNotFound {
			t.Errorf("%s: LookupByToken = (%v, %v); want (nil, ErrLeaseNotFound)", name, got, err)
		}
	}
}

// Every transition is one UPDATE conditioned on the state it expects, clears
// the token hash and stamps ended_at on an ending, and never leaves a terminal
// state; an unknown target or source state is an error.
func TestLeaseStore_TransitionsAreConditional(t *testing.T) {
	now := time.Date(2030, 1, 2, 5, 0, 0, 0, time.UTC)
	stamp := "2030-01-02T05:00:00Z"
	store, db := newLeaseStore(t, now)
	for n := 1; n <= 6; n++ {
		insertRawLease(t, db, leaseID(n), "widgets", "leased", HashToken(testToken(n)))
	}
	must := func(what string, want bool) func(changed bool, err error) {
		return func(changed bool, err error) {
			t.Helper()
			if err != nil || changed != want {
				t.Errorf("%s = (%v, %v); want (%v, nil)", what, changed, err, want)
			}
		}
	}
	// leased -> active keeps the token and records the box; a second MarkActive
	// and a FailLease on an active lease change nothing.
	must("MarkActive", true)(store.MarkActive(leaseID(1), "box-1"))
	must("MarkActive again", false)(store.MarkActive(leaseID(1), "box-2"))
	must("FailLease on active", false)(store.FailLease(leaseID(1), "late"))
	if l, _ := store.GetLease(leaseID(1)); l.State != "active" || *l.SandboxID != "box-1" || l.EndedAt != nil || l.UpdatedAt != stamp {
		t.Errorf("after MarkActive: %+v", l)
	}
	if s := readState(t, db, leaseID(1)); !s.hash.Valid {
		t.Error("MarkActive cleared the token hash")
	}
	// leased -> failed records the message and ends the lease without a reason.
	must("FailLease", true)(store.FailLease(leaseID(2), "secret_missing: GIT_PAT"))
	if l, _ := store.GetLease(leaseID(2)); l.State != "failed" || *l.Error != "secret_missing: GIT_PAT" || *l.EndedAt != stamp || l.EndReason != nil || readState(t, db, leaseID(2)).hash.Valid {
		t.Errorf("after FailLease: %+v", l)
	}
	// leased -> expired, leased -> destroyed, active -> destroyed record the reason.
	must("ExpireLease", true)(store.ExpireLease(leaseID(3), "expired"))
	must("DestroyLease from leased", true)(store.DestroyLease(leaseID(4), "leased", "api"))
	must("DestroyLease from active", true)(store.DestroyLease(leaseID(1), "active", "client"))
	for n, want := range map[int][2]string{3: {"expired", "expired"}, 4: {"destroyed", "api"}, 1: {"destroyed", "client"}} {
		if s := readState(t, db, leaseID(n)); s.state != want[0] || s.endReason != want[1] || s.endedAt != stamp || s.updatedAt != stamp || s.hash.Valid {
			t.Errorf("lease %d reads %+v; want %v with the hash cleared", n, s, want)
		}
	}
	// A wrong expected state, a terminal lease and an unknown state change nothing.
	must("DestroyLease(leased, from active)", false)(store.DestroyLease(leaseID(5), "active", "client"))
	must("ExpireLease on active", false)(func() (bool, error) {
		store.MarkActive(leaseID(6), "b")
		return store.ExpireLease(leaseID(6), "expired")
	}())
	for _, id := range []string{leaseID(1), leaseID(2), leaseID(3)} {
		must("MarkActive on terminal", false)(store.MarkActive(id, "box"))
		must("DestroyLease on terminal", false)(store.DestroyLease(id, "active", "api"))
		must("EndLease on terminal", false)(store.EndLease(&Lease{ID: id}, "destroyed", "api"))
	}
	for _, from := range []string{"destroyed", "bogus", ""} {
		if changed, err := store.DestroyLease(leaseID(5), from, "api"); err == nil || changed {
			t.Errorf("DestroyLease from %q = (%v, %v); want an error", from, changed, err)
		}
	}
	for _, target := range []string{"leased", "active", "bogus"} {
		if changed, err := store.EndLease(&Lease{ID: leaseID(5)}, target, "api"); err == nil || changed {
			t.Errorf("EndLease to %q = (%v, %v); want an error", target, changed, err)
		}
	}
	must("EndLease to expired", true)(store.EndLease(&Lease{ID: leaseID(5)}, "expired", "timeout"))
}

// ListLeases is newest first and capped at 200, live only unless all;
// GetWorkspaceLease finds a lease of the workspace in any state and refuses
// a malformed, unknown or foreign id with ErrLeaseNotFound.
func TestLeaseStore_ListAndGetWorkspaceLease(t *testing.T) {
	store, db := newLeaseStore(t, time.Now())
	for n := 1; n <= 205; n++ {
		insertRawLease(t, db, leaseID(n), "widgets", "leased", HashToken(testToken(n)))
		testutil.Exec(t, db, `UPDATE sandbox_leases SET created_at = ? WHERE id = ?`, fmt.Sprintf("2030-01-02T03:%02d:%02dZ", n/60, n%60), leaseID(n))
	}
	insertRawLease(t, db, leaseID(300), "gadgets", "destroyed", nil)
	live, err := store.ListLeases("widgets", false)
	if err != nil || len(live) != 200 || live[0].ID != leaseID(205) || live[199].ID != leaseID(6) {
		t.Errorf("ListLeases(widgets) = %d leases (%v), first %s, last %s; want 200 newest first", len(live), err, live[0].ID, live[199].ID)
	}
	if all, _ := store.ListLeases("gadgets", true); len(all) != 1 || all[0].ID != leaseID(300) {
		t.Errorf("ListLeases(gadgets, all) = %v", ids2(all))
	}
	if none, _ := store.ListLeases("gadgets", false); len(none) != 0 || none == nil {
		t.Errorf("ListLeases(gadgets) = %v; want an empty, non-nil list", none)
	}
	if every, _ := store.ListLeases("", true); len(every) != 200 {
		t.Errorf("ListLeases(every workspace) = %d; want the cap", len(every))
	}
	if l, err := store.GetWorkspaceLease("gadgets", leaseID(300)); err != nil || l.State != "destroyed" {
		t.Errorf("GetWorkspaceLease = (%v, %v)", l, err)
	}
	for name, id := range map[string]string{"malformed": "xyz", "upper-case": "sbx_" + strings.Repeat("A", 32), "unknown": leaseID(999), "foreign": leaseID(300)} {
		if _, err := store.GetWorkspaceLease("widgets", id); !errors.Is(err, ErrLeaseNotFound) {
			t.Errorf("%s: GetWorkspaceLease = %v; want ErrLeaseNotFound", name, err)
		}
	}
}

func ids2(leases []*Lease) []string {
	out := make([]string, len(leases))
	for i, l := range leases {
		out[i] = l.ID
	}
	return out
}

// countLive counts the leased and active leases across workspaces whose
// expires_at is ahead, less the leased ones whose deadline for a Create is at or
// before now.
func TestLeaseStore_CountLive(t *testing.T) {
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	store, db := newLeaseStore(t, now)
	future, at, past := "2030-01-02T03:04:06Z", "2030-01-02T03:04:05Z", "2030-01-02T03:04:04Z"
	seed := func(n int, workspace, state, expires string, deadline any) {
		insertRawLease(t, db, leaseID(n), workspace, state, HashToken(testToken(n)))
		testutil.Exec(t, db, `UPDATE sandbox_leases SET expires_at = ?, lease_expires_at = ? WHERE id = ?`, expires, deadline, leaseID(n))
	}
	seed(1, "widgets", "leased", future, nil)
	seed(2, "gadgets", "active", future, nil)
	seed(3, "widgets", "destroyed", future, nil)
	seed(4, "gadgets", "leased", at, nil)
	seed(5, "widgets", "leased", past, nil)
	seed(6, "widgets", "expired", future, nil)
	seed(7, "widgets", "leased", future, future)
	seed(8, "widgets", "leased", future, at)
	seed(9, "widgets", "active", future, past)
	if got, err := store.countLive(db, now); err != nil || got != 4 {
		t.Errorf("countLive = (%d, %v); want 4: leases 1, 2, 7 and 9", got, err)
	}
	if got, _ := store.countLive(db, now.Add(-2*time.Second)); got != 7 {
		t.Errorf("countLive two seconds earlier = %d; want 7", got)
	}
}
