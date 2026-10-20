package sandboxes

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// remoteToken is hub's token for the in-process remote gateway: 33 characters,
// more than the 32 a gatewayd accepts for a static token.
const remoteToken = "remote-hub-token-0123456789abcdef"

// remoteGateway is an in-process agentkit-gateway, the "remote" of the tests: a
// gatewayd.Server on 127.0.0.1:0 over a recording provider, which records every
// token its authenticator is shown, one for each RPC. It can be shut down and
// started again on the same address.
type remoteGateway struct {
	t       *testing.T
	backend *recordingProvider
	addr    string

	mu   sync.Mutex
	seen []string
	srv  *gatewayd.Server
}

// startRemoteGateway starts a remote over backend and stops it when the test
// ends.
func startRemoteGateway(t *testing.T, backend *recordingProvider) *remoteGateway {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the remote gateway: %v", err)
	}
	r := &remoteGateway{t: t, backend: backend, addr: l.Addr().String()}
	r.serve(l)
	t.Cleanup(r.stop)
	return r
}

// serve serves a new gatewayd on l, and returns once it has done its first
// reap of the backend, so that the counts of the backend are steady.
func (r *remoteGateway) serve(l net.Listener) {
	r.t.Helper()
	srv, err := gatewayd.New(gatewayd.Config{
		Backend:      r.backend,
		Authenticate: r.authenticate,
		NetworkModes: []sandbox.NetworkMode{sandbox.NetworkNone},
	})
	if err != nil {
		r.t.Fatalf("gatewayd.New: %v", err)
	}
	reaps := r.backend.reapCount()
	go func() { _ = srv.Serve(l) }()
	for deadline := time.Now().Add(10 * time.Second); r.backend.reapCount() == reaps; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			r.t.Fatal("the remote gateway did not reap when it started serving")
		}
	}
	r.mu.Lock()
	r.srv = srv
	r.mu.Unlock()
}

// authenticate accepts hub's token and records every token it is shown.
func (r *remoteGateway) authenticate(_ context.Context, token string) (string, bool) {
	r.mu.Lock()
	r.seen = append(r.seen, token)
	r.mu.Unlock()
	return "hub", subtle.ConstantTimeCompare([]byte(token), []byte(remoteToken)) == 1
}

// url is the address hub is configured with.
func (r *remoteGateway) url() string { return "http://" + r.addr }

// host is the url's Host.
func (r *remoteGateway) host() string { return r.addr }

// tokens returns every token the remote has been shown, in order.
func (r *remoteGateway) tokens() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.seen)
}

// rpcs is the number of RPCs the remote has authenticated or refused.
func (r *remoteGateway) rpcs() int { return len(r.tokens()) }

// stop shuts the remote down, which closes the boxes it holds, and does nothing
// when it is down already.
func (r *remoteGateway) stop() {
	r.mu.Lock()
	srv := r.srv
	r.srv = nil
	r.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// restart starts the remote again on the address it had.
func (r *remoteGateway) restart() {
	r.t.Helper()
	var l net.Listener
	var err error
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if l, err = net.Listen("tcp", r.addr); err == nil {
			break
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("listening again on %s: %v", r.addr, err)
		}
	}
	r.serve(l)
}

// remoteFactory is a GatewayFactory that records its calls and builds the
// client with delegate, the real gateway.Dial when it is nil. The client it
// returns counts the Lists made on it.
type remoteFactory struct {
	delegate GatewayFactory

	mu      sync.Mutex
	calls   int
	urls    []string
	tokens  []string
	lists   int
	clients []RemoteClient
}

func (f *remoteFactory) dial(ctx context.Context, url string, opts gateway.Options) (RemoteClient, error) {
	f.mu.Lock()
	f.calls++
	f.urls = append(f.urls, url)
	f.tokens = append(f.tokens, opts.Token)
	delegate := f.delegate
	f.mu.Unlock()
	if delegate == nil {
		delegate = DialGateway
	}
	client, err := delegate(ctx, url, opts)
	if err != nil || client == nil {
		return client, err
	}
	wrapped := &countingRemote{RemoteClient: client, f: f}
	f.mu.Lock()
	f.clients = append(f.clients, wrapped)
	f.mu.Unlock()
	return wrapped, nil
}

func (f *remoteFactory) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *remoteFactory) listCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists
}

// countingRemote counts the Lists made on the client it wraps and passes Close
// on to it.
type countingRemote struct {
	RemoteClient
	f *remoteFactory
}

func (c *countingRemote) List(ctx context.Context) ([]gateway.Info, error) {
	c.f.mu.Lock()
	c.f.lists++
	c.f.mu.Unlock()
	return c.RemoteClient.List(ctx)
}

func (c *countingRemote) Close() error {
	if closer, ok := c.RemoteClient.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// stubRemote is a RemoteClient that reaches nothing: a fakeProvider that also
// lists. Its List can fail or block until its context ends.
type stubRemote struct {
	*fakeProvider

	lmu       sync.Mutex
	lists     int
	listErr   error
	listBlock bool
}

func newStubRemote() *stubRemote { return &stubRemote{fakeProvider: newFakeProvider()} }

func (s *stubRemote) List(ctx context.Context) ([]gateway.Info, error) {
	s.lmu.Lock()
	s.lists++
	block, err := s.listBlock, s.listErr
	s.lmu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, err
}

func (s *stubRemote) listCount() int {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	return s.lists
}

// stubFactory is a factory that returns client and err, whatever it is called
// with.
func stubFactory(client RemoteClient, err error) *remoteFactory {
	return &remoteFactory{delegate: func(context.Context, string, gateway.Options) (RemoteClient, error) {
		return client, err
	}}
}

// noPodman is a Podman factory that fails the test when it is called.
func noPodman(t *testing.T) ProviderFactory {
	t.Helper()
	return func(podman.Options) (sandbox.Provider, error) {
		t.Error("the Podman factory was called")
		return nil, errors.New("the Podman factory must not be called")
	}
}

// remoteLeaseConfig is gatewayLeaseConfig for provider "gateway" at url, with
// hub's token for the in-process remote.
func remoteLeaseConfig(url string) *Config {
	cfg := gatewayLeaseConfig()
	cfg.Provider = "gateway"
	cfg.GatewayURL = url
	cfg.GatewayToken = remoteToken
	return cfg
}

// recordsOf parses what a JSON slog handler wrote to the buffer into one map per
// record.
func recordsOf(t *testing.T, b *logBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v\n%s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// remoteEnv is a backend environment whose provider is the client of an
// in-process remote gateway, built by Start with the real gateway.Dial: hub in
// front of a remote, with the remote's recording provider as env.prov.
type remoteEnv struct {
	*backendEnv
	remote  *remoteGateway
	factory *remoteFactory
	logs    *logBuffer
}

// newRemoteEnv starts a remote and a hub service over it, with the default
// profiles of gatewayLeaseConfig.
func newRemoteEnv(t *testing.T, opts leaseEnvOptions) *remoteEnv {
	t.Helper()
	logs := captureDefaultLog(t)
	prov := newRecordingProvider()
	remote := startRemoteGateway(t, prov)
	factory := &remoteFactory{}
	svc, err := Start(remoteLeaseConfig(remote.url()), noPodman(t), WithGatewayFactory(factory.dial))
	if err != nil {
		t.Fatalf("Start against the remote gateway: %v", err)
	}
	t.Cleanup(func() {
		if closer, ok := svc.Provider().(io.Closer); ok {
			_ = closer.Close()
		}
	})
	opts.service, opts.recording = svc, prov
	return &remoteEnv{backendEnv: newBackendEnv(t, opts), remote: remote, factory: factory, logs: logs}
}
