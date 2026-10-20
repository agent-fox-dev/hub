package sandboxes

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
)

// freeAddr returns a loopback address with a port nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	defer l.Close()
	return l.Addr().String()
}

// bindable reports whether addr can be listened on, which it cannot while a
// listener holds it.
func bindable(addr string) bool {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// testKeyPair writes a self-signed certificate for 127.0.0.1 and its key, and
// returns their paths and a pool that trusts the certificate.
func testKeyPair(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "hub gateway test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating a certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("encoding the key: %v", err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "gw.crt"), filepath.Join(dir, "gw.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certFile, keyFile, pool
}

// gatewayTestEnv is a backend environment with a configuration the gateway tests
// change, and the dependencies StartGateway takes.
func gatewayTestEnv(t *testing.T, edit func(*Config)) (*backendEnv, GatewayDeps) {
	t.Helper()
	cfg := gatewayLeaseConfig()
	if edit != nil {
		edit(cfg)
	}
	env := newBackendEnv(t, leaseEnvOptions{config: cfg})
	return env, GatewayDeps{DB: env.db, Secrets: env.secrets, Audit: env.emitter, Now: env.clock.Now}
}

// stopAfter shuts a started gateway down when the test ends. That closes its
// listener and ends Serve without an error, which the gateway does not treat as
// a failure.
func stopAfter(t *testing.T, gw *Gateway) {
	t.Helper()
	if gw == nil {
		return
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = gw.server.Shutdown(ctx)
	})
}

// TS-27-8: A key pair that cannot be loaded refuses start with both field names
// and the loader's error, before anything is bound.
// Requirement: 27-REQ-1.8
func TestStartGateway_UnloadableKeyPair_TS_27_8(t *testing.T) {
	goodCert, goodKey, _ := testKeyPair(t)
	otherCert, otherKey, _ := testKeyPair(t)
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a PEM file"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing.pem")

	for name, pair := range map[string][2]string{
		"garbage files":                        {garbage, garbage},
		"a certificate and the key of another": {goodCert, otherKey},
		"missing files":                        {missing, missing},
	} {
		t.Run(name, func(t *testing.T) {
			addr := freeAddr(t)
			env, deps := gatewayTestEnv(t, func(c *Config) {
				c.Listen, c.TLSCert, c.TLSKey = addr, pair[0], pair[1]
			})
			gw, err := StartGateway(env.profile, deps)
			stopAfter(t, gw)
			if err == nil {
				t.Fatal("StartGateway accepted a key pair that cannot be loaded")
			}
			_, loader := tls.LoadX509KeyPair(pair[0], pair[1])
			for _, want := range []string{"sandbox.tls_cert", "sandbox.tls_key", loader.Error()} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if !bindable(addr) {
				t.Error("the listen address is bound although the key pair could not be loaded")
			}
		})
	}
	_, _ = otherCert, goodKey
}

// capturingSeams passes gatewayd.New through and keeps the Config it was given.
func capturingSeams(captured *gatewayd.Config) gatewaySeams {
	return gatewaySeams{
		newServer: func(cfg gatewayd.Config) (*gatewayd.Server, error) {
			*captured = cfg
			return gatewayd.New(cfg)
		},
		serve: func(s *gatewayd.Server, l net.Listener) error { return s.Serve(l) },
		exit:  func(int) { panic("the gateway must not exit") },
	}
}

// TS-27-9: Start binds the listener, builds gatewayd with hub's settings and no
// static tokens, and a real client connects.
// Requirement: 27-REQ-2.1
func TestStartGateway_BuildsGatewaydAndServes_TS_27_9(t *testing.T) {
	maxTTL := "3h"
	maxSandboxes := 5.0
	env, deps := gatewayTestEnv(t, func(c *Config) {
		c.MaxTTL = &maxTTL
		c.MaxSandboxes = &maxSandboxes
		c.NetworkModes = []string{"none", "unrestricted"}
	})
	_, token := env.issueToken(`{"profile":"tools"}`)

	var captured gatewayd.Config
	gw, err := startGateway(env.profile, deps, capturingSeams(&captured))
	if err != nil {
		t.Fatalf("startGateway: %v", err)
	}
	stopAfter(t, gw)

	if _, ok := captured.Backend.(*Backend); !ok {
		t.Errorf("Config.Backend is a %T; want hub's wrapping backend", captured.Backend)
	}
	if captured.Authenticate == nil || len(captured.Tokens) != 0 {
		t.Errorf("Config.Authenticate nil = %v, Tokens = %v; want the lease authenticator and no static tokens", captured.Authenticate == nil, captured.Tokens)
	}
	if captured.TLS != nil {
		t.Error("Config.TLS is set; want nil without a key pair")
	}
	if want := []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}; !reflect.DeepEqual(captured.NetworkModes, want) {
		t.Errorf("Config.NetworkModes = %v; want %v", captured.NetworkModes, want)
	}
	if captured.MaxSandboxes != 5 || captured.MaxTTL != 3*time.Hour {
		t.Errorf("Config.MaxSandboxes = %d, MaxTTL = %v; want 5 and 3h", captured.MaxSandboxes, captured.MaxTTL)
	}

	client, err := gateway.Dial(context.Background(), gw.URL(), gateway.Options{Token: token})
	if err != nil {
		t.Fatalf("gateway.Dial: %v", err)
	}
	defer client.Close()
	if _, err := client.List(context.Background()); err != nil {
		t.Errorf("List through the gateway: %v", err)
	}
	if info := client.Gateway(); info.MaxTTL != 3*time.Hour || info.MaxSandboxes != 5 {
		t.Errorf("the gateway reports MaxTTL %v, MaxSandboxes %d; want 3h and 5", info.MaxTTL, info.MaxSandboxes)
	}
}

// TS-27-10: An address that cannot be bound refuses start naming sandbox.listen
// with the bind error.
// Requirement: 27-REQ-2.2
func TestStartGateway_BindFailure_TS_27_10(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	addr := taken.Addr().String()
	_, bindErr := net.Listen("tcp", addr)

	env, deps := gatewayTestEnv(t, func(c *Config) { c.Listen = addr })
	gw, err := StartGateway(env.profile, deps)
	stopAfter(t, gw)
	if err == nil {
		t.Fatal("StartGateway bound an address that is taken")
	}
	for _, want := range []string{"sandbox.listen", bindErr.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// TS-27-11: A gatewayd that cannot be built refuses start with the error wrapped
// and the listener closed.
// Requirement: 27-REQ-2.3
func TestStartGateway_NewFailure_TS_27_11(t *testing.T) {
	addr := freeAddr(t)
	env, deps := gatewayTestEnv(t, func(c *Config) { c.Listen = addr })
	sentinel := errors.New("gatewayd refuses this configuration")

	gw, err := startGateway(env.profile, deps, gatewaySeams{
		newServer: func(gatewayd.Config) (*gatewayd.Server, error) { return nil, sentinel },
		serve:     func(*gatewayd.Server, net.Listener) error { t.Error("serve was called"); return nil },
		exit:      func(int) { t.Error("exit was called") },
	})
	stopAfter(t, gw)
	if !errors.Is(err, sentinel) {
		t.Fatalf("startGateway = %v; want an error wrapping the gatewayd.New error", err)
	}
	if gw != nil {
		t.Error("startGateway returned a gateway with the error")
	}
	if !bindable(addr) {
		t.Error("the listener is still bound after the failure")
	}
}

// TS-27-12: A Serve that returns an error is logged once and ends the process
// with a non-zero status. A Serve that returns nil has been shut down and is not
// a failure.
// Requirement: 27-REQ-2.4
func TestStartGateway_ServeReturns_TS_27_12(t *testing.T) {
	errServe := errors.New("accept tcp: use of closed network connection")

	run := func(t *testing.T, serveErr error) (codes chan int, logs *logBuffer) {
		t.Helper()
		logs = captureDefaultLog(t)
		env, deps := gatewayTestEnv(t, nil)
		codes = make(chan int, 4)
		var mu sync.Mutex
		var held net.Listener
		t.Cleanup(func() {
			mu.Lock()
			defer mu.Unlock()
			if held != nil {
				held.Close()
			}
		})
		gw, err := startGateway(env.profile, deps, gatewaySeams{
			newServer: gatewayd.New,
			serve: func(_ *gatewayd.Server, l net.Listener) error {
				mu.Lock()
				held = l
				mu.Unlock()
				return serveErr
			},
			exit: func(code int) { codes <- code },
		})
		if err != nil {
			t.Fatalf("startGateway: %v", err)
		}
		stopAfter(t, gw)
		return codes, logs
	}

	t.Run("an error", func(t *testing.T) {
		codes, logs := run(t, errServe)
		select {
		case code := <-codes:
			if code == 0 {
				t.Errorf("exit code = %d; want non-zero", code)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the process was not ended")
		}
		out := logs.String()
		if n := strings.Count(out, errServe.Error()); n != 1 || !strings.Contains(out, `"level":"ERROR"`) {
			t.Errorf("the Serve error is in %d log lines; want one error record:\n%s", n, out)
		}
	})

	t.Run("nil after a shutdown", func(t *testing.T) {
		codes, logs := run(t, nil)
		select {
		case code := <-codes:
			t.Errorf("the process was ended with %d although Serve returned nil", code)
		case <-time.After(300 * time.Millisecond):
		}
		if out := logs.String(); strings.Contains(out, `"level":"ERROR"`) {
			t.Errorf("an error was logged for a Serve that returned nil:\n%s", out)
		}
	})
}

// TS-27-13: With the feature off nothing is bound and no gatewayd is built.
// Requirement: 27-REQ-2.5
func TestStartGateway_FeatureOff_TS_27_13(t *testing.T) {
	built := 0
	gw, err := startGateway(nil, GatewayDeps{}, gatewaySeams{
		newServer: func(gatewayd.Config) (*gatewayd.Server, error) { built++; return nil, errors.New("built") },
		serve:     func(*gatewayd.Server, net.Listener) error { return nil },
		exit:      func(int) {},
	})
	if err != nil || gw != nil {
		t.Fatalf("startGateway(nil) = (%v, %v); want (nil, nil)", gw, err)
	}
	if built != 0 {
		t.Errorf("gatewayd.New was called %d times; want 0", built)
	}
	if gw.URL() != "" {
		t.Errorf("the URL of no gateway = %q; want empty", gw.URL())
	}
	if gw2, err := StartGateway(nil, GatewayDeps{}); err != nil || gw2 != nil {
		t.Errorf("StartGateway(nil) = (%v, %v); want (nil, nil)", gw2, err)
	}
}

// TS-27-14: The gateway URL is external_url, or the scheme and the address the
// listener is really on.
// Requirement: 27-REQ-2.6
func TestStartGateway_GatewayURL_TS_27_14(t *testing.T) {
	t.Run("plaintext", func(t *testing.T) {
		env, deps := gatewayTestEnv(t, nil)
		gw, err := StartGateway(env.profile, deps)
		if err != nil {
			t.Fatalf("StartGateway: %v", err)
		}
		stopAfter(t, gw)
		addr := strings.TrimPrefix(gw.URL(), "http://")
		if addr == gw.URL() {
			t.Fatalf("URL = %q; want http://<address>", gw.URL())
		}
		if _, port, err := net.SplitHostPort(addr); err != nil || port == "0" {
			t.Errorf("URL = %q; want the port the listener really got, not 0 (%v)", gw.URL(), err)
		}
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatalf("the address in the URL does not accept connections: %v", err)
		}
		conn.Close()
	})

	t.Run("TLS", func(t *testing.T) {
		certFile, keyFile, pool := testKeyPair(t)
		env, deps := gatewayTestEnv(t, func(c *Config) { c.TLSCert, c.TLSKey = certFile, keyFile })
		_, token := env.issueToken(`{"profile":"tools"}`)
		gw, err := StartGateway(env.profile, deps)
		if err != nil {
			t.Fatalf("StartGateway: %v", err)
		}
		stopAfter(t, gw)
		addr := strings.TrimPrefix(gw.URL(), "https://")
		if addr == gw.URL() {
			t.Fatalf("URL = %q; want https://<address>", gw.URL())
		}
		client, err := gateway.Dial(context.Background(), gw.URL(), gateway.Options{Token: token, TLSConfig: &tls.Config{RootCAs: pool}})
		if err != nil {
			t.Fatalf("gateway.Dial over TLS: %v", err)
		}
		client.Close()
		if _, err := gateway.Dial(context.Background(), "http://"+addr, gateway.Options{Token: token}); err == nil {
			t.Error("a plaintext client reached a gateway that serves TLS")
		}
	})

	t.Run("external_url", func(t *testing.T) {
		env, deps := gatewayTestEnv(t, func(c *Config) { c.ExternalURL = "https://gw.example.com" })
		gw, err := StartGateway(env.profile, deps)
		if err != nil {
			t.Fatalf("StartGateway: %v", err)
		}
		stopAfter(t, gw)
		if gw.URL() != "https://gw.example.com" {
			t.Errorf("URL = %q; want exactly https://gw.example.com", gw.URL())
		}
	})
}

// gatewayURL puts the scheme on the address the listener is on.
func TestGatewayURL(t *testing.T) {
	for _, c := range []struct {
		name, external string
		useTLS         bool
		addr           net.Addr
		want           string
	}{
		{"plaintext", "", false, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4711}, "http://127.0.0.1:4711"},
		{"TLS", "", true, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4711}, "https://127.0.0.1:4711"},
		{"IPv6", "", false, &net.TCPAddr{IP: net.ParseIP("::1"), Port: 4711}, "http://[::1]:4711"},
		{"external", "https://gw.example.com/", false, &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 8443}, "https://gw.example.com/"},
	} {
		if got := gatewayURL(c.external, c.useTLS, c.addr); got != c.want {
			t.Errorf("%s: gatewayURL = %q; want %q", c.name, got, c.want)
		}
	}
}

// gatewayd reaps, and so has hub's backend run the sweep, when it serves and at
// reap_interval: the interval of the configuration, not gatewayd's own ten minutes.
// Requirement: 28-REQ-5.5
func TestStartGateway_GatewaydReapsAtReapInterval(t *testing.T) {
	env, deps := gatewayTestEnv(t, func(c *Config) { c.ReapInterval = strPtr("45s") })
	var captured gatewayd.Config
	gw, err := startGateway(env.profile, deps, capturingSeams(&captured))
	if err != nil {
		t.Fatalf("startGateway: %v", err)
	}
	stopAfter(t, gw)
	if captured.ReapInterval != 45*time.Second {
		t.Errorf("gatewayd.Config.ReapInterval = %v; want 45s", captured.ReapInterval)
	}
}
