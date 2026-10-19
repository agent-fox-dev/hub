package sandboxes

import (
	"bytes"
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/txsvc/apikit"
)

// writeConfigFile writes content to dir/config.toml and returns its path.
func writeConfigFile(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// profileNames returns the profile names of cfg in file order.
func profileNames(cfg *Config) []string {
	names := make([]string, 0, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		names = append(names, p.Name)
	}
	return names
}

// mustLoad loads path and fails the test on an error or a nil configuration.
func mustLoad(t *testing.T, path string) *Config {
	t.Helper()
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s) returned error: %v", path, err)
	}
	if cfg == nil {
		t.Fatalf("LoadConfig(%s) returned a nil configuration", path)
	}
	return cfg
}

// TS-25-1: The loader reads the sandbox section from the path apikit resolves,
// $XDG_CONFIG_HOME/config.toml or ./config.toml when the variable is unset or
// empty, and keeps profile file order.
// Requirement: 25-REQ-1.1
func TestConfigPath_TS_25_1(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	writeConfigFile(t, dirA, `
[sandbox]
provider = "podman"

[[sandbox.profiles]]
name = "zeta"

[[sandbox.profiles]]
name = "alpha"
`)
	writeConfigFile(t, dirB, `
[sandbox]
provider = "from-working-dir"

[[sandbox.profiles]]
name = "only"
`)

	t.Run("XDG_CONFIG_HOME set", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", dirA)
		path := apikit.ConfigPath()
		if want := filepath.Join(dirA, "config.toml"); path != want {
			t.Fatalf("config path = %q; want %q", path, want)
		}
		cfg := mustLoad(t, path)
		if got := profileNames(cfg); strings.Join(got, ",") != "zeta,alpha" {
			t.Errorf("profiles = %v; want [zeta alpha] in file order", got)
		}
		if cfg.Provider != "podman" {
			t.Errorf("provider = %q; want podman", cfg.Provider)
		}
	})

	for name, setup := range map[string]func(t *testing.T){
		"XDG_CONFIG_HOME empty": func(t *testing.T) { t.Setenv("XDG_CONFIG_HOME", "") },
		"XDG_CONFIG_HOME unset": func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", "")
			if err := os.Unsetenv("XDG_CONFIG_HOME"); err != nil {
				t.Fatalf("unsetting XDG_CONFIG_HOME: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			setup(t)
			t.Chdir(dirB)
			path := apikit.ConfigPath()
			if filepath.Clean(path) != "config.toml" {
				t.Fatalf("config path = %q; want ./config.toml", path)
			}
			cfg := mustLoad(t, path)
			if cfg.Provider != "from-working-dir" {
				t.Errorf("provider = %q; want the working directory's config", cfg.Provider)
			}
		})
	}
}

// A missing file, a file without a [sandbox] table and an empty [sandbox]
// table: the first two mean the feature is off (nil configuration, nil error),
// the last one is a section that validation will reject for its missing
// provider.
// Requirement: 25-REQ-1.2
func TestLoadConfig_NoSection(t *testing.T) {
	dir := t.TempDir()

	cfg, err := LoadConfig(filepath.Join(dir, "does-not-exist.toml"))
	if cfg != nil || err != nil {
		t.Errorf("missing file: LoadConfig = (%v, %v); want (nil, nil)", cfg, err)
	}

	cfg, err = LoadConfig(writeConfigFile(t, dir, "[server]\nport = 9090\n"))
	if cfg != nil || err != nil {
		t.Errorf("no [sandbox] table: LoadConfig = (%v, %v); want (nil, nil)", cfg, err)
	}

	cfg, err = LoadConfig(writeConfigFile(t, dir, "[sandbox]\n"))
	if err != nil || cfg == nil {
		t.Errorf("empty [sandbox] table: LoadConfig = (%v, %v); want a configuration", cfg, err)
	}
}

// A value of the wrong type is a decode error that names the key, not a
// silently dropped or zeroed field.
// Requirement: 25-REQ-1.6
func TestLoadConfig_WrongValueTypeIsDecodeError(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), `
[sandbox]
provider = "podman"

[[sandbox.profiles]]
name = "dev"
memory_mb = "lots"
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig accepted a string for memory_mb")
	}
	if !strings.Contains(err.Error(), "sandbox.profiles.memory_mb") {
		t.Errorf("error %q does not name the key sandbox.profiles.memory_mb", err)
	}
}

// envVars are the variables the expansion property test sets; envUnset are
// never set.
var (
	envVars  = []string{"SBXT_SET_A", "SBXT_SET_B", "SBXT_SET_C"}
	envUnset = []string{"SBXT_UNSET_X", "SBXT_UNSET_Y"}
)

// validEnvName reports whether name is a legal ${NAME} reference: a letter or
// underscore followed by letters, digits and underscores.
func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// modelExpand is the reference expansion the loader is compared with: every
// ${NAME} becomes vars[NAME] (empty when absent) and the replacement text is
// not scanned again; everything else, including a bare $, is copied.
func modelExpand(s string, vars map[string]string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "${") {
			if end := strings.IndexByte(s[i:], '}'); end > 0 {
				if name := s[i+2 : i+end]; validEnvName(name) {
					out.WriteString(vars[name])
					i += end + 1
					continue
				}
			}
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// genExpandable composes a string from literal text, bare dollar signs,
// ${NAME} references to set and unset variables, and malformed references.
func genExpandable(r *rand.Rand) string {
	pieces := []string{
		"plain", "a b", "x-y/z:1", "$", "$$", "$5", "${", "}", "${}", "${1BAD}", "$ {SBXT_SET_C}",
		"${SBXT_SET_A}", "${SBXT_SET_B}", "${SBXT_SET_C}", "${SBXT_UNSET_X}", "${SBXT_UNSET_Y}",
	}
	var sb strings.Builder
	for range 1 + r.IntN(6) {
		sb.WriteString(pieces[r.IntN(len(pieces))])
	}
	return sb.String()
}

// TS-25-3: For any string value in the sandbox section, ${NAME} is replaced by
// the variable's value (empty when unset) and a bare $ is left untouched. The
// same expansion applies to scalar, list and env-map values.
// Requirement: 25-REQ-1.3
func TestLoadConfig_EnvExpansionProperty_TS_25_3(t *testing.T) {
	r := rand.New(rand.NewPCG(25, 3))

	// The values contain quotes, dollar signs and a reference to another
	// variable, which must not be expanded a second time.
	values := []string{`va"lue`, "cost$5", "${SBXT_SET_B}", "plain", "a$"}
	vars := map[string]string{}
	for _, name := range envVars {
		vars[name] = values[r.IntN(len(values))]
		t.Setenv(name, vars[name])
	}
	for _, name := range envUnset {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unsetting %s: %v", name, err)
		}
	}

	dir := t.TempDir()
	for i := range 200 {
		s := genExpandable(r)
		doc := struct {
			Sandbox Config `toml:"sandbox"`
		}{Sandbox: Config{
			Provider:     "podman",
			NetworkModes: []string{s},
			Profiles: []ProfileConfig{{
				Name:    "p",
				Image:   s,
				Secrets: []string{s},
				Env:     map[string]string{"K": s},
			}},
		}}
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
			t.Fatalf("encoding case %d (%q): %v", i, s, err)
		}

		cfg := mustLoad(t, writeConfigFile(t, dir, buf.String()))
		want := modelExpand(s, vars)
		p := cfg.Profiles[0]
		for field, got := range map[string]string{
			"image":            p.Image,
			"secrets[0]":       p.Secrets[0],
			"network_modes[0]": cfg.NetworkModes[0],
			"env.K":            p.Env["K"],
		} {
			if got != want {
				t.Errorf("case %d: %s of %q = %q; want %q", i, field, s, got, want)
			}
		}
	}
}

// TS-25-4: Expansion is limited to the sandbox section, a bare $ stays literal,
// and a value that expands to a quote does not break the file.
// Requirement: 25-REQ-1.3
func TestLoadConfig_ExpansionScopeAndLiterals_TS_25_4(t *testing.T) {
	t.Setenv("SANDBOX_TOKEN", "abc")
	// A quote in a variable would make the file invalid TOML if the loader
	// expanded the whole text before decoding it.
	t.Setenv("HOME_X", `/home/"x"`)

	content := `
[server]
external_url = "${HOME_X}/hub"

[workspace]
path = "${HOME_X}/workspaces"

[sandbox]
provider = "podman"

[[sandbox.profiles]]
name = "dev"
image = "cost$5"

[sandbox.profiles.env]
K = "q\"${SANDBOX_TOKEN}"
`
	path := writeConfigFile(t, t.TempDir(), content)

	cfg := mustLoad(t, path)
	p := cfg.Profiles[0]
	if want := `q"abc`; p.Env["K"] != want {
		t.Errorf("env K = %q; want %q", p.Env["K"], want)
	}
	if p.Image != "cost$5" {
		t.Errorf("image = %q; want cost$5 left literal", p.Image)
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading config back: %v", err)
	}
	if string(onDisk) != content {
		t.Error("the loader changed the config file")
	}
}

// TS-25-5: An unknown key under [sandbox] or under a profile refuses with its
// full key path, and the error carries no env value.
// Requirement: 25-REQ-1.4
func TestLoadConfig_UnknownKey_TS_25_5(t *testing.T) {
	t.Run("profile key is reported with its index", func(t *testing.T) {
		path := writeConfigFile(t, t.TempDir(), `
[sandbox]
provider = "podman"

[[sandbox.profiles]]
name = "a"
memory_mb = 256

[[sandbox.profiles]]
name = "b"
memroy_mb = 512

[sandbox.profiles.env]
TOKEN = "env-value-sentinel"
`)
		_, err := LoadConfig(path)
		if err == nil || !strings.Contains(err.Error(), "sandbox.profiles[1].memroy_mb") {
			t.Fatalf("error = %v; want it to contain sandbox.profiles[1].memroy_mb", err)
		}
		if strings.Contains(err.Error(), "env-value-sentinel") {
			t.Errorf("error leaks an env value: %v", err)
		}
	})

	t.Run("deployment key", func(t *testing.T) {
		path := writeConfigFile(t, t.TempDir(), `
[sandbox]
provider = "podman"
listenx = ":9000"
`)
		_, err := LoadConfig(path)
		if err == nil || !strings.Contains(err.Error(), "sandbox.listenx") {
			t.Fatalf("error = %v; want it to contain sandbox.listenx", err)
		}
	})

	t.Run("every unknown key is reported once", func(t *testing.T) {
		path := writeConfigFile(t, t.TempDir(), `
[sandbox]
provider = "podman"
listenx = ":9000"

[[sandbox.profiles]]
name = "a"
cpu = 1

[[sandbox.profiles]]
name = "b"
cpu = 2

[sandbox.profiles.limits]
pids = 10
`)
		_, err := LoadConfig(path)
		if err == nil {
			t.Fatal("LoadConfig accepted unknown keys")
		}
		for _, want := range []string{
			"sandbox.listenx",
			"sandbox.profiles[0].cpu",
			"sandbox.profiles[1].cpu",
			"sandbox.profiles[1].limits",
		} {
			if n := strings.Count(err.Error(), want); n != 1 {
				t.Errorf("error mentions %s %d times; want once:\n%v", want, n, err)
			}
		}
		if strings.Contains(err.Error(), "limits.pids") {
			t.Errorf("error reports the child of an unknown table:\n%v", err)
		}
	})
}

// TS-25-6: Keys of the sections apikit owns, and of any table other than
// sandbox, never produce an unknown-key error.
// Requirement: 25-REQ-1.5
func TestLoadConfig_ForeignSectionsIgnored_TS_25_6(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), `
[server]
port = 9090
unknown_server_key = true

[database]
path = "x.db"
unknown_database_key = 1

[workspace]
path = "/ws"
unknown_workspace_key = "y"

[[oauth.providers]]
name = "github"
client_id = "id"
unknown_oauth_key = "z"

[sandbox_extra]
anything = 1

[sandbox]
provider = "podman"

[[sandbox.profiles]]
name = "dev"
`)
	cfg := mustLoad(t, path)
	if cfg.Provider != "podman" || len(cfg.Profiles) != 1 {
		t.Errorf("config = %+v; want the podman provider and one profile", cfg)
	}
}

// TS-25-7: An unreadable or undecodable config file returns an error that
// wraps the underlying read or TOML error.
// Requirement: 25-REQ-1.6
func TestLoadConfig_ReadAndDecodeErrors_TS_25_7(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "config.toml"), 0o700); err != nil {
			t.Fatalf("creating directory named config.toml: %v", err)
		}
		_, err := LoadConfig(filepath.Join(dir, "config.toml"))
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("error = %v (%T); want it to wrap an *fs.PathError", err, err)
		}
	})

	t.Run("invalid TOML", func(t *testing.T) {
		path := writeConfigFile(t, t.TempDir(), "[sandbox\nprovider = \n")
		_, err := LoadConfig(path)
		var parseErr toml.ParseError
		if !errors.As(err, &parseErr) {
			t.Fatalf("error = %v (%T); want it to wrap a toml.ParseError", err, err)
		}
	})
}

// Unknown keys are the one loader error that comes with the decoded
// configuration, so that start-up can report them together with the
// validation failures of the rest of the section (25-PATH-3). The unknown key
// itself is not part of the configuration.
// Requirement: 25-REQ-1.4
func TestLoadConfig_UnknownKeysComeWithConfiguration(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), `
[sandbox]
provider = "podman"
listenx  = ":9000"

[[sandbox.profiles]]
name  = "dev"
image = "registry.example/dev:1"
`)
	cfg, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "sandbox.listenx") {
		t.Fatalf("error = %v; want it to name sandbox.listenx", err)
	}
	if cfg == nil || cfg.Provider != "podman" || len(cfg.Profiles) != 1 || cfg.Profiles[0].Name != "dev" {
		t.Errorf("configuration = %+v; want the decoded section next to the error", cfg)
	}

	// Every other loader error comes with a nil configuration.
	cfg, err = LoadConfig(writeConfigFile(t, t.TempDir(), "[sandbox\n"))
	if err == nil || cfg != nil {
		t.Errorf("LoadConfig(invalid TOML) = (%v, %v); want a nil configuration and an error", cfg, err)
	}
}

// leaseSection returns a valid [sandbox] section whose deployment keys include
// extra.
func leaseSection(extra string) string {
	return "[sandbox]\nprovider = \"podman\"\n" + extra + `
[[sandbox.profiles]]
name  = "dev"
image = "registry.example/dev:1"
ttl   = "1h"
`
}

// TS-26-29: max_sandboxes defaults to 16, and an explicit value loads without an
// unknown-key error and reaches the service.
// Requirement: 26-REQ-5.1
func TestMaxSandboxes_DefaultAndExplicit_TS_26_29(t *testing.T) {
	dir := t.TempDir()

	absent := requireValid(t, mustLoad(t, writeConfigFile(t, dir, leaseSection(""))))
	if absent.MaxSandboxes != 16 {
		t.Errorf("limit without max_sandboxes = %d; want 16", absent.MaxSandboxes)
	}

	cfg, err := LoadConfig(writeConfigFile(t, dir, leaseSection("max_sandboxes = 3\n")))
	if err != nil {
		t.Fatalf("a section with max_sandboxes = 3 failed to load: %v", err)
	}
	explicit := requireValid(t, cfg)
	if explicit.MaxSandboxes != 3 {
		t.Errorf("limit with max_sandboxes = 3 is %d; want 3", explicit.MaxSandboxes)
	}

	svc, err := New(explicit, newFakeProvider())
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if svc.MaxSandboxes() != 3 {
		t.Errorf("Service.MaxSandboxes() = %d; want 3", svc.MaxSandboxes())
	}
}

// TS-26-30: An invalid max_sandboxes fails naming the key.
// Requirement: 26-REQ-5.2
func TestMaxSandboxes_Invalid_TS_26_30(t *testing.T) {
	for _, value := range []string{"0", "-1", "2.5", "0.9", "1e300", "inf", "nan"} {
		t.Run(value, func(t *testing.T) {
			cfg, err := LoadConfig(writeConfigFile(t, t.TempDir(), leaseSection("max_sandboxes = "+value+"\n")))
			if err == nil {
				_, err = Validate(cfg)
			}
			if err == nil || !strings.Contains(err.Error(), "sandbox.max_sandboxes") {
				t.Errorf("max_sandboxes = %s: error = %v; want it to name sandbox.max_sandboxes", value, err)
			}
		})
	}

	// A value of the wrong type is a decode error that names the key.
	_, err := LoadConfig(writeConfigFile(t, t.TempDir(), leaseSection("max_sandboxes = \"3\"\n")))
	if err == nil || !strings.Contains(err.Error(), "max_sandboxes") {
		t.Errorf("max_sandboxes = \"3\": error = %v; want it to name max_sandboxes", err)
	}

	for _, ok := range []string{"1", "16", "1000"} {
		cfg := mustLoad(t, writeConfigFile(t, t.TempDir(), leaseSection("max_sandboxes = "+ok+"\n")))
		if _, err := Validate(cfg); err != nil {
			t.Errorf("max_sandboxes = %s was refused: %v", ok, err)
		}
	}
}
