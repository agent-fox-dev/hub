package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/scrub"
)

// issueIn posts a lease of the body's profile in a workspace and returns its id.
func (e *backendEnv) issueIn(slug, body string) string {
	e.t.Helper()
	rec := e.post(slug, body, apiKeyAuth())
	if rec.Code != 201 {
		e.t.Fatalf("POST to %s answered %d: %s", slug, rec.Code, rec.Body)
	}
	return jsonObject(e.t, rec)["id"].(string)
}

// orgWorkspace adds the workspace "orgws", owned by leaseOwner in the org
// "org-1", of which the owner is a member when member is true.
func (e *backendEnv) orgWorkspace(member bool) {
	e.t.Helper()
	e.addWorkspace("orgws", leaseOwner, "org-1", "active")
	if member {
		if _, err := e.db.Exec(`INSERT INTO org_members (org_id, user_id) VALUES ('org-1', ?)`, leaseOwner); err != nil {
			e.t.Fatalf("adding the org member: %v", err)
		}
	}
}

// TS-27-28: The secrets are read when the box is created, tier by tier, the first
// tier that has the name wins, and a value changed after the lease was issued is
// the one injected.
// Requirement: 27-REQ-5.1
func TestBackendCreate_ResolvesSecretsAtCreate_TS_27_28(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	env.plantSecret("user", leaseOwner, "GIT_PAT", "USER-VALUE")
	id := env.issue(`{"profile":"tools"}`)
	if _, err := env.secrets.UpdateSecret("workspace", "widgets", "GIT_PAT", "ROTATED-VALUE"); err != nil {
		t.Fatalf("rotating the secret: %v", err)
	}

	if _, err := env.create(id, sandbox.Spec{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := env.prov.lastSpec().Env["GIT_PAT"]; got != "ROTATED-VALUE" {
		t.Errorf("GIT_PAT = %q; want the workspace's current value ROTATED-VALUE, not the user's and not the one at POST", got)
	}
}

// TS-27-29: The org tier is searched only when the workspace owner is a member
// of the org, whichever credential issued the lease.
// Requirement: 27-REQ-5.2
func TestBackendCreate_OrgTierNeedsMembership_TS_27_29(t *testing.T) {
	t.Run("a member", func(t *testing.T) {
		env := newBackendEnv(t, leaseEnvOptions{})
		env.orgWorkspace(true)
		env.plantSecret("org", "org-1", "GIT_PAT", "org-value")
		id := env.issueIn("orgws", `{"profile":"tools"}`)
		if _, err := env.create(id, sandbox.Spec{}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got := env.prov.lastSpec().Env["GIT_PAT"]; got != "org-value" {
			t.Errorf("GIT_PAT = %q; want the org's org-value", got)
		}
	})

	t.Run("not a member", func(t *testing.T) {
		env := newBackendEnv(t, leaseEnvOptions{})
		env.orgWorkspace(true)
		env.plantSecret("org", "org-1", "GIT_PAT", "org-value")
		id := env.issueIn("orgws", `{"profile":"tools"}`)
		if _, err := env.db.Exec(`DELETE FROM org_members`); err != nil {
			t.Fatal(err)
		}

		_, err := env.create(id, sandbox.Spec{})
		wantCode(t, err, codes.FailedPrecondition)
		lease := env.lease(id)
		if lease.State != "failed" || lease.Error == nil || !strings.HasPrefix(*lease.Error, "secret_missing:") {
			t.Errorf("lease state %q, error %v; want failed with a secret_missing error", lease.State, lease.Error)
		}
	})
}

// TS-27-30: A required secret found in no tier fails the lease with a message
// that names the key and the scopes searched, one failed event, and no provider
// call.
// Requirement: 27-REQ-5.3
func TestBackendCreate_RequiredSecretMissing_TS_27_30(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	env.orgWorkspace(true)
	env.plantSecret("org", "org-1", "GIT_PAT", "org-value")
	id := env.issueIn("orgws", `{"profile":"tools"}`)
	if err := env.secrets.DeleteSecret("org", "org-1", "GIT_PAT"); err != nil {
		t.Fatalf("deleting the secret: %v", err)
	}

	_, err := env.create(id, sandbox.Spec{})
	wantCode(t, err, codes.FailedPrecondition)
	msg := err.Error()
	if !strings.HasPrefix(msg, "secret_missing:") {
		t.Errorf("error %q does not start with secret_missing:", msg)
	}
	for _, part := range []string{"GIT_PAT", "workspace", "org", "user"} {
		if !strings.Contains(msg, part) {
			t.Errorf("error %q does not name %s", msg, part)
		}
	}
	if strings.Contains(msg, "org-1") || strings.Contains(msg, leaseOwner) {
		t.Errorf("error %q names an owner or an org id; want scope types only", msg)
	}
	lease := env.lease(id)
	if lease.State != "failed" || lease.Error == nil || *lease.Error != msg {
		t.Errorf("lease state %q, error %v; want failed with the message of the error", lease.State, lease.Error)
	}
	if events := env.eventsOf(audit.EventSandboxFailed); len(events) != 1 {
		t.Errorf("hub.sandbox.failed events = %d; want 1", len(events))
	}
	if n := env.prov.creates(); n != 0 {
		t.Errorf("provider Create calls = %d; want 0", n)
	}
}

// TS-27-31: A missing optional secret is left out, and one that exists is put in.
// Requirement: 27-REQ-5.4
func TestBackendCreate_OptionalSecret_TS_27_31(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	first := env.issue(`{"profile":"tools"}`)
	if _, err := env.create(first, sandbox.Spec{}); err != nil {
		t.Fatalf("Create without the optional secret: %v", err)
	}
	if _, has := env.prov.lastSpec().Env["EXTRA_TOKEN"]; has {
		t.Errorf("Env = %v; want no EXTRA_TOKEN: it is stored nowhere", env.prov.lastSpec().Env)
	}

	env.plantSecret("user", leaseOwner, "EXTRA_TOKEN", "extra-value")
	second := env.issue(`{"profile":"tools"}`)
	if _, err := env.create(second, sandbox.Spec{}); err != nil {
		t.Fatalf("Create with the optional secret: %v", err)
	}
	if got := env.prov.lastSpec().Env["EXTRA_TOKEN"]; got != "extra-value" {
		t.Errorf("EXTRA_TOKEN = %q; want extra-value", got)
	}
}

// flakyReader is a secret reader whose first lookups fail.
type flakyReader struct {
	SecretReader
	mu       sync.Mutex
	failures int
}

func (f *flakyReader) GetSecretValue(ownerType, ownerID, key string) (string, error) {
	f.mu.Lock()
	failing := f.failures > 0
	if failing {
		f.failures--
	}
	f.mu.Unlock()
	if failing {
		return "", errors.New("database is locked")
	}
	return f.SecretReader.GetSecretValue(ownerType, ownerID, key)
}

// TS-27-32: A store error leaves the lease leased with its claim released, so a
// retry goes through.
// Requirement: 27-REQ-5.5
func TestBackendCreate_StoreErrorLeavesTheLeaseLeased_TS_27_32(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	flaky := &flakyReader{SecretReader: env.secrets, failures: 1}
	b := newBackend(backendConfig{Service: env.profile, Store: env.store, Secrets: flaky, DB: env.db, Audit: env.emitter, Now: env.clock.Now, Principal: testPrincipal})
	id := env.issue(`{"profile":"tools"}`)
	ctx := withPrincipal(context.Background(), id)

	_, err := b.Create(ctx, sandbox.Spec{})
	wantCode(t, err, codes.Internal)
	if strings.Contains(err.Error(), "locked") {
		t.Errorf("error %q repeats the cause of the failure", err)
	}
	if lease := env.lease(id); lease.State != "leased" || lease.Error != nil {
		t.Errorf("lease state %q, error %v; want leased with no error", lease.State, lease.Error)
	}
	if b.claimed(id) {
		t.Error("the claim is still held after the failed Create")
	}
	if n := len(env.eventsOf(audit.EventSandboxFailed)); n != 0 {
		t.Errorf("hub.sandbox.failed events = %d; want 0: the lease did not fail", n)
	}

	if _, err := b.Create(ctx, sandbox.Spec{}); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if lease := env.lease(id); lease.State != "active" {
		t.Errorf("lease state %q after the retry; want active", lease.State)
	}
}

// TS-27-33: The environment is the fixed environment plus each secret under the
// profile's spelling of its name.
// Requirement: 27-REQ-5.6
func TestBackendCreate_EnvIsFixedPlusSecrets_TS_27_33(t *testing.T) {
	cfg := defaultLeaseConfig()
	cfg.Profiles[0].Env = map[string]string{"A": "1", "B": "2"}
	cfg.Profiles[0].OptionalSecrets = nil
	env := newBackendEnv(t, leaseEnvOptions{config: cfg})
	env.addWorkspace("w2", leaseOwner, "", "active")
	env.plantSecret("workspace", "w2", "git_pat", "V-1")

	id := env.issueIn("w2", `{"profile":"tools"}`)
	if _, err := env.create(id, sandbox.Spec{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := map[string]string{"A": "1", "B": "2", "GIT_PAT": "V-1"}
	if got := env.prov.lastSpec().Env; !reflect.DeepEqual(got, want) {
		t.Errorf("Env = %v; want %v", got, want)
	}
}

// TS-27-34: No column of any lease row holds the secret value, after a Create
// that succeeds and after one that fails with an error that echoes the value.
// Requirement: 27-REQ-5.7
func TestBackendCreate_NoSecretInTheDatabase_TS_27_34(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	ok := env.issue(`{"profile":"tools"}`)
	if _, err := env.create(ok, sandbox.Spec{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := env.prov.lastSpec().Env["GIT_PAT"]; got != plantedSecret {
		t.Fatalf("GIT_PAT = %q; the secret was not injected", got)
	}

	env.prov.mu.Lock()
	env.prov.createErr = fmt.Errorf("engine refused: -e GIT_PAT=%s", plantedSecret)
	env.prov.mu.Unlock()
	failed := env.issue(`{"profile":"tools"}`)
	if _, err := env.create(failed, sandbox.Spec{}); err == nil {
		t.Fatal("Create with a failing provider returned no error")
	}

	for _, id := range []string{ok, failed} {
		if row := strings.Join(env.rowText(id), "\n"); strings.Contains(row, plantedSecret) {
			t.Errorf("a column of lease %s holds the secret value:\n%s", id, row)
		}
	}
	if lease := env.lease(failed); lease.State != "failed" {
		t.Errorf("lease state %q; want failed", lease.State)
	}
}

// TS-27-35: A Create that succeeds moves the lease to active with the id of the
// box, emits one create event and returns the box.
// Requirement: 27-REQ-6.1
func TestBackendCreate_Success_TS_27_35(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue(`{"profile":"tools"}`)

	sb, err := env.create(id, sandbox.Spec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	lease := env.lease(id)
	if lease.State != "active" || lease.SandboxID == nil || *lease.SandboxID != sb.ID() {
		t.Errorf("lease state %q, sandbox_id %v; want active with the id of the box %s", lease.State, lease.SandboxID, sb.ID())
	}
	if lease.EndedAt != nil || lease.Error != nil {
		t.Errorf("ended_at %v, error %v; want both null", lease.EndedAt, lease.Error)
	}
	if got := env.prov.live(); len(got) != 1 || got[0] != sb.ID() {
		t.Errorf("live boxes = %v; want just %s", got, sb.ID())
	}
	events := env.eventsOf(audit.EventSandboxCreate)
	if len(events) != 1 {
		t.Fatalf("hub.sandbox.create events = %d; want 1", len(events))
	}
	want := map[string]any{"lease_id": id, "sandbox_id": sb.ID(), "image": "registry.example/tools:1", "network": "none"}
	if !reflect.DeepEqual(events[0].Metadata, want) {
		t.Errorf("event metadata = %v; want %v", events[0].Metadata, want)
	}
	if n := len(env.eventsOf(audit.EventSandboxFailed)); n != 0 {
		t.Errorf("hub.sandbox.failed events = %d; want 0", n)
	}
}

// TS-27-36: A DELETE that ends the lease while the provider creates the box
// leaves the lease destroyed, the new box is destroyed, and no create event is
// emitted.
// Requirement: 27-REQ-6.2
func TestBackendCreate_DeleteDuringCreate_TS_27_36(t *testing.T) {
	logs := captureDefaultLog(t)
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue(`{"profile":"tools"}`)
	block := make(chan struct{})
	env.prov.mu.Lock()
	env.prov.block = block
	env.prov.mu.Unlock()

	result := make(chan error, 1)
	go func() {
		_, err := env.create(id, sandbox.Spec{})
		result <- err
	}()
	select {
	case <-env.prov.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("Create never reached the provider")
	}
	if changed, err := env.store.DestroyLease(id, "leased", "api"); err != nil || !changed {
		t.Fatalf("DestroyLease = (%v, %v)", changed, err)
	}
	close(block)

	var err error
	select {
	case err = <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("Create never returned")
	}
	wantCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "destroyed") {
		t.Errorf("error %q does not name the state destroyed", err)
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes = %v; want none: the box of a deleted lease must be destroyed", live)
	}
	if n := len(env.eventsOf(audit.EventSandboxCreate)); n != 0 {
		t.Errorf("hub.sandbox.create events = %d; want 0", n)
	}
	lease := env.lease(id)
	if lease.State != "destroyed" || lease.SandboxID != nil {
		t.Errorf("lease state %q, sandbox_id %v; want destroyed and no box", lease.State, lease.SandboxID)
	}
	box := env.prov.boxes[0].ID()
	if out := logs.String(); !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, id) || !strings.Contains(out, box) {
		t.Errorf("no warning with the lease id %s and the box id %s:\n%s", id, box, out)
	}
}

// TS-27-37: A provider error that echoes a secret value, an environment value and
// URL user information is scrubbed before it is stored on the lease, put in the
// failed event or returned.
// Requirements: 27-REQ-6.3, 27-REQ-9.3
func TestBackendCreate_ProviderErrorIsScrubbed_TS_27_37(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue(`{"profile":"tools"}`)
	raw := fmt.Sprintf("pull https://u:tok@registry.example/tools:1 failed: run -e GIT_PAT=%s -e SECRET_ENV=envval", plantedSecret)
	env.prov.mu.Lock()
	env.prov.createErr = errors.New(raw)
	env.prov.mu.Unlock()

	_, err := env.create(id, sandbox.Spec{})
	if err == nil {
		t.Fatal("Create with a failing provider returned no error")
	}
	want := scrub.Text(raw, plantedSecret, "envval")
	if want == raw || strings.Contains(want, plantedSecret) || strings.Contains(want, "envval") || strings.Contains(want, "u:tok") {
		t.Fatalf("the scrubbed text %q still holds a value", want)
	}
	if err.Error() != want {
		t.Errorf("returned error = %q; want the scrubbed %q", err, want)
	}
	lease := env.lease(id)
	if lease.State != "failed" || lease.Error == nil || *lease.Error != want {
		t.Errorf("lease state %q, error %v; want failed with %q", lease.State, lease.Error, want)
	}
	events := env.eventsOf(audit.EventSandboxFailed)
	if len(events) != 1 {
		t.Fatalf("hub.sandbox.failed events = %d; want 1", len(events))
	}
	if events[0].Metadata["error"] != want || events[0].Metadata["lease_id"] != id {
		t.Errorf("event metadata = %v; want lease_id and the scrubbed error", events[0].Metadata)
	}
	if n := len(env.eventsOf(audit.EventSandboxCreate)); n != 0 {
		t.Errorf("hub.sandbox.create events = %d; want 0", n)
	}
}

// failingMark is a lease repository whose MarkActive fails.
type failingMark struct{ leaseRepo }

func (failingMark) MarkActive(string, string) (bool, error) {
	return false, errors.New("database is locked")
}

// A box the lease store cannot record is destroyed, the lease stays leased so
// that the caller can retry, and the call fails without repeating the cause.
func TestBackendCreate_RecordingFailureDestroysTheBox(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	b := newBackend(backendConfig{
		Service: env.profile, Store: failingMark{env.store}, Secrets: env.secrets, DB: env.db,
		Audit: env.emitter, Now: env.clock.Now, Principal: testPrincipal,
	})
	id := env.issue(`{"profile":"tools"}`)

	_, err := b.Create(withPrincipal(context.Background(), id), sandbox.Spec{})
	wantCode(t, err, codes.Internal)
	if strings.Contains(err.Error(), "locked") {
		t.Errorf("error %q repeats the cause of the failure", err)
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes = %v; want none", live)
	}
	if lease := env.lease(id); lease.State != "leased" {
		t.Errorf("lease state %q; want leased", lease.State)
	}
	if n := len(env.eventsOf(audit.EventSandboxCreate)); n != 0 {
		t.Errorf("hub.sandbox.create events = %d; want 0", n)
	}
}
