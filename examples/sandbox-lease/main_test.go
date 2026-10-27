package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

const (
	testToken  = "sbl_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testSecret = "s3cr3t-value"
)

// hubBackend stands in for the hub behind its gateway: it builds the sandbox
// from the lease rather than from the caller's spec (here: no image, the
// secret in the environment, the hub's labels), over local, which runs the
// sandbox in this process. It offers what the hub offers by default: images
// and no network.
type hubBackend struct{ sandbox.Provider }

func (b hubBackend) Capabilities() sandbox.Capabilities {
	c := b.Provider.Capabilities()
	c.Image, c.Network = true, []sandbox.NetworkMode{sandbox.NetworkNone}
	return c
}

func (b hubBackend) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	spec.Image, spec.Network = "", sandbox.Network{Mode: sandbox.NetworkUnrestricted}
	spec.Env = map[string]string{"DEMO_TOKEN": testSecret}
	spec.Labels["hub.lease"], spec.Labels["hub.workspace"] = "sbx_1", "widgets"
	return b.Provider.Create(ctx, spec)
}

// startGateway serves a gateway that accepts testToken on a loopback port and
// returns its URL.
func startGateway(t *testing.T) string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	srv, err := gatewayd.New(gatewayd.Config{Backend: hubBackend{local.Unisolated()}, Tokens: []string{testToken}})
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l) //nolint:errcheck // ends with Shutdown
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return "http://" + l.Addr().String()
}

// writeLease writes a lease as afc sandbox create prints it with
// --token-file, changed by edit, and returns its path.
func writeLease(t *testing.T, dir, gatewayURL string, edit func(map[string]any)) string {
	t.Helper()
	l := map[string]any{
		"id": "sbx_1", "workspace": "widgets", "profile": "agent", "state": "leased",
		"gateway_url": gatewayURL, "image": "registry.example.com/agent:1", "network": "none",
		"expires_at":       time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339),
		"lease_expires_at": time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
		"secrets":          []string{"DEMO_TOKEN"}, "optional_secrets": []string{"OPTIONAL_KEY"},
	}
	if edit != nil {
		edit(l)
	}
	raw, _ := json.Marshal(l)
	path := filepath.Join(dir, "lease.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The lease's sandbox is created through the gateway with the hub's labels,
// --dir goes in, the tour reports the secrets without their values, --out
// comes back out, and closing leaves the gateway holding no sandbox.
func TestRun_CreatesWorksInAndClosesTheLeaseSandbox(t *testing.T) {
	url, dir := startGateway(t), t.TempDir()
	project, out := filepath.Join(dir, "project"), filepath.Join(dir, "out")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(dir, "lease.token")
	if err := os.WriteFile(tokenFile, []byte(testToken), 0o600); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	err := run(context.Background(), &b, nil, options{lease: writeLease(t, dir, url, nil), tokenFile: tokenFile, dir: project, out: out})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, b.String())
	}
	for _, want := range []string{"lease sbx_1: workspace widgets, profile agent", "backend local",
		"labels example=sandbox-lease hub.lease=sbx_1 hub.workspace=widgets", "$ ls -la", "README.md",
		"DEMO_TOKEN: set", "OPTIONAL_KEY: not set", "exported the workspace to " + out, "sandbox closed; lease sbx_1 has ended"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
	if strings.Contains(b.String(), testSecret) {
		t.Errorf("the output shows the secret's value:\n%s", b.String())
	}
	if _, err := os.Stat(filepath.Join(out, "README.md")); err != nil {
		t.Errorf("the workspace was not exported: %v", err)
	}
	p, err := gateway.Dial(context.Background(), url, gateway.Options{Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if infos, err := p.List(context.Background()); err != nil || len(infos) != 0 {
		t.Errorf("after run the gateway holds %d sandboxes (%v), want 0", len(infos), err)
	}
}

// A --run command that fails is shown and makes run fail, after the others
// ran and the sandbox closed.
func TestRun_FailsWhenACommandFails(t *testing.T) {
	url, dir := startGateway(t), t.TempDir()
	path := writeLease(t, dir, url, func(l map[string]any) { l["token"] = testToken })
	var b strings.Builder
	err := run(context.Background(), &b, nil, options{lease: path, run: []string{"exit 3", "echo after"}})
	if err == nil || !strings.Contains(err.Error(), `command "exit 3" failed`) {
		t.Fatalf("run = %v, want the failed command named", err)
	}
	for _, want := range []string{"$ exit 3", "[exit 3]", "after", "sandbox closed"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
}

// A lease that cannot create its sandbox fails with a message that says
// what to do about it.
func TestRun_SaysWhyTheLeaseCannotCreate(t *testing.T) {
	url := startGateway(t)
	for name, tc := range map[string]struct {
		edit func(map[string]any)
		want string
	}{
		"not leased": {func(l map[string]any) { l["state"] = "destroyed" },
			"lease sbx_1 is destroyed: only a leased lease can create its sandbox; issue a new one with afc sandbox create widgets"},
		"out of time": {func(l map[string]any) { l["expires_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) },
			"lease sbx_1 ran out of time"},
		"token refused": {func(l map[string]any) { l["token"] = "sbl_" + strings.Repeat("B", 44) },
			"the gateway refused the lease token"},
	} {
		t.Run(name, func(t *testing.T) {
			path := writeLease(t, t.TempDir(), url, func(l map[string]any) {
				l["token"] = testToken
				tc.edit(l)
			})
			if err := run(context.Background(), io.Discard, nil, options{lease: path}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("run = %v, want an error saying %q", err, tc.want)
			}
		})
	}
}

// The token comes from --token-file when one is given, else from the lease
// JSON; the lease may come from stdin.
func TestReadLease_FindsTheToken(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "lease.token")
	if err := os.WriteFile(tokenFile, []byte("sbl_from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withToken := `{"id":"sbx_1","gateway_url":"https://h","token":"sbl_from-json"}`
	noToken := `{"id":"sbx_1","gateway_url":"https://h"}`
	for name, tc := range map[string]struct {
		json, tokenFile, want, wantErr string
	}{
		"from the JSON":                 {json: withToken, want: "sbl_from-json"},
		"from the file, trimmed":        {json: noToken, tokenFile: tokenFile, want: "sbl_from-file"},
		"the file wins":                 {json: withToken, tokenFile: tokenFile, want: "sbl_from-file"},
		"none":                          {json: noToken, wantErr: "no lease token"},
		"not a lease":                   {json: `{"profiles":[]}`, wantErr: "no id or no gateway_url"},
		"not JSON":                      {json: "warning: the token will not be shown again", wantErr: "not the JSON"},
		"a token file that is not here": {json: noToken, tokenFile: filepath.Join(dir, "missing"), wantErr: "reading the lease token"},
	} {
		t.Run(name, func(t *testing.T) {
			l, err := readLease("-", tc.tokenFile, strings.NewReader(tc.json))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("readLease = %v, want an error saying %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || l.Token != tc.want {
				t.Errorf("readLease = token %q, %v; want %q", l.Token, err, tc.want)
			}
		})
	}
}
