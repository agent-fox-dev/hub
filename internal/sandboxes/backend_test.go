package sandboxes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/txsvc/apikit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/scrub"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

func wantCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if err == nil || status.Code(err) != code {
		t.Fatalf("error %v has code %s; want %s", err, status.Code(err), code)
	}
}

// pastLeaseDeadline moves the clock past the 5m deadline for a Create but not
// past expires_at.
func (e *backendEnv) pastLeaseDeadline() { e.clock.Advance(6 * time.Minute) }

// A Create without a principal is Unauthenticated before anything is read; a
// second Create for a lease with one in flight is FailedPrecondition without a
// provider call, and every claim is released when its Create returns.
func TestBackendCreate_PrincipalAndClaim(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	repo := &countingRepo{leaseRepo: env.store}
	for _, b := range []*Backend{
		newBackend(backendConfig{Service: env.profile, Store: repo, Principal: func(context.Context) (string, bool) { return "", false }}),
		newBackend(backendConfig{Service: env.profile, Store: repo}), // gatewayd's principal, absent from a plain context
	} {
		_, err := b.Create(context.Background(), sandbox.Spec{})
		wantCode(t, err, codes.Unauthenticated)
	}
	if env.prov.creates() != 0 || repo.gets != 0 {
		t.Errorf("creates %d, leases loaded %d; want none", env.prov.creates(), repo.gets)
	}

	id := env.issue("")
	release, entered := env.blockCreates()
	first := make(chan error, 1)
	go func() { _, err := env.create(id, sandbox.Spec{}); first <- err }()
	<-entered
	if !env.backend.claimed(id) {
		t.Error("the lease is not claimed while its Create is in flight")
	}
	_, err := env.create(id, sandbox.Spec{})
	wantCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "already in progress") || env.prov.creates() != 1 {
		t.Errorf("second Create = %v with %d provider calls", err, env.prov.creates())
	}
	close(release)
	if err := <-first; err != nil || env.backend.claimed(id) {
		t.Errorf("the first Create = %v, claimed after %v", err, env.backend.claimed(id))
	}
	env.prov.setBlock(nil)
	env.prov.setCreateErr(errors.New("engine unreachable"))
	other := env.issue("")
	if _, err := env.create(other, sandbox.Spec{}); err == nil || env.backend.claimed(other) {
		t.Error("a failing Create kept its claim")
	}
}

func (b *Backend) claimed(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, busy := b.claims[id]
	return busy
}

// countingRepo counts the leases it is asked for and can fail the read or MarkActive.
type countingRepo struct {
	leaseRepo
	mu      sync.Mutex
	gets    int
	getErr  error
	markErr error
}

func (c *countingRepo) GetLease(id string) (*Lease, error) {
	c.mu.Lock()
	c.gets++
	err := c.getErr
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return c.leaseRepo.GetLease(id)
}

func (c *countingRepo) MarkActive(id, box string) (bool, error) {
	if c.markErr != nil {
		return false, c.markErr
	}
	return c.leaseRepo.MarkActive(id, box)
}

// A Create is refused, with the lease left as it is and no provider call, for a
// lease that does not exist (NotFound), that is not leased (FailedPrecondition
// naming the state) or that the store cannot read (Internal, cause hidden).
func TestBackendCreate_RefusesWhatCannotBeCreatedFrom(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	active := env.issue("")
	env.store.MarkActive(active, "box-1")
	before := *env.lease(active)
	_, err := env.create(active, sandbox.Spec{})
	wantCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "is active") || !reflect.DeepEqual(before, *env.lease(active)) {
		t.Errorf("Create on an active lease = %v, changed %v", err, !reflect.DeepEqual(before, *env.lease(active)))
	}
	_, err = env.create(leaseID(404), sandbox.Spec{})
	wantCode(t, err, codes.NotFound)
	repo := &countingRepo{leaseRepo: env.store, getErr: errors.New("disk I/O error at /var/lib/hub/afhub.db")}
	b := newBackend(backendConfig{Service: env.profile, Store: repo, Principal: testPrincipal})
	_, err = b.Create(withPrincipal(context.Background(), leaseID(1)), sandbox.Spec{})
	wantCode(t, err, codes.Internal)
	if strings.Contains(err.Error(), "afhub.db") || env.prov.creates() != 0 {
		t.Errorf("error %q, %d provider calls", err, env.prov.creates())
	}
}

// A Create after the deadline (lease_expires_at, else expires_at; under a second
// left counts) is refused and ends the lease as expired in the same call, with
// one expire event and no provider call; a second Create finds it ended.
func TestBackendCreate_LateCreateExpiresTheLease(t *testing.T) {
	env := newSweepEnv(t)
	late := env.issue("")
	legacy := env.issue("")
	testutil.Exec(t, env.db, `UPDATE sandbox_leases SET lease_expires_at = NULL WHERE id = ?`, legacy)
	env.pastLeaseDeadline()
	_, err := env.create(late, sandbox.Spec{})
	wantCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "has expired") || env.prov.creates() != 0 {
		t.Errorf("Create = %v with %d provider calls", err, env.prov.creates())
	}
	if s := env.readState(late); s.state != "expired" || s.endReason != "expired" || s.hash.Valid {
		t.Errorf("the lease reads %+v", s)
	}
	_, err = env.create(late, sandbox.Spec{})
	wantCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "is expired") || len(env.eventsOf(audit.EventSandboxExpire)) != 1 {
		t.Errorf("second Create = %v, %d expire events", err, len(env.eventsOf(audit.EventSandboxExpire)))
	}
	env.mustCreate(legacy) // past the column's deadline, before expires_at

	cfg := bigLeaseConfig()
	cfg.LeaseTimeout = strPtr("1h")
	env2 := newBackendEnv(t, leaseEnvOptions{config: cfg})
	for name, offset := range map[string]time.Duration{"at expires_at": 0, "less than a second left": 500 * time.Millisecond} {
		id := env2.issue("")
		expires, _ := apikit.ParseUTC(env2.lease(id).ExpiresAt)
		env2.clock.Advance(expires.Sub(env2.clock.Now()) - offset)
		if _, err := env2.create(id, sandbox.Spec{}); status.Code(err) != codes.FailedPrecondition || env2.lease(id).State != "expired" {
			t.Errorf("%s: Create = %v, lease %s", name, err, env2.lease(id).State)
		}
	}
	whole := env2.issue("")
	expires, _ := apikit.ParseUTC(env2.lease(whole).ExpiresAt)
	env2.clock.Advance(expires.Sub(env2.clock.Now()) - time.Second)
	env2.mustCreate(whole)
	if ttl := env2.prov.lastSpec().TTL; ttl != time.Second {
		t.Errorf("TTL with a second left = %v", ttl)
	}
}

// The provider gets hub's spec, built from the lease and never from the caller,
// of which only the workdir and the labels are read: hub.* and the provider's
// reserved labels dropped, hub.lease and hub.workspace set last. A profile
// edited since does not change the box.
func TestBackendCreate_BuildsTheSpecFromTheLease(t *testing.T) {
	cfg := defaultLeaseConfig()
	cfg.Profiles[0].TTL, cfg.LeaseTimeout = "2h", strPtr("2h")
	env := newBackendEnv(t, leaseEnvOptions{config: cfg})
	id := env.issue(`{"profile":"tools","ttl":"90m","labels":{"team":"x"}}`)
	env.clock.Advance(10*time.Minute + 500*time.Millisecond)

	edited := defaultLeaseConfig()
	edited.NetworkModes = []string{"none", "unrestricted"}
	p := &edited.Profiles[0]
	p.Image, p.CPUs, p.MemoryMB, p.PIDs, p.Network, p.RequireLandlock = "registry.example/other:9", 8, 4096, 1000, strPtr("unrestricted"), false
	p.Env = map[string]string{"SECRET_ENV": "changed", "NEW": "x"}
	restarted, err := New(requireValid(t, edited), env.prov)
	if err != nil {
		t.Fatal(err)
	}
	b := newBackend(backendConfig{Service: restarted, Store: env.store, Secrets: env.secrets, DB: env.db, Audit: env.emitter, Now: env.clock.Now, Principal: testPrincipal})
	if _, err := b.Create(withPrincipal(context.Background(), id), sandbox.Spec{
		Image: "evil", Env: map[string]string{"X": "1", "SECRET_ENV": "forged"}, Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted},
		Resources: sandbox.Resources{CPUs: 64, MemoryBytes: 1 << 40}, TTL: 99 * time.Hour, Workdir: "/w", Provider: map[string]any{"privileged": true},
		Labels: map[string]string{"a": "1", "hub.lease": "forged", "hub.team": "forged", "agentkit.sandbox": "x", "agentkit.sandbox.id": "y", "agentkit.sandboxes": "kept"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := env.prov.lastSpec()
	if got.Image != "registry.example/tools:1" || got.Network.Mode != sandbox.NetworkNone || got.Resources != (sandbox.Resources{CPUs: 1, MemoryBytes: 512 << 20, PIDs: 100}) ||
		!got.RequireLandlock || got.Workdir != "/w" || got.TTL != 79*time.Minute+59*time.Second || got.Provider != nil {
		t.Errorf("spec = %+v", got)
	}
	if !reflect.DeepEqual(got.Env, map[string]string{"SECRET_ENV": "envval", "GIT_PAT": plantedSecret}) {
		t.Errorf("Env = %v; want the lease's fixed env plus the secret", got.Env)
	}
	if want := map[string]string{"a": "1", "agentkit.sandboxes": "kept", "hub.team": "x", "hub.lease": id, "hub.workspace": "widgets"}; !reflect.DeepEqual(got.Labels, want) {
		t.Errorf("Labels = %v; want %v", got.Labels, want)
	}
	// A size that does not fit in bytes, and a network the provider no longer
	// honours, cannot be built into a spec.
	lease := &Lease{ID: "sbx_1", Network: "none", Resources: Resources{MemoryMB: 1 << 62, DiskMB: 1 << 62}}
	if _, err := buildSpec(lease, sandbox.Spec{}, env.prov.Capabilities(), time.Hour); err == nil || !strings.Contains(err.Error(), "memory_mb") || !strings.Contains(err.Error(), "disk_mb") {
		t.Errorf("buildSpec with enormous sizes = %v", err)
	}
	if _, err := buildSpec(&Lease{Network: "host"}, sandbox.Spec{}, env.prov.Capabilities(), time.Hour); err == nil || !strings.Contains(err.Error(), `"host"`) {
		t.Errorf("buildSpec with an unknown network = %v", err)
	}
}

// The secrets are read at Create down the chain (the org scope only with
// membership), the first scope wins, a rotated or optional value is injected
// and no row holds one; a profile that is gone, a network no longer honoured or
// a required secret found nowhere fail the lease with one event and no box.
func TestBackendCreate_ResolvesSecretsAndFailsWhatCannotBeBuilt(t *testing.T) {
	cfg := defaultLeaseConfig()
	cfg.NetworkModes = []string{"none", "unrestricted"}
	env := newBackendEnv(t, leaseEnvOptions{config: cfg})
	env.addWorkspace("orgws", "org-1", "active")
	env.addMember("org-1", owner)
	env.plantSecret("org", "org-1", "GIT_PAT", "org-value")
	env.plantSecret("user", owner, "GIT_PAT", "USER-VALUE")

	rotated := env.issue("")
	env.secrets.UpdateSecret("workspace", "widgets", "GIT_PAT", "ROTATED-VALUE")
	env.mustCreate(rotated)
	if got := env.prov.lastSpec().Env; got["GIT_PAT"] != "ROTATED-VALUE" || got["EXTRA_TOKEN"] != "" {
		t.Errorf("Env = %v; want the workspace's current value and no absent optional secret", got)
	}
	env.plantSecret("user", owner, "EXTRA_TOKEN", "extra-value")
	env.mustCreate(env.mustIssueAs("orgws", "", ownerKey())["id"].(string))
	if got := env.prov.lastSpec().Env; got["GIT_PAT"] != "org-value" || got["EXTRA_TOKEN"] != "extra-value" {
		t.Errorf("Env = %v; want the org's value and the optional secret", got)
	}
	if row := rowText(t, env.db, rotated); strings.Contains(row, "ROTATED-VALUE") || strings.Contains(row, plantedSecret) {
		t.Errorf("the row holds a secret value: %s", row)
	}

	creates := env.prov.creates()
	for _, tc := range []struct {
		name    string
		prepare func() string
		want    string
	}{
		{"profile gone", func() string {
			id := env.issue("")
			testutil.Exec(t, env.db, `UPDATE sandbox_leases SET profile = 'gone' WHERE id = ?`, id)
			return id
		}, `profile "gone" is no longer configured`},
		{"network not honoured", func() string {
			id := env.issue("")
			testutil.Exec(t, env.db, `UPDATE sandbox_leases SET network = 'host' WHERE id = ?`, id)
			return id
		}, `network mode "host"`},
		{"not a member", func() string {
			id := env.mustIssueAs("orgws", "", ownerKey())["id"].(string)
			testutil.Exec(t, env.db, `DELETE FROM org_members`)
			env.secrets.DeleteSecret("user", owner, "GIT_PAT")
			env.secrets.DeleteSecret("workspace", "widgets", "GIT_PAT")
			return id
		}, "secret_missing: GIT_PAT (searched scopes: workspace, user)"},
	} {
		name := tc.name
		id := tc.prepare()
		_, err := env.create(id, sandbox.Spec{})
		wantCode(t, err, codes.FailedPrecondition)
		if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "org-1") {
			t.Errorf("%s: error = %q; want %q", name, err, tc.want)
		}
		if l := env.lease(id); l.State != "failed" || l.Error == nil || *l.Error != err.Error() {
			t.Errorf("%s: lease %s with error %v; want failed with the message", name, l.State, l.Error)
		}
	}
	events := env.eventsOf(audit.EventSandboxFailed)
	if len(events) != 3 || env.prov.creates() != creates || events[0].Metadata["lease_id"] == nil || events[0].Metadata["error"] == nil {
		t.Errorf("%d failed events, %d new provider calls; want 3 and 0", len(events), env.prov.creates()-creates)
	}
}

// A store that fails while the secrets are read leaves the lease leased with
// its claim released, so a retry goes through; a box the store cannot record is
// destroyed and the lease stays leased.
func TestBackendCreate_StoreErrorsLeaveTheLeaseLeased(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	flaky := &flakyReader{SecretReader: env.secrets, failures: 1}
	b := newBackend(backendConfig{Service: env.profile, Store: env.store, Secrets: flaky, DB: env.db, Audit: env.emitter, Now: env.clock.Now, Principal: testPrincipal})
	id := env.issue("")
	ctx := withPrincipal(context.Background(), id)
	_, err := b.Create(ctx, sandbox.Spec{})
	wantCode(t, err, codes.Internal)
	if strings.Contains(err.Error(), "locked") || env.lease(id).State != "leased" || b.claimed(id) || len(env.eventsOf(audit.EventSandboxFailed)) != 0 {
		t.Errorf("after the store error: %v, lease %s", err, env.lease(id).State)
	}
	if _, err := b.Create(ctx, sandbox.Spec{}); err != nil || env.lease(id).State != "active" {
		t.Errorf("the retry: %v, lease %s", err, env.lease(id).State)
	}

	repo := &countingRepo{leaseRepo: env.store, markErr: errors.New("database is locked")}
	b = newBackend(backendConfig{Service: env.profile, Store: repo, Secrets: env.secrets, DB: env.db, Audit: env.emitter, Now: env.clock.Now, Principal: testPrincipal})
	second := env.issue("")
	_, err = b.Create(withPrincipal(context.Background(), second), sandbox.Spec{})
	wantCode(t, err, codes.Internal)
	if strings.Contains(err.Error(), "locked") || len(env.prov.live()) != 1 || env.lease(second).State != "leased" || len(env.eventsOf(audit.EventSandboxCreate)) != 1 {
		t.Errorf("a box that could not be recorded: %v, live %v, lease %s", err, env.prov.live(), env.lease(second).State)
	}
}

type flakyReader struct {
	SecretReader
	mu       sync.Mutex
	failures int
}

func (f *flakyReader) GetSecretValue(ownerType, ownerID, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures > 0 {
		f.failures--
		return "", errors.New("database is locked")
	}
	return f.SecretReader.GetSecretValue(ownerType, ownerID, key)
}

// A Create that succeeds activates the lease with one create event; a provider
// error is scrubbed before it is stored, emitted or returned and fails the lease;
// every accepted Create counts once as created or failed, refusals count nowhere.
func TestBackendCreate_OutcomesAndCounters(t *testing.T) {
	m := audit.NewMetrics()
	env := newBackendEnv(t, leaseEnvOptions{metrics: m, config: bigLeaseConfig()})
	id := env.issue("")
	sb := env.mustCreate(id)
	l := env.lease(id)
	if l.State != "active" || *l.SandboxID != sb.ID() || l.EndedAt != nil || len(env.prov.live()) != 1 {
		t.Errorf("after Create: %+v", l)
	}
	events := env.eventsOf(audit.EventSandboxCreate)
	if len(events) != 1 || events[0].ActorType != "lease" || events[0].ActorID != owner || !reflect.DeepEqual(events[0].Metadata, map[string]any{"lease_id": id, "sandbox_id": sb.ID(), "image": "registry.example/tools:1", "network": "none"}) {
		t.Errorf("create events = %+v", events)
	}

	raw := fmt.Sprintf("pull https://u:tok@registry.example/tools:1 failed: run -e GIT_PAT=%s -e SECRET_ENV=envval", plantedSecret)
	env.prov.setCreateErr(errors.New(raw))
	failed := env.issue("")
	_, err := env.create(failed, sandbox.Spec{})
	want := scrub.Text(raw, plantedSecret, "envval")
	if err == nil || err.Error() != want || strings.Contains(want, plantedSecret) || strings.Contains(want, "u:tok") {
		t.Errorf("error = %v; want the scrubbed %q", err, want)
	}
	if l := env.lease(failed); l.State != "failed" || *l.Error != want || strings.Contains(rowText(t, env.db, failed), plantedSecret) {
		t.Errorf("lease = %+v", l)
	}
	if ev := env.eventsOf(audit.EventSandboxFailed); len(ev) != 1 || ev[0].Metadata["error"] != want {
		t.Errorf("failed events = %+v", ev)
	}
	env.prov.setCreateErr(nil)

	// Refusals: unknown, a second Create, past the deadline, in shutdown mode.
	before := []float64{createsCounter(t, m, "created"), createsCounter(t, m, "failed")}
	if before[0] != 1 || before[1] != 1 {
		t.Fatalf("counters = %v; want one created and one failed", before)
	}
	late := env.issue("")
	env.pastLeaseDeadline()
	for name, lid := range map[string]string{"unknown": leaseID(77), "second": id, "late": late} {
		if _, err := env.create(lid, sandbox.Spec{}); err == nil {
			t.Errorf("%s: Create returned no error", name)
		}
	}
	env.profile.EnterShutdown()
	_, err = env.create(env.issue(""), sandbox.Spec{})
	wantCode(t, err, codes.Unavailable)
	if after := []float64{createsCounter(t, m, "created"), createsCounter(t, m, "failed")}; !reflect.DeepEqual(before, after) {
		t.Errorf("counters went from %v to %v on refusals", before, after)
	}
}

// A lease that leaves leased while its box is made (DELETE, a sweep) has the box
// destroyed at once, keeps its state and reason and counts as a failed create;
// a discarded box the provider cannot destroy stays held for the sweep.
func TestBackendCreate_LeaseEndsDuringCreate(t *testing.T) {
	logs := captureDefaultLog(t)
	for name, end := range map[string]func(env *sweepEnv, id string){
		"deleted": func(env *sweepEnv, id string) {
			if env.delete(id).Code != 204 {
				env.t.Fatal("DELETE failed")
			}
		},
		"expired by a sweep": func(env *sweepEnv, id string) { env.pastLeaseDeadline(); env.sweep() },
	} {
		t.Run(name, func(t *testing.T) {
			env := newSweepEnv(t)
			id := env.issue("")
			release, entered := env.blockCreates()
			done := make(chan error, 1)
			go func() { _, err := env.create(id, sandbox.Spec{}); done <- err }()
			<-entered
			end(env, id)
			close(release)
			err := <-done
			wantCode(t, err, codes.FailedPrecondition)
			if live := env.prov.live(); len(live) != 0 || env.prov.allBoxes()[0].closes != 1 {
				t.Errorf("live %v, closes %d; want the new box destroyed once", live, env.prov.allBoxes()[0].closes)
			}
			if s := env.readState(id); !strings.Contains(err.Error(), s.state) || s.hash.Valid {
				t.Errorf("error %v, lease %+v", err, s)
			}
			if createsCounter(t, env.metrics, "failed") != 1 || len(env.eventsOf(audit.EventSandboxCreate)) != 0 || !strings.Contains(logs.String(), id) {
				t.Error("the discard was not counted as failed, or emitted a create event, or was not logged")
			}
		})
	}
	env := newSweepEnv(t)
	id := env.issue("")
	release, entered := env.blockCreates()
	env.prov.setCloseErr(errors.New("podman rm failed"))
	done := make(chan error, 1)
	go func() { _, err := env.create(id, sandbox.Spec{}); done <- err }()
	<-entered
	env.delete(id)
	close(release)
	<-done
	if held := env.backend.heldBoxes(); len(held) != 1 || held[0].Labels["hub.lease"] != id || len(env.prov.live()) != 1 {
		t.Fatalf("held %v; want the box that could not be destroyed", held)
	}
	env.prov.setCloseErr(nil)
	if err := env.backend.closeProviderBox(context.Background(), env.backend.boxes[id]); err != nil || len(env.backend.heldBoxes()) != 0 {
		t.Errorf("closing the held box: %v, held %v", err, env.backend.heldBoxes())
	}
}

// Capabilities is the provider's with the network modes narrowed to
// network_modes; Reap runs the sweep only without a principal and returns
// nothing, the provider's text going to the log scrubbed.
func TestBackend_CapabilitiesAndReap(t *testing.T) {
	cfg := defaultLeaseConfig()
	cfg.NetworkModes = []string{"none", "unrestricted"}
	env := newBackendEnv(t, leaseEnvOptions{config: cfg})
	got := env.backend.Capabilities()
	if want := []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}; !reflect.DeepEqual(got.Network, want) || len(env.prov.Capabilities().Network) != 3 {
		t.Errorf("Network = %v; want %v with the provider's own untouched", got.Network, want)
	}
	got.Network = nil
	provided := env.prov.Capabilities()
	provided.Network = nil
	if !reflect.DeepEqual(got, provided) {
		t.Errorf("Capabilities = %+v; want the provider's apart from the modes", got)
	}

	logs := captureDefaultLog(t)
	env.prov.setReap([]string{"left-over"}, errors.New("podman rm failed at https://user:pw@registry.example/x"))
	before := env.prov.reapCount()
	if ids, err := env.backend.Reap(context.Background()); err != nil || len(ids) != 0 || env.prov.reapCount()-before != 1 {
		t.Errorf("Reap = (%v, %v) with %d provider reaps; want nothing and one sweep", ids, err, env.prov.reapCount()-before)
	}
	if out := logs.String(); !strings.Contains(out, "podman rm failed") || strings.Contains(out, "user:pw") {
		t.Errorf("the log:\n%s", out)
	}
	if ids, err := env.backend.Reap(withPrincipal(context.Background(), leaseID(1))); err != nil || len(ids) != 0 || env.prov.reapCount()-before != 1 {
		t.Errorf("Reap with a principal = (%v, %v) with %d reaps; want nothing done", ids, err, env.prov.reapCount()-before)
	}
}

// The handle redacts the env values of its spec and delegates the rest; its
// Close ends the lease as the client's once: not again, not after the lease
// ended, not within two seconds of the lease's time or after it, and with the
// reason shutdown in shutdown mode. Ending marks are counted.
func TestHandle_SpecAndClose(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue("")
	sb := env.mustCreate(id)
	built := env.prov.lastSpec()
	got := sb.Spec()
	for key := range built.Env {
		if got.Env[key] != "[redacted]" {
			t.Errorf("Env[%s] = %q; want [redacted]", key, got.Env[key])
		}
	}
	got.Env, built.Env = nil, nil
	if !reflect.DeepEqual(got, built) || env.prov.allBoxes()[0].spec.Env["GIT_PAT"] != plantedSecret {
		t.Errorf("Spec = %+v; want what was built with the provider's own spec intact", got)
	}
	if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(make([]byte, 1024)), Path: "."}); err != nil {
		t.Errorf("Sync: %v", err)
	}
	for range 2 {
		if err := sb.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if l := env.lease(id); l.State != "destroyed" || *l.EndReason != "client" || len(env.eventsOf(audit.EventSandboxDestroy)) != 1 || len(env.prov.live()) != 0 {
		t.Errorf("after Close twice: %s/%v, %d events", l.State, l.EndReason, len(env.eventsOf(audit.EventSandboxDestroy)))
	}

	for name, tc := range map[string]struct {
		before func(id string)
		reason string
	}{
		"after the lease was ended": {func(id string) { env.store.DestroyLease(id, "active", "api") }, "api"},
		"a second before expires_at": {func(id string) {
			expires, _ := apikit.ParseUTC(env.lease(id).ExpiresAt)
			env.clock.Advance(expires.Sub(env.clock.Now()) - time.Second)
		}, ""},
		"after expires_at": {func(id string) { env.clock.Advance(2 * time.Hour) }, ""},
	} {
		env.clock.Advance(-env.clock.Now().Sub(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)))
		id := env.issue("")
		sb := env.mustCreate(id)
		events := len(env.eventsOf(audit.EventSandboxDestroy))
		tc.before(id)
		if err := sb.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		l := env.lease(id)
		if (tc.reason == "" && l.State != "active") || (tc.reason != "" && *l.EndReason != tc.reason) || len(env.eventsOf(audit.EventSandboxDestroy)) != events {
			t.Errorf("%s: lease %s/%v, %d new events; want %q and none", name, l.State, l.EndReason, len(env.eventsOf(audit.EventSandboxDestroy))-events, tc.reason)
		}
	}
	env.clock.Advance(-env.clock.Now().Sub(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)))
	shut := env.issue("")
	sb = env.mustCreate(shut)
	env.profile.EnterShutdown()
	if err := sb.Close(context.Background()); err != nil || *env.lease(shut).EndReason != "shutdown" {
		t.Errorf("Close in shutdown mode: %v, reason %v", err, env.lease(shut).EndReason)
	}

	b := env.backend
	first, second := b.beginEnding("L"), b.beginEnding("L")
	first()
	if !b.isEnding("L") || b.isEnding("other") {
		t.Error("the mark went with one caller still destroying, or another lease is marked")
	}
	second()
	second()
	if b.isEnding("L") {
		t.Error("the mark stayed")
	}
}
