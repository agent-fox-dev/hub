package sandboxes

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// remoteConfigFile writes a config file for provider "gateway" at url, with the
// token in the environment variable AGENTKIT_GATEWAY_TOKEN, as an operator
// would, and one profile.
func remoteConfigFile(t *testing.T, url, token string) string {
	t.Helper()
	t.Setenv("AGENTKIT_GATEWAY_TOKEN", token)
	return writeConfigFile(t, t.TempDir(), gatewayTOML(`
gateway_url = "`+url+`"
gateway_token = "${AGENTKIT_GATEWAY_TOKEN}"
`))
}

// requireStartFailure fails the test unless err is the failure to start against
// the remote gateway: it begins with sandbox:, names the three keys, and holds
// none of the forbidden texts.
func requireStartFailure(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("start succeeded; want it refused")
	}
	text := err.Error()
	if !strings.HasPrefix(text, "sandbox:") {
		t.Errorf("the error does not start with sandbox:: %v", err)
	}
	for _, key := range []string{"sandbox.provider", "sandbox.gateway_url", "sandbox.gateway_token"} {
		if !strings.Contains(text, key) {
			t.Errorf("the error does not name %s: %v", key, err)
		}
	}
	for _, bad := range forbidden {
		if bad != "" && strings.Contains(text, bad) {
			t.Errorf("the error holds %q: %v", bad, err)
		}
	}
}

// TS-29-2: Init with provider gateway against a reachable remote returns a
// running service whose provider is the remote client.
// Requirement: 29-REQ-1.1
func TestInit_GatewayProviderAgainstRemote_TS_29_2(t *testing.T) {
	remote := startRemoteGateway(t, newRecordingProvider())
	path := remoteConfigFile(t, remote.url(), remoteToken)
	logger, logs := newCapturedLogger()

	svc, err := Init(logger, path, noPodman(t))
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if svc == nil {
		t.Fatal("Init returned a nil service")
	}
	client, ok := svc.Provider().(*gateway.Provider)
	if !ok {
		t.Fatalf("Provider() is a %T; want the *gateway.Provider that gateway.Dial returned", svc.Provider())
	}
	t.Cleanup(func() { _ = client.Close() })
	if info := client.Gateway(); info.URL != remote.url() {
		t.Errorf("the client is for %q; want the configured %q", info.URL, remote.url())
	}
	if recs := logRecords(t, logs); len(recs) != 1 || recs[0]["provider"] != "gateway" {
		t.Errorf("start-up log = %v; want one record for provider gateway", recs)
	}
}

// TS-29-3: No Podman provider is built and no engine binary runs under provider
// gateway, from start-up to a lease's Create.
// Requirement: 29-REQ-1.3
func TestGatewayProvider_RunsNoEngine_TS_29_3(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "podman-ran")
	script := "#!/bin/sh\necho ran > '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "podman"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	env := newRemoteEnv(t, leaseEnvOptions{})
	gw := env.startGateway()
	_, token := env.issueToken(`{"profile":"tools"}`)
	client := gw.dial(t, token)
	box := createVia(t, client)

	if env.prov.creates() != 1 {
		t.Fatalf("the remote saw %d Creates; want 1", env.prov.creates())
	}
	if err := box.Close(context.Background()); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the sentinel podman binary was run")
	}
}

// TS-29-18: The gateway factory is called once with exactly the configured URL
// and token.
// Requirement: 29-REQ-5.1
func TestStart_GatewayFactoryReceivesURLAndToken_TS_29_18(t *testing.T) {
	stub := newStubRemote()
	factory := stubFactory(stub, nil)
	cfg := remoteConfig("https://gw.example:8443")

	svc, err := Start(cfg, noPodman(t), WithGatewayFactory(factory.dial))
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if svc.Provider() == nil {
		t.Fatal("the service has no provider")
	}
	if factory.callCount() != 1 {
		t.Errorf("the factory was called %d times; want 1", factory.callCount())
	}
	if len(factory.urls) != 1 || factory.urls[0] != "https://gw.example:8443" || factory.tokens[0] != testGatewayToken {
		t.Errorf("the factory got url %q and a token of %d bytes; want the configured ones", factory.urls, len(factory.tokens[0]))
	}
}

// TS-29-19: Start-up issues one List and no Create or Destroy on the remote, and
// the remote sees only hub's token.
// Requirement: 29-REQ-5.2
func TestInit_ProbeIsOneListAndCreatesNothing_TS_29_19(t *testing.T) {
	prov := newRecordingProvider()
	remote := startRemoteGateway(t, prov)
	factory := &remoteFactory{}
	path := remoteConfigFile(t, remote.url(), remoteToken)
	reaps, rpcs := prov.reapCount(), remote.rpcs()

	svc, err := Init(discardLogger(), path, noPodman(t), WithGatewayFactory(factory.dial))
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	t.Cleanup(func() { _ = svc.Provider().(*countingRemote).Close() })

	if n := factory.listCount(); n != 1 {
		t.Errorf("the client was asked to List %d times; want 1", n)
	}
	// gateway.Dial reads the remote's capabilities and the probe lists: two RPCs.
	if got := remote.rpcs() - rpcs; got != 2 {
		t.Errorf("the remote saw %d RPCs during start-up; want 2 (Dial's capabilities and the List)", got)
	}
	for _, token := range remote.tokens()[rpcs:] {
		if token != remoteToken {
			t.Errorf("the remote was shown a token that is not hub's: %q", token)
		}
	}
	if prov.creates() != 0 || len(prov.liveBoxes()) != 0 || prov.reapCount() != reaps {
		t.Errorf("the remote's backend saw %d Creates, %d live boxes, %d Reaps; want 0, 0, 0", prov.creates(), len(prov.liveBoxes()), prov.reapCount()-reaps)
	}
	for _, box := range prov.boxes {
		if box.closes != 0 {
			t.Errorf("a box of the remote was closed during start-up")
		}
	}
}

// TS-29-20: A static failure returns the joined error without building a client or
// listing anything.
// Requirement: 29-REQ-5.3
func TestStart_StaticFailureBuildsNothing_TS_29_20(t *testing.T) {
	stub := newStubRemote()
	factory := stubFactory(stub, nil)
	cfg := remoteConfig("https://gw.example/not-allowed")
	cfg.MaxTTL = strPtr("bogus")

	svc, err := Start(cfg, noPodman(t), WithGatewayFactory(factory.dial))
	if svc != nil {
		t.Error("Start returned a service for a configuration with static failures")
	}
	requireFailure(t, err, "sandbox.gateway_url")
	requireFailure(t, err, "sandbox.max_ttl")
	if factory.callCount() != 0 || stub.listCount() != 0 {
		t.Errorf("the factory was called %d times and List %d times; want 0 and 0", factory.callCount(), stub.listCount())
	}

	// An unknown provider builds nothing either, whichever factory it could use.
	unknown := validConfig()
	unknown.Provider = "kubernetes"
	var podmanCalls atomic.Int32
	_, err = Start(unknown, func(podman.Options) (sandbox.Provider, error) {
		podmanCalls.Add(1)
		return nil, errors.New("no")
	}, WithGatewayFactory(factory.dial))
	requireFailure(t, err, "sandbox.provider", "podman", "gateway")
	if factory.callCount() != 0 || podmanCalls.Load() != 0 {
		t.Errorf("an unknown provider called the gateway factory %d times and the podman factory %d times", factory.callCount(), podmanCalls.Load())
	}
}

// TS-29-21: A factory error, an unreachable remote and a refused token each
// refuse start with a sandbox: error that names the three keys and holds neither
// the token nor URL user information.
// Requirement: 29-REQ-5.4
func TestInit_RemoteFailuresRefuseStart_TS_29_21(t *testing.T) {
	const otherToken = "other-hub-token-9876543210fedcba"

	t.Run("factory error", func(t *testing.T) {
		failure := errors.New("dial https://user:pw@host.example failed using the token " + remoteToken)
		factory := stubFactory(nil, failure)
		path := remoteConfigFile(t, "https://gw.example", remoteToken)
		svc, err := Init(discardLogger(), path, noPodman(t), WithGatewayFactory(factory.dial))
		if svc != nil {
			t.Error("Init returned a service")
		}
		requireStartFailure(t, err, remoteToken, "user:pw", "pw@")
		if !strings.Contains(err.Error(), "[redacted]") {
			t.Errorf("the token is not replaced by [redacted]: %v", err)
		}
		if !strings.Contains(err.Error(), `sandbox.gateway_url "https://gw.example"`) {
			t.Errorf("the error does not quote the configured address: %v", err)
		}
		if factory.callCount() != 1 {
			t.Errorf("the factory was called %d times; want 1", factory.callCount())
		}
	})

	t.Run("unreachable remote", func(t *testing.T) {
		remote := startRemoteGateway(t, newRecordingProvider())
		url := remote.url()
		remote.stop()
		path := remoteConfigFile(t, url, remoteToken)
		svc, err := Init(discardLogger(), path, noPodman(t))
		if svc != nil {
			t.Error("Init returned a service")
		}
		requireStartFailure(t, err, remoteToken)
		if !strings.Contains(err.Error(), url) {
			t.Errorf("the error does not show the address of the remote: %v", err)
		}
	})

	t.Run("refused token", func(t *testing.T) {
		remote := startRemoteGateway(t, newRecordingProvider())
		path := remoteConfigFile(t, remote.url(), otherToken)
		svc, err := Init(discardLogger(), path, noPodman(t))
		if svc != nil {
			t.Error("Init returned a service")
		}
		requireStartFailure(t, err, remoteToken, otherToken)
		if !strings.Contains(err.Error(), "refused the token") {
			t.Errorf("the error does not say the token was refused: %v", err)
		}
	})

	t.Run("List fails", func(t *testing.T) {
		stub := newStubRemote()
		stub.listErr = errors.New("list failed for " + remoteToken + " at https://user:pw@host.example")
		path := remoteConfigFile(t, "https://gw.example", remoteToken)
		svc, err := Init(discardLogger(), path, noPodman(t), WithGatewayFactory(stubFactory(stub, nil).dial))
		if svc != nil {
			t.Error("Init returned a service")
		}
		requireStartFailure(t, err, remoteToken, "user:pw", "pw@")
		if stub.listCount() != 1 {
			t.Errorf("List was called %d times; want 1", stub.listCount())
		}
	})

	t.Run("a factory that returns no client", func(t *testing.T) {
		path := remoteConfigFile(t, "https://gw.example", remoteToken)
		svc, err := Init(discardLogger(), path, noPodman(t), WithGatewayFactory(stubFactory(nil, nil).dial))
		if svc != nil {
			t.Error("Init returned a service")
		}
		requireStartFailure(t, err, remoteToken)
	})
}

// A client that has been built is closed when start is refused, since it holds a
// connection.
func TestStart_RefusedStartClosesTheClient(t *testing.T) {
	for name, edit := range map[string]func(*closingStub){
		"List fails":            func(s *closingStub) { s.listErr = errors.New("nobody home") },
		"a profile is rejected": func(s *closingStub) {},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &closingStub{stubRemote: newStubRemote()}
			edit(stub)
			cfg := remoteConfig("https://gw.example")
			if name != "List fails" {
				cfg.NetworkModes = []string{"none", "unrestricted"} // the stub honours none only
			}
			_, err := Start(cfg, noPodman(t), WithGatewayFactory(stubFactory(stub, nil).dial))
			if err == nil {
				t.Fatal("start succeeded")
			}
			if stub.closes.Load() != 1 {
				t.Errorf("the client was closed %d times; want 1", stub.closes.Load())
			}
		})
	}
}

// closingStub is a stubRemote that can be closed.
type closingStub struct {
	*stubRemote
	closes atomic.Int32
}

func (s *closingStub) Close() error {
	s.closes.Add(1)
	return nil
}

// TS-29-22: A remote that never answers List fails start within eleven seconds of
// the probe's ten-second limit.
// Requirement: 29-REQ-5.5
func TestStart_RemoteThatNeverAnswersFailsAtTheLimit_TS_29_22(t *testing.T) {
	stub := newStubRemote()
	stub.listBlock = true
	factory := stubFactory(stub, nil)
	path := remoteConfigFile(t, "https://gw.example", remoteToken)

	began := time.Now()
	svc, err := Init(discardLogger(), path, noPodman(t), WithGatewayFactory(factory.dial))
	elapsed := time.Since(began)

	if svc != nil {
		t.Error("Init returned a service")
	}
	requireStartFailure(t, err, remoteToken)
	if elapsed < 10*time.Second || elapsed > 11*time.Second {
		t.Errorf("Init returned after %v; want between 10s and 11s", elapsed)
	}
	if !strings.Contains(err.Error(), "no answer within 10s") {
		t.Errorf("the error does not say the remote did not answer in time: %v", err)
	}
}

// The probe's context ends at its limit, for a Dial that blocks as well.
func TestStart_DialThatNeverReturnsFailsAtTheLimit(t *testing.T) {
	prev := probeTimeout
	probeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { probeTimeout = prev })
	factory := &remoteFactory{delegate: func(ctx context.Context, _ string, _ gateway.Options) (RemoteClient, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	_, err := Start(remoteConfig("https://gw.example"), noPodman(t), WithGatewayFactory(factory.dial))
	requireStartFailure(t, err, testGatewayToken)
	if !strings.Contains(err.Error(), "no answer within") {
		t.Errorf("the error does not say the remote did not answer in time: %v", err)
	}
}

// discardLogger is a logger that writes nowhere.
func discardLogger() *slog.Logger { l, _ := newCapturedLogger(); return l }
