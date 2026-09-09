package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCredentialHelper(t *testing.T, stdin string) string {
	t.Helper()
	cmd := BuildRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs([]string{"credential-helper", "get"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("credential-helper get: %v", err)
	}
	return out.String()
}

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

// The helper answers only for the hub's own scheme and host:port; a plain
// http request for the same host must not receive the API key.
func TestCredentialHelper_MatchesSchemeAndPort(t *testing.T) {
	t.Setenv("ENDPOINT_URL", "")
	t.Setenv("API_KEY", "")
	writeHelperConfig(t, "https://hub.example.com", "af_secretkey")

	out := runCredentialHelper(t, "protocol=https\nhost=hub.example.com\n\n")
	if !strings.Contains(out, "password=af_secretkey") {
		t.Fatalf("https request for hub host should be answered; got %q", out)
	}
	out = runCredentialHelper(t, "protocol=https\nhost=hub.example.com:443\n\n")
	if !strings.Contains(out, "password=af_secretkey") {
		t.Fatalf("explicit default port should match; got %q", out)
	}
	out = runCredentialHelper(t, "protocol=http\nhost=hub.example.com\n\n")
	if strings.Contains(out, "af_secretkey") {
		t.Fatalf("plain-http request must not receive the API key; got %q", out)
	}
	out = runCredentialHelper(t, "protocol=https\nhost=hub.example.com:8443\n\n")
	if strings.Contains(out, "af_secretkey") {
		t.Fatalf("different port must not receive the API key; got %q", out)
	}
}

// Environment-only configuration (no ~/.af/config.toml) works for git too.
func TestCredentialHelper_EnvFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ENDPOINT_URL", "http://localhost:8080")
	t.Setenv("API_KEY", "af_envkey")

	out := runCredentialHelper(t, "protocol=http\nhost=localhost:8080\n\n")
	if !strings.Contains(out, "password=af_envkey") || !strings.Contains(out, "username=x-token-auth") {
		t.Fatalf("env-configured helper should answer; got %q", out)
	}
}
