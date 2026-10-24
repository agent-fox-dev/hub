package sandboxes

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
)

// Without a [sandbox] section Init starts nothing, returns (nil, nil) and logs
// one info line saying sandboxes are disabled.
func TestInit_DisabledWithoutASection(t *testing.T) {
	dir := t.TempDir()
	for name, path := range map[string]string{
		"no file":      filepath.Join(dir, "missing.toml"),
		"no [sandbox]": writeConfigFile(t, dir, "[server]\nport = 9090\nlease_timeout = \"abc\"\n"),
	} {
		t.Run(name, func(t *testing.T) {
			logger, buf := newCapturedLogger()
			factory := &recordingFactory{provider: newFakeProvider()}
			svc, err := Init(logger, path, factory.build)
			if svc != nil || err != nil || factory.calls != 0 {
				t.Fatalf("Init = (%v, %v) with %d factory calls; want (nil, nil) and none", svc, err, factory.calls)
			}
			records := logRecords(t, buf)
			if len(records) != 1 || records[0]["level"] != "INFO" || !strings.Contains(records[0]["msg"].(string), "disabled") {
				t.Errorf("log = %s; want one info line saying sandboxes are disabled", buf)
			}
		})
	}
}

// A valid section starts the service and logs one info line holding only the
// provider and the profile names, in file order.
func TestInit_EnabledLogsProviderAndProfilesOnly(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), `
[sandbox]
provider      = "podman"
engine_binary = "/opt/engine-distinctive/podman"
max_ttl       = "3h"

[[sandbox.profiles]]
name    = "alpha"
image   = "registry.distinctive.example/image-alpha:9"
ttl     = "1h"
secrets = ["SECRET_NAME_DISTINCTIVE"]

[sandbox.profiles.env]
ENV_KEY_DISTINCTIVE = "env-value-distinctive"

[[sandbox.profiles]]
name  = "beta"
image = "registry.distinctive.example/image-beta:9"
ttl   = "2h"
`)
	logger, buf := newCapturedLogger()
	fake := newFakeProvider()
	svc, err := Init(logger, path, (&recordingFactory{provider: fake}).build)
	if err != nil || svc == nil || svc.Provider() != fake {
		t.Fatalf("Init = (%v, %v); want a service over the built provider", svc, err)
	}
	records := logRecords(t, buf)
	// The record has slog's three attributes, the provider and the profiles.
	if len(records) != 1 || records[0]["level"] != "INFO" || len(records[0]) != 5 || records[0]["provider"] != "podman" {
		t.Fatalf("log = %s; want one info record with provider and profiles", buf)
	}
	if got, _ := json.Marshal(records[0]["profiles"]); string(got) != `["alpha","beta"]` {
		t.Errorf("profiles = %s; want [alpha beta]", got)
	}
	if strings.Contains(buf.String(), "distinctive") || strings.Contains(buf.String(), "3h") {
		t.Errorf("the log line holds a setting or a value: %s", buf)
	}
}

// An invalid section, an unknown key (reported with the other faults, without
// a provider), an unreachable engine and an undecodable file refuse start with
// a sandbox: error, log nothing and leak no env value.
func TestInit_RefusalsNameTheFaultAndLogNothing(t *testing.T) {
	const secretValue = "env-value-never-printed"
	section := func(extra string) string {
		return "[sandbox]\nprovider = \"podman\"\n" + extra + "\n[[sandbox.profiles]]\nname = \"dev\"\nimage = \"registry.example/dev:1\"\nttl = \"1h\"\n[sandbox.profiles.env]\nTOKEN = \"" + secretValue + "\"\n"
	}
	for name, tc := range map[string]struct {
		config string
		fail   bool
		want   []string
	}{
		"unknown key and a fault": {config: strings.Replace(section("listenx = \":9\"\nmax_ttl = \"never\"\n"), `"1h"`, `"never"`, 1), want: []string{"sandbox.listenx", "sandbox.max_ttl", `profile "dev"`}},
		"invalid ttl":             {config: strings.Replace(section(""), `"1h"`, `"never"`, 1), want: []string{`profile "dev"`}},
		"engine unreachable":      {config: section(""), fail: true, want: []string{"sandbox.engine_binary", "engine down"}},
		"invalid TOML":            {config: "[sandbox\n", want: []string{"decoding"}},
	} {
		t.Run(name, func(t *testing.T) {
			path := writeConfigFile(t, t.TempDir(), tc.config)
			logger, buf := newCapturedLogger()
			fake := newFakeProvider()
			if tc.fail {
				fake.reapErr = errors.New("engine down")
			}
			factory := &recordingFactory{provider: fake}
			svc, err := Init(logger, path, factory.build)
			if svc != nil || err == nil || !strings.HasPrefix(err.Error(), "sandbox:") {
				t.Fatalf("Init = (%v, %v); want a sandbox: refusal", svc, err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if !tc.fail && factory.calls != 0 {
				t.Error("the provider was built for a static fault")
			}
			if strings.Contains(err.Error()+buf.String(), secretValue) || buf.String() != "" {
				t.Errorf("a refused start logged or leaked:\n%v\n%s", err, buf)
			}
		})
	}
}

// Init with provider gateway against a reachable remote returns a service
// whose provider is the real client, with one List and no Create on the
// remote, and logs one record for provider gateway without the token.
func TestInit_GatewayProviderAgainstARemote(t *testing.T) {
	prov := newRecordingProvider()
	remote := startRemoteGateway(t, prov)
	t.Setenv("AGENTKIT_GATEWAY_TOKEN", remoteToken)
	path := writeConfigFile(t, t.TempDir(), "[sandbox]\nprovider = \"gateway\"\ngateway_url = \""+remote.url()+"\"\ngateway_token = \"${AGENTKIT_GATEWAY_TOKEN}\"\n\n[[sandbox.profiles]]\nname = \"dev\"\nimage = \"registry.example/dev:1\"\nttl = \"1h\"\n")
	logger, logs := newCapturedLogger()
	rpcs := remote.rpcs()

	svc, err := Init(logger, path, noPodman(t), WithGatewayFactory(DialGateway))
	if err != nil || svc == nil {
		t.Fatalf("Init = (%v, %v); want a service", svc, err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	client, ok := svc.Provider().(*gateway.Provider)
	if !ok || client.Gateway().URL != remote.url() {
		t.Fatalf("Provider() is a %T for %v; want the gateway client of %s", svc.Provider(), svc.Provider(), remote.url())
	}
	if got := remote.rpcs() - rpcs; got != 2 || prov.creates() != 0 {
		t.Errorf("start-up cost the remote %d RPCs and %d Creates; want 2 (capabilities and the List) and 0", got, prov.creates())
	}
	for _, token := range remote.tokens() {
		if token != remoteToken {
			t.Error("the remote was shown a token that is not hub's")
		}
	}
	records := logRecords(t, logs)
	if len(records) != 1 || records[0]["provider"] != "gateway" || len(records[0]) != 5 || strings.Contains(logs.String(), remoteToken) {
		t.Errorf("start-up log = %s; want one record for provider gateway without the token", logs)
	}

	// A remote that refuses the token, and one that is gone, refuse start.
	t.Setenv("AGENTKIT_GATEWAY_TOKEN", "other-hub-token-9876543210fedcba")
	svc, err = Init(logger, path, noPodman(t))
	if svc != nil || err == nil || !strings.Contains(err.Error(), "refused the token") {
		t.Errorf("a refused token: Init = (%v, %v)", svc, err)
	}
	containsNone(t, "the error", err.Error(), remoteToken, "other-hub-token")
	remote.stop()
	if svc, err = Init(logger, path, noPodman(t)); svc != nil || err == nil || !strings.Contains(err.Error(), remote.url()) {
		t.Errorf("a remote that is gone: Init = (%v, %v); want a refusal naming the address", svc, err)
	}
}
