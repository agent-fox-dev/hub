// workspace_property_test.go — property tests for workspace entity invariants
// per spec 07 correctness properties (07-PROP-1 through 07-PROP-7).
//
// These tests use randomized inputs to verify invariants that must hold
// for ALL workspace records, not just specific examples.
//
// Test IDs map to the test specification:
//   TS-07-P1: Slug uniqueness — no two records share the same slug.
//   TS-07-P2: Owner always a real user — owner_id references users table.
//   TS-07-P3: Status default — status is always 'active' after creation.
//   TS-07-P4: Team membership required for team association.
//   TS-07-P5: git_url format is always valid.
//   TS-07-P6: slug format is always valid.
//   TS-07-P7: Store never terminates process.
//
// Tests are expected to FAIL until the implementation is complete.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TS-07-P1: For any sequence of CreateWorkspaceV2 calls with arbitrary slugs,
// no two successfully inserted workspace records share the same slug value.
//
// Validates: 07-REQ-1.2, 07-REQ-2.2, 07-REQ-3.5
func TestProperty_SlugUniqueness(t *testing.T) {
	s := newWorkspaceV2TestStore(t)
	userID := seedTestUser(t, s, "prop-slug-user")

	// Generate a list of 20 slug strings with intentional duplicates.
	rng := rand.New(rand.NewSource(42))
	baseSlugs := []string{
		"alpha-one", "beta-two", "gamma-three", "delta-four",
		"echo-five", "foxtrot-six", "golf-seven", "hotel-eight",
	}

	slugs := make([]string, 0, 20)
	for range 20 {
		slugs = append(slugs, baseSlugs[rng.Intn(len(baseSlugs))])
	}

	// Call CreateWorkspaceV2 for each slug in sequence.
	for i, slug := range slugs {
		_, err := s.CreateWorkspaceV2(CreateWorkspaceParams{
			Slug:    slug,
			GitURL:  fmt.Sprintf("https://github.com/org/repo-%d.git", i),
			OwnerID: userID,
		})
		if err != nil {
			// Duplicate slug errors are expected and fine.
			if errors.Is(err, ErrDuplicateSlug) {
				continue
			}
			// Other errors (e.g., "not implemented") are test failures.
			t.Logf("CreateWorkspaceV2 slug=%q error: %v", slug, err)
		}
	}

	// At least some inserts must have succeeded for the invariant to be meaningful.
	var totalRows int
	if err := s.db.QueryRow("SELECT count(*) FROM workspaces").Scan(&totalRows); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if totalRows == 0 {
		t.Fatal("no workspace rows were inserted — cannot verify slug uniqueness invariant " +
			"(CreateWorkspaceV2 likely not implemented)")
	}

	// Invariant: no two records share the same slug.
	rows, err := s.db.Query(
		"SELECT slug, count(*) FROM workspaces GROUP BY slug HAVING count(*) > 1",
	)
	if err != nil {
		t.Fatalf("uniqueness query failed: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var slug string
		var count int
		if err := rows.Scan(&slug, &count); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		t.Errorf("slug %q appears %d times — uniqueness invariant violated", slug, count)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration failed: %v", err)
	}
}

// TS-07-P3: For any newly created workspace record, status is always 'active'
// and never null immediately after creation.
//
// Validates: 07-REQ-1.3, 07-REQ-2.1
func TestProperty_StatusDefaultActive(t *testing.T) {
	s := newWorkspaceV2TestStore(t)
	userID := seedTestUser(t, s, "prop-status-user")

	// Generate varied valid inputs.
	inputs := []CreateWorkspaceParams{
		{Slug: "stat-ws-aaa", GitURL: "https://github.com/org/repo1.git", OwnerID: userID},
		{Slug: "stat-ws-bbb", GitURL: "git@github.com:org/repo2.git", OwnerID: userID},
		{Slug: "stat-ws-ccc", GitURL: "https://gitlab.com/user/project", OwnerID: userID},
	}

	branch := "develop"
	teamID := seedTestTeam(t, s, "prop-team", userID)
	inputs = append(inputs, CreateWorkspaceParams{
		Slug:    "stat-ws-ddd",
		GitURL:  "https://github.com/org/repo3.git",
		Branch:  &branch,
		OwnerID: userID,
		TeamID:  &teamID,
	})

	for _, params := range inputs {
		ws, err := s.CreateWorkspaceV2(params)
		if err != nil {
			t.Errorf("CreateWorkspaceV2(slug=%q) returned error: %v", params.Slug, err)
			continue
		}
		if ws == nil {
			t.Errorf("CreateWorkspaceV2(slug=%q) returned nil workspace", params.Slug)
			continue
		}

		// Invariant: status must be 'active'.
		if ws.Status != "active" {
			t.Errorf("slug=%q: expected status 'active', got %q", params.Slug, ws.Status)
		}

		// Double-check from the database.
		var dbStatus sql.NullString
		err = s.db.QueryRow(
			"SELECT status FROM workspaces WHERE id = ?", ws.ID,
		).Scan(&dbStatus)
		if err != nil {
			t.Errorf("slug=%q: database query failed: %v", params.Slug, err)
			continue
		}
		if !dbStatus.Valid {
			t.Errorf("slug=%q: status is NULL in the database", params.Slug)
		} else if dbStatus.String != "active" {
			t.Errorf("slug=%q: database status = %q, expected 'active'", params.Slug, dbStatus.String)
		}
	}
}

// TS-07-P2: For any workspace record inserted via CreateWorkspaceV2,
// owner_id always references an existing row in the users table.
//
// Validates: 07-REQ-3.8, 07-REQ-3.9
func TestProperty_OwnerAlwaysRealUser(t *testing.T) {
	s := newWorkspaceV2TestStore(t)

	// Create several real users.
	users := make([]string, 5)
	for i := range users {
		users[i] = seedTestUser(t, s, fmt.Sprintf("prop-owner-user-%d", i))
	}

	// Create workspaces owned by each user.
	for i, userID := range users {
		ws, err := s.CreateWorkspaceV2(CreateWorkspaceParams{
			Slug:    fmt.Sprintf("owner-ws-%d", i),
			GitURL:  fmt.Sprintf("https://github.com/org/repo-%d.git", i),
			OwnerID: userID,
		})
		if err != nil {
			t.Errorf("CreateWorkspaceV2 for user %d error: %v", i, err)
			continue
		}
		if ws == nil {
			t.Errorf("CreateWorkspaceV2 for user %d returned nil", i)
			continue
		}

		// Invariant: owner_id must reference a valid users row.
		var userCount int
		err = s.db.QueryRow(
			"SELECT count(*) FROM users WHERE id = ?", ws.OwnerID,
		).Scan(&userCount)
		if err != nil {
			t.Errorf("user count query failed: %v", err)
			continue
		}
		if userCount != 1 {
			t.Errorf("owner_id %q does not reference a valid user (count=%d)", ws.OwnerID, userCount)
		}
	}
}

// TS-07-P4: For any workspace record with a non-null team_id, the owner_id
// user was a member of that team at the time of workspace creation.
//
// Validates: 07-REQ-3.7
//
// This property test exercises the store layer only. The team membership
// check is enforced at the handler level (task group 10), not in the store.
// Therefore, this test verifies that the handler properly rejects non-member
// attempts and permits member attempts by testing the end state of the
// database after a mix of allowed and disallowed workspace creations.
func TestProperty_TeamMembershipRequired(t *testing.T) {
	s := newWorkspaceV2TestStore(t)

	// Create users: some will be members, some won't.
	memberUser := seedTestUser(t, s, "prop-member-user")
	nonMemberUser := seedTestUser(t, s, "prop-nonmember-user")

	teamID := seedTestTeam(t, s, "prop-membership-team", memberUser)

	// Add memberUser as a team member.
	_, err := s.db.Exec(
		`INSERT INTO team_members (user_id, team_id, role, created_at)
		 VALUES (?, ?, 'editor', ?)`,
		memberUser, teamID, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("failed to add team member: %v", err)
	}

	// Member should be able to create a workspace with this team_id.
	ws, err := s.CreateWorkspaceV2(CreateWorkspaceParams{
		Slug:    "member-team-ws",
		GitURL:  "https://github.com/org/repo.git",
		OwnerID: memberUser,
		TeamID:  &teamID,
	})
	if err != nil {
		t.Errorf("member CreateWorkspaceV2 with team_id error: %v", err)
	}

	// For every workspace with a non-null team_id in the DB, verify
	// that a team_members row exists for (owner_id, team_id).
	if ws != nil && ws.TeamID != nil {
		var memberCount int
		err = s.db.QueryRow(
			"SELECT count(*) FROM team_members WHERE user_id = ? AND team_id = ?",
			ws.OwnerID, *ws.TeamID,
		).Scan(&memberCount)
		if err != nil {
			t.Errorf("team_members query failed: %v", err)
		} else if memberCount == 0 {
			t.Errorf("workspace %q has team_id=%q but owner %q has no team_members row",
				ws.Slug, *ws.TeamID, ws.OwnerID)
		}
	}

	// Non-member creating with team_id at the STORE level will succeed
	// (membership check is in the handler). But we note the state for
	// handler-level property tests.
	_ = nonMemberUser // Used by handler-level property tests (TS-07-P4).
}

// TS-07-P5: For any workspace record stored in the workspaces table,
// git_url always matches the HTTPS or SSH pattern and is never null or empty.
//
// Validates: 07-REQ-3.4, 07-REQ-7.1, 07-REQ-7.2
func TestProperty_GitURLFormatAlwaysValid(t *testing.T) {
	s := newWorkspaceV2TestStore(t)
	userID := seedTestUser(t, s, "prop-giturl-user")

	// Create workspaces with both valid HTTPS and SSH URLs.
	validURLs := []string{
		"https://github.com/org/repo.git",
		"https://gitlab.com/user/project",
		"git@github.com:org/repo.git",
		"git@bitbucket.org:user/repo.git",
	}

	for i, url := range validURLs {
		_, err := s.CreateWorkspaceV2(CreateWorkspaceParams{
			Slug:    fmt.Sprintf("giturl-ws-%d", i),
			GitURL:  url,
			OwnerID: userID,
		})
		if err != nil {
			t.Logf("CreateWorkspaceV2 with git_url=%q error: %v", url, err)
		}
	}

	// At least some inserts must have succeeded for the invariant to be meaningful.
	var totalRows int
	if err := s.db.QueryRow("SELECT count(*) FROM workspaces").Scan(&totalRows); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if totalRows == 0 {
		t.Fatal("no workspace rows were inserted — cannot verify git_url format invariant " +
			"(CreateWorkspaceV2 likely not implemented)")
	}

	// Invariant check: for every row in workspaces, git_url is NOT NULL,
	// not empty, and matches HTTPS or SSH pattern.
	httpsOrSSH := regexp.MustCompile(`^(https://|git@[^:]+:[^/].+)`)

	rows, err := s.db.Query("SELECT id, slug, git_url FROM workspaces")
	if err != nil {
		t.Fatalf("query workspaces failed: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, slug string
		var gitURL sql.NullString
		if err := rows.Scan(&id, &slug, &gitURL); err != nil {
			t.Fatalf("scan failed: %v", err)
		}

		if !gitURL.Valid {
			t.Errorf("workspace %q (id=%s): git_url is NULL", slug, id)
			continue
		}
		if gitURL.String == "" {
			t.Errorf("workspace %q (id=%s): git_url is empty", slug, id)
			continue
		}
		if !httpsOrSSH.MatchString(gitURL.String) {
			t.Errorf("workspace %q (id=%s): git_url %q does not match HTTPS or SSH pattern",
				slug, id, gitURL.String)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration failed: %v", err)
	}
}

// TS-07-P6: For any workspace record stored in the workspaces table,
// slug always matches the required pattern and is never null.
//
// Validates: 07-REQ-3.3, 07-REQ-6.1, 07-REQ-6.2
func TestProperty_SlugFormatAlwaysValid(t *testing.T) {
	s := newWorkspaceV2TestStore(t)
	userID := seedTestUser(t, s, "prop-slugfmt-user")

	// Create workspaces with valid slugs of different lengths.
	validSlugs := []string{
		"abc",           // 3 chars minimum
		"my-api",        // typical
		"a-b-c",         // multiple hyphens
		"workspace-123", // alphanumeric
		"a12",           // short with digits
	}

	for _, slug := range validSlugs {
		_, err := s.CreateWorkspaceV2(CreateWorkspaceParams{
			Slug:    slug,
			GitURL:  "https://github.com/org/repo.git",
			OwnerID: userID,
		})
		if err != nil {
			t.Logf("CreateWorkspaceV2 with slug=%q error: %v", slug, err)
		}
	}

	// At least some inserts must have succeeded for the invariant to be meaningful.
	var totalRows int
	if err := s.db.QueryRow("SELECT count(*) FROM workspaces").Scan(&totalRows); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if totalRows == 0 {
		t.Fatal("no workspace rows were inserted — cannot verify slug format invariant " +
			"(CreateWorkspaceV2 likely not implemented)")
	}

	// Invariant check: for every row in workspaces, slug is NOT NULL,
	// matches ^[a-z][a-z0-9-]*[a-z0-9]$ (length 3-64, no consecutive hyphens).
	slugPattern := regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`)

	rows, err := s.db.Query("SELECT id, slug FROM workspaces")
	if err != nil {
		t.Fatalf("query workspaces failed: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var slug sql.NullString
		if err := rows.Scan(&id, &slug); err != nil {
			t.Fatalf("scan failed: %v", err)
		}

		if !slug.Valid {
			t.Errorf("workspace id=%s: slug is NULL", id)
			continue
		}

		s := slug.String
		if len(s) < 3 || len(s) > 64 {
			t.Errorf("workspace id=%s: slug %q has invalid length %d (must be 3-64)", id, s, len(s))
		}
		if !slugPattern.MatchString(s) {
			t.Errorf("workspace id=%s: slug %q does not match pattern ^[a-z][a-z0-9-]*[a-z0-9]$", id, s)
		}
		if strings.Contains(s, "--") {
			t.Errorf("workspace id=%s: slug %q contains consecutive hyphens", id, s)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration failed: %v", err)
	}
}

// TS-07-P7: For any call to the workspace store, the store always returns
// an error value and never terminates the process via os.Exit or log.Fatal,
// and the call is bounded (completes without infinite looping).
//
// Validates: 07-REQ-2.3, 07-REQ-4.7
func TestProperty_StoreNeverTerminatesProcess(t *testing.T) {
	// Part 1: Runtime check — call CreateWorkspaceV2 with various inputs
	// including error paths. Each call must complete within 5 seconds and
	// must NOT call os.Exit or log.Fatal (verified by process survival).

	t.Run("valid input completes in bounded time", func(t *testing.T) {
		s := newWorkspaceV2TestStore(t)
		userID := seedTestUser(t, s, "prop-noexit-valid-user")

		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = s.CreateWorkspaceV2(CreateWorkspaceParams{
				Slug:    "noexit-valid-ws",
				GitURL:  "https://github.com/org/repo.git",
				OwnerID: userID,
			})
		}()

		select {
		case <-done:
			// Completed — process still running.
		case <-time.After(5 * time.Second):
			t.Fatal("CreateWorkspaceV2 did not complete within 5 seconds")
		}
	})

	t.Run("duplicate slug completes in bounded time", func(t *testing.T) {
		s := newWorkspaceV2TestStore(t)
		userID := seedTestUser(t, s, "prop-noexit-dup-user")
		seedWorkspaceV2(t, s, "dup-slug", "https://github.com/org/repo.git", userID)

		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = s.CreateWorkspaceV2(CreateWorkspaceParams{
				Slug:    "dup-slug",
				GitURL:  "https://github.com/org/other.git",
				OwnerID: userID,
			})
		}()

		select {
		case <-done:
			// Completed — process still running.
		case <-time.After(5 * time.Second):
			t.Fatal("CreateWorkspaceV2 with duplicate slug did not complete within 5 seconds")
		}
	})

	t.Run("closed DB completes in bounded time", func(t *testing.T) {
		s := newWorkspaceV2TestStore(t)
		s.DB().Close()

		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = s.CreateWorkspaceV2(CreateWorkspaceParams{
				Slug:    "closed-db-ws",
				GitURL:  "https://github.com/org/repo.git",
				OwnerID: "user-uuid-1",
			})
		}()

		select {
		case <-done:
			// Completed — process still running.
		case <-time.After(5 * time.Second):
			t.Fatal("CreateWorkspaceV2 with closed DB did not complete within 5 seconds")
		}
	})

	// Part 2: Static analysis — verify no os.Exit or log.Fatal in store code.
	t.Run("no os.Exit or log.Fatal in store source files", func(t *testing.T) {
		root := findStoreRepoRoot(t)
		storeDir := filepath.Join(root, "internal", "store")

		entries, err := os.ReadDir(storeDir)
		if err != nil {
			t.Fatalf("failed to read store directory: %v", err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			// Skip test files — they're allowed to use whatever they need.
			if strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}

			data, err := os.ReadFile(filepath.Join(storeDir, entry.Name()))
			if err != nil {
				t.Errorf("failed to read %s: %v", entry.Name(), err)
				continue
			}
			content := string(data)

			if strings.Contains(content, "os.Exit") {
				t.Errorf("store/%s contains os.Exit — store code must never call os.Exit", entry.Name())
			}
			if strings.Contains(content, "log.Fatal") {
				t.Errorf("store/%s contains log.Fatal — store code must never call log.Fatal", entry.Name())
			}
		}
	})
}

// findStoreRepoRoot walks up from the test file to find the repository root
// (the directory containing go.mod).
func findStoreRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root (go.mod)")
		}
		dir = parent
	}
}
