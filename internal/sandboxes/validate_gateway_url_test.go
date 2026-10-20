package sandboxes

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// testGatewayToken is a gateway_token that passes validation: 32 printable
// characters, which is also the shortest token a gatewayd accepts.
const testGatewayToken = "gw-token-0123456789abcdef0123456"

// remoteConfig returns a configuration for provider "gateway" with the given
// gateway_url, the valid token and one valid profile.
func remoteConfig(rawURL string) *Config {
	return &Config{
		Provider:     "gateway",
		GatewayURL:   rawURL,
		GatewayToken: testGatewayToken,
		Profiles:     []ProfileConfig{validProfile("dev")},
	}
}

// gatewayTOML returns a config file with a [sandbox] table for provider
// "gateway", the given extra lines under it and one valid profile.
func gatewayTOML(lines string) string {
	return "[sandbox]\nprovider = \"gateway\"\n" + lines + `
[[sandbox.profiles]]
name = "dev"
image = "registry.example/dev:1"
ttl = "1h"
`
}

// unsetEnv makes sure name is not set for the length of the test.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unsetting %s: %v", name, err)
	}
}

// TS-29-5: gateway_url and gateway_token are known keys, ${NAME} is expanded in
// both, and the failures of both arrive in one joined error with the others.
// Requirement: 29-REQ-2.1
func TestGatewayKeys_KnownExpandedAndJoined_TS_29_5(t *testing.T) {
	t.Setenv("GW_URL", "http://127.0.0.1:9000")
	t.Setenv("GW_TOKEN", testGatewayToken)
	path := writeConfigFile(t, t.TempDir(), gatewayTOML(`
gateway_url = "${GW_URL}"
gateway_token = "${GW_TOKEN}"
`))
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error (an unknown key?): %v", err)
	}
	if cfg.GatewayURL != "http://127.0.0.1:9000" || cfg.GatewayToken != testGatewayToken {
		t.Errorf("gateway_url, gateway_token = %q, %q; want the expanded values", cfg.GatewayURL, cfg.GatewayToken)
	}
	v := requireValid(t, cfg)
	if v.GatewayURL != "http://127.0.0.1:9000" || v.GatewayToken.reveal() != testGatewayToken {
		t.Errorf("the validated definition holds %q and a token of %d bytes; want the expanded values", v.GatewayURL, len(v.GatewayToken.reveal()))
	}

	unsetEnv(t, "AF_TEST_GATEWAY_TOKEN_UNSET")
	t.Setenv("GW_URL_BAD", "https://gw.example/api")
	bad := writeConfigFile(t, t.TempDir(), gatewayTOML(`
max_ttl = "bogus"
gateway_url = "${GW_URL_BAD}"
gateway_token = "${AF_TEST_GATEWAY_TOKEN_UNSET}"
`))
	cfg, err = LoadConfig(bad)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	_, err = Validate(cfg)
	if err == nil {
		t.Fatal("Validate accepted a configuration with three faults")
	}
	var joined interface{ Unwrap() []error }
	if !errors.As(err, &joined) || len(joined.Unwrap()) < 3 {
		t.Errorf("Validate returned %T, not one joined error of at least three failures: %v", err, err)
	}
	for _, key := range []string{"sandbox.gateway_url", "sandbox.gateway_token", "sandbox.max_ttl"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("the joined error does not name %s: %v", key, err)
		}
	}
}

// TS-29-6: A missing or empty gateway_url under provider gateway is refused as
// required.
// Requirement: 29-REQ-2.2
func TestGatewayURL_Required_TS_29_6(t *testing.T) {
	unsetEnv(t, "AF_TEST_GATEWAY_URL_UNSET")
	cases := map[string]string{
		"absent":       "",
		"empty":        `gateway_url = ""`,
		"unset ${...}": `gateway_url = "${AF_TEST_GATEWAY_URL_UNSET}"`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfigFile(t, t.TempDir(), gatewayTOML(line+"\ngateway_token = \""+testGatewayToken+"\"\n"))
			_, err := Validate(mustLoad(t, path))
			requireFailure(t, err, "sandbox.gateway_url", "required")
		})
	}
}

// TS-29-7: https and http to a loopback host pass; every other scheme, plaintext
// to another host and a value that is not an absolute URL fail.
// Requirement: 29-REQ-2.3
func TestGatewayURL_SchemeAndParse_TS_29_7(t *testing.T) {
	good := []string{
		"http://127.0.0.1:9000", "http://localhost:9000", "https://gw.example:443",
		"https://gw.example", "http://[::1]:9000", "https://127.0.0.1:9000",
	}
	for _, u := range good {
		if _, err := Validate(remoteConfig(u)); err != nil {
			t.Errorf("Validate(%q) returned error: %v", u, err)
		}
	}
	bad := map[string]string{
		"http://gw.example:9000":       "plaintext",
		"ftp://127.0.0.1":              "scheme",
		"gw.example:9000":              "scheme", // Go reads gw.example as the scheme
		"gw.example":                   "scheme",
		"/just/a/path":                 "scheme",
		"https:gw.example":             "scheme://host",
		"http://0.0.0.0:9000":          "plaintext",
		"http://127.0.0.1.nip.io:9000": "plaintext",
		"://nohost":                    "not a valid URL",
	}
	for u, rule := range bad {
		_, err := Validate(remoteConfig(u))
		if err == nil {
			t.Errorf("Validate(%q) accepted the URL", u)
			continue
		}
		requireFailure(t, err, "sandbox.gateway_url", rule)
	}
}

// TS-29-8: An empty host and a port that is not a number from 1 to 65535 fail.
// Requirement: 29-REQ-2.4
func TestGatewayURL_HostAndPort_TS_29_8(t *testing.T) {
	for _, u := range []string{"https://gw.example:1", "https://gw.example:65535"} {
		if _, err := Validate(remoteConfig(u)); err != nil {
			t.Errorf("Validate(%q) returned error: %v", u, err)
		}
	}
	for u, rule := range map[string]string{
		"https://gw.example:0":     "port",
		"https://gw.example:65536": "port",
		"https://gw.example:abc":   "port",
		"https://gw.example:-1":    "port",
		"https://gw.example:":      "port",
		"https://:443":             "name a host",
		"https://":                 "name a host",
	} {
		_, err := Validate(remoteConfig(u))
		if err == nil {
			t.Errorf("Validate(%q) accepted the URL", u)
			continue
		}
		requireFailure(t, err, "sandbox.gateway_url", rule)
	}
}

// TS-29-9: User information, a query, a fragment and a path other than / fail;
// an empty path and / pass.
// Requirement: 29-REQ-2.5
func TestGatewayURL_NoUserinfoQueryFragmentPath_TS_29_9(t *testing.T) {
	for _, u := range []string{"https://gw.example/", "https://gw.example"} {
		if _, err := Validate(remoteConfig(u)); err != nil {
			t.Errorf("Validate(%q) returned error: %v", u, err)
		}
	}
	rules := map[string]string{
		"https://gw.example/api":         "path",
		"https://gw.example?x=1":         "query",
		"https://gw.example/?x=1":        "query",
		"https://gw.example?":            "query",
		"https://gw.example#f":           "fragment",
		"https://gw.example#":            "fragment",
		"https://u:p@gw.example":         "user information",
		"https://u@gw.example":           "user information",
		"http://u:p@127.0.0.1:9000":      "user information",
		"https://gw.example/a/../b":      "path",
		"https://gw.example//":           "path",
		"https://gw.example:9000/x?y#z":  "query",
		"https://u:p@gw.example/api?x=1": "user information",
	}
	for u, rule := range rules {
		_, err := Validate(remoteConfig(u))
		if err == nil {
			t.Errorf("Validate(%q) accepted the URL", u)
			continue
		}
		requireFailure(t, err, "sandbox.gateway_url", rule)
	}
}

// TS-29-10: User information is taken out of gateway_url before the URL is
// quoted in a failure; no failure repeats the credentials, whatever else is
// wrong with the URL.
// Requirement: 29-REQ-2.6
func TestGatewayURL_UserinfoNeverQuoted_TS_29_10(t *testing.T) {
	urls := []string{
		"https://user:pw@gw.example",
		"http://user:pw@gw.example:9000",
		"ftp://user:pw@gw.example",
		"https://user:pw@gw.example:99999",
		"https://user:pw@gw.example:abc",
		"https://user:pw@gw.example/api?x=1#f",
		"https://user:pw@",
		"https://user:pw@:443",
		"user:pw@gw.example",
	}
	for _, u := range urls {
		_, err := Validate(remoteConfig(u))
		if err == nil {
			t.Errorf("Validate(%q) accepted the URL", u)
			continue
		}
		requireFailure(t, err, "sandbox.gateway_url")
		for _, leak := range []string{"user:pw", "pw@", "user@"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("Validate(%q) repeats %q: %v", u, leak, err)
			}
		}
	}

	// The failures that quote the URL show the scheme and the host, and no more.
	_, err := Validate(remoteConfig("http://user:pw@gw.example:9000"))
	requireFailure(t, err, "sandbox.gateway_url", "http://gw.example:9000")
}
