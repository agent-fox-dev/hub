package sandboxes

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// gatewaySettings are the keys of the [sandbox] section that place the gateway,
// as validated.
type gatewaySettings struct {
	listen      string
	tlsCert     string
	tlsKey      string
	externalURL string
}

// GatewayDeps is what the gateway is built from besides the sandbox service.
type GatewayDeps struct {
	DB *sql.DB
	// Secrets reads the secret values a box is filled with.
	Secrets SecretReader
	// Audit receives the gateway's events; nil means none.
	Audit audit.Emitter
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Metrics receives the sandbox metrics; nil records none.
	Metrics *audit.Metrics
}

// Gateway is the sandbox gateway hub serves on a second listener: a gatewayd
// server over hub's wrapping backend, for as long as the process lives.
type Gateway struct {
	server  *gatewayd.Server
	backend *Backend
	sweeper *Sweeper
	url     string
	// listener and seams are what serve needs; serving starts once.
	listener  net.Listener
	seams     gatewaySeams
	serveOnce sync.Once
	// shutdownLimit is the time Shutdown has; tests shorten it.
	shutdownLimit time.Duration
}

// URL is the address a lease holder dials with the lease's token. A nil gateway,
// which is the feature turned off, has none.
func (g *Gateway) URL() string {
	if g == nil {
		return ""
	}
	return g.url
}

// gatewaySeams are the three things a test replaces: how a gatewayd server is
// built, how it serves a listener, and how the process ends.
type gatewaySeams struct {
	newServer func(gatewayd.Config) (*gatewayd.Server, error)
	serve     func(*gatewayd.Server, net.Listener) error
	exit      func(int)
}

// withDefaults fills the seams a caller left out with the production ones.
func (s gatewaySeams) withDefaults() gatewaySeams {
	if s.newServer == nil {
		s.newServer = gatewayd.New
	}
	if s.serve == nil {
		s.serve = func(server *gatewayd.Server, l net.Listener) error { return server.Serve(l) }
	}
	if s.exit == nil {
		s.exit = os.Exit
	}
	return s
}

// StartGateway binds the gateway's listener and serves a gatewayd server on it
// for the life of the process. It is NewGateway followed by serving at once; hub
// itself uses NewGateway and Run, which do the start-up work of the expiry first.
func StartGateway(svc *Service, deps GatewayDeps) (*Gateway, error) {
	return startGateway(svc, deps, gatewaySeams{})
}

func startGateway(svc *Service, deps GatewayDeps, seams gatewaySeams) (*Gateway, error) {
	g, err := newGateway(svc, deps, seams)
	if err != nil || g == nil {
		return g, err
	}
	g.serve()
	return g, nil
}

// NewGateway binds the gateway's listener and builds the gatewayd server over it,
// without serving: connections wait in the listener's backlog until Run, or serve,
// begins. It returns (nil, nil) when svc is nil, which is the feature turned off:
// nothing is bound and no server is built.
//
// The key pair, if there is one, is loaded before anything is bound, and a
// failure names sandbox.tls_cert and sandbox.tls_key. A listener that cannot be
// bound names sandbox.listen. Either refuses start.
//
// If Serve returns an error later, the gateway logs it and ends the process:
// hub would otherwise go on issuing leases nobody can use. A Serve that returns
// nil has been shut down.
func NewGateway(svc *Service, deps GatewayDeps) (*Gateway, error) {
	return newGateway(svc, deps, gatewaySeams{})
}

func newGateway(svc *Service, deps GatewayDeps, seams gatewaySeams) (*Gateway, error) {
	if svc == nil {
		return nil, nil
	}
	seams = seams.withDefaults()
	settings := svc.gateway

	tlsConfig, err := loadGatewayTLS(settings)
	if err != nil {
		return nil, err
	}
	modes, err := gatewayNetworkModes(svc)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", settings.listen)
	if err != nil {
		return nil, fmt.Errorf("sandbox.listen %q: cannot bind the sandbox gateway: %w", settings.listen, err)
	}

	now := deps.Now
	if now == nil {
		now = time.Now
	}
	store := NewLeaseStore(deps.DB, now).WithMetrics(deps.Metrics)
	// The gateway is the sweeper's Boxes, so it exists before its server does.
	g := &Gateway{shutdownLimit: ShutdownLimit}
	sweeper := NewSweeper(SweeperConfig{
		DB: deps.DB, Provider: svc.Provider(), Boxes: g, Audit: deps.Audit, Metrics: deps.Metrics, Now: now,
		Interval: svc.ReapInterval(), Service: svc,
	})
	backend := newBackend(backendConfig{
		Service: svc, Store: store, Secrets: deps.Secrets, DB: deps.DB, Audit: deps.Audit, Now: now,
		Metrics: deps.Metrics, Sweeper: sweeper,
	})
	server, err := seams.newServer(gatewayd.Config{
		Backend:      backend,
		Authenticate: NewLeaseAuthenticator(store, now),
		TLS:          tlsConfig,
		NetworkModes: modes,
		MaxSandboxes: svc.MaxSandboxes(),
		MaxTTL:       svc.maxTTL,
		// gatewayd calls Backend.Reap, which runs the sweep, when it serves and at
		// this interval, so reap_interval is the longest between two sweeps.
		ReapInterval: svc.ReapInterval(),
	})
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("sandbox gateway: %w", err)
	}

	g.server, g.backend, g.sweeper = server, backend, sweeper
	g.listener, g.seams = listener, seams
	g.url = gatewayURL(settings.externalURL, tlsConfig != nil, listener.Addr())
	return g, nil
}

// serve starts serving the listener, once. A serve that ends with an error ends
// the process.
func (g *Gateway) serve() {
	g.serveOnce.Do(func() {
		go func() {
			if err := g.seams.serve(g.server, g.listener); err != nil {
				slog.Error("sandbox gateway: the gateway stopped serving, and hub cannot issue leases nobody can use", "error", err)
				g.seams.exit(1)
			}
		}()
		slog.Info("the sandbox gateway is serving", "url", g.url)
	})
}

// Run does the start-up work of the expiry and then lets the gateway serve, so
// that no connection is accepted before the leases an earlier process left active
// have been ended and the first sweep is done (28-REQ-5.1). It returns what failed
// in that work, which is logged where it happens and never stops the gateway from
// serving. The reaper it starts runs until ctx ends.
func (g *Gateway) Run(ctx context.Context) error {
	err := g.sweeper.RunStartup(ctx)
	g.serve()
	return err
}

// loadGatewayTLS loads the key pair, and returns nil when there is none.
func loadGatewayTLS(s gatewaySettings) (*tls.Config, error) {
	if s.tlsCert == "" && s.tlsKey == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(s.tlsCert, s.tlsKey)
	if err != nil {
		return nil, fmt.Errorf("sandbox.tls_cert and sandbox.tls_key: cannot load the key pair: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

// gatewayNetworkModes turns the names in network_modes into the modes gatewayd
// takes, through the provider's own list.
func gatewayNetworkModes(svc *Service) ([]sandbox.NetworkMode, error) {
	caps := svc.Provider().Capabilities()
	names := svc.NetworkModes()
	modes := make([]sandbox.NetworkMode, 0, len(names))
	for _, name := range names {
		mode, ok := networkMode(caps, name)
		if !ok {
			return nil, fmt.Errorf("sandbox.network_modes %q is not a network mode of provider %q", name, caps.Provider)
		}
		modes = append(modes, mode)
	}
	return modes, nil
}

// gatewayURL is the address a lease holder dials: external_url when it is set,
// else the scheme, https when the gateway serves TLS and http when it does not,
// and the address the listener is really on, so that port 0 is the port it got.
func gatewayURL(external string, useTLS bool, addr net.Addr) string {
	if external != "" {
		return external
	}
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	return scheme + "://" + addr.String()
}

// DestroyBox destroys the box of an active lease. A lease with no box has nothing
// to destroy. A box that is already gone counts as destroyed; any other failure
// of the provider is returned, scrubbed, and the box may still be there.
func (g *Gateway) DestroyBox(ctx context.Context, lease *Lease) error {
	if lease.SandboxID == nil {
		return nil
	}
	return g.backend.endBox(ctx, g.server, lease)
}

// List lists the boxes hub holds: those the gateway made and has not seen closed.
// The provider cannot list its boxes, so a box of an earlier process is not here.
func (g *Gateway) List(context.Context) ([]Box, error) {
	return g.backend.heldBoxes(), nil
}

// Destroy destroys the box with this id, the way DELETE destroys the box of a
// lease. A box hub does not hold is gone, and counts as destroyed.
func (g *Gateway) Destroy(ctx context.Context, id string) error {
	return g.backend.destroyBox(ctx, g.server, id, g.backend.leaseOfBox(id))
}

var _ Boxes = (*Gateway)(nil)

// ShutdownLimit is how long Shutdown may take in all.
const ShutdownLimit = 20 * time.Second

// Shutdown is the sandbox part of hub's shutdown, run when the HTTP server's Start
// returns, while the databases are still open. Within ShutdownLimit it puts hub in
// shutdown mode, stops the reaper, destroys the box of every active lease and ends
// the lease with the reason shutdown, and only then stops the gatewayd server,
// which closes any box it still holds. When the limit runs out the leases still
// active stay so, for the recovery at the next start. Leases in state leased are
// not changed. A second call does nothing, and neither does a call on a nil
// Gateway, the feature turned off.
func (g *Gateway) Shutdown(ctx context.Context) error {
	if g == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, g.shutdownLimit)
	defer cancel()
	err := g.sweeper.Shutdown(ctx)

	// gatewayd closes the boxes it still holds before it looks at its context, one
	// at a time and without a limit, so a provider that hangs would hold the
	// shutdown past its limit. The stop is left to finish on its own then; the hub
	// is about to exit.
	stopped := make(chan error, 1)
	go func() { stopped <- g.server.Shutdown(ctx) }()
	select {
	case stopErr := <-stopped:
		return errors.Join(err, stopErr)
	case <-ctx.Done():
		return errors.Join(err, fmt.Errorf("stopping the gateway: %w", ctx.Err()))
	}
}
