package sandboxes

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// secretsConfig is the default section with the default profile requiring the
// given secrets.
func secretsConfig(required ...string) *Config {
	cfg := bigLeaseConfig()
	cfg.Profiles[0].Secrets = required
	return cfg
}

// TS-26-27: Missing required secrets answer 422 secret_missing, naming every
// missing key in profile order and the scope types searched, and no owner or
// org.
// Requirement: 26-REQ-4.4
func TestIssue_MissingSecrets_TS_26_27(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: secretsConfig("C_KEY", "A_KEY", "B_KEY")})
	env.plantSecret("user", leaseOwner, "B_KEY", "value-b")

	rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
	if rec.Code != http.StatusUnprocessableEntity || errorType(t, rec) != "secret_missing" {
		t.Fatalf("status %d, error_type %q; want 422 secret_missing: %s", rec.Code, errorType(t, rec), rec.Body)
	}
	msg := message(t, rec)
	if c, a := strings.Index(msg, "C_KEY"), strings.Index(msg, "A_KEY"); c < 0 || a < 0 || c > a {
		t.Errorf("message %q does not name C_KEY then A_KEY in profile order", msg)
	}
	if strings.Contains(msg, "B_KEY") {
		t.Errorf("message %q names a secret that exists", msg)
	}
	if !strings.Contains(msg, "workspace, user") {
		t.Errorf("message %q does not list the scopes searched, 'workspace, user'", msg)
	}
	for _, leak := range []string{leaseOwner, "widgets", "value-b"} {
		if strings.Contains(msg, leak) {
			t.Errorf("message %q contains %q", msg, leak)
		}
	}
	if n := env.countLeases(); n != 0 {
		t.Errorf("%d leases stored although secrets are missing", n)
	}

	// A workspace whose owner belongs to its org also searches the org scope.
	env.addWorkspace("teamed", leaseOwner, "org-77", "ready")
	if _, err := env.db.Exec(`INSERT INTO org_members (org_id, user_id) VALUES ('org-77', ?)`, leaseOwner); err != nil {
		t.Fatal(err)
	}
	rec = env.post("teamed", `{"profile":"tools"}`, apiKeyAuth())
	if msg := message(t, rec); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(msg, "workspace, org, user") || strings.Contains(msg, "org-77") {
		t.Errorf("teamed workspace: status %d, message %q; want 422 searching 'workspace, org, user' without the org id", rec.Code, msg)
	}

	// Once the secrets exist, in any scope, the lease is issued.
	env.plantSecret("workspace", "widgets", "C_KEY", "x")
	env.plantSecret("user", leaseOwner, "A_KEY", "y")
	if rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth()); rec.Code != http.StatusCreated {
		t.Errorf("with every required secret present: status %d; want 201: %s", rec.Code, rec.Body)
	}
}

// TS-26-28: A secrets store or membership lookup error answers 500 and stores
// nothing.
// Requirement: 26-REQ-4.6
func TestIssue_LookupErrors_TS_26_28(t *testing.T) {
	lister := newFakeLister()
	lister.err = errors.New("boom-secret-detail")
	env := newLeaseEnv(t, leaseEnvOptions{lister: lister})

	rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("a failing secrets store: status %d; want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boom-secret-detail") {
		t.Errorf("the 500 body leaks the cause: %s", rec.Body)
	}
	if env.countLeases() != 0 {
		t.Errorf("%d leases stored after a store error", env.countLeases())
	}

	env = newLeaseEnv(t, leaseEnvOptions{})
	env.addWorkspace("teamed", leaseOwner, "org-77", "ready")
	if _, err := env.db.Exec(`DROP TABLE org_members`); err != nil {
		t.Fatal(err)
	}
	if rec := env.post("teamed", `{"profile":"tools"}`, apiKeyAuth()); rec.Code != http.StatusInternalServerError {
		t.Errorf("a missing org_members table: status %d; want 500", rec.Code)
	}
	if env.countLeases() != 0 {
		t.Errorf("%d leases stored after a membership lookup error", env.countLeases())
	}
}
