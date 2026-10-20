package sandboxes

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
)

// remoteLeaseEnv is hub over the in-process remote with hub's gateway serving.
type remoteLeaseEnv struct {
	*remoteEnv
	gw *testGateway
}

func newRemoteLeaseEnv(t *testing.T) *remoteLeaseEnv {
	t.Helper()
	env := newRemoteEnv(t, leaseEnvOptions{})
	return &remoteLeaseEnv{remoteEnv: env, gw: env.startGateway()}
}

// issueLease posts a lease of the profile and returns its representation, the
// token among it.
func (e *remoteLeaseEnv) issueLease(profile string) map[string]any {
	e.t.Helper()
	rec := e.post("widgets", `{"profile":"`+profile+`"}`, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
	}
	return jsonObject(e.t, rec)
}

// remoteLive lists the ids of the boxes the remote gateway holds for hub's token.
func (e *remoteLeaseEnv) remoteLive() []string {
	e.t.Helper()
	infos, err := e.profile.Provider().(RemoteClient).List(context.Background())
	if err != nil {
		e.t.Fatalf("List on the remote: %v", err)
	}
	var ids []string
	for _, info := range infos {
		ids = append(ids, info.ID)
	}
	return ids
}

// TS-29-27: A lease followed by Dial and Create produces one box on the remote
// with the lease's image and TTL and an environment of the profile's env merged
// with the resolved secrets.
// Requirement: 29-REQ-7.1
func TestRemoteLease_CreatesOneBoxWithTheLeasesSpec_TS_29_27(t *testing.T) {
	env := newRemoteLeaseEnv(t)
	before := env.prov.creates()
	lease := env.issueLease("tools")

	client := env.gw.dial(t, lease["token"].(string))
	box := createVia(t, client)
	if box == nil || box.ID() == "" {
		t.Fatal("the caller received no sandbox handle")
	}

	if got := env.prov.creates() - before; got != 1 {
		t.Fatalf("the remote's backend saw %d Creates; want 1", got)
	}
	spec := env.prov.lastSpec()
	if spec.Image != lease["image"] || spec.Image != "registry.example/tools:1" {
		t.Errorf("the box's image = %q; want the lease's %q", spec.Image, lease["image"])
	}
	if spec.TTL != time.Hour {
		t.Errorf("the box's TTL = %v; want the hour the lease has left", spec.TTL)
	}
	wantEnv := map[string]string{"SECRET_ENV": "envval", "GIT_PAT": plantedSecret}
	if len(spec.Env) != len(wantEnv) {
		t.Errorf("the box's environment = %v; want %v", spec.Env, wantEnv)
	}
	for k, v := range wantEnv {
		if spec.Env[k] != v {
			t.Errorf("the box's %s = %q; want %q (the profile's env merged with the resolved secrets)", k, spec.Env[k], v)
		}
	}
	if live := env.remoteLive(); len(live) != 1 || live[0] != box.ID() {
		t.Errorf("the remote holds %v; want the one box %s", live, box.ID())
	}
	if l := env.lease(lease["id"].(string)); l.State != "active" || l.SandboxID == nil || *l.SandboxID != box.ID() {
		t.Errorf("lease state %q, sandbox_id %v; want active with the box", l.State, l.SandboxID)
	}
}

// TS-29-28: Only hub's token reaches the remote, never a lease token, and the
// lease's gateway_url is hub's own gateway, not the configured remote.
// Requirement: 29-REQ-7.2
func TestRemoteLease_OnlyHubsTokenReachesTheRemote_TS_29_28(t *testing.T) {
	env := newRemoteLeaseEnv(t)
	first := env.issueLease("tools")
	second := env.issueLease("data")
	createVia(t, env.gw.dial(t, first["token"].(string)))
	createVia(t, env.gw.dial(t, second["token"].(string)))

	for _, token := range env.remote.tokens() {
		if token != remoteToken {
			t.Errorf("the remote was shown a token that is not hub's (%d bytes)", len(token))
		}
		for _, lease := range []map[string]any{first, second} {
			if token == lease["token"] {
				t.Error("a lease token reached the remote")
			}
		}
	}
	if len(env.remote.tokens()) == 0 {
		t.Fatal("the remote was shown no token at all")
	}
	for _, lease := range []map[string]any{first, second} {
		if lease["gateway_url"] != env.gw.URL() {
			t.Errorf("gateway_url = %v; want hub's own gateway %s", lease["gateway_url"], env.gw.URL())
		}
		if lease["gateway_url"] == env.remote.url() || strings.Contains(lease["gateway_url"].(string), env.remote.host()) {
			t.Errorf("gateway_url %v is the configured remote", lease["gateway_url"])
		}
	}
	if env.profile.gateway.externalURL != "" || env.gw.URL() == env.remote.url() {
		t.Errorf("hub's gateway is at %s, the remote at %s; the test needs two addresses", env.gw.URL(), env.remote.url())
	}
}

// TS-29-29: A second Create under a lease's token is refused FailedPrecondition by
// the backend (Unavailable with its message over the wire), and a token hub never
// minted is Unauthenticated, neither reaching the remote.
// Requirement: 29-REQ-7.3
func TestRemoteLease_RefusalsDoNotReachTheRemote_TS_29_29(t *testing.T) {
	env := newRemoteLeaseEnv(t)
	lease := env.issueLease("tools")
	id, token := lease["id"].(string), lease["token"].(string)
	client := env.gw.dial(t, token)
	createVia(t, client)
	creates, rpcs := env.prov.creates(), env.remote.rpcs()

	// The backend itself.
	_, err := env.create(id, sandbox.Spec{})
	wantCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(err.Error(), "only a leased lease can create its box") {
		t.Errorf("the second Create was refused with %q", err)
	}
	// Through hub's gateway, where gatewayd answers a backend's refusal Unavailable
	// with the message.
	_, err = client.Create(context.Background(), sandbox.Spec{Image: "ignored"})
	if err == nil || !strings.Contains(err.Error(), "only a leased lease can create its box") {
		t.Errorf("the second Create through the gateway answered %v; want a refusal with the backend's message", err)
	}
	// A token hub never minted.
	_, err = gateway.Dial(context.Background(), env.gw.URL(), gateway.Options{Token: "never-minted-token-0123456789-abcdef"})
	if !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("Dial with a token hub never minted answered %v; want Unauthenticated", err)
	}

	if got := env.prov.creates(); got != creates {
		t.Errorf("the remote saw %d Creates after the refusals; want %d", got, creates)
	}
	if got := env.remote.rpcs(); got != rpcs {
		t.Errorf("the refusals cost the remote %d RPCs; want none", got-rpcs)
	}
	if l := env.lease(id); l.State != "active" {
		t.Errorf("lease state %q after the refused Create; want active", l.State)
	}
}

// TS-29-30: DELETE and the caller's Close each destroy the box on the remote and
// revoke the lease token.
// Requirement: 29-REQ-7.4
func TestRemoteLease_DeleteAndCloseDestroyTheRemoteBox_TS_29_30(t *testing.T) {
	env := newRemoteLeaseEnv(t)
	first, second := env.issueLease("tools"), env.issueLease("tools")
	firstID, firstToken := first["id"].(string), first["token"].(string)
	secondID, secondToken := second["id"].(string), second["token"].(string)
	createVia(t, env.gw.dial(t, firstToken))
	secondBox := createVia(t, env.gw.dial(t, secondToken))
	if live := env.remoteLive(); len(live) != 2 {
		t.Fatalf("the remote holds %v; want two boxes", live)
	}

	if code, body := env.deleteLease(firstID); code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", code, body)
	}
	if live := env.remoteLive(); len(live) != 1 {
		t.Errorf("after DELETE the remote holds %v; want the second box only", live)
	}
	if err := secondBox.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if live := env.remoteLive(); len(live) != 0 {
		t.Errorf("after Close the remote holds %v; want nothing", live)
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("the remote's backend holds %v; want nothing", live)
	}

	for name, token := range map[string]string{"DELETE": firstToken, "Close": secondToken} {
		_, err := gateway.Dial(context.Background(), env.gw.URL(), gateway.Options{Token: token})
		if !errors.Is(err, gateway.ErrUnauthenticated) {
			t.Errorf("after %s, dialling with the lease token answered %v; want Unauthenticated", name, err)
		}
	}
	if l := env.lease(firstID); l.State != "destroyed" || l.EndReason == nil || *l.EndReason != "api" {
		t.Errorf("DELETE: lease state %q, end_reason %v", l.State, l.EndReason)
	}
	if l := env.lease(secondID); l.State != "destroyed" || l.EndReason == nil || *l.EndReason != "client" {
		t.Errorf("Close: lease state %q, end_reason %v", l.State, l.EndReason)
	}
	if !slices.Equal(env.remote.tokens()[len(env.remote.tokens())-1:], []string{remoteToken}) {
		t.Error("the last token the remote saw is not hub's")
	}
}
