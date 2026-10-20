package sandboxes

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"os"
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
}

// Gateway is the sandbox gateway hub serves on a second listener: a gatewayd
// server over hub's wrapping backend, for as long as the process lives.
type Gateway struct {
	server   *gatewayd.Server
	backend  *Backend
	listener net.Listener
	url      string
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
// for the life of the process. It returns (nil, nil) when svc is nil, which is
// the feature turned off: nothing is bound and no server is built.
//
// The key pair, if there is one, is loaded before anything is bound, and a
// failure names sandbox.tls_cert and sandbox.tls_key. A listener that cannot be
// bound names sandbox.listen. Either refuses start.
//
// If Serve returns an error later, the gateway logs it and ends the process:
// hub would otherwise go on issuing leases nobody can use. A Serve that returns
// nil has been shut down. StartGateway never shuts the gateway down; destroying
// the boxes when hub stops belongs to the expiry work.
func StartGateway(svc *Service, deps GatewayDeps) (*Gateway, error) {
	return startGateway(svc, deps, gatewaySeams{})
}

func startGateway(svc *Service, deps GatewayDeps, seams gatewaySeams) (*Gateway, error) {
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
	store := NewLeaseStore(deps.DB, now)
	backend := newBackend(backendConfig{Service: svc, Store: store, Secrets: deps.Secrets, DB: deps.DB, Audit: deps.Audit, Now: now})
	server, err := seams.newServer(gatewayd.Config{
		Backend:      backend,
		Authenticate: NewLeaseAuthenticator(store, now),
		TLS:          tlsConfig,
		NetworkModes: modes,
		MaxSandboxes: svc.MaxSandboxes(),
		MaxTTL:       svc.maxTTL,
	})
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("sandbox gateway: %w", err)
	}

	g := &Gateway{
		server:   server,
		backend:  backend,
		listener: listener,
		url:      gatewayURL(settings.externalURL, tlsConfig != nil, listener.Addr()),
	}
	go func() {
		if err := seams.serve(server, listener); err != nil {
			slog.Error("sandbox gateway: the gateway stopped serving, and hub cannot issue leases nobody can use", "error", err)
			seams.exit(1)
		}
	}()
	slog.Info("the sandbox gateway is serving", "url", g.url)
	return g, nil
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
