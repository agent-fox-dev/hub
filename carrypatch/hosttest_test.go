package carrypatch

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/txsvc/apikit"
)

// The module's tests run against doubles of the host: in-process locks with
// the semantics of the hub's wslock, and the hub's workspace access rule
// (owner and admin token read and write, a member of the workspace's org
// reads, anyone else sees no workspace) read from the test database.

func TestMain(m *testing.M) {
	// Several fixtures init bare repos and clone empty ones without naming
	// the branch, then push main; that needs init.defaultBranch=main, which a
	// stock git (default branch master) does not set.
	os.Setenv("GIT_CONFIG_COUNT", "1")
	os.Setenv("GIT_CONFIG_KEY_0", "init.defaultBranch")
	os.Setenv("GIT_CONFIG_VALUE_0", "main")

	testLocks = newLockSet()
	SetHost(Host{
		Lock:         testLocks.Lock,
		TryLock:      testLocks.TryLock,
		BeginRebuild: testLocks.BeginRebuild,
		Authorize:    testAuthorize,
	})
	os.Exit(m.Run())
}

// testLocks is the lock set the tests bind the module to; tests read
// RebuildActive from it.
var testLocks *lockSet

// lockSet is an in-process per-workspace lock with a rebuild guard, the
// behaviour of the hub's wslock package.
type lockSet struct {
	mu            sync.Mutex
	locks         map[string]*sync.Mutex
	rebuildGuards map[string]*struct{ _ byte }
}

func newLockSet() *lockSet {
	return &lockSet{locks: map[string]*sync.Mutex{}, rebuildGuards: map[string]*struct{ _ byte }{}}
}

func (s *lockSet) get(slug string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[slug]
	if !ok {
		l = &sync.Mutex{}
		s.locks[slug] = l
	}
	return l
}

// Lock takes the workspace lock, waiting for it.
func (s *lockSet) Lock(slug string) func() {
	l := s.get(slug)
	l.Lock()
	return l.Unlock
}

// TryLock takes the workspace lock if it is free.
func (s *lockSet) TryLock(slug string) (func(), bool) {
	l := s.get(slug)
	if !l.TryLock() {
		return nil, false
	}
	return l.Unlock, true
}

// BeginRebuild marks a rebuild active on the workspace.
func (s *lockSet) BeginRebuild(slug string) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, set := s.rebuildGuards[slug]; set {
		return func() {}, false
	}
	token := &struct{ _ byte }{}
	s.rebuildGuards[slug] = token
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.rebuildGuards[slug] == token {
			delete(s.rebuildGuards, slug)
		}
	}, true
}

// RebuildActive reports whether a rebuild is marked active on the workspace.
func (s *lockSet) RebuildActive(slug string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, set := s.rebuildGuards[slug]
	return set
}

// testAuthorize is the hub's workspace access rule over the test database: an
// admin token and the owner read and write; a user with an org_members row for
// the workspace's org reads, and is refused a write with 403; everyone else,
// and an unknown slug, is 404. A missing org_members table counts as no
// membership.
func testAuthorize(db *sql.DB, auth *apikit.AuthInfo, slug string, need Need) (int, string) {
	if auth == nil {
		return http.StatusUnauthorized, "authentication required"
	}
	var ownerID string
	var orgID sql.NullString
	err := db.QueryRow(`SELECT owner_id, org_id FROM workspaces WHERE slug = ?`, slug).Scan(&ownerID, &orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound, "workspace not found"
	}
	if err != nil {
		return http.StatusInternalServerError, "internal server error"
	}
	if auth.CredentialType == "admin_token" || (auth.UserID != "" && auth.UserID == ownerID) {
		return 0, ""
	}
	if auth.UserID == "" || !orgID.Valid || orgID.String == "" {
		return http.StatusNotFound, "workspace not found"
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?`, orgID.String, auth.UserID).Scan(&n); err != nil || n == 0 {
		return http.StatusNotFound, "workspace not found"
	}
	if need == NeedWrite {
		return http.StatusForbidden, "org members have read-only access to this workspace"
	}
	return 0, ""
}
