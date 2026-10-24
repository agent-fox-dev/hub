package sandboxes

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// freeAddr is a loopback address nothing listens on; bindable reports whether an
// address can be listened on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func bindable(addr string) bool {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// testKeyPair writes a self-signed certificate for 127.0.0.1 and its key.
func testKeyPair(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hub gateway test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "gw.crt"), filepath.Join(dir, "gw.key")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	cert, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certFile, keyFile, pool
}

// gatewayEnv is a backend environment over an edited gateway configuration,
// with the dependencies the gateway takes.
func gatewayEnv(t *testing.T, edit func(*Config)) (*backendEnv, GatewayDeps) {
	t.Helper()
	cfg := gatewayLeaseConfig()
	if edit != nil {
		edit(cfg)
	}
	env := newBackendEnv(t, leaseEnvOptions{config: cfg})
	return env, GatewayDeps{DB: env.db, Secrets: env.secrets, Audit: env.emitter, Now: env.clock.Now}
}

// The authenticator accepts the token of a leased or active lease whose
// expires_at is ahead, naming the lease; it refuses everything else, reads the
// store on every call, refuses and logs a store error or an unreadable
// expires_at, and never logs a token or its hash.
func TestLeaseAuthenticator(t *testing.T) {
	logs := captureDefaultLog(t)
	expiry := time.Date(2030, 1, 2, 4, 4, 5, 0, time.UTC)
	now := expiry.Add(-time.Hour)
	store, db := newLeaseStore(t, now)
	auth := NewLeaseAuthenticator(store, func() time.Time { return now })
	insertRawLease(t, db, leaseID(1), "widgets", "leased", HashToken(testToken(1)))
	insertRawLease(t, db, leaseID(2), "widgets", "active", HashToken(testToken(2)))
	insertRawLease(t, db, leaseID(3), "widgets", "destroyed", HashToken(testToken(3)))
	for n := 1; n <= 2; n++ {
		if principal, ok := auth(context.Background(), testToken(n)); !ok || principal != leaseID(n) {
			t.Errorf("auth(token %d) = (%q, %v)", n, principal, ok)
		}
	}
	for name, token := range map[string]string{"empty": "", "garbage": "garbage", "unknown": testToken(99), "the hash": HashToken(testToken(1)), "destroyed": testToken(3), "padded": testToken(1) + " "} {
		if principal, ok := auth(context.Background(), token); ok || principal != "" {
			t.Errorf("auth(%s) = (%q, %v); want a refusal", name, principal, ok)
		}
	}
	now = expiry.Add(-time.Second)
	if _, ok := auth(context.Background(), testToken(1)); !ok {
		t.Error("a second before expires_at was refused")
	}
	for _, at := range []time.Time{expiry, expiry.Add(24 * time.Hour)} {
		now = at
		if _, ok := auth(context.Background(), testToken(1)); ok {
			t.Errorf("at %s the token was accepted; want a refusal from expires_at on", at)
		}
	}
	now = expiry.Add(-time.Hour)
	store.DestroyLease(leaseID(2), "active", "api")
	if _, ok := auth(context.Background(), testToken(2)); ok {
		t.Error("the token of a lease that just ended was accepted")
	}

	bad := testToken(5)
	lookup := failingLookup{
		leases: map[string]*Lease{testToken(4): {ID: leaseID(4), State: "leased", ExpiresAt: "tomorrow"}, testToken(6): {ID: leaseID(6), State: "bogus", ExpiresAt: "2030-01-02T04:04:05Z"}},
		failOn: bad, err: fmt.Errorf("query for %s with hash %s failed: disk I/O error", bad, HashToken(bad)),
	}
	injected := newLeaseAuthenticator(lookup, func() time.Time { return now })
	for name, token := range map[string]string{"store error": bad, "unreadable expiry": testToken(4), "unknown state": testToken(6)} {
		if _, ok := injected(context.Background(), token); ok {
			t.Errorf("%s: the token was accepted", name)
		}
	}
	out := logs.String()
	if !strings.Contains(out, "disk I/O error") || !strings.Contains(out, leaseID(4)) {
		t.Errorf("the store error and the unreadable expiry were not logged:\n%s", out)
	}
	containsNone(t, "the log", out, bad, HashToken(bad), testToken(4), HashToken(testToken(4)))
}

type failingLookup struct {
	leases map[string]*Lease
	failOn string
	err    error
}

func (f failingLookup) LookupByToken(token string) (*Lease, error) {
	if token == f.failOn {
		return nil, f.err
	}
	if l, ok := f.leases[token]; ok {
		return l, nil
	}
	return nil, ErrLeaseNotFound
}

// Start binds the listener, builds gatewayd with hub's backend, authenticator,
// network modes, limits and reap interval and no static tokens, and a real
// client connects; the URL is external_url, else the scheme and the address the
// listener really got, TLS when a key pair is set.
func TestNewGateway_BuildsGatewaydAndServes(t *testing.T) {
	env, deps := gatewayEnv(t, func(c *Config) {
		c.MaxTTL, c.ReapInterval, c.NetworkModes = strPtr("3h"), strPtr("45s"), []string{"none", "unrestricted"}
		limit := 5.0
		c.MaxSandboxes = &limit
	})
	_, token := env.issueToken("")
	var captured gatewayd.Config
	gw, err := startGateway(env.profile, deps, gatewaySeams{newServer: func(cfg gatewayd.Config) (*gatewayd.Server, error) {
		captured = cfg
		return gatewayd.New(cfg)
	}})
	if err != nil {
		t.Fatalf("startGateway: %v", err)
	}
	stopAfter(t, gw)
	if _, ok := captured.Backend.(*Backend); !ok || captured.Authenticate == nil || len(captured.Tokens) != 0 || captured.TLS != nil ||
		!reflect.DeepEqual(captured.NetworkModes, []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}) ||
		captured.MaxSandboxes != 5 || captured.MaxTTL != 3*time.Hour || captured.ReapInterval != 45*time.Second {
		t.Errorf("gatewayd.Config = %+v", captured)
	}
	addr := strings.TrimPrefix(gw.URL(), "http://")
	if _, port, err := net.SplitHostPort(addr); err != nil || port == "0" {
		t.Errorf("URL = %q; want http://<address with the port it got>", gw.URL())
	}
	client := dialGateway(t, gw.URL(), token)
	if _, err := client.List(context.Background()); err != nil || client.Gateway().MaxTTL != 3*time.Hour || client.Gateway().MaxSandboxes != 5 {
		t.Errorf("List = %v, gateway %+v", err, client.Gateway())
	}

	certFile, keyFile, pool := testKeyPair(t)
	env, deps = gatewayEnv(t, func(c *Config) { c.TLSCert, c.TLSKey = certFile, keyFile })
	_, token = env.issueToken("")
	gw, err = startGateway(env.profile, deps, gatewaySeams{})
	if err != nil {
		t.Fatalf("startGateway with TLS: %v", err)
	}
	stopAfter(t, gw)
	if !strings.HasPrefix(gw.URL(), "https://") {
		t.Fatalf("URL = %q", gw.URL())
	}
	if p, err := gateway.Dial(context.Background(), gw.URL(), gateway.Options{Token: token, TLSConfig: &tls.Config{RootCAs: pool}}); err != nil {
		t.Errorf("gateway.Dial over TLS: %v", err)
	} else {
		p.Close()
	}
	if _, err := gateway.Dial(context.Background(), "http://"+strings.TrimPrefix(gw.URL(), "https://"), gateway.Options{Token: token}); err == nil {
		t.Error("a plaintext client reached a TLS gateway")
	}
	env, deps = gatewayEnv(t, func(c *Config) { c.ExternalURL = "https://gw.example.com" })
	gw, err = startGateway(env.profile, deps, gatewaySeams{})
	stopAfter(t, gw)
	if err != nil || gw.URL() != "https://gw.example.com" {
		t.Errorf("external_url: URL = %q (%v)", gw.URL(), err)
	}
	if got := gatewayURL("", false, &net.TCPAddr{IP: net.ParseIP("::1"), Port: 4711}); got != "http://[::1]:4711" {
		t.Errorf("gatewayURL = %q", got)
	}
}

// An unloadable key pair, an address that is taken and a gatewayd that cannot
// be built refuse start naming the keys, binding nothing; a nil service binds
// nothing and has no URL; a Serve that errs is logged once and ends the
// process, one that returns nil is not a failure.
func TestNewGateway_RefusalsAndServeFailures(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	os.WriteFile(garbage, []byte("not a PEM file"), 0o600)
	addr := freeAddr(t)
	env, deps := gatewayEnv(t, func(c *Config) { c.Listen, c.TLSCert, c.TLSKey = addr, garbage, garbage })
	gw, err := startGateway(env.profile, deps, gatewaySeams{})
	_, loader := tls.LoadX509KeyPair(garbage, garbage)
	if gw != nil || err == nil || !containsAll(err.Error(), []string{"sandbox.tls_cert", "sandbox.tls_key", loader.Error()}) || !bindable(addr) {
		t.Errorf("a bad key pair: (%v, %v), bindable %v", gw, err, bindable(addr))
	}

	taken, _ := net.Listen("tcp", "127.0.0.1:0")
	defer taken.Close()
	env, deps = gatewayEnv(t, func(c *Config) { c.Listen = taken.Addr().String() })
	if _, err := startGateway(env.profile, deps, gatewaySeams{}); err == nil || !strings.Contains(err.Error(), "sandbox.listen") || !strings.Contains(err.Error(), "cannot bind") {
		t.Errorf("a taken address: %v", err)
	}

	sentinel := errors.New("gatewayd refuses this configuration")
	env, deps = gatewayEnv(t, func(c *Config) { c.Listen = addr })
	gw, err = startGateway(env.profile, deps, gatewaySeams{newServer: func(gatewayd.Config) (*gatewayd.Server, error) { return nil, sentinel }})
	if gw != nil || !errors.Is(err, sentinel) || !bindable(addr) {
		t.Errorf("a gatewayd that cannot be built: (%v, %v), bindable %v", gw, err, bindable(addr))
	}

	built := 0
	gw, err = startGateway(nil, GatewayDeps{}, gatewaySeams{newServer: func(gatewayd.Config) (*gatewayd.Server, error) { built++; return nil, sentinel }})
	if gw != nil || err != nil || built != 0 || gw.URL() != "" {
		t.Errorf("a nil service: (%v, %v), built %d, URL %q", gw, err, built, gw.URL())
	}

	for name, serveErr := range map[string]error{"error": errors.New("accept tcp: use of closed network connection"), "nil": nil} {
		logs := captureDefaultLog(t)
		env, deps := gatewayEnv(t, nil)
		exits := make(chan int, 1)
		var held net.Listener
		var mu sync.Mutex
		gw, err := startGateway(env.profile, deps, gatewaySeams{
			serve: func(_ *gatewayd.Server, l net.Listener) error { mu.Lock(); held = l; mu.Unlock(); return serveErr },
			exit:  func(code int) { exits <- code },
		})
		if err != nil {
			t.Fatal(err)
		}
		stopAfter(t, gw)
		t.Cleanup(func() { mu.Lock(); defer mu.Unlock(); held.Close() })
		select {
		case code := <-exits:
			if serveErr == nil || code == 0 || strings.Count(logs.String(), serveErr.Error()) != 1 {
				t.Errorf("Serve %s: exit %d, log:\n%s", name, code, logs)
			}
		case <-time.After(300 * time.Millisecond):
			if serveErr != nil || strings.Contains(logs.String(), `"level":"ERROR"`) {
				t.Errorf("Serve %s: no exit, log:\n%s", name, logs)
			}
		}
	}
}

// Run does the start-up work before the gateway serves: the leases an earlier
// process left active are ended as destroyed/restart when the first connection
// is accepted.
func TestGatewayRun_RecoversBeforeItServes(t *testing.T) {
	env, deps := gatewayEnv(t, nil)
	id := env.insertLeaseAt(1, "widgets", "leased", env.clock.Now())
	env.store.MarkActive(id, "box-1")
	whenServing := make(chan leaseState, 1)
	gw, err := newGateway(env.profile, deps, gatewaySeams{serve: func(s *gatewayd.Server, l net.Listener) error {
		whenServing <- env.readState(id)
		return s.Serve(l)
	}})
	if err != nil {
		t.Fatal(err)
	}
	stopAfter(t, gw)
	select {
	case <-whenServing:
		t.Fatal("the gateway served before Run")
	case <-time.After(50 * time.Millisecond):
	}
	if err := gw.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := <-whenServing; s.state != "destroyed" || s.endReason != "restart" {
		t.Errorf("the lease read %+v when the gateway began to serve", s)
	}
}

// Over real gRPC a token hub never minted, and the token of a lease that was
// destroyed or failed, are Unauthenticated on the very next RPC, relayed agent
// calls included; a second Create under a token is refused (Unavailable with
// the backend's message over the wire) and the provider is asked once; a
// holder lists only its own box and cannot destroy another's.
func TestGateway_TokensAndBoxesOverGRPC(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	gw := env.startGateway()
	unknown := testToken(99)
	if err := rawCall(t, gw.URL(), unknown, rpcGatewayCreate); status.Code(err) != codes.Unauthenticated {
		t.Errorf("Create with an unknown token = %v", err)
	}
	if _, err := gateway.Dial(context.Background(), gw.URL(), gateway.Options{Token: unknown}); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("Dial with an unknown token = %v", err)
	}

	idA, tokenA := env.issueToken("")
	idB, tokenB := env.issueToken("")
	_, tokenC := env.issueToken("")
	clientA, clientB := gw.dial(t, tokenA), gw.dial(t, tokenB)
	a, b := createVia(t, clientA), createVia(t, clientB)
	if err := rawCall(t, gw.URL(), tokenA, rpcAgentHello); status.Code(err) == codes.Unauthenticated {
		t.Fatalf("the live lease was refused: %v", err)
	}
	_, err := clientA.Create(context.Background(), sandbox.Spec{Image: "x"})
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "is active") || env.prov.creates() != 2 {
		t.Errorf("second Create = %v with %d provider calls", err, env.prov.creates())
	}
	infos, err := clientA.List(context.Background())
	if err != nil || len(infos) != 1 || infos[0].ID != a.ID() || infos[0].Labels["hub.lease"] != idA {
		t.Errorf("List under A = (%+v, %v); want its own box", infos, err)
	}
	if infos, err := gw.dial(t, tokenC).List(context.Background()); err != nil || len(infos) != 0 {
		t.Errorf("List under a lease without a box = (%+v, %v)", infos, err)
	}
	before := *env.lease(idA)
	if err := clientB.Destroy(context.Background(), a.ID()); err != nil || len(env.prov.live()) != 2 || !reflect.DeepEqual(before, *env.lease(idA)) {
		t.Errorf("Destroy of another lease's box = %v, live %v", err, env.prov.live())
	}

	if env.delete(idA).Code != http.StatusNoContent {
		t.Fatal("DELETE failed")
	}
	if _, err := clientA.List(context.Background()); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("List after the lease was destroyed = %v", err)
	}
	if err := rawCall(t, gw.URL(), tokenA, rpcAgentHello); status.Code(err) != codes.Unauthenticated {
		t.Errorf("a relayed Agent call = %v", err)
	}
	if _, err := a.Tools(context.Background(), sandbox.ToolOptions{}); err == nil {
		t.Error("a relayed Tools call on the box of a destroyed lease succeeded")
	}
	// The gateway lists and destroys the boxes it holds, leaving the lease to the
	// caller; a box it does not hold is gone.
	if boxes, err := gw.List(context.Background()); err != nil || len(boxes) != 1 || boxes[0].ID != b.ID() || boxes[0].Labels["hub.lease"] != idB {
		t.Errorf("gateway List = (%v, %v)", boxes, err)
	}
	if err := gw.Destroy(context.Background(), b.ID()); err != nil || len(env.prov.live()) != 0 || env.lease(idB).State != "active" {
		t.Errorf("gateway Destroy = %v, live %v, lease %s", err, env.prov.live(), env.lease(idB).State)
	}
	if err := gw.Destroy(context.Background(), "never-existed"); err != nil {
		t.Errorf("Destroy of an unknown box = %v", err)
	}
	// A box the gateway could not destroy stays held, and hub's handle closes it later.
	idD, tokenD := env.issueToken("")
	d := createVia(t, gw.dial(t, tokenD))
	env.prov.setCloseErr(errors.New("podman rm failed at https://user:pw@registry.example/x"))
	if err := gw.Destroy(context.Background(), d.ID()); err == nil || strings.Contains(err.Error(), "user:pw") || len(gw.backend.heldBoxes()) != 1 {
		t.Errorf("a failing Destroy = %v, held %v", err, gw.backend.heldBoxes())
	}
	env.prov.setCloseErr(nil)
	if err := gw.Destroy(context.Background(), d.ID()); err != nil || len(env.prov.live()) != 0 || len(gw.backend.heldBoxes()) != 0 || env.lease(idD).State != "active" {
		t.Errorf("the retried Destroy = %v, live %v, held %v", err, env.prov.live(), gw.backend.heldBoxes())
	}
	env.secrets.DeleteSecret("workspace", "widgets", "GIT_PAT")
	clientC := gw.dial(t, tokenC)
	if _, err := clientC.Create(context.Background(), sandbox.Spec{Image: "x"}); err == nil || !strings.Contains(err.Error(), "secret_missing:") {
		t.Errorf("Create with a missing secret = %v", err)
	}
	if _, err := clientC.List(context.Background()); !errors.Is(err, gateway.ErrUnauthenticated) || env.prov.creates() != 3 {
		t.Errorf("after the failed Create: %v, %d provider calls", err, env.prov.creates())
	}
}

// Through the gateway Close destroys the box and ends the lease as the client's
// with one event (a box already gone at the provider too); a Close the provider
// fails returns the error scrubbed and leaves the lease active; a lease
// holder's Reap does nothing.
func TestGateway_ClientCloseAndReap(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	gw := env.startGateway()
	first, firstToken := env.issueToken("")
	second, secondToken := env.issueToken("")
	third, thirdToken := env.issueToken("")
	sbFirst, sbSecond, sbThird := createVia(t, gw.dial(t, firstToken)), createVia(t, gw.dial(t, secondToken)), createVia(t, gw.dial(t, thirdToken))
	for _, box := range env.prov.allBoxes() {
		if box.ID() == sbSecond.ID() {
			box.Close(context.Background()) // the provider lost it first
		}
	}
	for _, c := range []struct {
		id  string
		box sandbox.Sandbox
	}{{first, sbFirst}, {second, sbSecond}} {
		if err := c.box.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if l := env.lease(c.id); l.State != "destroyed" || *l.EndReason != "client" {
			t.Errorf("lease %s = %s/%v", c.id, l.State, l.EndReason)
		}
	}
	events := env.eventsOf(audit.EventSandboxDestroy)
	if len(events) != 2 || events[0].ActorType != "lease" || events[0].ActorID != owner || !reflect.DeepEqual(events[0].Metadata, map[string]any{"lease_id": first, "sandbox_id": sbFirst.ID(), "reason": "client"}) {
		t.Errorf("destroy events = %+v", events)
	}

	env.prov.setCloseErr(fmt.Errorf("podman rm failed: run -e GIT_PAT=%s -e SECRET_ENV=envval", plantedSecret))
	err := sbThird.Close(context.Background())
	if err == nil || strings.Contains(err.Error(), plantedSecret) || strings.Contains(err.Error(), "envval") || !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("Close error = %v", err)
	}
	if l := env.lease(third); l.State != "active" || len(env.eventsOf(audit.EventSandboxDestroy)) != 2 {
		t.Errorf("after a failed Close: %s, %d events", l.State, len(env.eventsOf(audit.EventSandboxDestroy)))
	}
	env.prov.setCloseErr(nil)
	env.prov.setReap([]string{"someone-elses-box"}, errors.New("podman rm failed"))
	reaps := env.prov.reapCount()
	if ids, err := gw.dial(t, thirdToken).Reap(context.Background()); err != nil || len(ids) != 0 || env.prov.reapCount() != reaps {
		t.Errorf("Reap by a holder = (%v, %v), %d provider reaps", ids, err, env.prov.reapCount()-reaps)
	}
}

// A DELETE that arrives when the lease is active but gatewayd has not yet
// registered the box still destroys it: hub holds the handle by then.
func TestGateway_DeleteBetweenActiveAndRegistered(t *testing.T) {
	gate := &gatedEmitter{recordingEmitter: &recordingEmitter{}, eventType: audit.EventSandboxCreate, reached: make(chan struct{}, 1), release: make(chan struct{})}
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig(), emitter: gate})
	gw := env.startGateway()
	id, token := env.issueToken("")
	client := gw.dial(t, token)
	created := make(chan error, 1)
	go func() { _, err := client.Create(context.Background(), sandbox.Spec{Image: "x"}); created <- err }()
	<-gate.reached
	if env.lease(id).State != "active" {
		t.Fatal("the lease is not active while the create event is held")
	}
	if rec := env.delete(id); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d", rec.Code)
	}
	close(gate.release)
	<-created
	if env.lease(id).State != "destroyed" || len(env.prov.live()) != 0 {
		t.Errorf("lease %s, live %v; want destroyed and no box", env.lease(id).State, env.prov.live())
	}
}

// gatedEmitter holds an event of one type until released.
type gatedEmitter struct {
	*recordingEmitter
	eventType string
	reached   chan struct{}
	release   chan struct{}
}

func (g *gatedEmitter) Emit(ctx context.Context, event audit.HubEvent) error {
	if event.EventType == g.eventType {
		g.reached <- struct{}{}
		<-g.release
	}
	return g.recordingEmitter.Emit(ctx, event)
}

// Through the whole stack a planted secret, a fixed env value and URL userinfo
// echoed by the provider reach no HTTP body, event, log line, gateway error,
// lease field or box spec, no token or hash is logged, and every event names
// the lease's issuer for the gateway's events and the HTTP credential for a
// DELETE.
func TestGateway_NothingSecretLeavesHub(t *testing.T) {
	logs := captureDefaultLog(t)
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	gw := env.startGateway()
	ctx := context.Background()
	echo := fmt.Errorf("engine: run -e GIT_PAT=%s -e SECRET_ENV=envval https://user:pw@registry.example/tools", plantedSecret)
	var seen []string
	keep := func(v any) { seen = append(seen, fmt.Sprint(v)) }

	idFail, tokenFail := env.issueToken("")
	env.prov.setCreateErr(echo)
	_, err := gw.dial(t, tokenFail).Create(ctx, sandbox.Spec{Image: "x"})
	keep(err)
	env.prov.setCreateErr(nil)
	// An admin's lease: made, reported, listed and closed, its events naming admin.
	adminClient := gw.dial(t, env.mustIssueAs("widgets", "", adminAuth())["token"].(string))
	sb := createVia(t, adminClient)
	keep(fmt.Sprintf("%+v", sb.Spec()))
	infos, _ := adminClient.List(ctx)
	keep(infos)
	keep(sb.Close(ctx))
	// A Close the provider fails, then a DELETE it fails and one it does not.
	idClose, tokenClose := env.issueToken("")
	failing := createVia(t, gw.dial(t, tokenClose))
	env.prov.setCloseErr(echo)
	closeErr := failing.Close(ctx)
	keep(closeErr)
	idDel, tokenDel := env.issueToken("")
	createVia(t, gw.dial(t, tokenDel))
	keep(env.delete(idDel).Body)
	env.prov.setCloseErr(nil)
	keep(env.delete(idDel).Body)
	keep(env.get("/api/v1/workspaces/widgets/sandboxes?all=true", ownerKey()).Body)
	events, _ := json.Marshal(env.emitter.recorded())
	keep(string(events))
	for _, id := range []string{idFail, idClose, idDel} {
		if l := env.lease(id); l.Error != nil {
			keep(*l.Error)
		}
		containsNone(t, "the row of "+id, rowText(t, env.db, id), plantedSecret, "user:pw") // the fixed env is stored by design
	}
	for i, out := range seen {
		containsNone(t, fmt.Sprintf("output %d", i), out, plantedSecret, "envval", "user:pw")
	}
	containsNone(t, "the log", logs.String(), plantedSecret, "envval", "user:pw", tokenFail, HashToken(tokenFail), tokenClose, HashToken(tokenClose), tokenDel, HashToken(tokenDel))
	if l := env.lease(idFail); l.State != "failed" || !strings.Contains(*l.Error, "[redacted]") || closeErr == nil || !strings.Contains(closeErr.Error(), "[redacted]") {
		t.Errorf("the failed lease = %+v, the Close error = %v", l, closeErr)
	}
	actors := map[string][2]string{}
	for _, e := range env.emitter.recorded() {
		actors[e.EventType+" "+e.ResourceID] = [2]string{e.ActorType, e.ActorID}
	}
	for key, want := range map[string][2]string{
		audit.EventSandboxFailed + " " + idFail: {"lease", owner}, audit.EventSandboxCreate + " " + idDel: {"lease", owner},
		audit.EventSandboxDestroy + " " + idDel: {"api_key", owner},
	} {
		if actors[key] != want {
			t.Errorf("%s by %v; want %v", key, actors[key], want)
		}
	}
	adminEvents := 0
	for _, e := range env.emitter.recorded() {
		if e.ActorType == "lease" && e.ActorID == "admin" {
			adminEvents++
		}
	}
	if adminEvents != 2 {
		t.Errorf("%d gateway events name admin as the issuer; want the create and the destroy", adminEvents)
	}
}
