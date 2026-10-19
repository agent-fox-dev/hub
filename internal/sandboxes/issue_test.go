package sandboxes

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"
)

var (
	leaseIDPattern    = regexp.MustCompile(`^sbx_[0-9a-f]{32}$`)
	leaseTokenPattern = regexp.MustCompile(`^sbl_[A-Za-z0-9_-]{44}$`)
)

// bigLeaseConfig is the default section with room for many live leases.
func bigLeaseConfig() *Config {
	cfg := defaultLeaseConfig()
	limit := 1000.0
	cfg.MaxSandboxes = &limit
	return cfg
}

// TS-26-3: Every issued lease id is sbx_ plus 32 lowercase hex characters, and
// ids are unique.
// Requirement: 26-REQ-1.3
func TestIssue_LeaseIDs_Property_TS_26_3(t *testing.T) {
	r := rand.New(rand.NewPCG(26, 3))
	for round := range 5 {
		env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
		n := 1 + r.IntN(100)
		seen := map[string]bool{}
		for i := range n {
			id, _ := env.mustIssue()["id"].(string)
			if !leaseIDPattern.MatchString(id) {
				t.Fatalf("round %d lease %d: id %q does not match ^sbx_[0-9a-f]{32}$", round, i, id)
			}
			if seen[id] {
				t.Fatalf("round %d: id %s was issued twice", round, id)
			}
			seen[id] = true
		}
		if len(seen) != n {
			t.Errorf("round %d: %d distinct ids for %d leases", round, len(seen), n)
		}
	}
}

// TS-26-35: Every issued token is sbl_ plus 44 RawURL base64 characters, 33
// bytes of randomness.
// Requirement: 26-REQ-6.1
func TestIssue_Tokens_Property_TS_26_35(t *testing.T) {
	r := rand.New(rand.NewPCG(26, 35))
	for round := range 5 {
		env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
		n := 1 + r.IntN(100)
		seen := map[string]bool{}
		for i := range n {
			token, _ := env.mustIssue()["token"].(string)
			if len(token) != 48 || !leaseTokenPattern.MatchString(token) {
				t.Fatalf("round %d token %d: %q is not sbl_ plus 44 RawURL characters", round, i, token)
			}
			raw, err := base64.RawURLEncoding.DecodeString(token[4:])
			if err != nil || len(raw) != 33 {
				t.Fatalf("round %d token %d: decodes to %d bytes (%v); want 33", round, i, len(raw), err)
			}
			if seen[token] {
				t.Fatalf("round %d: a token was issued twice", round)
			}
			seen[token] = true
		}
	}
}

// TS-26-4: An issued lease stores the full profile snapshot, and the stored
// values do not follow later edits of the profile.
// Requirement: 26-REQ-1.4
func TestIssue_StoresProfileSnapshot_TS_26_4(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	env.plantSecret("workspace", "widgets", "EXTRA_TOKEN", "x")

	rec := env.post("widgets", `{"profile":"tools","purpose":"triage","labels":{"team":"red","Run.Id":"42"}}`, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
	}
	id := jsonObject(t, rec)["id"].(string)

	// Edit the profile the service holds, in place, the way a careless caller of
	// Profiles() could: the stored lease must not change.
	p := env.profile.profiles[0]
	p.Env["SECRET_ENV"] = "changed"
	p.Secrets[0] = "CHANGED"
	env.profile.profiles[0].Image = "registry.example/changed:9"

	got, err := env.store.GetLease(id)
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if got.Workspace != "widgets" || got.Profile != "tools" || got.Purpose != "triage" || got.State != "leased" {
		t.Errorf("lease = %s/%s purpose %q in %s; want widgets/tools triage in leased", got.Workspace, got.Profile, got.Purpose, got.State)
	}
	if got.Image != "registry.example/tools:1" || got.Network != "none" || !got.RequireLandlock {
		t.Errorf("image %q, network %q, landlock %v; want the profile's at issue", got.Image, got.Network, got.RequireLandlock)
	}
	if got.Resources != (Resources{CPUs: 1, MemoryMB: 512, PIDs: 100}) {
		t.Errorf("resources = %+v; want the profile's", got.Resources)
	}
	if len(got.Secrets) != 1 || got.Secrets[0] != "GIT_PAT" || len(got.OptionalSecrets) != 1 || got.OptionalSecrets[0] != "EXTRA_TOKEN" {
		t.Errorf("secrets %v, optional %v; want [GIT_PAT] and [EXTRA_TOKEN]", got.Secrets, got.OptionalSecrets)
	}
	if got.Env["SECRET_ENV"] != "envval" || len(got.Env) != 1 {
		t.Errorf("env = %v; want {SECRET_ENV: envval}", got.Env)
	}
	if len(got.Labels) != 2 || got.Labels["team"] != "red" || got.Labels["Run.Id"] != "42" {
		t.Errorf("labels = %v; want the labels exactly as given", got.Labels)
	}
	if got.TTLSeconds != 3600 || got.CreatedBy != leaseOwner || got.CreatedAt != "2030-01-02T03:04:05Z" || got.UpdatedAt != got.CreatedAt {
		t.Errorf("ttl %d, created_by %q, created_at %q, updated_at %q", got.TTLSeconds, got.CreatedBy, got.CreatedAt, got.UpdatedAt)
	}
	if got.ExpiresAt != "2030-01-02T04:04:05Z" {
		t.Errorf("expires_at = %q; want created_at plus the profile's 1h", got.ExpiresAt)
	}
	if got.SandboxID != nil || got.Error != nil || got.EndedAt != nil || got.EndReason != nil {
		t.Errorf("sandbox_id, error, ended_at or end_reason is set on a new lease: %+v", got)
	}
}

// The lease Issue returns owns its lists and maps: editing the profile the
// service holds afterwards does not change it (the snapshot is a copy).
// Requirement: 26-REQ-1.4
func TestIssue_SnapshotIsACopy(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	params, err := env.svc.newIssueParams(issueRequest{})
	if err != nil {
		t.Fatalf("newIssueParams: %v", err)
	}
	lease, _, err := env.svc.Issue(context.Background(), "widgets", apiKeyAuth(), params)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	held := env.profile.profiles[0]
	held.Env["SECRET_ENV"] = "changed"
	held.Secrets[0] = "CHANGED"
	held.OptionalSecrets[0] = "CHANGED"
	if lease.Env["SECRET_ENV"] != "envval" || lease.Secrets[0] != "GIT_PAT" || lease.OptionalSecrets[0] != "EXTRA_TOKEN" {
		t.Errorf("the lease follows the profile: env %v, secrets %v, optional %v", lease.Env, lease.Secrets, lease.OptionalSecrets)
	}
}

// The clock and the ttl are cut to whole seconds, so that expires_at minus
// created_at is exactly ttl_seconds whatever fraction the clock or the ttl has.
// Requirement: 26-REQ-1.6
func TestIssue_SubSecondClockAndTTL(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{start: time.Date(2030, 1, 2, 3, 4, 5, 900_000_000, time.UTC)})
	got := env.mustIssueWith(`{"profile":"tools","ttl":"61.5s"}`)
	if got["created_at"] != "2030-01-02T03:04:05Z" || got["expires_at"] != "2030-01-02T03:05:06Z" {
		t.Errorf("created_at %v, expires_at %v; want 03:04:05Z and 03:05:06Z (61 whole seconds)", got["created_at"], got["expires_at"])
	}
	var seconds int
	if err := env.db.QueryRow(`SELECT ttl_seconds FROM sandbox_leases WHERE id = ?`, got["id"]).Scan(&seconds); err != nil || seconds != 61 {
		t.Errorf("ttl_seconds = %d (%v); want 61", seconds, err)
	}
}

// TS-26-5: created_by is "admin" for an admin token and the UserID otherwise.
// Requirement: 26-REQ-1.5
func TestIssue_CreatedBy_TS_26_5(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	for name, tc := range map[string]struct {
		auth *apikit.AuthInfo
		want string
	}{
		"admin token": {adminAuth(), "admin"},
		"api key":     {&apikit.AuthInfo{CredentialType: "api_key", UserID: leaseOwner, KeyID: "k"}, leaseOwner},
		"PAT":         {patAuth("sandboxes:write"), "user-1"},
	} {
		rec := env.post("widgets", `{}`, tc.auth)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: POST answered %d: %s", name, rec.Code, rec.Body)
		}
		if got := jsonObject(t, rec)["created_by"]; got != tc.want {
			t.Errorf("%s: created_by = %v; want %q", name, got, tc.want)
		}
	}
}

// TS-26-6: Timestamps come from the injected clock in RFC 3339 UTC, and
// expires_at is created_at plus the ttl.
// Requirement: 26-REQ-1.6
func TestIssue_Timestamps_TS_26_6(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{start: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)})
	body := env.mustIssueWith(`{"profile":"tools","ttl":"30m"}`)

	id := body["id"].(string)
	if body["created_at"] != apikit.FormatUTC(env.clock.Now()) {
		t.Errorf("created_at = %v; want %s from the injected clock", body["created_at"], apikit.FormatUTC(env.clock.Now()))
	}
	created, _ := apikit.ParseUTC(body["created_at"].(string))
	expires, _ := apikit.ParseUTC(body["expires_at"].(string))
	if expires.Sub(created) != 1800*time.Second {
		t.Errorf("expires_at - created_at = %v; want 1800s", expires.Sub(created))
	}
	if body["updated_at"] != body["created_at"] {
		t.Errorf("updated_at = %v; want created_at on a new lease", body["updated_at"])
	}
	var seconds int64
	if err := env.db.QueryRow(`SELECT ttl_seconds FROM sandbox_leases WHERE id = ?`, id).Scan(&seconds); err != nil || seconds != 1800 || seconds != int64(expires.Sub(created)/time.Second) {
		t.Errorf("ttl_seconds = %d (%v); want 1800, equal to expires_at - created_at", seconds, err)
	}

	env.clock.Advance(10 * time.Minute)
	if changed, err := env.store.EndLease(&Lease{ID: id}, "destroyed", "api"); err != nil || !changed {
		t.Fatalf("EndLease = (%v, %v)", changed, err)
	}
	ended, err := env.store.GetLease(id)
	if err != nil || ended.EndedAt == nil {
		t.Fatalf("GetLease after EndLease: %v, %+v", err, ended)
	}
	for name, ts := range map[string]string{"created_at": ended.CreatedAt, "updated_at": ended.UpdatedAt, "expires_at": ended.ExpiresAt, "ended_at": *ended.EndedAt} {
		if got, err := apikit.ParseUTC(ts); err != nil || !strings.HasSuffix(ts, "Z") || apikit.FormatUTC(got) != ts {
			t.Errorf("%s = %q is not RFC 3339 UTC: %v", name, ts, err)
		}
	}
	if *ended.EndedAt != "2030-01-02T03:14:05Z" {
		t.Errorf("ended_at = %s; want 10 minutes after the issue", *ended.EndedAt)
	}
}

// mustIssueWith posts body to workspace widgets as the owner and returns the
// decoded 201 body.
func (e *leaseEnv) mustIssueWith(body string) map[string]any {
	e.t.Helper()
	rec := e.post("widgets", body, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("POST %s answered %d: %s", body, rec.Code, rec.Body)
	}
	return jsonObject(e.t, rec)
}

// TS-26-7: Neither a planted secret value nor the issued token is stored in any
// lease column.
// Requirement: 26-REQ-1.7
func TestIssue_StoresNoSecretValueOrToken_TS_26_7(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	body := env.mustIssue()
	token := body["token"].(string)

	for i, col := range env.rowText(body["id"].(string)) {
		if strings.Contains(col, "PLANTED-VALUE-123") {
			t.Errorf("column %d holds the planted secret value", i)
		}
		if strings.Contains(col, token) {
			t.Errorf("column %d holds the plaintext token", i)
		}
	}
}

// TS-26-11: A missing or unowned workspace answers 404 while an admin token
// passes.
// Requirement: 26-REQ-2.4
func TestIssue_WorkspaceOwnership_TS_26_11(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	stranger := &apikit.AuthInfo{CredentialType: "api_key", UserID: "user-2", KeyID: "k2"}

	unowned := env.post("widgets", `{"profile":"tools"}`, stranger)
	missing := env.post("nosuch", `{"profile":"tools"}`, stranger)
	if unowned.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
		t.Fatalf("unowned %d, missing %d; want 404 for both", unowned.Code, missing.Code)
	}
	if message(t, unowned) != message(t, missing) {
		t.Errorf("an unowned workspace (%q) is told apart from a missing one (%q)", message(t, unowned), message(t, missing))
	}
	if env.countLeases() != 0 {
		t.Fatalf("a refused request stored %d leases", env.countLeases())
	}

	if rec := env.post("widgets", `{"profile":"tools"}`, adminAuth()); rec.Code != http.StatusCreated {
		t.Errorf("admin token: POST answered %d; want 201: %s", rec.Code, rec.Body)
	}
	if n := env.countLeases(); n != 1 {
		t.Errorf("%d leases; want only the admin's", n)
	}
}

// TS-26-12: POST on a non-active workspace answers 400 'workspace is not
// active'.
// Requirement: 26-REQ-2.5
func TestIssue_NonActiveWorkspace_TS_26_12(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	env.addWorkspace("old", leaseOwner, "", "archived")

	rec := env.post("old", `{"profile":"tools"}`, apiKeyAuth())
	if rec.Code != http.StatusBadRequest || message(t, rec) != "workspace is not active" {
		t.Errorf("status %d, message %q; want 400 'workspace is not active'", rec.Code, message(t, rec))
	}
	if env.countLeases() != 0 {
		t.Errorf("%d leases stored for an archived workspace", env.countLeases())
	}
}

// TS-26-15: POST with a profile returns 201 with the full lease and the token,
// and only the token's SHA-256 is stored.
// Requirement: 26-REQ-3.1
func TestIssue_Created_TS_26_15(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201: %s", rec.Code, rec.Body)
	}
	body := jsonObject(t, rec)

	if body["state"] != "leased" || body["profile"] != "tools" || body["workspace"] != "widgets" {
		t.Errorf("state %v, profile %v, workspace %v; want leased/tools/widgets", body["state"], body["profile"], body["workspace"])
	}
	if body["image"] != "registry.example/tools:1" || body["network"] != "none" || body["require_landlock"] != true {
		t.Errorf("image %v, network %v, landlock %v; want the profile's", body["image"], body["network"], body["require_landlock"])
	}
	resources, _ := body["resources"].(map[string]any)
	if resources["cpus"] != float64(1) || resources["memory_mb"] != float64(512) || resources["pids"] != float64(100) || resources["disk_mb"] != float64(0) {
		t.Errorf("resources = %v; want the profile's", resources)
	}
	secretNames, _ := body["secrets"].([]any)
	optionalNames, _ := body["optional_secrets"].([]any)
	if len(secretNames) != 1 || secretNames[0] != "GIT_PAT" || len(optionalNames) != 1 || optionalNames[0] != "EXTRA_TOKEN" {
		t.Errorf("secrets %v, optional %v; want the profile's names", secretNames, optionalNames)
	}
	created, _ := apikit.ParseUTC(body["created_at"].(string))
	expires, _ := apikit.ParseUTC(body["expires_at"].(string))
	if expires.Sub(created) != time.Hour {
		t.Errorf("expires_at - created_at = %v; want 1h", expires.Sub(created))
	}

	token, _ := body["token"].(string)
	if len(token) != 48 || !leaseTokenPattern.MatchString(token) {
		t.Fatalf("token %q is not 48 characters of sbl_ and base64", token)
	}
	sum := sha256.Sum256([]byte(token))
	var stored string
	if err := env.db.QueryRow(`SELECT token_hash FROM sandbox_leases WHERE id = ?`, body["id"]).Scan(&stored); err != nil {
		t.Fatalf("reading the hash: %v", err)
	}
	if stored != hex.EncodeToString(sum[:]) {
		t.Errorf("stored hash %q is not the SHA-256 of the token", stored)
	}
	if found, err := env.store.LookupByToken(token); err != nil || found.ID != body["id"] {
		t.Errorf("LookupByToken(issued token) = (%v, %v); want the new lease", found, err)
	}
}

// TS-26-16: An empty body, or one without profile, ttl or purpose, uses the
// default profile, its TTL and an empty purpose.
// Requirement: 26-REQ-3.2
func TestIssue_Defaults_TS_26_16(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	for _, body := range []string{"", "{}", `{"labels":{}}`} {
		rec := env.post("widgets", body, apiKeyAuth())
		if rec.Code != http.StatusCreated {
			t.Fatalf("body %q: status %d; want 201: %s", body, rec.Code, rec.Body)
		}
		got := jsonObject(t, rec)
		created, _ := apikit.ParseUTC(got["created_at"].(string))
		expires, _ := apikit.ParseUTC(got["expires_at"].(string))
		if got["profile"] != "tools" || got["purpose"] != "" || expires.Sub(created) != time.Hour {
			t.Errorf("body %q: profile %v, purpose %q, ttl %v; want the default profile tools, '' and 1h", body, got["profile"], got["purpose"], expires.Sub(created))
		}
	}
}

// The presence check, reached through the handler: a secret stored under
// another case satisfies a required name, an absent optional secret does not
// block, and the lease service lists names and never reads a value.
// Requirements: 26-REQ-4.1, 26-REQ-4.2, 26-REQ-4.5
func TestIssue_PresenceCheckThroughHandler_TS_26_24_25(t *testing.T) {
	lister := newFakeLister()
	lister.add("workspace", "widgets", "git_pat")
	env := newLeaseEnv(t, leaseEnvOptions{lister: lister})

	if rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth()); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201 with git_pat satisfying GIT_PAT and EXTRA_TOKEN absent: %s", rec.Code, rec.Body)
	}
	if len(lister.calls) == 0 {
		t.Error("the handler did not list any scope")
	}
}
