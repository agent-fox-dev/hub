package sandboxes

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
)

// orderedRemote is a remote client over the recording provider that writes the
// destroy of every box and the Close of the client into one log, in the order they
// happen. closeErr is what Close returns, and closeBlock, when set, makes it wait.
type orderedRemote struct {
	scriptedRemote
	log        *callLog
	closeErr   error
	closeBlock <-chan struct{}
}

func (o orderedRemote) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	box, err := o.scriptedRemote.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	return orderedBox{Sandbox: box, log: o.log}, nil
}

func (o orderedRemote) Close() error {
	o.log.add("close")
	if o.closeBlock != nil {
		<-o.closeBlock
	}
	return o.closeErr
}

// orderedBox is a box that logs its destruction.
type orderedBox struct {
	sandbox.Sandbox
	log *callLog
}

func (b orderedBox) Unwrap() sandbox.Sandbox { return b.Sandbox }

func (b orderedBox) Close(ctx context.Context) error {
	b.log.add("destroy " + b.ID())
	return b.Sandbox.Close(ctx)
}

// closingEnv is hub over a client made by mk, with hub's gateway serving.
type closingEnv struct {
	*backendEnv
	gw  *testGateway
	svc *Service
}

func newClosingEnv(t *testing.T, mk func(*recordingProvider) RemoteClient) *closingEnv {
	t.Helper()
	rec := newRecordingProvider()
	factory := func(context.Context, string, gateway.Options) (RemoteClient, error) { return mk(rec), nil }
	svc, err := Start(remoteLeaseConfig("https://gw.example"), noPodman(t), WithGatewayFactory(factory))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	env := newBackendEnv(t, leaseEnvOptions{service: svc, recording: rec})
	return &closingEnv{backendEnv: env, gw: env.startGateway(), svc: svc}
}

// activeBox issues a lease of profile tools and makes its box through hub's gateway.
func (e *closingEnv) activeBox() (id string, box sandbox.Sandbox) {
	e.t.Helper()
	id, token := e.issueToken(`{"profile":"tools"}`)
	return id, createVia(e.t, e.gw.dial(e.t, token))
}

// TS-29-45: Hub's shutdown destroys every active box and only then closes the
// remote client, once; a client that cannot be closed shuts down without an error.
// Requirement: 29-REQ-9.7
func TestShutdown_ClosesTheRemoteClientAfterTheLastDestroy_TS_29_45(t *testing.T) {
	log := &callLog{}
	env := newClosingEnv(t, func(rec *recordingProvider) RemoteClient {
		return orderedRemote{scriptedRemote: scriptedRemote{rec}, log: log}
	})
	idA, _ := env.activeBox()
	idB, _ := env.activeBox()

	if err := env.gw.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	got := log.all()
	if len(got) != 3 || got[2] != "close" || !strings.HasPrefix(got[0], "destroy ") || !strings.HasPrefix(got[1], "destroy ") || got[0] == got[1] {
		t.Errorf("the calls were %v; want the destroy of each of the two boxes, then close", got)
	}
	for _, id := range []string{idA, idB} {
		if l := env.lease(id); l.State != "destroyed" || l.EndReason == nil || *l.EndReason != "shutdown" {
			t.Errorf("lease %s: state %q, end_reason %v; want destroyed by the shutdown", id, l.State, l.EndReason)
		}
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("the remote's backend holds %v after the shutdown", live)
	}

	// A second shutdown, and a second Close, close nothing again.
	if err := env.gw.Shutdown(context.Background()); err != nil {
		t.Errorf("the second Shutdown: %v", err)
	}
	if err := env.svc.Close(); err != nil {
		t.Errorf("Close after the shutdown: %v", err)
	}
	if n := countOf(log.all(), "close"); n != 1 {
		t.Errorf("the client was closed %d times; want 1", n)
	}
}

// A client without Close shuts down without an error, and so does a service that
// is not remote or a nil one.
func TestShutdown_AClientWithoutCloseIsFine(t *testing.T) {
	env := newClosingEnv(t, func(rec *recordingProvider) RemoteClient { return scriptedRemote{rec} })
	id, _ := env.activeBox()
	if err := env.gw.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if l := env.lease(id); l.State != "destroyed" {
		t.Errorf("lease state %q after the shutdown; want destroyed", l.State)
	}
	if err := env.svc.Close(); err != nil {
		t.Errorf("Close of a client that cannot be closed: %v", err)
	}

	podman := newBackendEnv(t, leaseEnvOptions{})
	if err := podman.profile.Close(); err != nil {
		t.Errorf("Close of a service that is not remote: %v", err)
	}
	if err := (*Service)(nil).Close(); err != nil {
		t.Errorf("Close of a nil service: %v", err)
	}
}

// The error of Close is part of the shutdown's result, and a Close that never
// returns does not hold the shutdown past its limit.
func TestShutdown_TheClosesErrorAndLimit(t *testing.T) {
	boom := errors.New("closing the client failed")
	env := newClosingEnv(t, func(rec *recordingProvider) RemoteClient {
		return orderedRemote{scriptedRemote: scriptedRemote{rec}, log: &callLog{}, closeErr: boom}
	})
	if err := env.gw.Shutdown(context.Background()); !errors.Is(err, boom) {
		t.Errorf("Shutdown returned %v; want the error of Close", err)
	}

	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	env = newClosingEnv(t, func(rec *recordingProvider) RemoteClient {
		return orderedRemote{scriptedRemote: scriptedRemote{rec}, log: &callLog{}, closeBlock: hang}
	})
	env.gw.shutdownLimit = 300 * time.Millisecond
	began := time.Now()
	err := env.gw.Shutdown(context.Background())
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Shutdown took %v with a client that never closes; want it to stop at its limit", took)
	}
	if err == nil || !strings.Contains(err.Error(), "remote gateway") {
		t.Errorf("Shutdown returned %v; want an error that says the client was not closed in time", err)
	}
}

// Close is safe to call from many goroutines: the client is closed once.
func TestServiceClose_ClosesOnce(t *testing.T) {
	log := &callLog{}
	env := newClosingEnv(t, func(rec *recordingProvider) RemoteClient {
		return orderedRemote{scriptedRemote: scriptedRemote{rec}, log: log}
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _ = env.svc.Close() })
	}
	wg.Wait()
	if n := countOf(log.all(), "close"); n != 1 {
		t.Errorf("the client was closed %d times; want 1", n)
	}
}

// countOf counts the entries equal to want.
func countOf(entries []string, want string) int {
	n := 0
	for _, e := range entries {
		if e == want {
			n++
		}
	}
	return n
}
