package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
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
)

// countingRepo is a lease repository that counts the leases it is asked for and
// can fail the read.
type countingRepo struct {
	leaseRepo
	mu     sync.Mutex
	gets   int
	getErr error
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

// wantCode fails the test unless err carries the gRPC code.
func wantCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("got no error; want one with code %s", code)
	}
	if got := status.Code(err); got != code {
		t.Fatalf("error %q has code %s; want %s", err, got, code)
	}
}

// TS-27-20: Create without a principal fails before a lease is loaded or the
// provider is called.
// Requirement: 27-REQ-4.1
func TestBackendCreate_NoPrincipal_TS_27_20(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	repo := &countingRepo{leaseRepo: env.store}
	b := newBackend(backendConfig{
		Service: env.profile, Store: repo, Now: env.clock.Now,
		Principal: func(context.Context) (string, bool) { return "", false },
	})
	if _, err := b.Create(context.Background(), sandbox.Spec{}); err == nil {
		t.Fatal("Create without a principal returned no error")
	}
	if env.prov.creates() != 0 || repo.gets != 0 {
		t.Errorf("provider Create calls = %d, leases loaded = %d; want 0 and 0", env.prov.creates(), repo.gets)
	}

	// The default reads the principal gatewayd puts on a context, which a plain
	// context does not have.
	b = newBackend(backendConfig{Service: env.profile, Store: repo, Now: env.clock.Now})
	if _, err := b.Create(context.Background(), sandbox.Spec{}); err == nil {
		t.Fatal("Create with the default principal function and a plain context returned no error")
	}
	if env.prov.creates() != 0 || repo.gets != 0 {
		t.Errorf("provider Create calls = %d, leases loaded = %d; want 0 and 0", env.prov.creates(), repo.gets)
	}
}

// TS-27-21: A Create for a lease that has one in flight is refused with
// FailedPrecondition and without a provider call, and every claim is released
// when its Create returns, whatever the outcome.
// Requirement: 27-REQ-4.2
func TestBackendCreate_ClaimIsExclusiveAndReleased_TS_27_21(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue(`{"profile":"tools"}`)

	block := make(chan struct{})
	env.prov.mu.Lock()
	env.prov.block = block
	env.prov.mu.Unlock()

	first := make(chan error, 1)
	go func() {
		_, err := env.create(id, sandbox.Spec{})
		first <- err
	}()
	select {
	case <-env.prov.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first Create never reached the provider")
	}
	if !env.backend.claimed(id) {
		t.Error("the lease is not claimed while its Create is in flight")
	}

	// A claim that did not hold would send this call to the blocked provider, so
	// it is bounded.
	second, cancel := context.WithTimeout(withPrincipal(context.Background(), id), 5*time.Second)
	defer cancel()
	_, err := env.backend.Create(second, sandbox.Spec{})
	wantCode(t, err, codes.FailedPrecondition)
	if n := env.prov.creates(); n != 1 {
		t.Errorf("provider Create calls = %d; want 1: the second Create must not reach the provider", n)
	}

	close(block)
	select {
	case err := <-first:
		if err != nil {
			t.Fatalf("the first Create failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first Create never returned")
	}
	if env.backend.claimed(id) {
		t.Error("the claim is still held after the Create returned")
	}

	t.Run("a failing Create releases its claim", func(t *testing.T) {
		other := env.issue(`{"profile":"tools"}`)
		env.prov.mu.Lock()
		env.prov.block = nil
		env.prov.createErr = errors.New("engine unreachable")
		env.prov.mu.Unlock()
		if _, err := env.create(other, sandbox.Spec{}); err == nil {
			t.Fatal("Create with a failing provider returned no error")
		}
		if env.backend.claimed(other) {
			t.Error("the claim is still held after a failing Create")
		}
	})
}

// TS-27-22: Create on a lease that is not leased names the state and changes
// nothing.
// Requirement: 27-REQ-4.3
func TestBackendCreate_NotLeased_TS_27_22(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue(`{"profile":"tools"}`)
	if changed, err := env.store.MarkActive(id, "box-1"); err != nil || !changed {
		t.Fatalf("MarkActive = (%v, %v)", changed, err)
	}
	before := *env.lease(id)

	_, err := env.create(id, sandbox.Spec{})
	wantCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "active") {
		t.Errorf("error %q does not name the state active", err)
	}
	if after := *env.lease(id); !reflect.DeepEqual(before, after) {
		t.Errorf("the lease changed: %+v -> %+v", before, after)
	}
	if n := env.prov.creates(); n != 0 {
		t.Errorf("provider Create calls = %d; want 0", n)
	}
}

// TS-27-23: Create on a leased lease that has expired is refused. A lease with
// less than a second left is expired too: its TTL, cut to whole seconds, would be
// zero, which a provider reads as an hour.
//
// Spec 28 (28-REQ-3.1) ends such a lease as expired in the same call, where spec 27
// left it as it was, so the lease reads expired afterwards. The lease timeout is
// the profile's whole hour in these tests, so that the deadline for a Create is the
// lease's expires_at and these cases are about the time the lease has left.
// Requirement: 27-REQ-4.4
func TestBackendCreate_Expired_TS_27_23(t *testing.T) {
	cfg := defaultLeaseConfig()
	cfg.LeaseTimeout = strPtr("1h")
	for name, offset := range map[string]time.Duration{
		"expires_at is now":          0,
		"expires_at is in the past":  -time.Minute,
		"less than a second is left": 500 * time.Millisecond,
	} {
		t.Run(name, func(t *testing.T) {
			env := newBackendEnv(t, leaseEnvOptions{config: cfg})
			id := env.issue(`{"profile":"tools"}`)
			expires, _ := apikit.ParseUTC(env.lease(id).ExpiresAt)
			env.clock.Advance(expires.Sub(env.clock.Now()) - offset)

			_, err := env.create(id, sandbox.Spec{})
			wantCode(t, err, codes.FailedPrecondition)
			if !strings.Contains(err.Error(), "expired") {
				t.Errorf("error %q does not say the lease has expired", err)
			}
			if after := env.lease(id); after.State != "expired" || after.EndReason == nil || *after.EndReason != "expired" {
				t.Errorf("the lease reads %s/%v; want it ended as expired by the Create", after.State, after.EndReason)
			}
			if n := env.prov.creates(); n != 0 {
				t.Errorf("provider Create calls = %d; want 0", n)
			}
		})
	}

	t.Run("a second left is a one second TTL", func(t *testing.T) {
		env := newBackendEnv(t, leaseEnvOptions{config: cfg})
		id := env.issue(`{"profile":"tools"}`)
		expires, _ := apikit.ParseUTC(env.lease(id).ExpiresAt)
		env.clock.Advance(expires.Sub(env.clock.Now()) - time.Second)
		if _, err := env.create(id, sandbox.Spec{}); err != nil {
			t.Fatalf("Create with a second left: %v", err)
		}
		if ttl := env.prov.lastSpec().TTL; ttl != time.Second {
			t.Errorf("TTL = %v; want 1s", ttl)
		}
	})
}

// TS-27-24: A lease whose profile is no longer configured fails with a message
// that names the profile, and one failed event.
// Requirement: 27-REQ-4.5
func TestBackendCreate_ProfileGone_TS_27_24(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue(`{"profile":"tools"}`)

	// The hub that serves the Create has been restarted with a file that no
	// longer has the profile "tools".
	only := &Config{Provider: "podman", Profiles: []ProfileConfig{{Name: "data", Image: "registry.example/data:1", TTL: "30m"}}}
	restarted, err := New(requireValid(t, only), env.prov)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b := newBackend(backendConfig{Service: restarted, Store: env.store, Audit: env.emitter, Now: env.clock.Now, Principal: testPrincipal})

	_, err = b.Create(withPrincipal(context.Background(), id), sandbox.Spec{})
	wantCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), `"tools"`) {
		t.Errorf("error %q does not name the profile", err)
	}
	lease := env.lease(id)
	if lease.State != "failed" || lease.Error == nil || *lease.Error != err.Error() {
		t.Errorf("lease state %q, error %v; want failed with the message of the error", lease.State, lease.Error)
	}
	events := env.eventsOf(audit.EventSandboxFailed)
	if len(events) != 1 {
		t.Fatalf("hub.sandbox.failed events = %d; want 1", len(events))
	}
	if events[0].Metadata["lease_id"] != id || events[0].Metadata["error"] != err.Error() {
		t.Errorf("event metadata = %v; want the lease id and the error", events[0].Metadata)
	}
	if n := env.prov.creates(); n != 0 {
		t.Errorf("provider Create calls = %d; want 0", n)
	}
}

// TS-27-25: The provider receives hub's spec, built from the lease, and not the
// caller's. Spec 26's lease records every field of the profile's snapshot (the
// environment, optional secrets, require_landlock and every resource), so none
// is read from the profile; the profile is only looked up to see that it is
// still configured.
// Requirement: 27-REQ-4.6
func TestBackendCreate_BuildsHubsSpec_TS_27_25(t *testing.T) {
	cfg := defaultLeaseConfig()
	cfg.Profiles[0].TTL = "2h"
	cfg.LeaseTimeout = strPtr("2h") // the lease's time to Create is its whole TTL, as in spec 27
	env := newBackendEnv(t, leaseEnvOptions{config: cfg})
	id := env.issue(`{"profile":"tools","ttl":"90m"}`)
	// 79 minutes and 59.5 seconds are left.
	env.clock.Advance(10*time.Minute + 500*time.Millisecond)

	_, err := env.create(id, sandbox.Spec{
		Image:           "evil",
		Env:             map[string]string{"X": "1"},
		Network:         sandbox.Network{Mode: sandbox.NetworkUnrestricted},
		Resources:       sandbox.Resources{CPUs: 64, MemoryBytes: 1 << 40, PIDs: 1 << 20, DiskBytes: 1 << 40},
		TTL:             99 * time.Hour,
		RequireLandlock: false,
		Workdir:         "/w",
		Provider:        map[string]any{"privileged": true},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := env.prov.lastSpec()
	if got.Image != "registry.example/tools:1" {
		t.Errorf("Image = %q; want the lease's image", got.Image)
	}
	if got.Network.Mode != sandbox.NetworkNone || len(got.Network.Allow) != 0 {
		t.Errorf("Network = %+v; want the lease's network none", got.Network)
	}
	if want := (sandbox.Resources{CPUs: 1, MemoryBytes: 512 << 20, PIDs: 100}); got.Resources != want {
		t.Errorf("Resources = %+v; want %+v", got.Resources, want)
	}
	if !got.RequireLandlock {
		t.Error("RequireLandlock = false; want the lease's true")
	}
	if got.Workdir != "/w" {
		t.Errorf("Workdir = %q; want the caller's /w", got.Workdir)
	}
	if want := 79*time.Minute + 59*time.Second; got.TTL != want {
		t.Errorf("TTL = %v; want %v: the time left, in whole seconds", got.TTL, want)
	}
	if _, has := got.Env["X"]; has || got.Env["SECRET_ENV"] != "envval" {
		t.Errorf("Env = %v; want the lease's fixed env and no key X", got.Env)
	}
	if got.Provider != nil {
		t.Errorf("Provider options = %v; want none", got.Provider)
	}
}

// The lease decides, not the profile it was copied from: after the hub is
// restarted with the profile edited in every field the lease copied, a lease issued
// before still gets the box it was issued for.
// Requirement: 27-REQ-4.6
func TestBackendCreate_TheLeaseWinsOverTheEditedProfile(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue(`{"profile":"tools"}`)

	edited := defaultLeaseConfig()
	edited.NetworkModes = []string{"none", "unrestricted"}
	p := &edited.Profiles[0]
	p.Image, p.CPUs, p.MemoryMB, p.PIDs, p.DiskMB = "registry.example/other:9", 8, 4096, 1000, 2048
	p.Network, p.RequireLandlock = strPtr("unrestricted"), false
	p.Env = map[string]string{"SECRET_ENV": "changed", "NEW": "x"}
	restarted, err := New(requireValid(t, edited), env.prov)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b := newBackend(backendConfig{Service: restarted, Store: env.store, Secrets: env.secrets, DB: env.db, Audit: env.emitter, Now: env.clock.Now, Principal: testPrincipal})

	if _, err := b.Create(withPrincipal(context.Background(), id), sandbox.Spec{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := env.prov.lastSpec()
	if got.Image != "registry.example/tools:1" || got.Network.Mode != sandbox.NetworkNone || !got.RequireLandlock {
		t.Errorf("image %q, network %v, landlock %v; want the lease's tools:1, none, true", got.Image, got.Network.Mode, got.RequireLandlock)
	}
	if want := (sandbox.Resources{CPUs: 1, MemoryBytes: 512 << 20, PIDs: 100}); got.Resources != want {
		t.Errorf("Resources = %+v; want the lease's %+v", got.Resources, want)
	}
	if got.Env["SECRET_ENV"] != "envval" {
		t.Errorf("Env = %v; want the lease's SECRET_ENV=envval", got.Env)
	}
	if _, has := got.Env["NEW"]; has {
		t.Errorf("Env = %v; want nothing from the edited profile", got.Env)
	}
}

// TS-27-26: The labels drop the caller's hub.* keys, carry the lease's labels as
// hub.<key>, and end with hub.lease and hub.workspace.
// Requirement: 27-REQ-4.7
func TestBackendCreate_Labels_TS_27_26(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue(`{"profile":"tools","labels":{"team":"x"}}`)

	_, err := env.create(id, sandbox.Spec{Labels: map[string]string{"a": "1", "hub.lease": "forged", "hub.workspace": "other", "hub.team": "forged", "hub.extra": "forged"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := map[string]string{"a": "1", "hub.team": "x", "hub.lease": id, "hub.workspace": "widgets"}
	if got := env.prov.lastSpec().Labels; !reflect.DeepEqual(got, want) {
		t.Errorf("Labels = %v; want %v", got, want)
	}
}

// The provider reserves the label agentkit.sandbox and every label under it
// (agentkit.sandbox.*), and refuses a spec that has one, which would let a caller
// fail its own lease; hub drops them. A key that only starts with the same letters
// is not reserved.
func TestBackendCreate_DropsTheProvidersReservedLabels(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue(`{"profile":"tools"}`)
	caller := map[string]string{"agentkit.sandbox": "x", "agentkit.sandbox.id": "y", "agentkit.sandbox.": "z", "agentkit.sandboxes": "kept", "a": "1"}
	if _, err := env.create(id, sandbox.Spec{Labels: caller}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := env.prov.lastSpec().Labels
	for _, key := range []string{"agentkit.sandbox", "agentkit.sandbox.id", "agentkit.sandbox."} {
		if _, has := got[key]; has {
			t.Errorf("Labels = %v; want the reserved key %q dropped", got, key)
		}
	}
	if got["a"] != "1" || got["agentkit.sandboxes"] != "kept" {
		t.Errorf("Labels = %v; want the other keys kept", got)
	}
}

// TS-27-27: Whatever the caller puts in its spec, the image, the network, the
// resources, require_landlock and the environment of the provider's spec are
// those of an empty caller spec on an equivalent lease.
// Requirement: 27-REQ-4.8
func TestBackendCreate_CallerSpecNeverInfluences_TS_27_27(t *testing.T) {
	cfg := defaultLeaseConfig()
	room := 1000.0 // every iteration issues a lease of its own
	cfg.MaxSandboxes = &room
	env := newBackendEnv(t, leaseEnvOptions{config: cfg})
	specFor := func(caller sandbox.Spec) sandbox.Spec {
		t.Helper()
		id := env.issue(`{"profile":"tools"}`)
		sb, err := env.create(id, caller)
		if err != nil {
			t.Fatalf("Create(%+v): %v", caller, err)
		}
		if err := sb.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		return env.prov.lastSpec()
	}
	base := specFor(sandbox.Spec{})

	rng := rand.New(rand.NewPCG(27, 27))
	word := func() string { return fmt.Sprintf("v%d", rng.IntN(1000)) }
	for i := range 60 {
		caller := sandbox.Spec{
			Image:           word(),
			Workdir:         word(),
			Env:             map[string]string{word(): word(), "SECRET_ENV": word()},
			Resources:       sandbox.Resources{CPUs: float64(rng.IntN(64)), MemoryBytes: rng.Int64N(1 << 40), PIDs: rng.Int64N(1 << 20), DiskBytes: rng.Int64N(1 << 40)},
			Network:         sandbox.Network{Mode: sandbox.NetworkMode(rng.IntN(3))},
			TTL:             time.Duration(rng.IntN(100)) * time.Hour,
			RequireLandlock: rng.IntN(2) == 0,
			Labels:          map[string]string{word(): word(), "hub." + word(): word()},
			Provider:        map[string]any{word(): rng.IntN(10)},
		}
		got := specFor(caller)
		if got.Image != base.Image || got.Network.Mode != base.Network.Mode || len(got.Network.Allow) != len(base.Network.Allow) ||
			got.Resources != base.Resources || got.RequireLandlock != base.RequireLandlock || !reflect.DeepEqual(got.Env, base.Env) {
			t.Fatalf("iteration %d: caller spec %+v changed the provider's spec:\n got %+v\nwant %+v", i, caller, got, base)
		}
	}
}

// A lease that names a network the provider no longer honours fails with a
// message that names it, and the provider is not asked for a box with another
// network.
func TestBackendCreate_NetworkNotHonoured(t *testing.T) {
	cfg := defaultLeaseConfig()
	cfg.NetworkModes = []string{"none", "unrestricted"}
	cfg.Profiles[0].Network = strPtr("unrestricted")
	env := newBackendEnv(t, leaseEnvOptions{config: cfg})
	id := env.issue(`{"profile":"tools"}`)

	env.prov.mu.Lock()
	env.prov.caps.Network = []sandbox.NetworkMode{sandbox.NetworkNone}
	env.prov.mu.Unlock()

	_, err := env.create(id, sandbox.Spec{})
	wantCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "unrestricted") {
		t.Errorf("error %q does not name the network", err)
	}
	if lease := env.lease(id); lease.State != "failed" || lease.Error == nil {
		t.Errorf("lease state %q, error %v; want failed with a message", lease.State, lease.Error)
	}
	if n := env.prov.creates(); n != 0 {
		t.Errorf("provider Create calls = %d; want 0", n)
	}
	if got := len(env.eventsOf(audit.EventSandboxFailed)); got != 1 {
		t.Errorf("hub.sandbox.failed events = %d; want 1", got)
	}
}

// A memory size that does not fit in bytes cannot be built into a spec.
func TestBuildSpec_MemoryTooLarge(t *testing.T) {
	lease := &Lease{ID: "sbx_1", Network: "none", Resources: Resources{MemoryMB: 1 << 62}, ExpiresAt: "2030-01-02T04:04:05Z"}
	caps := newRecordingProvider().Capabilities()
	if _, err := buildSpec(lease, sandbox.Spec{}, caps, time.Hour); err == nil || !strings.Contains(err.Error(), "memory_mb") {
		t.Errorf("buildSpec with an enormous memory_mb = %v; want an error naming memory_mb", err)
	}
	lease.Resources = Resources{DiskMB: 1 << 62}
	if _, err := buildSpec(lease, sandbox.Spec{}, caps, time.Hour); err == nil || !strings.Contains(err.Error(), "disk_mb") {
		t.Errorf("buildSpec with an enormous disk_mb = %v; want an error naming disk_mb", err)
	}
}

// A lease the store cannot read is an internal error that tells the caller
// nothing, and a lease that is not there is not found.
func TestBackendCreate_StoreErrors(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	repo := &countingRepo{leaseRepo: env.store, getErr: errors.New("disk I/O error at /var/lib/hub/afhub.db")}
	b := newBackend(backendConfig{Service: env.profile, Store: repo, Now: env.clock.Now, Principal: testPrincipal})

	_, err := b.Create(withPrincipal(context.Background(), leaseID(1)), sandbox.Spec{})
	wantCode(t, err, codes.Internal)
	if strings.Contains(err.Error(), "afhub.db") {
		t.Errorf("the error repeats the cause of the failure: %q", err)
	}

	_, err = env.create(leaseID(404), sandbox.Spec{})
	wantCode(t, err, codes.NotFound)
	if n := env.prov.creates(); n != 0 {
		t.Errorf("provider Create calls = %d; want 0", n)
	}
}
