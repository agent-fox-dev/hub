package sandboxes

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// within runs step until it reports true, for at most 20 seconds. Hub does not
// redial a remote that went away: gRPC's client reconnects by itself, and after a
// failed attempt it waits a second or more before the next one, so a call made
// just after the remote is back can still fail.
func within(t *testing.T, what string, step func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if step() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// destroyFailures counts the error records of a box that could not be destroyed.
func destroyFailures(t *testing.T, logs *logBuffer) int {
	t.Helper()
	n := 0
	for _, rec := range recordsOf(t, logs) {
		if rec["level"] == "ERROR" && strings.Contains(rec["msg"].(string), "destroying a box") {
			n++
		}
	}
	return n
}

// outageEnv is hub over the remote with a network between them that can be cut.
type outageEnv struct {
	*remoteLeaseEnv
	net *flakyNet
}

func newOutageEnv(t *testing.T) *outageEnv {
	t.Helper()
	n := &flakyNet{}
	env := newRemoteEnvVia(t, leaseEnvOptions{}, n.dial)
	return &outageEnv{remoteLeaseEnv: &remoteLeaseEnv{remoteEnv: env, gw: env.startGateway()}, net: n}
}

// TS-29-39: Issuing a lease and listing the profiles keep working while the remote
// is down, since neither calls the provider.
// Requirement: 29-REQ-9.1
func TestRemoteDown_LeasesAndProfilesKeepWorking_TS_29_39(t *testing.T) {
	env := newRemoteLeaseEnv(t)
	env.remote.stop()

	if rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth()); rec.Code != http.StatusCreated {
		t.Errorf("POST answered %d: %s; want 201", rec.Code, rec.Body)
	}
	if rec := getProfiles(t, newHandlerEcho(env.profile), apiKeyAuth()); rec.Code != http.StatusOK {
		t.Errorf("the profile listing answered %d: %s; want 200", rec.Code, rec.Body)
	}
}

// TS-29-40: A Create while the remote is unreachable fails the lease with a
// scrubbed error.
// Requirement: 29-REQ-9.2
func TestRemoteDown_CreateFailsTheLease_TS_29_40(t *testing.T) {
	env := newRemoteLeaseEnv(t)
	env.remote.stop()
	lease := env.issueLease("tools")
	id := lease["id"].(string)

	began := time.Now()
	_, err := env.gw.dial(t, lease["token"].(string)).Create(context.Background(), sandbox.Spec{Image: "ignored"})
	if err == nil {
		t.Fatal("Create succeeded against a remote that is down")
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Create took %v against a remote that is down", took)
	}
	got := env.lease(id)
	if got.State != "failed" || got.Error == nil || *got.Error == "" {
		t.Fatalf("lease state %q, error %v; want failed with a message", got.State, got.Error)
	}
	for what, text := range map[string]string{"the lease's error": *got.Error, "the caller's error": err.Error()} {
		for _, secret := range []string{remoteToken, env.remote.url(), env.remote.host()} {
			if strings.Contains(text, secret) {
				t.Errorf("%s holds %q: %s", what, secret, text)
			}
		}
	}
	if n := len(env.eventsOf(audit.EventSandboxFailed)); n != 1 {
		t.Errorf("hub.sandbox.failed events = %d; want 1", n)
	}
	if env.profile.isRemote() && status.Code(err) != codes.Unavailable {
		t.Errorf("the caller's error has code %s; want Unavailable", status.Code(err))
	}
}

// TS-29-41: DELETE of an active lease while the remote is down answers 502
// sandbox_provider_error and changes nothing.
// Requirement: 29-REQ-9.3
func TestRemoteDown_DeleteAnswers502AndChangesNothing_TS_29_41(t *testing.T) {
	env := newRemoteLeaseEnv(t)
	lease := env.issueLease("tools")
	id, token := lease["id"].(string), lease["token"].(string)
	createVia(t, env.gw.dial(t, token))
	env.remote.stop()

	rec := env.deleteRecorded(id)
	if rec.Code != http.StatusBadGateway || errorType(t, rec) != "sandbox_provider_error" {
		t.Fatalf("DELETE answered %d (%s); want 502 sandbox_provider_error: %s", rec.Code, errorType(t, rec), rec.Body)
	}
	for _, secret := range []string{remoteToken, env.remote.url(), env.remote.host()} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("the response holds %q: %s", secret, rec.Body)
		}
	}
	if l := env.lease(id); l.State != "active" || l.EndReason != nil || l.EndedAt != nil {
		t.Errorf("lease state %q, end_reason %v; want active and not ended", l.State, l.EndReason)
	}
	if _, ok := lookupToken(env.backendEnv, token); !ok {
		t.Error("the lease token stopped working")
	}
	if p, err := gateway.Dial(context.Background(), env.gw.URL(), gateway.Options{Token: token}); err != nil {
		t.Errorf("dialling hub's gateway with the lease token answered %v; want it to authenticate", err)
	} else {
		_ = p.Close()
	}
	if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 0 {
		t.Errorf("hub.sandbox.destroy events = %d; want 0", n)
	}
}

// TS-29-42: A Close while the remote is down returns an error and leaves the lease
// active.
// Requirement: 29-REQ-9.4
func TestRemoteDown_CloseFailsAndLeavesTheLeaseActive_TS_29_42(t *testing.T) {
	env := newRemoteLeaseEnv(t)
	lease := env.issueLease("tools")
	id := lease["id"].(string)
	box := createVia(t, env.gw.dial(t, lease["token"].(string)))
	env.remote.stop()

	if err := box.Close(context.Background()); err == nil {
		t.Fatal("Close succeeded against a remote that is down")
	}
	if l := env.lease(id); l.State != "active" || l.EndReason != nil {
		t.Errorf("lease state %q, end_reason %v; want active", l.State, l.EndReason)
	}
}

// TS-29-43: The sweep that cannot destroy an overdue lease's box because the remote
// is down logs the failure, and tries the box again on its next run, which destroys
// it once the remote is back. As in spec 28 the lease itself is ended by the first
// sweep: access does not wait for the provider.
// Requirement: 29-REQ-9.5
func TestRemoteDown_SweepLogsAndTriesAgain_TS_29_43(t *testing.T) {
	env := newRemoteLeaseEnv(t)
	lease := env.issueLease("tools")
	id, token := lease["id"].(string), lease["token"].(string)
	createVia(t, env.gw.dial(t, token))
	env.clock.Advance(2 * time.Hour)
	env.remote.stop()

	if err := env.gw.sweeper.Sweep(context.Background()); err == nil {
		t.Error("the sweep reported no failure although the remote is down")
	}
	// The failed destroy of the overdue lease's box, and the orphan step's try at the
	// same box a moment later, are logged one record each; none holds the remote.
	if n := destroyFailures(t, env.logs); n != 2 {
		t.Errorf("%d records about a box that could not be destroyed; want 2: the expiry and the orphan step\n%s", n, env.logs)
	}
	if l := env.lease(id); l.State != "destroyed" || l.EndReason == nil || *l.EndReason != "expired" {
		t.Errorf("lease state %q, end_reason %v; want destroyed by expiry", l.State, l.EndReason)
	}
	if _, ok := lookupToken(env.backendEnv, token); ok {
		t.Error("the token of the expired lease still works")
	}
	if held := env.gw.backend.heldBoxes(); len(held) != 1 {
		t.Errorf("hub holds %v; want the one box it could not destroy", held)
	}

	env.remote.restart()
	within(t, "a sweep to destroy the box on the restarted remote", func() bool {
		return env.gw.sweeper.Sweep(context.Background()) == nil && len(env.gw.backend.heldBoxes()) == 0
	})
	if live := env.remoteLive(); len(live) != 0 {
		t.Errorf("the remote holds %v after the sweep; want nothing", live)
	}
}

// TS-29-44: After the remote restarts on the same address, hub's next Create
// succeeds without hub redialling: the factory was called once.
// Requirement: 29-REQ-9.6
func TestRemoteRestart_NextCreateSucceedsWithoutARedial_TS_29_44(t *testing.T) {
	env := newRemoteLeaseEnv(t)
	first := env.issueLease("tools")
	createVia(t, env.gw.dial(t, first["token"].(string)))

	env.remote.stop()
	env.remote.restart()

	var box sandbox.Sandbox
	within(t, "a Create to succeed on the restarted remote", func() bool {
		lease := env.issueLease("data")
		sb, err := env.gw.dial(t, lease["token"].(string)).Create(context.Background(), sandbox.Spec{Image: "ignored"})
		box = sb
		return err == nil
	})
	if box == nil || box.ID() == "" {
		t.Fatal("Create returned no sandbox")
	}
	if live := env.remoteLive(); len(live) != 1 || live[0] != box.ID() {
		t.Errorf("the restarted remote holds %v; want the new box %s", live, box.ID())
	}
	if n := env.factory.callCount(); n != 1 {
		t.Errorf("the gateway factory was called %d times; want 1: hub does not redial", n)
	}
}

// A network that is cut leaves the box on the remote, so a destroy that is tried
// again after the network is back has to reach the remote: DELETE repeated after
// the 502 destroys the box there, and so does the sweep for the box of a lease it
// ended while the network was down.
func TestRemotePartition_TryingAgainDestroysTheBoxOnTheRemote(t *testing.T) {
	t.Run("DELETE", func(t *testing.T) {
		env := newOutageEnv(t)
		lease := env.issueLease("tools")
		id := lease["id"].(string)
		createVia(t, env.gw.dial(t, lease["token"].(string)))
		env.net.cut()

		if rec := env.deleteRecorded(id); rec.Code != http.StatusBadGateway {
			t.Fatalf("DELETE answered %d: %s; want 502", rec.Code, rec.Body)
		}
		if live := env.remoteLive0(); len(live) != 1 {
			t.Fatalf("the remote holds %v behind the cut network; want its box", live)
		}
		env.net.heal()
		within(t, "DELETE to reach the remote again", func() bool {
			code, _ := env.deleteLease(id)
			return code == http.StatusNoContent
		})
		if live := env.remoteLive0(); len(live) != 0 {
			t.Errorf("the box is still on the remote after the DELETE that worked: %v", live)
		}
	})

	t.Run("sweep", func(t *testing.T) {
		env := newOutageEnv(t)
		lease := env.issueLease("tools")
		id := lease["id"].(string)
		createVia(t, env.gw.dial(t, lease["token"].(string)))
		env.clock.Advance(2 * time.Hour)
		env.net.cut()

		_ = env.gw.sweeper.Sweep(context.Background())
		if l := env.lease(id); l.State != "destroyed" {
			t.Fatalf("lease state %q after the sweep; want destroyed", l.State)
		}
		if live := env.remoteLive0(); len(live) != 1 {
			t.Fatalf("the remote holds %v behind the cut network; want its box", live)
		}
		env.net.heal()
		within(t, "a sweep to reach the remote again", func() bool {
			return env.gw.sweeper.Sweep(context.Background()) == nil && len(env.gw.backend.heldBoxes()) == 0
		})
		if live := env.remoteLive0(); len(live) != 0 {
			t.Errorf("the box is still on the remote after the sweep that worked: %v", live)
		}
	})
}

// remoteLive0 lists the boxes the remote's backend holds, from the remote's own
// side: it does not depend on the network between hub and the remote, and hub's
// client may still be backing off after the network is back.
func (e *outageEnv) remoteLive0() []string { return e.prov.live() }
