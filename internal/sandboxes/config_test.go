package sandboxes

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/txsvc/apikit"
)

func writeConfigFile(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func mustLoad(t *testing.T, path string) *Config {
	t.Helper()
	cfg, err := LoadConfig(path)
	if err != nil || cfg == nil {
		t.Fatalf("LoadConfig(%s) = (%v, %v); want a configuration", path, cfg, err)
	}
	return cfg
}

// leaseSection is a valid [sandbox] section with extra deployment keys.
func leaseSection(extra string) string {
	return "[sandbox]\nprovider = \"podman\"\n" + extra + "\n[[sandbox.profiles]]\nname = \"dev\"\nimage = \"registry.example/dev:1\"\nttl = \"1h\"\n"
}

// The loader reads the file apikit resolves and keeps the profiles in file order.
func TestLoadConfig_ReadsApikitsPathInFileOrder(t *testing.T) {
	dir := t.TempDir()
	writeConfigFile(t, dir, "[sandbox]\nprovider = \"podman\"\n\n[[sandbox.profiles]]\nname = \"zeta\"\n\n[[sandbox.profiles]]\nname = \"alpha\"\n")
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfg := mustLoad(t, apikit.ConfigPath())
	if cfg.Provider != "podman" || len(cfg.Profiles) != 2 || cfg.Profiles[0].Name != "zeta" || cfg.Profiles[1].Name != "alpha" {
		t.Errorf("config = %+v; want podman with [zeta alpha]", cfg)
	}
}

// A missing file or a file without [sandbox] is (nil, nil); an empty table is a
// configuration for validation to reject.
func TestLoadConfig_NoSectionMeansOff(t *testing.T) {
	dir := t.TempDir()
	for name, path := range map[string]string{
		"missing file": filepath.Join(dir, "none.toml"),
		"no table":     writeConfigFile(t, dir, "[server]\nport = 9090\nunknown_server_key = true\n\n[sandbox_extra]\nanything = 1\n"),
	} {
		if cfg, err := LoadConfig(path); cfg != nil || err != nil {
			t.Errorf("%s: LoadConfig = (%v, %v); want (nil, nil)", name, cfg, err)
		}
	}
	if cfg, err := LoadConfig(writeConfigFile(t, t.TempDir(), "[sandbox]\n")); err != nil || cfg == nil {
		t.Errorf("empty table: LoadConfig = (%v, %v); want a configuration", cfg, err)
	}
}

// Read and decode failures wrap the underlying error; a wrong value type names
// the key.
func TestLoadConfig_ReadAndDecodeErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	var pathErr *fs.PathError
	if _, err := LoadConfig(filepath.Join(dir, "config.toml")); !errors.As(err, &pathErr) {
		t.Errorf("unreadable: error = %v; want an *fs.PathError", err)
	}
	var parseErr toml.ParseError
	cfg, err := LoadConfig(writeConfigFile(t, t.TempDir(), "[sandbox\nprovider = \n"))
	if !errors.As(err, &parseErr) || cfg != nil {
		t.Errorf("invalid TOML: (%v, %v); want a nil configuration and a toml.ParseError", cfg, err)
	}
	_, err = LoadConfig(writeConfigFile(t, t.TempDir(), "[sandbox]\nprovider = \"podman\"\n\n[[sandbox.profiles]]\nname = \"dev\"\nmemory_mb = \"lots\"\n"))
	if err == nil || !strings.Contains(err.Error(), "sandbox.profiles.memory_mb") {
		t.Errorf("wrong type: error = %v; want it to name sandbox.profiles.memory_mb", err)
	}
}

// ${NAME} is expanded in every string of the section, scalar, list and map
// value alike, once, with a bare $ and malformed references left alone; the
// rest of the file is not expanded and the file is not changed.
func TestLoadConfig_ExpandsEnvInTheSectionOnly(t *testing.T) {
	t.Setenv("SBX_A", `va"lue`)
	t.Setenv("SBX_B", "${SBX_A}")
	t.Setenv("SBX_UNSET", "")
	if err := os.Unsetenv("SBX_UNSET"); err != nil {
		t.Fatal(err)
	}
	content := `
[server]
external_url = "${SBX_A}/hub"

[sandbox]
provider      = "podman"
network_modes = ["${SBX_A}", "x${SBX_UNSET}y"]
listen        = "${SBX_B}"

[[sandbox.profiles]]
name    = "p"
image   = "cost$5 $$ ${ } ${1BAD} ${SBX_A}"
secrets = ["${SBX_A}"]

[sandbox.profiles.env]
K = "q\"${SBX_A}"
`
	path := writeConfigFile(t, t.TempDir(), content)
	cfg := mustLoad(t, path)
	p := cfg.Profiles[0]
	for field, got := range map[string]struct{ got, want string }{
		"network_modes[0]": {cfg.NetworkModes[0], `va"lue`},
		"network_modes[1]": {cfg.NetworkModes[1], "xy"},
		"listen":           {cfg.Listen, "${SBX_A}"},
		"image":            {p.Image, `cost$5 $$ ${ } ${1BAD} va"lue`},
		"secrets[0]":       {p.Secrets[0], `va"lue`},
		"env.K":            {p.Env["K"], `q"va"lue`},
	} {
		if got.got != got.want {
			t.Errorf("%s = %q; want %q", field, got.got, got.want)
		}
	}
	if onDisk, _ := os.ReadFile(path); string(onDisk) != content {
		t.Error("the loader changed the file")
	}
}

// An unknown key under [sandbox] or a profile is refused by its full path, once,
// without the children of an unknown table and without any value, and the
// decoded configuration comes with the error.
func TestLoadConfig_UnknownKeysAreNamedOnce(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), `
[sandbox]
provider = "podman"
listenx  = ":9000"

[[sandbox.profiles]]
name = "a"
cpu  = 1

[[sandbox.profiles]]
name      = "b"
cpu       = 2
memroy_mb = 512

[sandbox.profiles.env]
TOKEN = "env-value-sentinel"

[sandbox.profiles.limits]
pids = 10
`)
	cfg, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig accepted unknown keys")
	}
	var unknown *unknownKeysError
	if !errors.As(err, &unknown) {
		t.Fatalf("error is a %T; want *unknownKeysError", err)
	}
	for _, want := range []string{"sandbox.listenx", "sandbox.profiles[0].cpu", "sandbox.profiles[1].cpu", "sandbox.profiles[1].memroy_mb", "sandbox.profiles[1].limits"} {
		if n := strings.Count(err.Error(), want); n != 1 {
			t.Errorf("error names %s %d times; want once:\n%v", want, n, err)
		}
	}
	if strings.Contains(err.Error(), "limits.pids") || strings.Contains(err.Error(), "env-value-sentinel") {
		t.Errorf("error names a child of an unknown table or an env value:\n%v", err)
	}
	if cfg == nil || cfg.Provider != "podman" || len(cfg.Profiles) != 2 {
		t.Errorf("configuration = %+v; want the decoded section next to the error", cfg)
	}
}

// The gateway and expiry keys are known, expanded and their failures joined
// with the rest of the section's.
func TestLoadConfig_DeploymentKeysAreKnown(t *testing.T) {
	t.Setenv("GWDIR", "/etc/hub")
	t.Setenv("GW_TOKEN", testGatewayToken)
	cfg := mustLoad(t, writeConfigFile(t, t.TempDir(), leaseSection(`
listen        = "127.0.0.1:0"
tls_cert      = "${GWDIR}/gw.crt"
tls_key       = "${GWDIR}/gw.key"
external_url  = "https://gw.example.com"
lease_timeout = "90s"
reap_interval = "30s"
max_sandboxes = 3
max_ttl       = "1h"
`)))
	v := requireValid(t, cfg)
	if v.Listen != "127.0.0.1:0" || v.TLSCert != "/etc/hub/gw.crt" || v.TLSKey != "/etc/hub/gw.key" || v.ExternalURL != "https://gw.example.com" ||
		v.LeaseTimeout.String() != "1m30s" || v.ReapInterval.String() != "30s" || v.MaxSandboxes != 3 {
		t.Errorf("validated = %+v; want the keys as written", v)
	}

	cfg = mustLoad(t, writeConfigFile(t, t.TempDir(), "[sandbox]\nprovider = \"gateway\"\nmax_ttl = \"bogus\"\ngateway_url = \"https://gw.example/api\"\ngateway_token = \"${GW_TOKEN}\"\n\n[[sandbox.profiles]]\nname = \"dev\"\nimage = \"i\"\nttl = \"1h\"\n"))
	if cfg.GatewayToken != testGatewayToken {
		t.Errorf("gateway_token = %q; want the expanded value", cfg.GatewayToken)
	}
	_, err := Validate(cfg)
	requireFailure(t, err, "sandbox.gateway_url", "path")
	requireFailure(t, err, "sandbox.max_ttl")
	if strings.Contains(err.Error(), testGatewayToken) {
		t.Errorf("the error holds the token: %v", err)
	}
}
