package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHelperConfig writes ~/.af/config.toml, which the credential helper
// reads, in a HOME of its own.
func writeHelperConfig(t *testing.T, endpoint, apiKey string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".af")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "endpoint_url = \"" + endpoint + "\"\nuser_id = \"u1\"\napi_key = \"" + apiKey + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// credentialHelper runs 'afc credential-helper <verb>' with stdin and returns
// what it printed.
func credentialHelper(t *testing.T, verb, stdin string) string {
	t.Helper()
	stdout, _, err := execute(t, stdin, "credential-helper", verb)
	if err != nil {
		t.Fatalf("credential-helper %s: %v", verb, err)
	}
	return stdout
}

// The helper answers a get for the hub's own scheme and host:port only, so
// that a plain-http or other-port request for the same host never receives
// the API key; store and erase are accepted and answered with nothing.
func TestCredentialHelper_AnswersOnlyTheHubsSchemeAndPort(t *testing.T) {
	t.Setenv("ENDPOINT_URL", "")
	t.Setenv("API_KEY", "")
	writeHelperConfig(t, "https://hub.example.com", "af_secretkey")

	for _, tc := range []struct {
		name, verb, stdin string
		answered          bool
	}{
		{"https on the hub host", "get", "protocol=https\nhost=hub.example.com\n\n", true},
		{"explicit default port", "get", "protocol=https\nhost=HUB.example.com:443\n\n", true},
		{"plain http", "get", "protocol=http\nhost=hub.example.com\n\n", false},
		{"another port", "get", "protocol=https\nhost=hub.example.com:8443\n\n", false},
		{"another host", "get", "protocol=https\nhost=other.example.com\n\n", false},
		{"no host", "get", "protocol=https\n\n", false},
		{"store", "store", "protocol=https\nhost=hub.example.com\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := credentialHelper(t, tc.verb, tc.stdin)
			if !tc.answered {
				if out != "" {
					t.Fatalf("output = %q; want nothing", out)
				}
				return
			}
			want := "protocol=https\nhost=" + strings.SplitN(strings.Split(tc.stdin, "\n")[1], "=", 2)[1] + "\nusername=x-token-auth\npassword=af_secretkey\n"
			if out != want {
				t.Errorf("output = %q; want %q", out, want)
			}
		})
	}
}

// ENDPOINT_URL and API_KEY stand in for the config file, so an
// environment-only setup works for git too; without either the helper stays
// silent.
func TestCredentialHelper_EnvironmentReplacesTheConfigFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ENDPOINT_URL", "http://localhost:8080")
	t.Setenv("API_KEY", "af_envkey")
	if out := credentialHelper(t, "get", "protocol=http\nhost=localhost:8080\n\n"); !strings.Contains(out, "password=af_envkey") {
		t.Errorf("output = %q; want the key from the environment", out)
	}

	t.Setenv("API_KEY", "")
	if out := credentialHelper(t, "get", "protocol=http\nhost=localhost:8080\n\n"); out != "" {
		t.Errorf("output = %q without an API key; want nothing", out)
	}
}
